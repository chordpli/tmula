package auth

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestParseCredentialsCSV parses a header+rows CSV into credentials, requiring a
// token column, treating subject as optional, preserving source order, and
// tolerating blank lines and a trailing newline.
func TestParseCredentialsCSV(t *testing.T) {
	body := []byte("subject,token\nalice,tok-a\nbob,tok-b\n")
	got, err := Parse(CSV, body)
	if err != nil {
		t.Fatalf("Parse CSV: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(got))
	}
	if got[0].Subject != "alice" || got[0].Secret != "tok-a" {
		t.Errorf("row 0 = %+v, want alice/tok-a", got[0])
	}
	if got[1].Subject != "bob" || got[1].Secret != "tok-b" {
		t.Errorf("row 1 = %+v, want bob/tok-b", got[1])
	}

	// token-only header (subject optional) is accepted; subjects are empty.
	tokOnly, err := Parse(CSV, []byte("token\ntok-x\ntok-y\n"))
	if err != nil {
		t.Fatalf("Parse token-only CSV: %v", err)
	}
	if len(tokOnly) != 2 || tokOnly[0].Secret != "tok-x" || tokOnly[0].Subject != "" {
		t.Errorf("token-only CSV = %+v, want two empty-subject rows", tokOnly)
	}

	// A header missing the token column is rejected.
	if _, err := Parse(CSV, []byte("subject\nalice\n")); err == nil {
		t.Error("CSV without a token column should be rejected")
	}

	// An empty body (no data rows) errors.
	if _, err := Parse(CSV, []byte("subject,token\n")); err == nil {
		t.Error("CSV with a header but no rows should error")
	}
	if _, err := Parse(CSV, nil); err == nil {
		t.Error("empty CSV body should error")
	}
}

// TestParseCredentialsJSONL parses one {subject,token} object per line, ignoring
// blank lines and a trailing newline and preserving order.
func TestParseCredentialsJSONL(t *testing.T) {
	body := []byte(`{"subject":"alice","token":"tok-a"}
{"subject":"bob","token":"tok-b"}

`)
	got, err := Parse(JSONL, body)
	if err != nil {
		t.Fatalf("Parse JSONL: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d rows, want 2 (blank line ignored)", len(got))
	}
	if got[0].Subject != "alice" || got[0].Secret != "tok-a" {
		t.Errorf("row 0 = %+v, want alice/tok-a", got[0])
	}
	if got[1].Subject != "bob" || got[1].Secret != "tok-b" {
		t.Errorf("row 1 = %+v, want bob/tok-b", got[1])
	}

	// A line missing a token is rejected.
	if _, err := Parse(JSONL, []byte(`{"subject":"alice"}`)); err == nil {
		t.Error("JSONL line without a token should be rejected")
	}
	// Malformed JSON is rejected.
	if _, err := Parse(JSONL, []byte(`{not json}`)); err == nil {
		t.Error("malformed JSONL should be rejected")
	}
	// An empty body errors.
	if _, err := Parse(JSONL, []byte("\n\n")); err == nil {
		t.Error("JSONL with only blank lines should error")
	}
}

// TestParseCredentialsPlainTokens treats each non-blank line as one secret with
// an empty subject, ignoring blank lines and a trailing newline.
func TestParseCredentialsPlainTokens(t *testing.T) {
	body := []byte("tok-a\n\ntok-b\n")
	got, err := Parse(Tokens, body)
	if err != nil {
		t.Fatalf("Parse tokens: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d tokens, want 2 (blank line ignored)", len(got))
	}
	if got[0].Secret != "tok-a" || got[0].Subject != "" {
		t.Errorf("token 0 = %+v, want empty-subject tok-a", got[0])
	}
	if got[1].Secret != "tok-b" || got[1].Subject != "" {
		t.Errorf("token 1 = %+v, want empty-subject tok-b", got[1])
	}

	// A body with no non-blank line errors.
	if _, err := Parse(Tokens, []byte("\n  \n")); err == nil {
		t.Error("token body with no non-blank line should error")
	}
}

// TestParseUnknownFormat rejects a format outside the explicit vocabulary.
func TestParseUnknownFormat(t *testing.T) {
	if _, err := Parse(Format("yaml"), []byte("x")); err == nil {
		t.Error("unknown format should be rejected")
	}
}

// TestInlineSourceLoad returns a defensive copy of the entries and errors when
// empty.
func TestInlineSourceLoad(t *testing.T) {
	entries := []domain.Credential{{Subject: "a", Secret: "s"}}
	src := InlineSource{Entries: entries}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Subject != "a" || got[0].Secret != "s" {
		t.Fatalf("Load = %+v, want one a/s entry", got)
	}
	// Mutating the returned slice must not corrupt the source's backing array.
	got[0].Secret = "tampered"
	if entries[0].Secret != "s" {
		t.Error("InlineSource.Load did not return a defensive copy")
	}

	if _, err := (InlineSource{}).Load(context.Background()); err == nil {
		t.Error("empty InlineSource should error")
	}
}

// TestEnvSourceLoad reads and parses credentials from an environment variable.
func TestEnvSourceLoad(t *testing.T) {
	t.Setenv("TMULA_TEST_CREDS", "tok-a\ntok-b\n")
	src := EnvSource{Var: "TMULA_TEST_CREDS", Format: Tokens}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Secret != "tok-a" || got[1].Secret != "tok-b" {
		t.Fatalf("Load = %+v, want two tokens", got)
	}
}

// TestEnvSourceMissing errors when the variable is empty and never echoes its
// value into the error.
func TestEnvSourceMissing(t *testing.T) {
	const secretValue = "do-not-echo-this-secret"
	t.Setenv("TMULA_TEST_EMPTY", "")
	src := EnvSource{Var: "TMULA_TEST_EMPTY", Format: Tokens}
	_, err := src.Load(context.Background())
	if err == nil {
		t.Fatal("empty env var should error")
	}
	if !strings.Contains(err.Error(), "TMULA_TEST_EMPTY") {
		t.Errorf("error should name the variable, got: %v", err)
	}

	// Even when the var holds a value the error path must never echo it. Here we
	// prove the unset case names the var without leaking any value.
	t.Setenv("TMULA_TEST_SECRET", secretValue)
	bad := EnvSource{Var: "TMULA_TEST_SECRET", Format: Format("nope")}
	if _, err := bad.Load(context.Background()); err == nil {
		t.Error("bad format should error")
	} else if strings.Contains(err.Error(), secretValue) {
		t.Errorf("error leaked the env var value: %v", err)
	}
}

// TestFileSourceLoad reads and parses credentials from a file within Root.
func TestFileSourceLoad(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "creds.csv")
	if err := os.WriteFile(path, []byte("subject,token\nalice,tok-a\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src := FileSource{Root: root, Path: "creds.csv", Format: CSV}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Subject != "alice" || got[0].Secret != "tok-a" {
		t.Fatalf("Load = %+v, want alice/tok-a", got)
	}
}

// TestFileSourceRejectsTraversal refuses a path that escapes Root via ".." and
// proves no file outside Root is read.
func TestFileSourceRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.csv")
	if err := os.WriteFile(outside, []byte("subject,token\nmallory,tok-m\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	src := FileSource{Root: root, Path: "../outside.csv", Format: CSV}
	got, err := src.Load(context.Background())
	if err == nil {
		t.Fatalf("traversal path should be rejected, got %+v", got)
	}
	// The error must not contain the secret the outside file held: nothing was read.
	if strings.Contains(err.Error(), "tok-m") {
		t.Error("traversal rejection appears to have read the outside file")
	}

	// A path with a NUL byte is rejected outright.
	if _, err := (FileSource{Root: root, Path: "creds\x00.csv", Format: CSV}).Load(context.Background()); err == nil {
		t.Error("path with a NUL byte should be rejected")
	}
}

// TestFileSourceRejectsSymlinkEscape refuses a symlink inside Root that points
// to a file outside Root — the case filepath.Clean+HasPrefix alone cannot catch.
func TestFileSourceRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	secretFile := filepath.Join(outsideDir, "secret.csv")
	if err := os.WriteFile(secretFile, []byte("subject,token\nmallory,tok-m\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(root, "link.csv")
	if err := os.Symlink(secretFile, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	src := FileSource{Root: root, Path: "link.csv", Format: CSV}
	_, err := src.Load(context.Background())
	if err == nil {
		t.Fatal("a symlink escaping Root must be rejected")
	}
	if strings.Contains(err.Error(), "tok-m") {
		t.Error("symlink rejection appears to have read the escaped file")
	}
}

// TestFileSourceRejectsOversize refuses a file larger than MaxBytes without
// returning its parsed contents.
func TestFileSourceRejectsOversize(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big.tokens")
	// Two tokens, but a 4-byte cap that the very first line already exceeds.
	if err := os.WriteFile(path, []byte("tok-aaaa\ntok-bbbb\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src := FileSource{Root: root, Path: "big.tokens", Format: Tokens, MaxBytes: 4}
	if _, err := src.Load(context.Background()); err == nil {
		t.Fatal("a file larger than MaxBytes should be rejected")
	}

	// A file at or under the cap is accepted (defaults apply when MaxBytes is 0).
	small := filepath.Join(root, "small.tokens")
	if err := os.WriteFile(small, []byte("tok-a\n"), 0o600); err != nil {
		t.Fatalf("write small: %v", err)
	}
	if _, err := (FileSource{Root: root, Path: "small.tokens", Format: Tokens}).Load(context.Background()); err != nil {
		t.Errorf("a small file under the default cap should load: %v", err)
	}
}

// TestFileSourceMissing errors when the file does not exist.
func TestFileSourceMissing(t *testing.T) {
	root := t.TempDir()
	src := FileSource{Root: root, Path: "nope.csv", Format: CSV}
	if _, err := src.Load(context.Background()); err == nil {
		t.Error("a missing file should error")
	}
}

// TestParseReaderMatchesParse pins the streaming decoder against the in-memory
// one: same rows, same order, for every format.
func TestParseReaderMatchesParse(t *testing.T) {
	cases := []struct {
		format Format
		body   string
	}{
		{CSV, "subject,token\na,t-a\nb,t-b\n"},
		{JSONL, `{"subject":"a","token":"t-a"}` + "\n" + `{"token":"t-b"}` + "\n"},
		{Tokens, "t-a\n\nt-b\n"},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			want, err := Parse(tc.format, []byte(tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got, err := ParseReader(tc.format, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("ParseReader: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("rows = %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// TestFileSourceDefaultCapFitsLargeJWTPools: ~300k JWTs are hundreds of MB, so
// the default cap must comfortably load a file well past the old 8 MiB — pin it
// with a 9 MiB synthetic tokens file that the old default rejected.
func TestFileSourceDefaultCapFitsLargeJWTPools(t *testing.T) {
	root := t.TempDir()
	var b bytes.Buffer
	// ~9 MiB of 1KiB-ish token lines.
	line := strings.Repeat("x", 1024)
	for b.Len() < 9<<20 {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(root, "big.tokens"), b.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src := FileSource{Root: root, Path: "big.tokens", Format: Tokens}
	creds, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("a 9 MiB pool must load under the default cap now: %v", err)
	}
	if len(creds) < 9000 {
		t.Errorf("rows = %d, want >= 9000", len(creds))
	}
}

// TestFileSourceOversizeKeepsErrorMessage pins the cap error's exact shape across
// the streaming rewrite, and that the cap error WINS over any parse error the
// truncation causes.
func TestFileSourceOversizeKeepsErrorMessage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.jsonl"), []byte(`{"token":"abcdefgh"}`+"\n"+`{"token":"ijklmnop"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src := FileSource{Root: root, Path: "big.jsonl", Format: JSONL, MaxBytes: 10}
	_, err := src.Load(context.Background())
	if err == nil {
		t.Fatal("a file over MaxBytes must be rejected")
	}
	want := `auth: file credential source "big.jsonl" exceeds the 10-byte limit`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

// TestJSONLLineBufferDecoupledFromCap: the per-line scanner buffer is its own
// bound (a line is a credential, not a file) — a 100 KiB line parses fine, and a
// line over the 1 MiB line bound errors as a scan failure rather than OOMing.
func TestJSONLLineBufferDecoupledFromCap(t *testing.T) {
	long := `{"token":"` + strings.Repeat("t", 100<<10) + `"}`
	creds, err := ParseReader(JSONL, strings.NewReader(long+"\n"))
	if err != nil {
		t.Fatalf("a 100 KiB line must parse: %v", err)
	}
	if len(creds) != 1 || len(creds[0].Secret) != 100<<10 {
		t.Fatalf("parsed %d creds, secret len %d", len(creds), len(creds[0].Secret))
	}

	over := `{"token":"` + strings.Repeat("t", (1<<20)+64) + `"}`
	if _, err := ParseReader(JSONL, strings.NewReader(over+"\n")); err == nil {
		t.Fatal("a line over the 1 MiB line bound must error")
	}
}

// TestSourceFromRefThreadsMaxBytes: the reference's maxBytes override reaches the
// file source (a worker resolving a shipped reference honors the same cap the
// authoring side declared).
func TestSourceFromRefThreadsMaxBytes(t *testing.T) {
	src, err := SourceFromRef(domain.CredentialSourceRef{File: "u.csv", Format: "csv", MaxBytes: 42}, ".")
	if err != nil {
		t.Fatalf("SourceFromRef: %v", err)
	}
	fs, ok := src.(FileSource)
	if !ok {
		t.Fatalf("source = %T, want FileSource", src)
	}
	if fs.MaxBytes != 42 {
		t.Errorf("MaxBytes = %d, want 42", fs.MaxBytes)
	}
}

// TestGeneratorSourceMaterializes renders subject+secret templates for i=0..N-1,
// substituting {{.userIndex}}, in order.
func TestGeneratorSourceMaterializes(t *testing.T) {
	src, err := NewGeneratorSource("user{{.userIndex}}", "pw-{{.userIndex}}", 3)
	if err != nil {
		t.Fatalf("NewGeneratorSource: %v", err)
	}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []domain.Credential{
		{Subject: "user0", Secret: "pw-0"},
		{Subject: "user1", Secret: "pw-1"},
		{Subject: "user2", Secret: "pw-2"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestGeneratorSourceEmptySubjectOK: a pattern may generate opaque secrets with
// no subject (subject template empty → empty subject), the tokens-style shape.
func TestGeneratorSourceEmptySubjectOK(t *testing.T) {
	src, err := NewGeneratorSource("", "tok-{{.userIndex}}", 2)
	if err != nil {
		t.Fatalf("NewGeneratorSource: %v", err)
	}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Subject != "" || got[0].Secret != "tok-0" {
		t.Fatalf("rows = %+v, want empty-subject tok-0/tok-1", got)
	}
}

// TestGeneratorSourceRejectsBadInput: a non-positive count, a count over the cap,
// an empty secret template, and a malformed template all fail loudly at build.
func TestGeneratorSourceRejectsBadInput(t *testing.T) {
	if _, err := NewGeneratorSource("u{{.userIndex}}", "pw", 0); err == nil {
		t.Error("count 0 should be rejected")
	}
	if _, err := NewGeneratorSource("u{{.userIndex}}", "pw", generatorMaxCount+1); err == nil {
		t.Error("count over the cap should be rejected")
	}
	if _, err := NewGeneratorSource("u{{.userIndex}}", "", 1); err == nil {
		t.Error("an empty secret template should be rejected")
	}
	if _, err := NewGeneratorSource("u{{.userInxed}}", "pw", 1); err != nil {
		// A typo'd var is a build-time render error (missingkey=error) on Load, not
		// at construction; assert Load surfaces it.
		t.Fatalf("construction should not fail on a parseable-but-wrong var: %v", err)
	}
	badVar, _ := NewGeneratorSource("u{{.userInxed}}", "pw", 1)
	if _, err := badVar.Load(context.Background()); err == nil {
		t.Error("a template referencing an undefined var should fail on Load")
	}
	if _, err := NewGeneratorSource("u{{.userIndex", "pw", 1); err == nil {
		t.Error("a malformed template should be rejected at construction")
	}
}
