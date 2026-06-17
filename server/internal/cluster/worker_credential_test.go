package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// TestResolveProvider pins the worker's source resolution: a source-less spec
// yields no provider (the run is unauthenticated, byte-for-byte as before), and
// a file-backed spec yields a PoolProvider whose Acquire keys by GLOBAL index so
// every worker reconstructs the same index-deterministic assignment.
func TestResolveProvider(t *testing.T) {
	t.Parallel()

	t.Run("source-less spec yields no provider", func(t *testing.T) {
		t.Parallel()
		w := NewWorkerServer()
		provider, err := w.resolveProvider(context.Background(), baseValidSpec())
		if err != nil {
			t.Fatalf("source-less resolveProvider must not error, got: %v", err)
		}
		if provider != nil {
			t.Fatal("a source-less spec must resolve to a nil provider (unauthenticated)")
		}
	})

	t.Run("file source yields an index-deterministic provider", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "creds.tokens"), []byte("tok-a\ntok-b\ntok-c\n"), 0o600); err != nil {
			t.Fatalf("write creds: %v", err)
		}
		w := NewWorkerServer(WithCredentialRoot(dir))
		s := baseValidSpec()
		s.CredentialSource = &domain.CredentialSourceRef{File: "creds.tokens", Format: "tokens"}

		provider, err := w.resolveProvider(context.Background(), s)
		if err != nil {
			t.Fatalf("resolveProvider: %v", err)
		}
		if provider == nil {
			t.Fatal("a file source must resolve to a provider")
		}
		// Acquire keys by global index and wraps around the 3 entries.
		want := []string{"tok-a", "tok-b", "tok-c", "tok-a", "tok-b"}
		for g, w := range want {
			cred, err := provider.Acquire(context.Background(), g)
			if err != nil {
				t.Fatalf("acquire %d: %v", g, err)
			}
			if cred.Secret != w {
				t.Errorf("global index %d: got secret %q, want %q", g, cred.Secret, w)
			}
		}
	})
}

// TestRunShardAuthenticatesByGlobalIndex is the end-to-end proof that a worker
// running a SUB-RANGE of the global pool authenticates each user as
// entries[globalIndex % N]: the SUT records the Authorization token it saw per
// user and we assert the second shard (offset 2) sent the tokens for global
// indices 2 and 3, not local 0 and 1.
func TestRunShardAuthenticatesByGlobalIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "creds.tokens"), []byte("tok-0\ntok-1\ntok-2\ntok-3\n"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	var mu sync.Mutex
	seen := map[string]bool{}
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")] = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)

	conn := startWorker(t, WithAdapter(load.NewRESTAdapter(2*time.Second)), WithCredentialRoot(dir))
	client := clusterpb.NewClusterServiceClient(conn)

	spec := authLinearSpec(sut.URL)
	spec.CredentialSource = &domain.CredentialSourceRef{File: "creds.tokens", Format: "tokens"}
	specJSON, err := encodeSpec(spec)
	if err != nil {
		t.Fatalf("encode spec: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run the SECOND shard only: global users 2 and 3.
	stream, err := client.RunShard(ctx, &clusterpb.RunShardRequest{
		SpecJson: specJSON, UserOffset: 2, UserCount: 2, Seed: spec.Seed, MaxSteps: int32(spec.MaxSteps), StartNode: string(spec.Start),
	})
	if err != nil {
		t.Fatalf("RunShard: %v", err)
	}
	for {
		if _, rerr := stream.Recv(); rerr != nil {
			break
		}
	}

	mu.Lock()
	defer mu.Unlock()
	got := make([]string, 0, len(seen))
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"Bearer tok-2", "Bearer tok-3"}
	if len(got) != len(want) {
		t.Fatalf("got tokens %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got tokens %v, want %v", got, want)
		}
	}
}

// authLinearSpec is a single-node spec whose template sends the credential token
// as an Authorization: Bearer header, so the SUT can observe which principal each
// shard user authenticated as.
func authLinearSpec(baseURL string) ShardSpec {
	tmpl := domain.APITemplate{
		ID: "t1", Protocol: domain.ProtocolREST, Method: http.MethodGet, Path: "/one",
		Headers: map[string]string{"Authorization": "Bearer {{.token}}"},
	}
	return ShardSpec{
		Graph: domain.ScenarioGraph{
			ID:    "g1",
			Nodes: []domain.Node{{ID: "n1", APITemplateID: "t1"}},
		},
		Templates:     map[domain.ID]domain.APITemplate{"t1": tmpl},
		TargetBaseURL: baseURL,
		Start:         "n1",
		MaxSteps:      1,
		Seed:          1,
	}
}
