---
name: tmula-enrich
description: Make an existing tmula scenario actually runnable and safe — substitute literal path params, wire auth headers from the credential pool, filter out destructive operations, and add dependencies / weights / finding thresholds. Use when you already have a scenario.json (from tmula-scaffold, hand-written, or pasted) that won't run cleanly or safely yet. Does NOT send traffic — that's tmula-run.
argument-hint: "[scenario path] (defaults to json/scenario.json)"
---

> **Required input — ask, never invent.** This skill needs a scenario to enrich. If no path is given and
> `json/scenario.json` doesn't exist, **STOP and ask the user** for the scenario (or run tmula-scaffold
> first). Never fabricate a scenario or its endpoints.

# tmula-enrich

Take a tmula `scenario.json` and apply the judgment a raw import can't: fill literal path params, wire
auth, **filter destructive operations**, and shape the flow. Output is a runnable, safe scenario the user
can hand to **tmula-run**.

> This step only edits a file. It never sends traffic.

## Inputs (Source options — works standalone)

- A scenario file path, else `json/scenario.json`, else ask. Works on any scenario — from tmula-scaffold,
  hand-written, or pasted. (For deriving values you also want the source spec if one exists, but enrich
  works without it.)

Artifact produced: the enriched scenario written back to `json/scenario.json` (or an `--out` path you choose).

## Process

1. **Read the scenario.** Identify each step's `request` (`METHOD /path`), and whether it's a linear `flow`
   or a graph-first scenario (`graph`+`templates`). Both support `headers`.

2. **Substitute every path parameter.** A literal `{...}` in a path makes tmula send `/pet/{petId}` → 404.
   Replace **every** brace (a leftover is the most common silent failure), priority:
   `example` → `enum[0]` → by `format`/`type` (integer→`1`, uuid→a fixed UUID, string→`"string"`) →
   fallback `1`. `GET /pet/{petId}` → `GET /pet/1`. Add query params the endpoint needs to return 200
   (e.g. `GET /pet/findByStatus?status=available`).

3. **Safety filter — default to read-only.** Find destructive/mutating ops (`DELETE`, and mutating
   `POST`/`PUT`/`PATCH`). **Remove them by default** and list what was removed. Re-include only on the
   user's explicit opt-in, and even then only against a confirmed sandbox (that gate lives in tmula-run).
   Example: Petstore v3 has 11 to drop (`POST /pet`, `PUT /pet`, `POST /user*`, `POST /pet/{petId}*`,
   `POST /store/order`, `PUT /user/{username}`, `DELETE /pet/{petId}`, `DELETE /user/{username}`,
   `DELETE /store/order/{orderId}`).

4. **Wire auth — the pool injects nothing on its own.** A credential's token is exposed only as the Go
   template variable `{{.token}}` (and `{{.subject}}`); tmula does **not** auto-add any header. To
   authenticate, a step must reference it in a `headers` block (linear steps and graph templates both
   support `headers`). Detect `securitySchemes` (OpenAPI 3) / `securityDefinitions` (Swagger 2) and wire
   per scheme — both verified to send the token end-to-end:
   - API-key header (e.g. `api_key`): `"headers": { "api_key": "{{.token}}" }`
   - OAuth2 / bearer: `"headers": { "Authorization": "Bearer {{.token}}" }`

   There are seven ways to supply credentials — pick the one that fits:

   **(a) Inline users (simplest, token in file):**
   ```json
   "auth": { "strategy": "pool", "users": [ { "subject": "tester", "token": "REPLACE_ME_API_KEY" } ] }
   ```
   The user must fill the placeholder. Use the dotted `{{.token}}` — `{{token}}` without the dot is a
   template error and the request won't send. If you can't determine the header, **say which endpoints
   will 401/403** rather than guess.

   **(b) External source (keep secrets out of the scenario file):**
   ```json
   "auth": {
     "strategy": "pool",
     "source": { "file": "creds.csv", "format": "csv" }
   }
   ```
   `source` is mutually exclusive with `users`. Supported formats:
   - `csv`: header row `subject,token`; one credential per line.
   - `jsonl`: one `{"subject":"...","token":"..."}` JSON object per line.
   - `tokens`: one raw secret per line, **no subject** — `{{.subject}}` renders empty with this format.
   The file path is resolved relative to the scenario file's directory. Use `"env": "VAR_NAME"` instead
   of `"file"` to read the body from an environment variable (useful in CI).

   **(c) Login strategy (mint a token at run time):**
   ```json
   "auth": {
     "strategy": "login",
     "login": {
       "flow": [ { "id": "signin", "request": "POST /auth/token",
                   "body": "{\"username\":\"u1\",\"password\":\"p1\"}",
                   "extract": { "tok": "$.access_token" } } ],
       "capture": { "token": "tok" },
       "scope": "per-user"
     }
   }
   ```
   tmula walks the login flow once per virtual user (scope `per-user`, the default) or once for all
   sessions (scope `shared`, client_credentials style), captures `$.access_token` into the `tok`
   variable, and sends it as `{{.token}}`. On a 401 mid-run it refreshes once (a derived
   `grant_type=refresh_token` exchange for an OAuth2 form login, else a re-login) and retries; that
   refresh traffic is excluded from findings. Login runs are in-process only — rejected against a
   remote `--engine` and distributed workers. **Session-cookie services:** if the login answers with a
   `Set-Cookie` instead of a body token, omit the capture — the session cookie
   (`session`/`token`/`jwt`/`auth`/`sid`-style names) is auto-captured; replay it with a step header
   `"Cookie": "session={{.token}}"`.

   **(d) Bootstrap signup (provision real accounts):**
   ```json
   "auth": {
     "strategy": "bootstrap-signup",
     "signup": {
       "flow": [ { "id": "register", "request": "POST /users",
                   "body": "{\"email\":\"user{{.subject}}@example.com\"}",
                   "extract": { "tok": "$.token", "uid": "$.id" } } ],
       "capture": { "token": "tok", "subject": "uid" },
       "teardown": [ { "id": "delete", "request": "DELETE /users/{{.subject}}" } ]
     }
   }
   ```
   Provisions one real account per virtual user before the run, then **tears all accounts down after**
   (even on kill/timeout). Requires a teardown flow OR `--keep-accounts`. Bootstrap runs are in-process
   only. **Only use against a confirmed non-production sandbox** — this creates and deletes real accounts.

   **(e) Pattern-generated accounts (`usersPattern`, no file):**
   ```json
   "auth": { "strategy": "pool",
     "usersPattern": { "subject": "user{{.userIndex}}", "token": "pw-{{.userIndex}}", "count": 100000 } }
   ```
   Generates the credential rows from a subject/token template pair at Expand time — tens of thousands
   of accounts with no file. Mutually exclusive with `users` and `source`. Works for `login` too (there
   `subject` = username, `token` = password). Not for opaque JWTs — that's mint.

   **(f) Mint (self-issue a JWT locally):**
   ```json
   "auth": { "strategy": "mint",
     "mint": { "alg": "HS256", "secretEncoding": "raw", "key": { "env": "MINT_SIGNING_KEY" },
               "subject": "user-{{.userIndex}}", "ttl": "1h" } }
   ```
   Signs a fresh JWT per virtual user with a key the operator holds — no login traffic. The key is a
   **reference** (env/file), never inlined. **Never for Auth0/Cognito/Firebase** — a managed IdP holds
   the signing key, so a self-minted token is rejected; use login instead. Mint may fan out to
   distributed workers (only the key reference ships).

   **(g) Exec (bring your own token — escape hatch):**
   ```json
   "auth": { "strategy": "exec",
     "exec": { "command": ["vault", "token", "create", "-field=token"], "timeout": "10s" } }
   ```
   Runs a local command per virtual user; its stdout becomes `{{.token}}` (argv may reference
   `{{.userIndex}}`). **Gated**: the run is rejected unless the operator passes `--allow-exec`, and the
   command's egress bypasses the allowlist/rate cap. Last resort only.

5. **Shape the flow (optional but valuable):**
   - **Step ids**: rename cryptic/colliding ids to stable human-readable ones *before* wiring `dependsOn`
     (duplicate ids break edge references).
   - **`dependsOn`**: mark hard prerequisites (e.g. a detail view `dependsOn` a list step) — that edge is
     never skipped.
   - **`weight`** (default 1): the relative probability of taking the edge to the next step — bias the happy
     path by giving the common next step a higher weight than the rare ones.
   - **`deviationRate`** (0..1): let users wander/abandon off the happy path (dependency edges still hold).
   - **`findings`** thresholds: `{ "errorRate": 0.05, "p95LatencyMs": 800, "availabilityStreak": 5 }`
     (omitting `p95LatencyMs` keeps the latency gate **disabled**; omitting `errorRate`/`availabilityStreak`
     falls back to defaults 0.2 / 5, not off).
   - **`users`**: closed-model VU count (default 20). Does **not** apply to a graph scenario learned from an
     access log — that uses the open arrival model (`open` + `maxSteps`); tune those instead.

6. **Write `json/scenario.json`** and report what changed: path params filled, auth wired (+ which header /
   placeholder to fill), destructive ops removed, flow shaped. Hand off: *"Run **tmula-run** to load-test
   it (it enforces the non-prod safety gate)."*

## Scenario fields you may set

`target` · `allow` (reachable hosts; default = target host, fail-closed) · per-step `id`/`request`/`body`/
`headers`/`dependsOn`/`weight` · `users` (closed model) · `open`+`maxSteps` (open model) · `seed` ·
`deviationRate` · `auth` (pool with inline `users`, external `source`, or generated `usersPattern`; `login` —
incl. session-cookie auto-capture; `bootstrap-signup`; `mint`; `exec`, gated behind `--allow-exec`) ·
`findings` (`errorRate`, `p95LatencyMs`, `availabilityStreak`).

## Iron Laws

- **Never auto-include DELETE / mutating ops.** Default the enriched flow to read-only; writes are opt-in.
- **Don't fabricate credentials.** Insert clearly-labeled placeholder tokens and tell the user to fill them.
- **Substitute every `{path-param}`** — a leftover brace silently 404s.
- **This skill never sends traffic.** It writes a file; running (and the safety gate) is tmula-run.

## Failure modes

- Run later shows 404/400 with `{param}` in logs → a brace was missed; re-scan the whole flow.
- `template: …` error / request won't send → used `{{token}}` without the dot; it must be `{{.token}}`.
- Protected endpoints still 401 → header not wired for that scheme, or the placeholder token is unfilled.
- Run refuses to start naming a `REPLACE_ME` field → scenario expand rejects an auth block still carrying
  a literal `REPLACE_ME`; fill the secret or supply it via `--auth-source file:…/env:…`.
- Duplicate/cryptic ids, `dependsOn` unresolved → rename to stable ids before wiring edges.
- Graph-first scenario (from access-log import) → enrich `templates` (they carry the per-node request +
  `headers`), not a linear `flow`; keep the `graph`/`start`.

## Example (read-only, auth wired, destructive ops dropped)

```json
{
  "target": "https://petstore3.swagger.io/api/v3",
  "allow": ["petstore3.swagger.io"],
  "users": 20,
  "auth": { "strategy": "pool", "users": [ { "subject": "tester", "token": "REPLACE_ME_API_KEY" } ] },
  "flow": [
    { "id": "login",        "request": "GET /user/login?username=tester&password=x" },
    { "id": "findByStatus", "request": "GET /pet/findByStatus?status=available", "headers": { "api_key": "{{.token}}" } },
    { "id": "getPet",       "request": "GET /pet/1", "dependsOn": "findByStatus", "headers": { "api_key": "{{.token}}" } },
    { "id": "getInventory", "request": "GET /store/inventory", "headers": { "api_key": "{{.token}}" } }
  ],
  "findings": { "errorRate": 0.05, "p95LatencyMs": 800 }
}
```

> Report: `{petId}`→`1`; `api_key` header wired to the pool (**fill `REPLACE_ME_API_KEY`** or switch to an
> external `source` / `login` strategy to keep secrets out of the file); dropped 11 mutating ops for safety. Next: tmula-run.
