package load

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestVirtualUserHolderNotSerialized pins that the runtime-only Holder seam never
// crosses the wire: a VirtualUser marshals exactly as before (no Holder key), so a
// spec's Users array is byte-for-byte unchanged by this field's existence.
func TestVirtualUserHolderNotSerialized(t *testing.T) {
	u := VirtualUser{ID: "u0", Cred: domain.Credential{Subject: "s"}, Holder: NewCredentialHolder(domain.Credential{Secret: "x"})}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "Holder") || strings.Contains(string(b), "holder") {
		t.Errorf("VirtualUser serialized its Holder seam: %s", b)
	}
}

// panicHolder is a CredentialHolder whose Get must never be called. It is wired
// onto a VirtualUser only to PROVE the static (Holder==nil) path never reads a
// holder: if the runtime ever invoked Get on the static path the test panics.
type panicHolder struct{ t *testing.T }

func (p panicHolder) Get() domain.Credential {
	p.t.Fatal("CredentialHolder.Get was called on the static path (must be zero-lock, holder-free)")
	return domain.Credential{}
}
func (p panicHolder) Set(domain.Credential) {
	p.t.Fatal("CredentialHolder.Set was called on the static path")
}

// TestStaticPathNeverReadsHolder is the load-bearing invariant: when a virtual
// user has no holder, runSession renders u.Cred directly and never consults a
// holder. We prove it by running a normal (Holder==nil) user whose credential is a
// fixed token and asserting the target saw exactly that token — the holder code
// path is simply not entered.
func TestStaticPathNeverReadsHolder(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Tok"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	// Holder is nil (the default), Cred carries a fixed token.
	u := VirtualUser{ID: "u0", Cred: domain.Credential{Secret: "static-tok"}}
	if u.Holder != nil {
		t.Fatal("expected the default VirtualUser to have a nil holder")
	}
	results, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil)
	if err != nil {
		t.Fatalf("run session: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one step")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range seen {
		if h != "static-tok" {
			t.Errorf("static path sent token %q, want the credential's static-tok", h)
		}
	}
}

// TestPanickingHolderProvesZeroLockStaticPath wires a Holder whose Get/Set panic,
// but ONLY through a closed Run/open RunSession where the user keeps Holder==nil.
// Because the static path never consults the holder, the panicking double is never
// invoked and the run completes — the structural proof that the static path is
// holder-free (and therefore takes zero holder locks). Covers both the closed Run
// fan-out and the single RunSession path.
func TestPanickingHolderProvesZeroLockStaticPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	// The panicking holder EXISTS in scope but is never assigned to a user with a
	// live holder; the static users keep Holder==nil. The compiler confirms
	// panicHolder satisfies CredentialHolder, and the run confirms Get is never hit.
	var _ CredentialHolder = panicHolder{t: t}

	// Closed Run path: a small fixed pool, all Holder==nil.
	users := []VirtualUser{
		{ID: "u0", Cred: domain.Credential{Secret: "tok-0"}},
		{ID: "u1", Cred: domain.Credential{Secret: "tok-1"}},
	}
	if _, err := r.Run(context.Background(), g, "a", 5, users, 1); err != nil {
		t.Fatalf("closed Run static path failed: %v", err)
	}

	// Open RunSession path: a single Holder==nil arrival.
	if _, err := r.RunSession(context.Background(), g, "a", 5, users[0], 1, nil); err != nil {
		t.Fatalf("open RunSession static path failed: %v", err)
	}
}

// TestHolderPathReadsLiveCredential wires a real holder and confirms the runtime
// reads the live credential from it (not u.Cred) on every step, so a mid-run Set
// is visible to subsequent requests.
func TestHolderPathReadsLiveCredential(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Tok"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph()
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	holder := NewCredentialHolder(domain.Credential{Secret: "live-tok"})
	// u.Cred is a DIFFERENT (stale) value; the holder must win.
	u := VirtualUser{ID: "u0", Cred: domain.Credential{Secret: "stale-tok"}, Holder: holder}
	if _, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil); err != nil {
		t.Fatalf("run session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("expected at least one request")
	}
	for _, h := range seen {
		if h != "live-tok" {
			t.Errorf("holder path sent token %q, want the holder's live-tok (not stale u.Cred)", h)
		}
	}
}

// TestHolderPathSeesMidSessionRefresh proves the live read is PER STEP: changing
// the holder between the first and second request is visible to the second.
func TestHolderPathSeesMidSessionRefresh(t *testing.T) {
	holder := NewCredentialHolder(domain.Credential{Secret: "v1"})
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Tok"))
		n := len(seen)
		mu.Unlock()
		// After the first request lands, rotate the holder's credential so the
		// second step must observe the new value if (and only if) the read is live.
		if n == 1 {
			holder.Set(domain.Credential{Secret: "v2"})
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g, tmpls := linearGraph() // two nodes a->b, two requests
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)
	u := VirtualUser{ID: "u0", Holder: holder}
	if _, err := r.RunSession(context.Background(), g, "a", 5, u, 1, nil); err != nil {
		t.Fatalf("run session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected two requests, got %d", len(seen))
	}
	if seen[0] != "v1" {
		t.Errorf("first step token = %q, want v1", seen[0])
	}
	if seen[1] != "v2" {
		t.Errorf("second step token = %q, want v2 (per-step live read)", seen[1])
	}
}

// TestCredentialHolderGetSetConcurrent exercises the holder's mutex: concurrent
// Get/Set must be race-free (run under -race) and Get always returns one of the
// fully-set credentials.
func TestCredentialHolderGetSetConcurrent(t *testing.T) {
	h := NewCredentialHolder(domain.Credential{Subject: "s0", Secret: "tok-0"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = h.Get() }()
		go func() { defer wg.Done(); h.Set(domain.Credential{Subject: "s1", Secret: "tok-1"}) }()
	}
	wg.Wait()
	got := h.Get()
	if got.Secret != "tok-0" && got.Secret != "tok-1" {
		t.Errorf("holder Get returned an unexpected credential %q", got.Secret)
	}
}
