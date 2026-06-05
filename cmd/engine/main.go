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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chordpli/tmula/internal/api"
	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
	"github.com/chordpli/tmula/internal/web"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// run wires up the process so it stays unit-testable (no os.Exit inside).
func run(args []string) error {
	fs := flag.NewFlagSet("tmula", flag.ContinueOnError)
	var (
		roleStr = fs.String("role", "local", "execution role: local | master | worker")
		addr    = fs.String("addr", ":8080", "HTTP listen address (control plane + UI)")
		showVer = fs.Bool("version", false, "print version and exit")
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","role":%q,"version":%q}`, role, version)
	})

	// Control plane under /api; the embedded UI under everything else.
	apiSrv := api.NewServer(load.NewRESTAdapter(30 * time.Second))
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = apiSrv.Shutdown(shutdownCtx) // cancel and drain in-flight runs first
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
