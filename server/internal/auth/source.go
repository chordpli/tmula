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
	"strconv"
	"strings"
	"text/template"

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

// credSourceMaxBytes caps how much a file source will read by default (512 MiB).
// A JWT is ~1 KiB, so a hundreds-of-thousands-of-accounts JSONL pool runs to
// hundreds of MB; parsing is streaming (ParseReader), so the cap bounds a
// runaway file, not memory-per-read. A scenario's auth.source.maxBytes can
// override it (the cap itself always stands — an override must be positive).
const credSourceMaxBytes = 512 << 20

// credSourceLineMaxBytes bounds ONE line of a JSONL/tokens body (1 MiB). A line
// is a single credential, so its bound is deliberately decoupled from the file
// cap — raising the file cap must not let one absurd line balloon the scanner.
const credSourceLineMaxBytes = 1 << 20

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
	return ParseReader(format, bytes.NewReader(body))
}

// ParseReader is the streaming form of Parse: it decodes credentials straight
// off a reader — one CSV record / one line at a time — so a hundreds-of-MB pool
// file never needs a whole-body buffer (only the parsed rows themselves).
func ParseReader(format Format, r io.Reader) ([]domain.Credential, error) {
	switch format {
	case CSV:
		return parseCSV(r)
	case JSONL:
		return parseJSONL(r)
	case Tokens:
		return parseTokens(r)
	default:
		return nil, fmt.Errorf("auth: unknown credential format %q (want one of %q, %q, %q)", format, CSV, JSONL, Tokens)
	}
}

// parseCSV reads a header row that must include a "token" column (and may
// include an optional "subject" column), then one credential per data row,
// streaming record by record.
func parseCSV(r io.Reader) ([]domain.Credential, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows; we index by header position
	header, err := cr.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("auth: csv credential source is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("auth: parse csv: %w", err)
	}
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
	var out []domain.Credential
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("auth: parse csv: %w", err)
		}
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

// parseJSONL reads one {subject,token} object per non-blank line, streaming.
// The scanner's max token is the per-LINE bound, deliberately decoupled from
// the file cap (a line is one credential).
func parseJSONL(r io.Reader) ([]domain.Credential, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), credSourceLineMaxBytes)
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

// parseTokens reads one secret per non-blank line, leaving the subject empty,
// streaming. The scanner's max token is the per-LINE bound (see parseJSONL).
func parseTokens(r io.Reader) ([]domain.Credential, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), credSourceLineMaxBytes)
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
	// Stream the parse (no whole-body buffer) under the cap: reading one byte past
	// the limit marks the file oversized. The cap check runs BEFORE any parse error
	// is surfaced — a truncated final line is a symptom of the oversize, not the
	// operator's real problem.
	cr := &countingReader{r: io.LimitReader(f, limit+1)}
	creds, perr := ParseReader(s.Format, cr)
	if cr.n > limit {
		return nil, fmt.Errorf("auth: file credential source %q exceeds the %s limit — raise it with auth.source.maxBytes", s.Path, humanizeBytes(limit))
	}
	if perr != nil {
		return nil, fmt.Errorf("auth: file credential source %q: %w", s.Path, perr)
	}
	return creds, nil
}

// countingReader counts the bytes actually read through it, so FileSource can
// tell "file bigger than the cap" apart from a clean EOF at the cap.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// generatorMaxCount caps how many credentials a pattern source materializes,
// matching the in-process pool ceiling (api.maxLocalPoolUsers) so a pattern
// cannot declare a pool larger than a single node can run.
const generatorMaxCount = 1_000_000

// GeneratorSource materializes a credential pool from a subject/secret TEMPLATE
// pair and a count, rendering {{.userIndex}} for i=0..Count-1 — so an operator
// can declare tens of thousands of accounts with a pattern instead of a file
// (auth.usersPattern). It renders with the auth package's own mini-template
// (the mint/exec convention), so it adds no dependency on the load package. The
// secret TEMPLATE is a secret: a GeneratorSource is built and materialized at
// Expand time and is NEVER carried as a non-secret reference (unlike FileSource/
// EnvSource), so the template string never rides the wire.
type GeneratorSource struct {
	subject *template.Template // nil when the subject template is empty
	secret  *template.Template
	count   int
}

// NewGeneratorSource parses the subject/secret templates and validates the count
// (positive and within the pool ceiling). The secret template is required; an
// empty subject template yields empty subjects (the opaque-token shape). A
// malformed template fails here; a template referencing an undefined variable
// fails on Load (missingkey=error), matching the mint strategy's strictness.
func NewGeneratorSource(subjectTemplate, secretTemplate string, count int) (*GeneratorSource, error) {
	if count <= 0 {
		return nil, fmt.Errorf("auth: pattern credential source needs a positive count")
	}
	if count > generatorMaxCount {
		return nil, fmt.Errorf("auth: pattern credential source count %d exceeds the %d limit — load the pool from a source file (auth.source) or distribute the run across workers", count, generatorMaxCount)
	}
	if strings.TrimSpace(secretTemplate) == "" {
		return nil, fmt.Errorf("auth: pattern credential source needs a secret (token/password) template")
	}
	g := &GeneratorSource{count: count}
	if strings.TrimSpace(subjectTemplate) != "" {
		t, err := parseClaimTemplate("usersPattern", "subject", subjectTemplate)
		if err != nil {
			return nil, err
		}
		g.subject = t
	}
	sec, err := parseClaimTemplate("usersPattern", "secret", secretTemplate)
	if err != nil {
		return nil, err
	}
	g.secret = sec
	return g, nil
}

// Load materializes the pool: for each index it renders the subject (if any) and
// the secret with {{.userIndex}} in scope. A render error (e.g. an undefined
// variable under missingkey=error) aborts with the failing index named.
func (g *GeneratorSource) Load(_ context.Context) ([]domain.Credential, error) {
	out := make([]domain.Credential, 0, g.count)
	for i := 0; i < g.count; i++ {
		data := map[string]string{"userIndex": strconv.Itoa(i)}
		var subject string
		if g.subject != nil {
			s, err := execTemplate(g.subject, data)
			if err != nil {
				return nil, fmt.Errorf("auth: usersPattern credential %d: render subject: %w (only {{.userIndex}} is available in usersPattern templates)", i, err)
			}
			subject = s
		}
		secret, err := execTemplate(g.secret, data)
		if err != nil {
			return nil, fmt.Errorf("auth: usersPattern credential %d: render secret: %w (only {{.userIndex}} is available in usersPattern templates)", i, err)
		}
		out = append(out, domain.Credential{Subject: subject, Secret: secret})
	}
	return out, nil
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
	return FileSource{Root: root, Path: ref.File, Format: format, MaxBytes: ref.MaxBytes}, nil
}

// humanizeBytes renders a byte count as the largest exact binary unit (B, KiB, MiB,
// GiB) so a cap error reads "512 MiB" instead of "536870912-byte". A count that is
// not an exact multiple of a larger unit falls back to the next unit down (a raw
// custom cap of 10 renders "10 B"), so the number stays exact — never rounded.
func humanizeBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n >= gib && n%gib == 0:
		return fmt.Sprintf("%d GiB", n/gib)
	case n >= mib && n%mib == 0:
		return fmt.Sprintf("%d MiB", n/mib)
	case n >= kib && n%kib == 0:
		return fmt.Sprintf("%d KiB", n/kib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// withinRoot reports whether path is root itself or lies inside it, comparing on
// path boundaries so "/a/rootx" is not treated as inside "/a/root".
func withinRoot(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}
