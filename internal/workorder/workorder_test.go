package workorder

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) *Order {
	t.Helper()
	path := filepath.Join("..", "..", "examples", "workorder", name, "ledger.json")
	order, err := Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return order
}

func analyze(t *testing.T, name string) (*Order, *Report) {
	t.Helper()
	order := load(t, name)
	report, err := Analyze(order)
	if err != nil {
		t.Fatalf("analyze %s: %v", name, err)
	}
	return order, report
}

func groupByName(t *testing.T, report *Report, name string) Group {
	t.Helper()
	for _, group := range report.Groups {
		if group.Name == name {
			return group
		}
	}
	t.Fatalf("group %q not in report", name)
	return Group{}
}

// TestSourceLinesArePreserved checks the hard requirement: every emitted edit
// carries the source line of the row it came from, with no duplication.
func TestSourceLinesArePreserved(t *testing.T) {
	order, report := analyze(t, "sample-historical")
	seen := make(map[int]int)
	for _, row := range order.Rows {
		seen[row.Line]++
	}
	for _, group := range report.Groups {
		if group.Replay == nil {
			continue
		}
		if len(group.Replay.SourceLines) != len(group.Replay.Request.Edits) {
			t.Fatalf("%s: %d source lines for %d edits",
				group.Name, len(group.Replay.SourceLines), len(group.Replay.Request.Edits))
		}
		for i, line := range group.Replay.SourceLines {
			edit := group.Replay.Request.Edits[i]
			var source *Row
			for j := range group.Rows {
				if group.Rows[j].Line == line {
					source = &group.Rows[j]
				}
			}
			if source == nil {
				t.Fatalf("%s: replay cites line %d outside the group", group.Name, line)
			}
			if source.Layer != edit.Layer || source.ID != edit.ID || source.Mode != edit.Mode {
				t.Fatalf("%s: line %d replayed as %+v, source is %+v", group.Name, line, edit, source)
			}
			seen[line]--
		}
	}
	for line, count := range seen {
		if count < 0 {
			t.Fatalf("line %d replayed more times than it appears", line)
		}
	}
}

// TestSwapNeedsWholeGroup is the "exchange only works as a batch" case: the
// delivered order must be rejected when split at any boundary, and the group
// must be flagged atomic under every ordering.
func TestSwapNeedsWholeGroup(t *testing.T) {
	_, report := analyze(t, "sample-historical")
	group := groupByName(t, report, "g1-swap")
	if group.Classification != "unique" {
		t.Fatalf("g1-swap classification = %q", group.Classification)
	}
	if !group.AtomicRequired {
		t.Fatal("g1-swap should be atomic under every ordering")
	}
	if len(group.SplitFailures) == 0 {
		t.Fatal("g1-swap should record rejected split boundaries")
	}
	for _, failure := range group.SplitFailures {
		if failure.Code != "conflict" {
			t.Fatalf("split after %d: expected conflict, got %q", failure.AfterLine, failure.Code)
		}
	}
	inWholeGroupOnly := false
	for _, name := range report.WholeGroupOnly {
		if name == "g1-swap" {
			inWholeGroupOnly = true
		}
	}
	if !inWholeGroupOnly {
		t.Fatal("g1-swap missing from whole_group_only_groups")
	}
}

// TestTwoLegalOrdersDiverge is the "two legal orderings, different final
// shape" case: both orderings must be accepted and must differ.
func TestTwoLegalOrdersDiverge(t *testing.T) {
	for _, name := range []string{"g4-shape-choice", "g6c-last-writer"} {
		group := groupByName(t, afterAnalyze(t, "sample-historical"), name)
		if group.Classification != "ambiguous" {
			t.Fatalf("%s classification = %q", name, group.Classification)
		}
		if len(group.Shapes) < 2 {
			t.Fatalf("%s reached %d shapes, want at least 2", name, len(group.Shapes))
		}
		digests := make(map[string]bool)
		for _, shape := range group.Shapes {
			digests[shape.Digest] = true
			if shape.Count == 0 {
				t.Fatalf("%s reported a shape with no ordering", name)
			}
		}
		if len(digests) != len(group.Shapes) {
			t.Fatalf("%s reports duplicate shape digests", name)
		}
		for _, want := range []string{"g4-shape-choice", "g6c-last-writer"} {
			found := false
			for _, listed := range afterAnalyze(t, "sample-historical").Ambiguous {
				if listed == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s missing from ambiguous list", want)
			}
		}
	}
}

func afterAnalyze(t *testing.T, name string) *Report {
	t.Helper()
	_, report := analyze(t, name)
	return report
}

// TestDownstreamOfAmbiguityIsBlocked checks that the tool refuses to invent a
// branch: a group touching a layer left ambiguous upstream is not replayed.
func TestDownstreamOfAmbiguityIsBlocked(t *testing.T) {
	_, report := analyze(t, "sample-historical")
	group := groupByName(t, report, "g4b-after-choice")
	if group.Classification != "blocked" {
		t.Fatalf("g4b-after-choice classification = %q", group.Classification)
	}
	if group.Replay != nil {
		t.Fatal("blocked group must not emit replay material")
	}
	if report.FinalDigest != "" {
		t.Fatal("final digest must stay unset when any group is undecided")
	}
}

// TestUnrecoverableGroup is the "no legal ordering" case.
func TestUnrecoverableGroup(t *testing.T) {
	_, report := analyze(t, "sample-historical")
	group := groupByName(t, report, "g7-lost")
	if group.Classification != "unrecoverable" {
		t.Fatalf("g7-lost classification = %q", group.Classification)
	}
	if group.Replay != nil || len(group.Shapes) != 0 {
		t.Fatal("unrecoverable group must produce neither replay nor shapes")
	}
}

// TestDeleteThenInsertSameID verifies the "remove then insert the same id in
// one group" shape: the received order is illegal, the canonical order is
// remove-then-insert, and the final shape is the inserted value.
func TestDeleteThenInsertSameID(t *testing.T) {
	_, report := analyze(t, "sample-historical")
	group := groupByName(t, report, "g2-recycle")
	if group.Classification != "unique" {
		t.Fatalf("g2-recycle classification = %q", group.Classification)
	}
	if got, want := group.Replay.SourceLines, []int{12, 11}; !equalInts(got, want) {
		t.Fatalf("replay lines = %v, want %v", got, want)
	}
	if group.Replay.Request.Edits[0].Mode != "remove" || group.Replay.Request.Edits[1].Mode != "insert" {
		t.Fatalf("canonical order should remove before insert, got %v",
			[]string{group.Replay.Request.Edits[0].Mode, group.Replay.Request.Edits[1].Mode})
	}
}

// TestFullyRecoverableEndState checks the positive end-to-end path: with no
// ambiguity, a single final digest is published.
func TestFullyRecoverableEndState(t *testing.T) {
	_, report := analyze(t, "fully-recoverable")
	if report.FinalDigest == "" {
		t.Fatal("fully recoverable ledger should publish a final digest")
	}
	if len(report.Ambiguous) != 0 || len(report.Unrecoverable) != 0 || len(report.Blocked) != 0 {
		t.Fatalf("unexpected unresolved groups: %+v", report)
	}
}

// TestExpectationsAreEnforced checks the ledger's own pinned classifications
// actually catch a drift.
func TestExpectationsAreEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	order := load(t, "minimal-pair")
	order.Expect["swap"] = "ambiguous"
	raw, err := MarshalOrder(order)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(loaded); err == nil {
		t.Fatal("expected an expectation mismatch error")
	}
}

// TestRejectsBadLedgers checks the input guards.
func TestRejectsBadLedgers(t *testing.T) {
	cases := map[string]func(*Order){
		"duplicate line": func(o *Order) { o.Rows[1].Line = o.Rows[0].Line },
		"unknown group":  func(o *Order) { o.Rows[0].Group = "nowhere" },
		"bad mode":       func(o *Order) { o.Rows[0].Mode = "upsert" },
		"missing layer":  func(o *Order) { o.Rows[0].Layer = "absent" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			order := load(t, "minimal-pair")
			mutate(order)
			if _, err := Analyze(order); err == nil {
				t.Fatalf("%s: expected an error", name)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
