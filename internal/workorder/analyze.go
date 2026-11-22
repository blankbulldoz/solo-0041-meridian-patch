package workorder

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// MaxEnumerated bounds exhaustive permutation search. Above it a group is
// reported as unresolved rather than guessed at.
const MaxEnumerated = 8

const maxExamples = 4

// Group is all ledger lines sharing one transaction group, plus the derived
// replay decision.
type Group struct {
	Name string `json:"group"`
	// Rows are in the order they were received in the shuffled ledger.
	Rows []Row `json:"rows"`
	// Layers lists every layer the group touches, in sorted order.
	Layers []string `json:"layers"`

	Classification string `json:"classification"`
	// AtomicRequired is the strong property: under every legal ordering at
	// least one commit boundary is invalid on its own, so no split exists.
	AtomicRequired bool `json:"atomic_required"`
	// ReplayNeedsWholeGroup means the delivered replay ordering itself has a
	// boundary that is rejected, even if some other ordering could be split.
	ReplayNeedsWholeGroup bool `json:"replay_needs_whole_group"`
	// Blocked means a previous ambiguous group left one of this group's
	// layers in an unknown state, so it cannot be analyzed at all.
	Blocked bool `json:"blocked,omitempty"`
	// Unresolved means exhaustive search was not attempted.
	Unresolved bool `json:"unresolved,omitempty"`

	// Replay is present only when exactly one final shape is reachable.
	Replay *Replay `json:"replay,omitempty"`
	// ReplayOrder carries the exact accepted row sequence for split analysis.
	ReplayOrder []Row `json:"-"`

	// Shapes lists every distinct final catalog reachable by a legal ordering.
	Shapes []ShapeCandidate `json:"shapes"`

	// SplitFailures lists commit boundaries rejected when sent separately.
	SplitFailures []SplitFailure `json:"split_failures,omitempty"`

	// FinalDigest is the shape after this group, when known.
	FinalDigest string   `json:"final_digest,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

// Replay is a ready-to-send batch. SourceLines is positionally parallel with
// Request.Edits, so every emitted edit keeps its provenance.
type Replay struct {
	Request     catalog.EditBatch `json:"request"`
	SourceLines []int             `json:"source_lines"`
}

// ShapeCandidate is one reachable final shape and the source-line orderings
// that produce it.
type ShapeCandidate struct {
	Digest   string  `json:"digest"`
	Count    int     `json:"count"`
	Examples [][]int `json:"examples"`
}

// SplitFailure records a commit boundary that cannot be honored: the edits
// before the boundary are rejected when submitted as their own batch. (The
// following suffix is not reported separately because prefix+suffix is the
// whole group, which is known to be accepted.)
type SplitFailure struct {
	AfterLine int    `json:"after_line"`
	Lines     []int  `json:"lines"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// Report is the complete deliverable.
type Report struct {
	Order          string   `json:"order"`
	Description    string   `json:"description,omitempty"`
	CommitOrder    []string `json:"commit_order"`
	BaselineDigest string   `json:"baseline_digest"`
	FinalDigest    string   `json:"final_digest,omitempty"`
	Groups         []Group  `json:"groups"`

	// Recovery lists are group names, not decisions: they say what cannot be
	// reproduced without guessing.
	Ambiguous      []string `json:"ambiguous_groups"`
	Unrecoverable  []string `json:"unrecoverable_groups"`
	WholeGroupOnly []string `json:"whole_group_only_groups"`
	Blocked        []string `json:"blocked_groups"`

	baseline *World
}

func Analyze(order *Order) (*Report, error) {
	if err := order.validate(); err != nil {
		return nil, err
	}
	baseline, err := order.Baseline()
	if err != nil {
		return nil, err
	}
	report := &Report{
		Order:          order.Name,
		Description:    order.Description,
		CommitOrder:    order.commitOrder(),
		BaselineDigest: baseline.ShapeDigest(),
		Groups:         make([]Group, 0),
		Ambiguous:      make([]string, 0),
		Unrecoverable:  make([]string, 0),
		WholeGroupOnly: make([]string, 0),
		Blocked:        make([]string, 0),
		baseline:       baseline,
	}
	world := baseline.Clone()
	tainted := make(map[string]bool)
	finalKnown := true
	for _, name := range report.CommitOrder {
		group := analyzeGroup(name, order.Rows, world, tainted)
		report.Groups = append(report.Groups, group)
		switch group.Classification {
		case "ambiguous":
			report.Ambiguous = append(report.Ambiguous, name)
			finalKnown = false
			for _, layer := range group.Layers {
				tainted[layer] = true
			}
		case "unrecoverable":
			report.Unrecoverable = append(report.Unrecoverable, name)
			finalKnown = false
			for _, layer := range group.Layers {
				tainted[layer] = true
			}
		case "blocked":
			report.Blocked = append(report.Blocked, name)
			finalKnown = false
			for _, layer := range group.Layers {
				tainted[layer] = true
			}
		case "unresolved":
			finalKnown = false
		}
		if group.AtomicRequired {
			report.WholeGroupOnly = append(report.WholeGroupOnly, name)
		}
		if group.Classification == "unique" {
			next, _, err := world.ApplyBatch(group.Replay.Request.Edits)
			if err != nil {
				finalKnown = false
				continue
			}
			world = next
		}
	}
	// The overall final shape is only meaningful when every group landed on a
	// single outcome; otherwise the digest would silently pick a branch.
	if finalKnown {
		report.FinalDigest = world.ShapeDigest()
	}
	if err := checkExpectations(order, report); err != nil {
		return nil, err
	}
	return report, nil
}

// checkExpectations verifies the ledger's own pinned classifications.
func checkExpectations(order *Order, report *Report) error {
	actual := make(map[string]string, len(report.Groups))
	for _, group := range report.Groups {
		actual[group.Name] = group.Classification
	}
	for name, wanted := range order.Expect {
		got, present := actual[name]
		if !present {
			return fmt.Errorf("expect names unknown group %q", name)
		}
		if got != wanted {
			return fmt.Errorf("group %q: ledger expects %q, analysis produced %q", name, wanted, got)
		}
	}
	return nil
}

func analyzeGroup(name string, all []Row, baseline *World, tainted map[string]bool) Group {
	rows := make([]Row, 0)
	for _, row := range all {
		if row.Group == name {
			rows = append(rows, row)
		}
	}
	group := Group{Name: name, Rows: rows, Layers: layersOf(rows), Shapes: make([]ShapeCandidate, 0)}

	if blockedLayers := taintedIntersection(group.Layers, tainted); len(blockedLayers) > 0 {
		group.Classification = "blocked"
		group.Blocked = true
		group.Notes = append(group.Notes, fmt.Sprintf(
			"前序组把图层 %s 留在未定状态；不先选定该结果就无法判定本组",
			strings.Join(blockedLayers, ", ")))
		return group
	}
	if len(rows) > MaxEnumerated {
		group.Classification = "unresolved"
		group.Unresolved = true
		group.Notes = append(group.Notes, fmt.Sprintf(
			"本组有 %d 行，穷举上限为 %d 行，未做判定", len(rows), MaxEnumerated))
		return group
	}

	byShape := make(map[string]*ShapeCandidate)
	shapeOrder := make([]string, 0)
	valid := 0
	for _, order := range permutations(len(rows)) {
		ordered := orderedRows(rows, order)
		world, _, err := baseline.ApplyBatch(editsOf(ordered))
		if err != nil {
			continue
		}
		valid++
		digest := world.ShapeDigestOf(group.Layers)
		candidate, ok := byShape[digest]
		if !ok {
			candidate = &ShapeCandidate{Digest: digest}
			byShape[digest] = candidate
			shapeOrder = append(shapeOrder, digest)
		}
		candidate.Count++
		lines := linesOf(ordered)
		if len(candidate.Examples) < maxExamples && !hasLines(candidate.Examples, lines) {
			candidate.Examples = append(candidate.Examples, lines)
		}
	}
	for _, digest := range shapeOrder {
		group.Shapes = append(group.Shapes, *byShape[digest])
	}

	switch {
	case valid == 0:
		group.Classification = "unrecoverable"
		group.Notes = append(group.Notes,
			"本组没有任何排序能对基线成立；除非猜测缺失、损坏或错分组的行，否则无法回放")
		group.Notes = append(group.Notes, rejectionSummary(rows, baseline))
	case len(group.Shapes) == 1:
		group.Classification = "unique"
		canonical := canonicalOrder(rows, baseline)
		group.Replay = &Replay{
			Request:     catalog.EditBatch{Edits: editsOf(canonical)},
			SourceLines: linesOf(canonical),
		}
		group.ReplayOrder = canonical
		group.FinalDigest = group.Shapes[0].Digest
		if len(canonical) != len(rows) {
			group.Notes = append(group.Notes, "选定的重放顺序短于接收到的行数，说明组内有重复行")
		}
		if varying := varyingOrderings(rows, baseline); varying > 1 {
			group.Notes = append(group.Notes, fmt.Sprintf(
				"共 %d 种排序合法，但全部收敛到同一最终形状，因此重放无歧义",
				varying))
		}
	default:
		group.Classification = "ambiguous"
		group.Notes = append(group.Notes, fmt.Sprintf(
			"存在 %d 种可达的最终形状；账本没有记录原提交落在哪一种",
			len(group.Shapes)))
	}

	if group.Classification == "unique" {
		group.SplitFailures = splitFailures(group.ReplayOrder, baseline)
		group.ReplayNeedsWholeGroup = len(group.SplitFailures) > 0
		group.AtomicRequired = atomicUnderEveryOrdering(rows, baseline)
	}
	switch {
	case group.AtomicRequired:
		group.Notes = append(group.Notes,
			"本组每一种合法排序都存在被单独拒绝的提交边界，因此无论顺序如何都只能整组一次发送")
	case group.ReplayNeedsWholeGroup:
		group.Notes = append(group.Notes,
			"本批次选定的顺序必须整组发送，但另一种合法排序可以拆批；仅凭分组不能证明该组原子")
	case group.Classification == "unique" && len(rows) > 1:
		group.Notes = append(group.Notes,
			"本批次顺序的每个切分点都单独合法，该组原本也可以拆成多次提交")
	}
	return group
}

// canonicalOrder returns the lexicographically smallest accepted permutation,
// so a unique group always yields deterministic replay material.
func canonicalOrder(rows []Row, baseline *World) []Row {
	if len(rows) == 1 {
		return rows
	}
	var best []Row
	var bestLines []int
	for _, order := range permutations(len(rows)) {
		ordered := orderedRows(rows, order)
		if _, _, err := baseline.ApplyBatch(editsOf(ordered)); err != nil {
			continue
		}
		lines := linesOf(ordered)
		if bestLines == nil || lessLines(lines, bestLines) {
			best = ordered
			bestLines = lines
		}
	}
	return best
}

func varyingOrderings(rows []Row, baseline *World) int {
	count := 0
	for _, order := range permutations(len(rows)) {
		if _, _, err := baseline.ApplyBatch(editsOf(orderedRows(rows, order))); err == nil {
			count++
		}
	}
	return count
}

// atomicUnderEveryOrdering reports whether every accepted ordering of the
// group has at least one split prefix that is rejected on its own.
func atomicUnderEveryOrdering(rows []Row, baseline *World) bool {
	if len(rows) < 2 {
		return false
	}
	accepted := 0
	for _, order := range permutations(len(rows)) {
		ordered := orderedRows(rows, order)
		if _, _, err := baseline.ApplyBatch(editsOf(ordered)); err != nil {
			continue
		}
		accepted++
		splittable := false
		for cut := 1; cut < len(ordered); cut++ {
			if applyErr(baseline, ordered[:cut]) == nil {
				splittable = true
				break
			}
		}
		if splittable {
			return false
		}
	}
	return accepted > 0
}

// splitFailures walks the delivered replay order and reports every commit
// boundary that is rejected when the group is sent as two batches.
func splitFailures(ordered []Row, baseline *World) []SplitFailure {
	failures := make([]SplitFailure, 0)
	if len(ordered) < 2 {
		return failures
	}
	if _, _, err := baseline.ApplyBatch(editsOf(ordered)); err != nil {
		return failures
	}
	for cut := 1; cut < len(ordered); cut++ {
		prefix, suffix := ordered[:cut], ordered[cut:]
		fail := applyErr(baseline, prefix)
		if fail == nil {
			continue
		}
		failures = append(failures, SplitFailure{
			AfterLine: prefix[len(prefix)-1].Line,
			Lines:     linesOf(suffix),
			Code:      fail.Code,
			Message:   fail.Message,
		})
	}
	return failures
}

func applyErr(baseline *World, rows []Row) *Fail {
	_, _, err := baseline.ApplyBatch(editsOf(rows))
	return asFail(err)
}

func rejectionSummary(rows []Row, baseline *World) string {
	fail := applyErr(baseline, rows)
	if fail == nil {
		return "按接收顺序提交会被拒绝"
	}
	return fmt.Sprintf("按接收顺序提交会被拒绝（%s）：%s", fail.Code, fail.Message)
}

func asFail(err error) *Fail {
	if err == nil {
		return nil
	}
	if fail, ok := err.(*Fail); ok {
		return fail
	}
	return &Fail{Code: "error", Message: err.Error()}
}

func orderedRows(rows []Row, order []int) []Row {
	ordered := make([]Row, len(rows))
	for i, index := range order {
		ordered[i] = rows[index]
	}
	return ordered
}

// acceptedOrder rebuilds one previously reported ordering from its line
// numbers, verifying it still applies cleanly to the given state.
func acceptedOrder(rows []Row, baseline *World, lines []int) []Row {
	ordered := make([]Row, 0, len(lines))
	for _, line := range lines {
		found := false
		for _, row := range rows {
			if row.Line == line {
				ordered = append(ordered, row)
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	if len(ordered) != len(rows) {
		return nil
	}
	if _, _, err := baseline.ApplyBatch(editsOf(ordered)); err != nil {
		return nil
	}
	return ordered
}

func editsOf(rows []Row) []catalog.Edit {
	edits := make([]catalog.Edit, len(rows))
	for i, row := range rows {
		edits[i] = catalog.Edit{Layer: row.Layer, ID: row.ID, Mode: row.Mode, Value: row.Value}
	}
	return edits
}

func linesOf(rows []Row) []int {
	lines := make([]int, len(rows))
	for i, row := range rows {
		lines[i] = row.Line
	}
	return lines
}

func layersOf(rows []Row) []string {
	seen := make(map[string]bool)
	layers := make([]string, 0, len(rows))
	for _, row := range rows {
		if !seen[row.Layer] {
			seen[row.Layer] = true
			layers = append(layers, row.Layer)
		}
	}
	sort.Strings(layers)
	return layers
}

func taintedIntersection(layers []string, tainted map[string]bool) []string {
	blocked := make([]string, 0, len(layers))
	for _, layer := range layers {
		if tainted[layer] {
			blocked = append(blocked, layer)
		}
	}
	return blocked
}

func hasLines(examples [][]int, lines []int) bool {
outer:
	for _, example := range examples {
		if len(example) != len(lines) {
			continue
		}
		for i := range lines {
			if example[i] != lines[i] {
				continue outer
			}
		}
		return true
	}
	return false
}

func lessLines(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// permutations returns every index permutation of n elements, lexicographic.
func permutations(n int) [][]int {
	if n <= 0 {
		return nil
	}
	current := make([]int, n)
	for i := range current {
		current[i] = i
	}
	out := make([][]int, 0, factorial(n))
	for {
		out = append(out, append([]int(nil), current...))
		if !nextPermutation(current) {
			return out
		}
	}
}

func nextPermutation(values []int) bool {
	i := len(values) - 2
	for i >= 0 && values[i] >= values[i+1] {
		i--
	}
	if i < 0 {
		return false
	}
	j := len(values) - 1
	for values[j] <= values[i] {
		j--
	}
	values[i], values[j] = values[j], values[i]
	for left, right := i+1, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
	return true
}

func factorial(n int) int {
	result := 1
	for i := 2; i <= n; i++ {
		result *= i
	}
	return result
}
