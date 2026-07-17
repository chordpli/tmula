package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// execPool builds a CredExec pool whose command re-execs the test binary as a token
// printer (auth's TestMain helper), so the exec run is self-contained and OS-portable.
func execPool() *domain.CredentialPool {
	return &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredExec,
		Exec: &domain.ExecSpec{
			Command: append([]string{os.Args[0]}, "g"),
			Env:     map[string]string{"TMULA_EXEC_HELPER": "bare", "TMULA_EXEC_IDX": "{{.userIndex}}"},
			Timeout: 5 * time.Second,
		},
	}
}

// startExecRunOn creates an exec spec on srv (against sutURL) and starts it, returning
// the StartRun error (nil when the run launched) and the run id. It is the assertion
// surface for the operator opt-in gate, which must fire at StartRun — before any
// provider/runner — exactly like the prod-lock gate.
func startExecRunOn(t *testing.T, srv *Server, sutURL string) (domain.ID, error) {
	t.Helper()
	spec := specAuth(sutURL, 2, execPool())
	spec.Experiment.Params.AuthStrategy = domain.CredExec
	id, err := srv.CreateExperiment(spec)
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	return srv.StartRun(id)
}

// TestStartRunExecRejectedWithoutOptIn proves a scenario carrying strategy:"exec" does
// NOT run anything without the explicit operator opt-in: the default server rejects it
// at StartRun with a loud error, before any command runs. A scenario file is untrusted,
// so merely declaring exec must never execute a local command.
func TestStartRunExecRejectedWithoutOptIn(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	_, err := startExecRunOn(t, srv, "http://127.0.0.1:0")
	if err == nil {
		t.Fatal("an exec run without the opt-in must be rejected at StartRun")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "exec") || !strings.Contains(msg, "allow") {
		t.Errorf("rejection should explain the exec opt-in (how to enable it), got %q", err.Error())
	}
	// The message must also tell a non-operator what to ask for, so someone who does
	// not run the engine knows the flag is the engine operator's to set.
	if !strings.Contains(msg, "ask its operator to start it with --allow-exec") {
		t.Errorf("rejection should tell a non-operator to ask the engine operator to enable exec, got %q", err.Error())
	}
}

// TestStartRunExecAllowedWithOptIn runs an opt-in exec run to completion and confirms
// the SUT saw distinct per-VU Authorization headers, proving the opt-in lets the run
// proceed AND that the exec-backed LoginProvider authenticated each virtual user with
// its own command-minted token.
func TestStartRunExecAllowedWithOptIn(t *testing.T) {
	sut, rec := newAuthEchoSUT()
	defer sut.Close()
	srv := NewServer(load.NewRESTAdapter(2*time.Second), WithAllowExec(true))
	id, err := startExecRunOn(t, srv, sut.URL)
	if err != nil {
		t.Fatalf("an exec run WITH the opt-in should proceed: %v", err)
	}
	rep := pollRun(t, srv, id, 10*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("exec run status = %q, want completed", rep.Run.Status)
	}
	if got := rec.distinct(); len(got) < 2 {
		t.Fatalf("expected 2 distinct per-VU tokens, got %v", got)
	}
}

// pollRun polls a started run until it reaches a terminal state (or the deadline).
func pollRun(t *testing.T, srv *Server, runID domain.ID, timeout time.Duration) Report {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rep, ok := srv.Report(runID)
		if !ok {
			t.Fatalf("run %s not found", runID)
		}
		switch rep.Run.Status {
		case domain.RunCompleted, domain.RunFailed, domain.RunKilled:
			return rep
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish within %s (last status %q)", timeout, rep.Run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestReproduceExecRejectedWithoutOptIn proves the exec opt-in gate ALSO guards the
// reproduce path, not just StartRun: sessionUser — which builds the credential provider
// and would RUN the operator's command for an exec finding — refuses without the opt-in.
// A scenario merely declaring exec must never execute a command on reproduce either.
func TestReproduceExecRejectedWithoutOptIn(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second)) // allowExec defaults to false
	spec := specAuth("http://127.0.0.1:0", 1, execPool())
	_, err := srv.sessionUser(context.Background(), spec, domain.EvidenceSession{SessionID: "s", UserIndex: 0}, nil)
	if err == nil {
		t.Fatal("reproduce of an exec run without the opt-in must be rejected")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "exec") || !strings.Contains(msg, "allow") {
		t.Errorf("rejection should explain the exec opt-in, got %q", err.Error())
	}
}

// TestReproduceExecAllowedWithOptIn confirms the gate does NOT block reproduce once the
// operator opted in: sessionUser proceeds past the gate, builds the exec provider, and
// Acquires a command-minted credential for the replayed principal.
func TestReproduceExecAllowedWithOptIn(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2*time.Second), WithAllowExec(true))
	spec := specAuth("http://127.0.0.1:0", 1, execPool())
	user, err := srv.sessionUser(context.Background(), spec, domain.EvidenceSession{SessionID: "s", UserIndex: 0}, nil)
	if err != nil {
		t.Fatalf("reproduce WITH the opt-in should proceed: %v", err)
	}
	if user.Cred.Secret == "" {
		t.Error("expected a command-minted credential on the reproduce user")
	}
}
