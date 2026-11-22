package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/httpapi"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8741", "HTTP service listen address")
	dir := flag.String("data", "./data", "exclusive database directory")
	reviewMax := flag.Int("review-max", db.DefaultReviewPolicy().Max, "maximum live review sessions")
	reviewTTL := flag.Duration("review-ttl", db.DefaultReviewPolicy().TTL, "review session lifetime before expiry")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	policy := db.ReviewPolicy{Max: *reviewMax, TTL: *reviewTTL}
	if err := serve(ctx, *addr, *dir, policy, log); err != nil {
		log.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, addr, dir string, policy db.ReviewPolicy, log *slog.Logger) (err error) {
	database, err := db.Open(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	database.SetReviewPolicy(policy)
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
