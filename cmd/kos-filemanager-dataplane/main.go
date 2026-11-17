// Command kos-filemanager-dataplane runs the file manager's own service as an
// independent process (KOSD-NG-10). It holds its own cluster credentials; the
// console BFF never does (KOSD-C-12).
//
// The process reads its deployment inputs from the environment and refuses to
// start when one is missing, so a half-wired deployment fails loudly instead of
// serving without an authorization input.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kos-filemanager-dataplane:", err)
		os.Exit(1)
	}
}

func run() error {
	deployment, err := dataplane.FromEnvironment(os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	deployment.Log = log
	service, err := dataplane.NewStandalone(deployment)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              deployment.Addr,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	answered := make(chan error, 1)
	go func() { answered <- server.ListenAndServe() }()
	log.Info("data plane listens", "address", deployment.Addr, "volumeRoot", deployment.VolumeRoot)
	select {
	case err := <-answered:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shut down: %w", err)
		}
	}
	return nil
}
