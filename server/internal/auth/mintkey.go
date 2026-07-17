package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chordpli/tmula/server/internal/domain"
)

// mintKeyMaxBytes caps how much a file-backed signing key will read (1 MiB) — far
// larger than any HMAC secret or PEM private key, while refusing a runaway file.
const mintKeyMaxBytes = 1 << 20

// ResolveMintKey reads the mint signing key BODY the spec's reference points at and
// decodes it (per the alg/encoding) into the in-process signing-key bytes the
// MintProvider holds. It is the single in-process resolution point for the mint key:
//
//   - An env reference reads the variable VERBATIM (the whole value is the key body),
//     erroring on an empty/unset variable — naming the variable, never its value, so a
//     misconfiguration message cannot leak the secret.
//   - A file reference reads the file confined under root (reusing FileSource's
//     containment/symlink guards), so a relative key path resolves predictably and
//     cannot escape the working directory.
//
// It NEVER serializes the key: the caller hands the returned bytes to
// auth.NewProvider via ProviderDeps.MintKey (in-process), while only the reference
// rides the wire. root is the resolver's working directory (an empty root falls back
// to "."), matching the credential-source resolution root.
func ResolveMintKey(ctx context.Context, spec domain.MintSpec, root string) ([]byte, error) {
	if spec.Key == nil {
		return nil, fmt.Errorf("auth: mint key reference is required")
	}
	body, err := readMintKeyBody(ctx, *spec.Key, root)
	if err != nil {
		return nil, err
	}
	key, err := spec.DecodeKey(body)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// readMintKeyBody reads the raw key body from the env var or file the reference names.
// Unlike a credential source it is a SINGLE secret, not a formatted list, so it is
// read verbatim (env) or as the whole file (with the path guards), never parsed.
func readMintKeyBody(_ context.Context, ref domain.CredentialSourceRef, root string) ([]byte, error) {
	hasFile, hasEnv := ref.File != "", ref.Env != ""
	if hasFile == hasEnv {
		return nil, fmt.Errorf("auth: mint key reference needs exactly one of file or env")
	}

	if hasEnv {
		val := os.Getenv(ref.Env)
		if val == "" {
			return nil, fmt.Errorf("auth: mint key env %q is empty or unset", ref.Env)
		}
		// Trim a trailing newline an env editor/shell may append to a single-line
		// secret; a PEM keeps its internal newlines, so only the outer ones go.
		return []byte(strings.TrimRight(val, "\r\n")), nil
	}

	// A file key reuses FileSource's containment/symlink/size guards via a tokens-format
	// source (the format is irrelevant — we read the confined file directly below), so
	// the same hardened path resolution covers the signing key. Resolve confinement,
	// then read the whole file under the cap.
	if root == "" {
		root = "."
	}
	if strings.ContainsAny(ref.File, "\x00") || strings.ContainsAny(ref.File, "\r\n") {
		return nil, fmt.Errorf("auth: mint key file path contains control characters")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("auth: mint key root: %w", err)
	}
	clean := filepath.Clean(filepath.Join(absRoot, ref.File))
	if !withinRoot(absRoot, clean) {
		return nil, fmt.Errorf("auth: mint key path %q escapes its root", ref.File)
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("auth: mint key root: %w", err)
	}
	evalPath, err := filepath.EvalSymlinks(clean)
	if err != nil {
		// Name the directory the key was looked for under, so a "no such file" reads as
		// "not beside the scenario" rather than a bare path error — the common cause is a
		// key.file resolved against the wrong root.
		return nil, fmt.Errorf("auth: mint key file %q not found under %s: %w", ref.File, absRoot, err)
	}
	if !withinRoot(evalRoot, evalPath) {
		return nil, fmt.Errorf("auth: mint key path %q escapes its root via a symlink", ref.File)
	}
	info, err := os.Stat(evalPath)
	if err != nil {
		return nil, fmt.Errorf("auth: stat mint key file %q: %w", ref.File, err)
	}
	if info.Size() > mintKeyMaxBytes {
		return nil, fmt.Errorf("auth: mint key file %q exceeds the %d-byte limit", ref.File, mintKeyMaxBytes)
	}
	body, err := os.ReadFile(evalPath)
	if err != nil {
		return nil, fmt.Errorf("auth: read mint key file %q: %w", ref.File, err)
	}
	return body, nil
}
