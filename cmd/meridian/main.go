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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/httpapi"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8741", "HTTP service listen address")
	dir := flag.String("data", "./data", "exclusive database directory")
	probeSpec := flag.String("probe", "", "readiness locate probe as layer,x,y,id1|id2")
	requireProbe := flag.Bool("require-probe", false, "refuse readiness when -probe is not supplied")
	reportPath := flag.String("readiness-report", "", "write the readiness evidence as JSON to this path")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	probe, err := parseProbe(*probeSpec)
	if err != nil {
		log.Error("invalid probe", "error", err, "probe", *probeSpec)
		os.Exit(2)
	}
	if *requireProbe && probe == nil {
		log.Error("meridian not ready", "stage", "probe", "data", *dir,
			"error", "probe is required but -probe was not supplied")
		os.Exit(stageExit["probe"])
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := serve(ctx, *addr, *dir, probe, *reportPath, log); err != nil {
		stage := db.StageOf(err)
		log.Error("service stopped", "error", err, "stage", stage)
		os.Exit(stageExitCode(stage))
	}
}

// stageExit maps each readiness gate to a distinct process exit status. Scripts
// and the takeover drill can classify a refused start without parsing logs.
var stageExit = map[string]int{
	"lock":  10,
	"image": 11,
	"index": 12,
	"probe": 13,
}

func stageExitCode(stage string) int {
	if code, present := stageExit[stage]; present {
		return code
	}
	return 1
}

func parseProbe(spec string) (*db.Probe, error) {
	if spec == "" {
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 4 {
		return nil, errors.New("probe must be layer,x,y,id1|id2")
	}
	x, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return nil, err
	}
	y, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
	if err != nil {
		return nil, err
	}
	var expect []string
	for _, id := range strings.Split(parts[3], "|") {
		if id = strings.TrimSpace(id); id != "" {
			expect = append(expect, id)
		}
	}
	probe := &db.Probe{Layer: strings.TrimSpace(parts[0]), Point: plane.Point{X: x, Y: y}, Expect: expect}
	return probe, probe.Validate()
}

func serve(ctx context.Context, addr, dir string, probe *db.Probe, reportPath string, log *slog.Logger) (err error) {
	database, report, err := db.OpenChecked(dir, probe)
	if err != nil {
		stage := db.StageOf(err)
		if stage == "" {
			stage = "open"
		}
		log.Error("meridian not ready", append([]any{
			"stage", stage, "data", dir, "error", err,
		}, readinessArgs(report)...)...)
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Info("meridian readiness gates passed", append([]any{"data", dir}, readinessArgs(report)...)...)
	if reportPath != "" {
		if writeErr := writeReadiness(reportPath, report); writeErr != nil {
			log.Warn("write readiness report", "path", reportPath, "error", writeErr)
		} else {
			log.Info("readiness report written", "path", reportPath)
		}
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

func readinessArgs(report *db.ReadinessReport) []any {
	if report == nil {
		return nil
	}
	args := []any{
		"revision", report.Revision,
		"lock", report.Lock,
		"image_verified", report.Image.Verified,
		"image_source", report.Image.Source,
		"image_bytes", report.Image.Bytes,
		"image_digest", report.Image.Digest,
		"image_revision", report.Image.Revision,
		"image_layers", report.Image.Layers,
		"image_features", report.Image.Features,
		"audited_layers", len(report.Index),
	}
	for _, audit := range report.Index {
		args = append(args, "index."+audit.Layer, fmt.Sprintf("records=%d checked_nodes=%d revision=%d",
			audit.Records, audit.CheckedNodes, audit.Revision))
	}
	if probe := report.Probe; probe != nil {
		args = append(args,
			"probe_layer", probe.Layer,
			"probe_point", fmt.Sprintf("%d,%d", probe.Point.X, probe.Point.Y),
			"probe_expected", probe.Expected,
			"probe_observed", probe.Observed,
			"probe_missing", probe.Missing,
			"probe_unexpected", probe.Unexpected,
			"probe_nodes", probe.Nodes,
			"probe_candidates", probe.Candidates,
			"probe_layer_revision", probe.LayerRevision,
		)
	}
	return args
}

// writeReadiness atomically replaces the readiness evidence file. Operators and
// the takeover drill read this instead of parsing the structured log.
func writeReadiness(path string, report *db.ReadinessReport) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".readiness-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
