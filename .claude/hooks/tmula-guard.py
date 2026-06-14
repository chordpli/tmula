#!/usr/bin/env python3
# tmula-guard core — reads a PreToolUse(Bash) hook JSON on stdin and decides
# whether to block a `tmula run`. Invoked by tmula-guard.sh. argv[1] = project dir.
#
# Blocks (exit 2) when a `tmula run` would:
#   1. target a host that is NOT loopback/private (possibly production), unless the
#      host is allowed via $TMULA_ALLOW_TARGET or a .tmula-allow file, or
#   2. name a scenario file that does not exist (missing required input).
# Everything else exits 0 (allow).
import sys, os, json, re, shlex, ipaddress
from urllib.parse import urlparse

proj = sys.argv[1] if len(sys.argv) > 1 else os.getcwd()
try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

cmd = ((data.get("tool_input") or {}).get("command")) or ""
cwd = data.get("cwd") or proj or os.getcwd()

# Only the load-sending `run` subcommand of a tmula binary. "tmula-run" (the skill
# name) has no space before -run, so it never matches.
if not re.search(r"(^|[\s/])tmula\s+run\b", cmd):
    sys.exit(0)

VALUE_FLAGS = {
    "--target", "--users", "--timeout", "--open", "--for", "--ramp-to", "--seed",
    "--engine", "--baseline", "--baseline-file", "--known-issues", "--fail-on-severity",
    "--summary", "--get", "--post",
}

try:
    toks = shlex.split(cmd)
except ValueError:
    sys.exit(0)  # unparseable — don't false-block

target_url, scen = None, None
seen_run = False
j = 0
while j < len(toks):
    t = toks[j]
    if t == "run" and j > 0 and toks[j - 1].endswith("tmula"):
        seen_run = True
        j += 1
        continue
    if seen_run:
        if t.startswith("--target"):
            if "=" in t:
                target_url = t.split("=", 1)[1]
            elif j + 1 < len(toks):
                target_url = toks[j + 1]
                j += 1
        elif t.startswith("-"):
            base = t.split("=", 1)[0]
            if base in VALUE_FLAGS and "=" not in t:
                j += 1  # consume this flag's value
        elif scen is None:
            scen = t
    j += 1


def block(msg):
    sys.stderr.write("tmula-guard: " + msg + "\n")
    sys.exit(2)


# Missing-input backstop: a named scenario file must exist.
if target_url is None and scen is not None:
    path = scen if os.path.isabs(scen) else os.path.join(cwd, scen)
    if not os.path.exists(path):
        block(f"scenario file not found: {scen}\n"
              f"  Run tmula-scaffold (and tmula-enrich) to create json/scenario.json first, "
              f"or pass the correct path.")
    try:
        with open(path, "rb") as f:
            raw = f.read()
        try:
            doc = json.loads(raw)
        except Exception:
            import yaml  # optional; if absent we can't classify → allow
            doc = yaml.safe_load(raw)
        target_url = (doc or {}).get("target")
    except Exception:
        sys.exit(0)  # can't read/parse target → don't false-block

if not target_url:
    sys.exit(0)  # single-endpoint or undetermined target → leave it

host = (urlparse(target_url).hostname or "").lower()


def is_safe(h):
    if not h or h == "localhost" or h.endswith(".local"):
        return True
    try:
        ip = ipaddress.ip_address(h)
        return ip.is_loopback or ip.is_private or ip.is_link_local or ip.is_unspecified
    except ValueError:
        return False  # a real hostname like api.example.com


if is_safe(host):
    sys.exit(0)

# External host — allow only if explicitly opted in.
allowed = set()
for h in re.split(r"[,\s]+", os.environ.get("TMULA_ALLOW_TARGET", "").strip()):
    if h:
        allowed.add(h.lower())
for base in (cwd, proj):
    af = os.path.join(base, ".tmula-allow")
    if os.path.isfile(af):
        try:
            for line in open(af):
                line = line.strip()
                if line and not line.startswith("#"):
                    allowed.add(line.lower())
        except Exception:
            pass

if "all" in allowed or "*" in allowed or host in allowed:
    sys.exit(0)

block(f"refusing to send load to '{host}' — it is not loopback/private, so it may be production.\n"
      f"  This is the non-prod safety backstop for `tmula run`.\n"
      f"  If '{host}' is a sandbox/staging host you are allowed to load, opt in and re-run:\n"
      f"    export TMULA_ALLOW_TARGET=\"{host}\"   (or add it to a .tmula-allow file)")
