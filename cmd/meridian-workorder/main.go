// Command meridian-workorder turns a shuffled historical edit ledger into
// per-transaction replay material plus an explicit list of groups that cannot
// be recovered unambiguously. It only reads JSON; with -verify it spins up a
// throwaway meridian server and replays every group through POST /edits.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/workorder"
)

func main() {
	source := flag.String("order", "", "path to the work-order JSON ledger (required)")
	out := flag.String("out", "", "directory for plan.json, plan.md and verify.json (default: alongside the ledger)")
	verify := flag.Bool("verify", false, "replay every group through a real POST /edits in a temp directory")
	binary := flag.String("binary", "", "existing meridian binary to use with -verify (default: go build into a temp dir)")
	flag.Parse()
	if *source == "" {
		fmt.Fprintln(os.Stderr, "meridian-workorder: -order is required")
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*source, *out, *verify, *binary); err != nil {
		fmt.Fprintf(os.Stderr, "meridian-workorder: %v\n", err)
		os.Exit(1)
	}
}

func run(source, out string, verify bool, binary string) error {
	order, err := workorder.Load(source)
	if err != nil {
		return err
	}
	report, err := workorder.Analyze(order)
	if err != nil {
		return err
	}
	if out == "" {
		out = filepath.Dir(source)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	planJSON, err := workorder.MarshalReport(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "plan.json"), planJSON, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "plan.md"), []byte(workorder.RenderMarkdown(report)), 0o644); err != nil {
		return err
	}
	artifact := workorder.BuildArtifact(order, report)
	if err := workorder.WriteArtifact(out, artifact); err != nil {
		return err
	}
	fmt.Printf("replay: %s\n", filepath.Join(out, "replay.json"))
	fmt.Printf("plan:   %s\n", filepath.Join(out, "plan.json"))
	fmt.Printf("groups: %d (ambiguous %d, unrecoverable %d, blocked %d, whole-group-only %d)\n",
		len(report.Groups), len(report.Ambiguous), len(report.Unrecoverable), len(report.Blocked), len(report.WholeGroupOnly))
	if !verify {
		return nil
	}
	verification, err := workorder.VerifyAgainstServerWith(order, report, binary)
	if err != nil {
		return err
	}
	raw, err := workorder.MarshalVerification(verification)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "verify.json"), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("verify: %s (all unique groups match: %t)\n", filepath.Join(out, "verify.json"), verification.AllMatches)
	if !verification.AllMatches {
		return fmt.Errorf("live replay disagreed with the static plan")
	}
	executed, err := workorder.ExecuteArtifact(artifact, report.FinalDigest)
	if err != nil {
		return err
	}
	if !executed.Matches {
		return fmt.Errorf("replay.json executed to %s, plan promised %s",
			executed.FinalDigest, report.FinalDigest)
	}
	fmt.Printf("artifact: %d steps, end digest %s (complete=%t)\n",
		executed.StepsRun, executed.FinalDigest, executed.Complete)
	return nil
}
