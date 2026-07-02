package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestDistributedMintRunAcrossWorkers pins the P4 distributed-mint carve-out end
// to end: a mint pool + two workers, where each worker resolves the SAME signing
// key from an env reference (never the wire) and self-issues a JWT per GLOBAL
// index. The SUT verifies every request's bearer JWT against the shared secret
// and records its sub — proving each worker signed with the right key for the
// right principal, with zero unauthorized requests.
func TestDistributedMintRunAcrossWorkers(t *testing.T) {
	const secret = "shared-mint-secret"
	t.Setenv("TMULA_MINT_KEY", secret)

	var mu sync.Mutex
	unauthorized := 0
	subs := map[string]bool{}
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub, ok := verifyHS256(r.Header.Get("Authorization"), secret)
		mu.Lock()
		if !ok {
			unauthorized++
		} else {
			subs[sub] = true
		}
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	w1, stop1 := startWorker(t)
	defer stop1()
	w2, stop2 := startWorker(t)
	defer stop2()

	cp, closeCP := newCP(t)
	defer closeCP()

	const users = 6
	spec := specFor(sut.URL, users)
	spec.Graph = domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}}
	spec.Templates = map[domain.ID]domain.APITemplate{
		"ta": {Method: "GET", Path: "/a", Headers: map[string]string{"Authorization": "Bearer {{.token}}"}},
	}
	spec.Start = "a"
	spec.MaxSteps = 1
	spec.Experiment.Params.AuthStrategy = domain.CredMint
	spec.CredentialPool = &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg:            domain.MintHS256,
			SecretEncoding: domain.MintEncodingRaw,
			Key:            &domain.CredentialSourceRef{Env: "TMULA_MINT_KEY"},
			Subject:        "user-{{.userIndex}}",
			TTL:            time.Hour,
		},
	}
	spec.Workers = []string{w1, w2}

	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d", resp.StatusCode)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)
	if report.Workers != 2 {
		t.Errorf("report workers = %d, want 2", report.Workers)
	}
	if report.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0", report.Stats.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	if unauthorized != 0 {
		t.Errorf("SUT saw %d unauthorized requests, want 0 (every worker must sign with the resolved key)", unauthorized)
	}
	// Each of the 6 global indices minted its own principal.
	for i := 0; i < users; i++ {
		want := "user-" + itoa(i)
		if !subs[want] {
			t.Errorf("SUT never saw a token for %q — a global index did not mint its principal (subs=%v)", want, subs)
		}
	}
}

// verifyHS256 checks an "Authorization: Bearer <jws>" header is a valid HS256 JWS
// signed with secret and returns its sub claim. It is the SUT's stand-in for a
// real resource server validating a self-issued token.
func verifyHS256(header, secret string) (sub string, ok bool) {
	const p = "Bearer "
	if !strings.HasPrefix(header, p) {
		return "", false
	}
	parts := strings.Split(header[len(p):], ".")
	if len(parts) != 3 {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(wantSig), []byte(parts[2])) {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	return claims.Sub, true
}

// TestShardSpecForCarriesMint pins the distributed-mint mapping: a mint pool +
// workers copies the mint spec (key REFERENCE only) onto the shard, an inline or
// nil pool leaves it nil, and the serialized shard never carries a key body.
func TestShardSpecForCarriesMint(t *testing.T) {
	mintPool := &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg: domain.MintHS256, SecretEncoding: domain.MintEncodingRaw,
			Key: &domain.CredentialSourceRef{Env: "TMULA_MINT_KEY"}, Subject: "user-{{.userIndex}}", TTL: time.Hour,
		},
	}
	spec := specFor("http://127.0.0.1:1", 4)
	spec.CredentialPool = mintPool

	got := shardSpecFor(spec, "run-1")
	if got.Mint == nil {
		t.Fatal("mint pool did not copy its spec onto the shard")
	}
	if got.Mint.Key == nil || got.Mint.Key.Env != "TMULA_MINT_KEY" {
		t.Errorf("mint key reference not copied faithfully: %+v", got.Mint.Key)
	}
	if got.CredentialSource != nil {
		t.Errorf("a mint pool must not place a credential source on the shard, got %+v", got.CredentialSource)
	}

	// The resolved key must never serialize even if one were resolved onto the spec.
	withKey := *mintPool.Mint
	withKey = withKey.WithResolvedKey([]byte("SUPER-SECRET-KEY-BYTES"))
	spec.CredentialPool = &domain.CredentialPool{ID: "p", Strategy: domain.CredMint, Mint: &withKey}
	shard := shardSpecFor(spec, "run-1")
	b, err := json.Marshal(shard)
	if err != nil {
		t.Fatalf("marshal shard: %v", err)
	}
	if strings.Contains(string(b), "SUPER-SECRET-KEY-BYTES") {
		t.Errorf("serialized shard leaked the resolved mint key: %s", b)
	}
}
