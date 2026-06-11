// Command tmula is the tmula entrypoint. The same binary runs as a
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

	"github.com/chordpli/tmula/server/internal/api"
	"github.com/chordpli/tmula/server/internal/cluster"
	"github.com/chordpli/tmula/server/internal/cluster/clusterpb"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/store"
	"github.com/chordpli/tmula/server/internal/web"
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
		case "bench":
			return runBench(args[1:])
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
		storePath  = fs.String("store", "", "local role: JSON snapshot file; loaded on start and written on graceful shutdown so run history survives a restart (blank = in-memory only)")
		dbDSN      = fs.String("db-dsn", "", "master role: Postgres DSN for the durable store (falls back to in-memory when blank; env TMULA_DB_DSN is used if this flag is unset)")
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

	// Persistence backend (system of record for finalized runs). Local uses an
	// in-memory store, optionally snapshotting to --store; master uses Postgres
	// when a DSN is given and otherwise falls back to in-memory. closeStore runs
	// on shutdown (snapshot the local store / close the Postgres pool).
	persistStore, closeStore, err := setupStore(role, *storePath, resolveDSN(*dbDSN))
	if err != nil {
		return err
	}
	defer closeStore()

	apiSrv := api.NewServer(load.NewRESTAdapter(30*time.Second),
		api.WithDefaultWorkers(defaultWorkers),
		api.WithStore(persistStore),
		// Let the UI prefill an experiment from an uploaded OpenAPI spec, HAR
		// capture, or access log. The conversion lives in cmd/tmula (which already
		// depends on importer + scenariofile) and is injected so the api package
		// avoids the cycle.
		api.WithImporter(importRunSpec),
	)
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

// resolveDSN returns the Postgres DSN: the --db-dsn flag when set, else the
// TMULA_DB_DSN environment variable. The flag wins so an explicit value can
// override an inherited environment.
func resolveDSN(flagDSN string) string {
	if strings.TrimSpace(flagDSN) != "" {
		return flagDSN
	}
	return strings.TrimSpace(os.Getenv("TMULA_DB_DSN"))
}

// setupStore builds the persistence backend for the control plane and a closer
// to run on shutdown. The closer is always non-nil, so callers can defer it
// unconditionally.
//
//   - master with a DSN: a Postgres store (migrated on start); the closer closes
//     the pool. A connect/migrate failure is fatal — a master asked for Postgres
//     should not silently degrade to memory.
//   - every other case (local, or master without a DSN): an in-memory store. When
//     dataPath is set its snapshot is loaded on start (a missing file is fine on
//     first run) and written by the closer on shutdown, so a restart keeps history.
//
// The zero-config `--role local` path takes the in-memory branch with no file and
// a no-op closer, so it never touches the disk or a database.
func setupStore(role domain.Role, dataPath, dataDSN string) (store.Store, func(), error) {
	noop := func() {}

	if role == domain.RoleMaster && dataDSN != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pg, err := store.NewPostgresStore(ctx, dataDSN)
		if err != nil {
			return nil, noop, fmt.Errorf("setup store: %w", err)
		}
		if err := pg.Migrate(ctx); err != nil {
			pg.Close()
			return nil, noop, fmt.Errorf("setup store: migrate: %w", err)
		}
		slog.Info("using postgres store")
		return pg, pg.Close, nil
	}

	mem := store.NewMemStore()
	if dataPath == "" {
		slog.Info("using in-memory store (no snapshot file)")
		return mem, noop, nil
	}
	if err := mem.LoadFromFile(dataPath); err != nil {
		// A missing file is expected on first run; only a real read/parse error is
		// worth surfacing. Either way startup proceeds with an empty store.
		if errors.Is(err, os.ErrNotExist) {
			slog.Info("no existing store snapshot; starting empty", "path", dataPath)
		} else {
			slog.Warn("could not load store snapshot; starting empty", "path", dataPath, "err", err)
		}
	} else {
		slog.Info("loaded store snapshot", "path", dataPath)
	}
	closer := func() {
		if err := mem.SaveToFile(dataPath); err != nil {
			slog.Error("could not write store snapshot", "path", dataPath, "err", err)
			return
		}
		slog.Info("wrote store snapshot", "path", dataPath)
	}
	return mem, closer, nil
}
