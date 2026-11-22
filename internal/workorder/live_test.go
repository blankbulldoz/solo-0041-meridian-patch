package workorder

import (
	"os/exec"
	"testing"
)

// TestLiveReplayMatchesPlan runs every group through a real meridian process
// in a temp directory. It is the guarantee that the static simulator and the
// HTTP surface agree; skip it only where a Go toolchain is unavailable.
func TestLiveReplayMatchesPlan(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	for _, name := range []string{"minimal-pair", "fully-recoverable", "sample-historical"} {
		t.Run(name, func(t *testing.T) {
			order, report := analyze(t, name)
			verification, err := VerifyAgainstServer(order, report)
			if err != nil {
				t.Fatal(err)
			}
			if verification.Baseline != verification.BaselineLive {
				t.Fatalf("baseline digest mismatch: plan %s, live %s",
					verification.Baseline, verification.BaselineLive)
			}
			if !verification.AllMatches {
				t.Fatalf("live replay disagreed with the plan: %+v", verification.Groups)
			}
			for _, group := range report.Groups {
				live := findVerification(t, verification, group.Name)
				switch group.Classification {
				case "unique":
					if !live.Matches {
						t.Fatalf("%s: planned %s, live %s",
							group.Name, live.PlannedDigest, live.ObservedDigest)
					}
					if len(live.SplitConfirmations) != len(group.SplitFailures) {
						t.Fatalf("%s: %d split boundaries planned, %d confirmed",
							group.Name, len(group.SplitFailures), len(live.SplitConfirmations))
					}
					for _, confirmation := range live.SplitConfirmations {
						if confirmation.Code == "" {
							t.Fatalf("%s: split after line %d was accepted by the server",
								group.Name, confirmation.AfterLine)
						}
					}
				case "ambiguous":
					if len(live.ShapeObservations) < 2 {
						t.Fatalf("%s: live server produced %d distinct shapes, want at least 2",
							group.Name, len(live.ShapeObservations))
					}
				}
			}
			if report.FinalDigest != "" {
				if !verification.FullyReplayed {
					t.Fatal("fully decided ledger should report a complete replay")
				}
				if verification.FinalDigest != report.FinalDigest {
					t.Fatalf("final digest: plan %s, live %s", report.FinalDigest, verification.FinalDigest)
				}
			} else if verification.FullyReplayed {
				t.Fatal("ambiguous ledger must not claim a complete replay")
			}
		})
	}
}

func findVerification(t *testing.T, verification *Verification, name string) GroupVerification {
	t.Helper()
	for _, group := range verification.Groups {
		if group.Group == name {
			return group
		}
	}
	t.Fatalf("group %q missing from live verification", name)
	return GroupVerification{}
}

// TestArtifactExecutes makes sure the shipped replay.json is itself runnable:
// an external client following it step by step must land on the planned state.
func TestArtifactExecutes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	order, report := analyze(t, "fully-recoverable")
	artifact := BuildArtifact(order, report)
	result, err := ExecuteArtifact(artifact, report.FinalDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete {
		t.Fatal("fully recoverable artifact must be complete")
	}
	if !result.Matches {
		t.Fatalf("artifact ended at %s, planned %s", result.FinalDigest, report.FinalDigest)
	}
	if result.StepsRun != len(artifact.Steps) {
		t.Fatalf("ran %d of %d steps", result.StepsRun, len(artifact.Steps))
	}
}

// TestSkippedArtifactStopsShort checks the artifact never replays a group the
// ledger cannot decide.
func TestSkippedArtifactStopsShort(t *testing.T) {
	order, report := analyze(t, "sample-historical")
	artifact := BuildArtifact(order, report)
	if len(artifact.Skipped) == 0 {
		t.Fatal("sample ledger has undecidable groups; artifact should skip them")
	}
	replayed := make(map[string]bool)
	for _, step := range artifact.Steps {
		if step.Group != "" {
			replayed[step.Group] = true
		}
	}
	for _, skip := range artifact.Skipped {
		if replayed[skip.Group] {
			t.Fatalf("group %q is both skipped and replayed", skip.Group)
		}
	}
	for _, group := range report.Ambiguous {
		if !containsGroup(artifact.Skipped, group) {
			t.Fatalf("ambiguous group %q missing from skipped list", group)
		}
	}
	want := 0
	for _, group := range report.Groups {
		if group.Classification != "unique" {
			want += len(group.Rows)
		}
	}
	if len(artifact.Unresolved) != want {
		t.Fatalf("unresolved lines = %d, want %d", len(artifact.Unresolved), want)
	}
}

func containsGroup(skipped []SkippedGroup, name string) bool {
	for _, skip := range skipped {
		if skip.Group == name {
			return true
		}
	}
	return false
}
