package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chordpli/tmula/server/internal/domain"
)

// Format names how a credential body is encoded. The vocabulary is explicit and
// closed — a source declares its format, it is never content-sniffed — so an
// operator's intent is unambiguous and a typo fails loudly.
type Format string

const (
	// CSV is a header row (subject,token; token required, subject optional)
	// followed by one record per line.
	CSV Format = "csv"
	// JSONL is one {"subject":..,"token":..} object per line.
	JSONL Format = "jsonl"
	// Tokens is one secret per non-blank line (subject left empty).
	Tokens Format = "tokens"
)

// credSourceMaxBytes caps how much a file source will read by default (8 MiB),
// large enough for tens of thousands of tokens while refusing a runaway file.
const credSourceMaxBytes = 8 << 20

// CredentialSource loads a set of credentials from somewhere external to the
// scenario document — an inline list, an environment variable, or a file — so an
// operator can point at a pool of secrets without inlining them in YAML. Load is
// the single read point; callers feed its result into a PoolProvider.
type CredentialSource interface {
	Load(ctx context.Context) ([]domain.Credential, error)
}

// Parse decodes a credential body in the given explicit format into an ordered
// slice of credentials. It preserves source order, ignores blank lines and a
// trailing newline, and errors when zero credentials are parsed (mirroring
// NewPoolProvider, which needs at least one credential).
func Parse(format Format, body []byte) ([]domain.Credential, error) {
	switch format {
	case CSV:
		return parseCSV(body)
	case JSONL:
		return parseJSONL(body)
	case Tokens:
		return parseTokens(body)
	default:
		return nil, fmt.Errorf("auth: unknown credential format %q (want one of %q, %q, %q)", format, CSV, JSONL, Tokens)
	}
}

// parseCSV reads a header row that must include a "token" column (and may
// include an optional "subject" column), then one credential per data row.
func parseCSV(body []byte) ([]domain.Credential, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1 // tolerate ragged rows; we index by header position
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("auth: parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("auth: csv credential source is empty")
	}
	header := records[0]
	subjectIdx, tokenIdx := -1, -1
	for i, col := range header {
		switch strings.TrimSpace(col) {
		case "subject":
			subjectIdx = i
		case "token":
			tokenIdx = i
		}
	}
	if tokenIdx < 0 {
		return nil, fmt.Errorf("auth: csv credential source needs a \"token\" column header")
	}
	out := make([]domain.Credential, 0, len(records)-1)
	for _, rec := range records[1:] {
		if tokenIdx >= len(rec) {
			return nil, fmt.Errorf("auth: csv row %v is missing its token column", rec)
		}
		var subject string
		if subjectIdx >= 0 && subjectIdx < len(rec) {
			subject = rec[subjectIdx]
		}
		out = append(out, domain.Credential{Subject: subject, Secret: rec[tokenIdx]})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("auth: csv credential source has no data rows (needs at least one credential)")
	}
	return out, nil
}

// jsonlCred is the per-line shape of a JSONL credential body.
type jsonlCred struct {
	Subject string `json:"subject"`
	Token   string `json:"token"`
}

// parseJSONL reads one {subject,token} object per non-blank line.
func parseJSONL(body []byte) ([]domain.Credential, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), credSourceMaxBytes)
	var out []domain.Credential
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c jsonlCred
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("auth: parse jsonl line: %w", err)
		}
		if c.Token == "" {
			return nil, fmt.Errorf("auth: jsonl credential is missing its token")
		}
		out = append(out, domain.Credential{Subject: c.Subject, Secret: c.Token})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("auth: scan jsonl: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("auth: jsonl credential source has no rows (needs at least one credential)")
	}
	return out, nil
}

// parseTokens reads one secret per non-blank line, leaving the subject empty.
func parseTokens(body []byte) ([]domain.Credential, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), credSourceMaxBytes)
	var out []domain.Credential
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		out = append(out, domain.Credential{Secret: line})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("auth: scan tokens: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("auth: token credential source has no non-blank line (needs at least one credential)")
	}
	return out, nil
}

// InlineSource is a credential source backed by an in-memory list — the literal
// users authored in a scenario file. It exists so the same Load seam covers both
// inlined and externally referenced pools.
type InlineSource struct {
	Entries []domain.Credential
}

// Load returns a defensive copy of the inline entries so a caller mutating the
// result cannot corrupt the source's backing array. An empty source errors.
func (s InlineSource) Load(_ context.Context) ([]domain.Credential, error) {
	if len(s.Entries) == 0 {
		return nil, fmt.Errorf("auth: inline credential source is empty (needs at least one credential)")
	}
	out := make([]domain.Credential, len(s.Entries))
	copy(out, s.Entries)
	return out, nil
}

// EnvSource reads a credential body from an environment variable in the given
// format. The value never appears in an error — only the variable name does — so
// a misconfiguration message cannot leak a secret.
type EnvSource struct {
	Var    string
	Format Format
}

// Load reads the named environment variable and parses it. An empty (or unset)
// variable errors naming the variable, never its value.
func (s EnvSource) Load(_ context.Context) ([]domain.Credential, error) {
	if s.Var == "" {
		return nil, fmt.Errorf("auth: env credential source needs a variable name")
	}
	body := os.Getenv(s.Var)
	if body == "" {
		return nil, fmt.Errorf("auth: env credential source %q is empty or unset", s.Var)
	}
	creds, err := Parse(s.Format, []byte(body))
	if err != nil {
		return nil, fmt.Errorf("auth: env credential source %q: %w", s.Var, err)
	}
	return creds, nil
}

// FileSource reads a credential body from a file resolved under Root in the given
// format. Path is operator-supplied and is treated as untrusted: it is confined
// to Root and rejected if it escapes via "..", a NUL/control byte, or a symlink
// pointing outside Root. The file is read under a byte cap (MaxBytes, default
// credSourceMaxBytes) so a runaway file cannot exhaust memory.
type FileSource struct {
	Root     string
	Path     string
	Format   Format
	MaxBytes int64
}

// Load resolves Path within Root, enforces the containment, symlink and size
// guards, then parses the file. The guards run before any read, so a rejected
// path never causes file contents to be loaded.
func (s FileSource) Load(_ context.Context) ([]domain.Credential, error) {
	if s.Path == "" {
		return nil, fmt.Errorf("auth: file credential source needs a path")
	}
	if strings.ContainsAny(s.Path, "\x00") || strings.ContainsAny(s.Path, "\r\n") {
		return nil, fmt.Errorf("auth: file credential source path contains control characters")
	}

	root, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, fmt.Errorf("auth: file credential source root: %w", err)
	}
	clean := filepath.Clean(filepath.Join(root, s.Path))
	if !withinRoot(root, clean) {
		return nil, fmt.Errorf("auth: file credential source path %q escapes its root", s.Path)
	}

	// Re-check after resolving symlinks: a symlink inside Root that points
	// outside Root passes the lexical Clean+prefix check above but must still be
	// rejected. EvalSymlinks resolves the final target; we re-confirm containment
	// against the (also symlink-resolved) root so a legitimately symlinked root
	// is not spuriously rejected.
	evalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("auth: file credential source root: %w", err)
	}
	evalPath, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return nil, fmt.Errorf("auth: file credential source %q: %w", s.Path, err)
	}
	if !withinRoot(evalRoot, evalPath) {
		return nil, fmt.Errorf("auth: file credential source path %q escapes its root via a symlink", s.Path)
	}

	limit := s.MaxBytes
	if limit <= 0 {
		limit = credSourceMaxBytes
	}
	f, err := os.Open(evalPath)
	if err != nil {
		return nil, fmt.Errorf("auth: open file credential source %q: %w", s.Path, err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("auth: read file credential source %q: %w", s.Path, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("auth: file credential source %q exceeds the %d-byte limit", s.Path, limit)
	}
	creds, err := Parse(s.Format, body)
	if err != nil {
		return nil, fmt.Errorf("auth: file credential source %q: %w", s.Path, err)
	}
	return creds, nil
}

// SourceFromRef builds a CredentialSource from a non-secret domain reference (a
// file path or an env-var name plus its format). A file reference is rooted at
// root and confined to it by FileSource's containment/symlink/size guards; root
// is the resolver's working directory (an empty root falls back to "."). It is
// the seam a worker (or the reproduce path) uses to turn the reference it
// received off the wire into a loadable pool, so the same index-deterministic
// PoolProvider can be reconstructed wherever the reference travels — never the
// secrets it resolves to.
func SourceFromRef(ref domain.CredentialSourceRef, root string) (CredentialSource, error) {
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("auth: credential source reference: %w", err)
	}
	format := Format(ref.Format)
	if ref.Env != "" {
		return EnvSource{Var: ref.Env, Format: format}, nil
	}
	if root == "" {
		root = "."
	}
	return FileSource{Root: root, Path: ref.File, Format: format}, nil
}

// withinRoot reports whether path is root itself or lies inside it, comparing on
// path boundaries so "/a/rootx" is not treated as inside "/a/root".
func withinRoot(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}
