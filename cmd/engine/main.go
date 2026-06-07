// Command engine is the tmula entrypoint. The same binary runs as a
// self-contained local engine or as a master/worker in distributed mode,
// serving the control-plane API and the embedded web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/chordpli/tmula/internal/api"
	"github.com/chordpli/tmula/internal/cluster"
	"github.com/chordpli/tmula/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/web"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		// --fail-on-findings is an expected CI gate, not a crash: report it
		// plainly and exit 2 rather than logging a fatal error.
		if errors.Is(err, errFindings) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// run dispatches subcommands. `tmula run ...` executes a one-shot experiment
// from a scenario file (or flags) and prints the findings; every other
// invocation starts the long-running engine (the back-compatible default).
func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return runScenario(args[1:])
		case "init":
			return initScenario(args[1:])
		}
	}
	return serve(args)
}

// serve starts the long-running engine: the control-plane API + embedded UI
// (local/master role) or the gRPC worker service (worker role). It stays
// unit-testable (no os.Exit inside).
func serve(args []string) error {
	fs := flag.NewFlagSet("tmula", flag.ContinueOnError)
	var (
		roleStr    = fs.String("role", "local", "execution role: local | master | worker")
		addr       = fs.String("addr", ":8080", "HTTP listen address (control plane + UI)")
		workersStr = fs.String("workers", "", "comma-separated gRPC worker addresses; experiments without their own workers are distributed across these (blank = run locally)")
		showVer    = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVer {
		fmt.Println(version)
		return nil
	}

	role, err := domain.ParseRole(*roleStr)
	if err != nil {
		return err
	}
	slog.Info("starting tmula", "role", role, "addr", *addr, "version", version)

	// A worker serves the gRPC cluster service (master/local serve the HTTP
	// control plane + UI below).
	if role == domain.RoleWorker {
		return runWorker(*addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","role":%q,"version":%q}`, role, version)
	})

	// Control plane under /api; the embedded UI under everything else. A
	// --workers list (if any) becomes the default worker pool for experiments
	// that do not specify their own.
	defaultWorkers := splitCSV(*workersStr)
	if len(defaultWorkers) > 0 {
		slog.Info("default worker pool configured", "workers", defaultWorkers)
	}
	apiSrv := api.NewServer(load.NewRESTAdapter(30*time.Second), api.WithDefaultWorkers(defaultWorkers))
	mux.Handle("/api/", http.StripPrefix("/api", apiSrv.Handler()))
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		// Each shutdown gets its own 5s budget: a shared context would let a slow
		// in-flight-run drain consume the whole deadline, leaving srv.Shutdown no
		// time and leaking the HTTP listener.
		apiCtx, apiCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer apiCancel()
		_ = apiSrv.Shutdown(apiCtx) // cancel and drain in-flight runs first
		srvCtx, srvCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer srvCancel()
		return srv.Shutdown(srvCtx)
	case err := <-errCh:
		return err
	}
}

// runWorker serves the gRPC cluster service: a master distributes shards of a
// run to workers, which execute them and stream results back.
func runWorker(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("worker: listen on %s: %w", addr, err)
	}
	gs := grpc.NewServer()
	clusterpb.RegisterClusterServiceServer(gs, cluster.NewWorkerServer())
	slog.Info("worker serving gRPC cluster service", "addr", addr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		// GracefulStop makes Serve return grpc.ErrServerStopped; that is the normal
		// shutdown path, not a failure, so don't surface it as a serve error.
		if err := gs.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		gs.GracefulStop()
		return nil
	case err := <-errCh:
		return fmt.Errorf("worker: serve: %w", err)
	}
}

// splitCSV parses a comma-separated flag value into trimmed, non-empty entries.
// A blank value yields nil, which the control plane treats as "run locally".
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
