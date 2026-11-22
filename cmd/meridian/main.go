package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/httpapi"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8741", "HTTP service listen address")
	dir := flag.String("data", "./data", "exclusive database directory")
	checkLock := flag.Bool("check-lock", false, "report writer-lock holders in -data and exit")
	probeLock := flag.String("probe-lock", "", "try a competitor's lock order in -data: 'legacy' (flock only), 'record' (record lock only) or 'both'; then exit")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if *checkLock || *probeLock != "" {
		if err := inspect(*dir, *checkLock, *probeLock); err != nil {
			log.Error("lock inspection failed", "error", err)
			if errors.Is(err, folio.ErrDirectoryBusy) {
				os.Exit(75) // EX_TEMPFAIL: orchestration may retry
			}
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := serve(ctx, *addr, *dir, log); err != nil {
		log.Error("service stopped", "error", err)
		if errors.Is(err, folio.ErrDirectoryBusy) {
			os.Exit(75) // EX_TEMPFAIL: a concurrent writer owns the directory
		}
		os.Exit(1)
	}
}

// inspect implements the pre-cutover and takeover checks. It never starts the
// HTTP service, so it is safe to run while the deployment is being staged.
func inspect(dir string, check bool, probe string) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if check {
		report, err := folio.LockState(dir)
		if err != nil {
			return err
		}
		if err := encoder.Encode(report); err != nil {
			return err
		}
	}
	switch probe {
	case "":
		return nil
	case "legacy":
		release, err := folio.GuardOnlyAcquire(dir)
		if err != nil {
			return fmt.Errorf("legacy flock acquisition refused: %w", err)
		}
		defer release()
		return encoder.Encode(map[string]any{"probe": "legacy", "acquired": true})
	case "record":
		release, err := folio.RecordOnlyAcquire(dir)
		if err != nil {
			return fmt.Errorf("record-lock acquisition refused: %w", err)
		}
		defer release()
		return encoder.Encode(map[string]any{"probe": "record", "acquired": true})
	case "both":
		store, err := folio.Open(dir)
		if err != nil {
			return err
		}
		defer store.Close()
		return encoder.Encode(map[string]any{"probe": "both", "acquired": true})
	default:
		return fmt.Errorf("unknown probe %q", probe)
	}
}

func serve(ctx context.Context, addr, dir string, log *slog.Logger) (err error) {
	database, err := db.Open(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           httpapi.New(database, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	log.Info("meridian ready", "address", listener.Addr().String(), "revision", database.Status().Revision)
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 16*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return errors.Join(err, server.Close())
		}
		if err := <-finished; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
