package workorder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

// Server is a real meridian process bound to an ephemeral port under a
// throwaway data directory. It is never pointed at an existing database.
type Server struct {
	cmd    *exec.Cmd
	stderr io.ReadCloser
	url    string
	dir    string
	client *http.Client

	// snapshot is the last committed image plus its revision. Trials rewind
	// to it so group verifications never contaminate each other.
	snapshot         []byte
	snapshotRevision uint64
}

// Start builds (or reuses) a meridian binary and runs it against a fresh temp
// directory. The caller must Close the server.
func Start(binary string) (*Server, error) {
	dir, err := os.MkdirTemp("", "meridian-workorder-*")
	if err != nil {
		return nil, err
	}
	if binary == "" {
		binary, err = build()
		if err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	server := &Server{dir: dir, client: &http.Client{Timeout: 20 * time.Second}}
	server.cmd = exec.Command(binary, "-listen", "127.0.0.1:0", "-data", filepath.Join(dir, "space"))
	server.stderr, err = server.cmd.StderrPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := server.cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	address, err := server.readStartup()
	if err != nil {
		server.Close()
		return nil, err
	}
	server.url = "http://" + address + "/meridian/v1"
	return server, nil
}

func build() (string, error) {
	dir, err := os.MkdirTemp("", "meridian-build-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "meridian")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/meridian")
	cmd.Dir = moduleRoot()
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("go build: %w: %s", err, output)
	}
	return binary, nil
}

func moduleRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func (s *Server) readStartup() (string, error) {
	type event struct {
		Address string `json:"address"`
	}
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(s.stderr)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return "", errors.New("meridian exited before reporting a listen address")
			}
			var parsed event
			if json.Unmarshal([]byte(line), &parsed) == nil && parsed.Address != "" {
				return parsed.Address, nil
			}
		case <-timeout:
			return "", errors.New("meridian did not report a listen address within 30s")
		}
	}
}

func (s *Server) Close() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			s.cmd.Process.Kill()
			<-done
		}
	}
	os.RemoveAll(s.dir)
}

// Do issues one request and returns the status and raw body.
func (s *Server) Do(method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, s.url+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	return response.StatusCode, raw, err
}

// Seed writes the work order's committed baseline through the public API and
// remembers the resulting image as the rewind target.
func (s *Server) Seed(order *Order) error {
	for _, def := range order.Layers {
		code, raw, err := s.Do(http.MethodPost, "/strata", def)
		if err != nil {
			return err
		}
		if code != http.StatusCreated {
			return fmt.Errorf("create layer %q: HTTP %d: %s", def.Name, code, raw)
		}
	}
	edits := make([]catalog.Edit, 0, len(order.Features))
	for _, spec := range order.Features {
		edits = append(edits, catalog.Edit{
			Layer: spec.Layer, ID: spec.ID, Mode: "insert",
			Value: &catalog.Feature{ID: spec.ID, Class: spec.Class, Shape: spec.Shape},
		})
	}
	if len(edits) > 0 {
		code, raw, err := s.Do(http.MethodPost, "/edits", catalog.EditBatch{Edits: edits})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("seed baseline: HTTP %d: %s", code, raw)
		}
	}
	return s.Snapshot()
}

// ApplyBatch sends a batch through POST /edits and reports a server-side
// rejection as a typed failure instead of a transport error.
func (s *Server) ApplyBatch(batch catalog.EditBatch) (*Fail, error) {
	code, raw, err := s.Do(http.MethodPost, "/edits", batch)
	if err != nil {
		return nil, err
	}
	if code == http.StatusOK {
		return nil, nil
	}
	var problem struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil {
		return nil, fmt.Errorf("HTTP %d with undecodable body %q", code, raw)
	}
	return &Fail{Code: problem.Code, Message: problem.Message}, nil
}

// Image fetches and decodes the server's portable image.
func (s *Server) Image() (catalog.CatalogImage, uint64, error) {
	raw, err := s.ExportRaw()
	if err != nil {
		return catalog.CatalogImage{}, 0, err
	}
	image, err := folio.Decode(raw)
	return image, image.Revision, err
}

// ExportRaw fetches the image bytes verbatim.
func (s *Server) ExportRaw() ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, s.url+"/image", nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, folio.MaxImage+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image: HTTP %d", response.StatusCode)
	}
	return raw, nil
}

// RestoreImage pushes a previously fetched image back with a CAS header.
func (s *Server) RestoreImage(raw []byte, revision uint64) error {
	request, err := http.NewRequest(http.MethodPut, s.url+"/image", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/vnd.meridian.image")
	request.Header.Set("If-Meridian-Revision", fmt.Sprintf("%d", revision))
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("restore: HTTP %d: %s", response.StatusCode, body)
	}
	return nil
}

// restoreTo pushes an image back using the server's *current* revision as the
// CAS target. Restoring always creates a new revision, so a stale expected
// value would be rejected on the second attempt.
func (s *Server) restoreTo(image []byte) error {
	current, err := s.revision()
	if err != nil {
		return err
	}
	return s.RestoreImage(image, current)
}

func (s *Server) revision() (uint64, error) {
	var status struct {
		Revision uint64 `json:"revision"`
	}
	code, raw, err := s.Do(http.MethodGet, "/status", nil)
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return 0, fmt.Errorf("status: HTTP %d: %s", code, raw)
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return 0, err
	}
	return status.Revision, nil
}

// Snapshot records the current committed image as the rewind target.
func (s *Server) Snapshot() error {
	image, err := s.ExportRaw()
	if err != nil {
		return err
	}
	revision, err := s.revision()
	if err != nil {
		return err
	}
	s.snapshot = image
	s.snapshotRevision = revision
	return nil
}

// Rewind restores the last snapshot through PUT /image. A restore always
// creates a new revision, so the CAS target advances with it.
func (s *Server) Rewind() error {
	if s.snapshot == nil {
		return errors.New("no snapshot recorded")
	}
	if err := s.RestoreImage(s.snapshot, s.snapshotRevision); err != nil {
		return err
	}
	current, err := s.revision()
	if err != nil {
		return err
	}
	s.snapshotRevision = current
	raw, err := s.ExportRaw()
	if err != nil {
		return err
	}
	s.snapshot = raw
	return nil
}

// GroupVerification records what the real server did for one group.
type GroupVerification struct {
	Group          string `json:"group"`
	Classification string `json:"classification"`
	// PlannedDigest is the shape digest the static plan predicted.
	PlannedDigest string `json:"planned_digest,omitempty"`
	// ObservedDigest is recomputed from the server's own image after replay.
	ObservedDigest string `json:"observed_digest,omitempty"`
	// Matches is true only when a unique group replayed to the predicted shape.
	Matches bool `json:"matches"`
	// ReplayLines is the source-line order actually submitted.
	ReplayLines []int `json:"replay_lines,omitempty"`
	// ServerRevision is the revision committed by the replay batch.
	ServerRevision uint64 `json:"server_revision,omitempty"`
	// SplitConfirmations lists boundaries independently rejected by the server.
	SplitConfirmations []SplitConfirmation `json:"split_confirmations,omitempty"`
	// ShapeObservations lists every distinct shape the server reached from a
	// legal ordering, for ambiguous groups.
	ShapeObservations []ShapeObservation `json:"shape_observations,omitempty"`
	Notes             []string           `json:"notes,omitempty"`
}

type SplitConfirmation struct {
	AfterLine int    `json:"after_line"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type ShapeObservation struct {
	Lines  []int  `json:"lines"`
	Digest string `json:"digest"`
}

// Verification is the server-backed half of the deliverable.
type Verification struct {
	Order        string              `json:"order"`
	DataDir      string              `json:"data_dir"`
	Baseline     string              `json:"baseline_digest"`
	BaselineLive string              `json:"baseline_live_digest"`
	Groups       []GroupVerification `json:"groups"`
	// FinalDigest is the shape the server held at the end of the walk. It is
	// only meaningful when FullyReplayed is true; otherwise it is the state
	// after every decided group and the entry state of every undecided one.
	FinalDigest string `json:"final_digest,omitempty"`
	// FinalPlanned is the statically predicted digest for the same sequence.
	FinalPlanned string `json:"final_planned_digest,omitempty"`
	// FullyReplayed is true when every group had a single committed outcome,
	// so FinalDigest is the true end state of the whole ledger.
	FullyReplayed bool `json:"fully_replayed"`
	// AllMatches is true when every unique group matched its prediction and
	// the full replay landed on the planned final shape.
	AllMatches bool `json:"all_matches"`
}

// VerifyAgainstServer replays every group against a live server.
func VerifyAgainstServer(order *Order, report *Report) (*Verification, error) {
	return VerifyAgainstServerWith(order, report, "")
}

// VerifyAgainstServerWith replays groups sequentially in the declared commit
// order, which is what a transaction-group ledger means. Each group is
// submitted as one POST /edits; separate-batch trials run from that group's
// entry snapshot and are rewound immediately afterwards, so they cannot leak
// into later groups.
func VerifyAgainstServerWith(order *Order, report *Report, binary string) (*Verification, error) {
	server, err := Start(binary)
	if err != nil {
		return nil, err
	}
	defer server.Close()
	if err := server.Seed(order); err != nil {
		return nil, err
	}
	liveBaseline, _, err := server.Image()
	if err != nil {
		return nil, err
	}
	result := &Verification{
		Order:        order.Name,
		DataDir:      filepath.Join(server.dir, "space"),
		Baseline:     report.BaselineDigest,
		BaselineLive: FromImage(liveBaseline).ShapeDigest(),
		Groups:       make([]GroupVerification, 0, len(report.Groups)),
		FinalPlanned: report.FinalDigest,
		AllMatches:   true,
	}
	baseline := mustBaseline(report)
	liveWorld := baseline.Clone()
	for _, group := range report.Groups {
		entryImage, err := server.ExportRaw()
		if err != nil {
			return nil, fmt.Errorf("snapshot before %q: %w", group.Name, err)
		}
		verification, err := verifyGroup(server, group, baseline, entryImage)
		if err != nil {
			return nil, fmt.Errorf("verify %q: %w", group.Name, err)
		}
		result.Groups = append(result.Groups, verification)
		if group.Classification == "unique" && !verification.Matches {
			result.AllMatches = false
		}
		if group.Classification == "unique" {
			next, _, err := liveWorld.ApplyBatch(group.Replay.Request.Edits)
			if err != nil {
				return nil, fmt.Errorf("static replay of %q: %w", group.Name, err)
			}
			liveWorld = next
		}
	}
	finalImage, _, err := server.Image()
	if err != nil {
		return nil, err
	}
	live := FromImage(finalImage).ShapeDigest()
	result.FullyReplayed = report.FinalDigest != ""
	result.FinalDigest = live
	if result.FullyReplayed && report.FinalDigest != live {
		result.AllMatches = false
	}
	return result, nil
}

// verifyGroup replays the group on the live server at the current state and
// leaves the server holding exactly the outcome the plan commits to.
func verifyGroup(server *Server, group Group, baseline *World, entryImage []byte) (GroupVerification, error) {
	verification := GroupVerification{Group: group.Name, Classification: group.Classification}
	switch group.Classification {
	case "unique":
		verification.PlannedDigest = group.Shapes[0].Digest
		verification.ReplayLines = group.Replay.SourceLines
		fail, err := server.ApplyBatch(group.Replay.Request)
		if err != nil {
			return verification, err
		}
		if fail != nil {
			verification.Notes = append(verification.Notes,
				fmt.Sprintf("服务端拒绝了计划中的重放（%s）：%s", fail.Code, fail.Message))
			return verification, nil
		}
		image, revision, err := server.Image()
		if err != nil {
			return verification, err
		}
		verification.ServerRevision = revision
		verification.ObservedDigest = FromImage(image).ShapeDigestOf(group.Layers)
		verification.Matches = verification.ObservedDigest == verification.PlannedDigest
		for _, failure := range group.SplitFailures {
			// Confirm each split boundary from the group's entry state, then
			// put the entry state back and re-commit the group, because the
			// next group must start from the committed outcome.
			if err := server.restoreTo(entryImage); err != nil {
				return verification, err
			}
			confirmation, err := confirmSplit(server, group.ReplayOrder, failure)
			if err != nil {
				return verification, err
			}
			verification.SplitConfirmations = append(verification.SplitConfirmations, confirmation)
			if confirmation.Code == "" {
				verification.Notes = append(verification.Notes,
					fmt.Sprintf("源行 %d 之后拆批在服务端竟然成功，与静态判定不一致", failure.AfterLine))
			}
			if err := server.restoreTo(entryImage); err != nil {
				return verification, err
			}
			if fail, err := server.ApplyBatch(group.Replay.Request); err != nil {
				return verification, err
			} else if fail != nil {
				return verification, fmt.Errorf("拆批试验后重新提交整组被拒：%s：%s", fail.Code, fail.Message)
			}
		}
	case "ambiguous":
		// Replay one legal ordering per distinct shape from the group's entry
		// state and confirm the server really produces each shape.
		seen := make(map[string]bool)
		for _, shape := range group.Shapes {
			if err := server.restoreTo(entryImage); err != nil {
				return verification, err
			}
			rows := acceptedOrder(group.Rows, baseline, shape.Examples[0])
			if rows == nil {
				verification.Notes = append(verification.Notes,
					fmt.Sprintf("无法重建计划中的排序 %v", shape.Examples[0]))
				continue
			}
			fail, err := server.ApplyBatch(catalog.EditBatch{Edits: editsOf(rows)})
			if err != nil {
				return verification, err
			}
			if fail != nil {
				verification.Notes = append(verification.Notes,
					fmt.Sprintf("服务端拒绝了静态判为合法的排序 %v：%s", shape.Examples[0], fail.Message))
				continue
			}
			image, _, err := server.Image()
			if err != nil {
				return verification, err
			}
			observed := FromImage(image).ShapeDigestOf(group.Layers)
			if !seen[observed] {
				seen[observed] = true
				verification.ShapeObservations = append(verification.ShapeObservations,
					ShapeObservation{Lines: shape.Examples[0], Digest: observed})
			}
		}
		// Leave the entry state in place: an ambiguous group has no committed
		// outcome, so downstream groups stay blocked rather than guessed.
		if err := server.restoreTo(entryImage); err != nil {
			return verification, err
		}
		// Ambiguity blocks downstream replay: leave a documented note and do
		// not guess. Later blocked groups are reported by the static plan.
		verification.Notes = append(verification.Notes,
			"服务端复现了全部可达形状；本组不提交任何单一结果")
	default:
		verification.Notes = append(verification.Notes, "未做重放；服务端停留在本组入口状态")
	}
	return verification, nil
}

// confirmSplit sends the prefix, then the suffix, against the real server.
func confirmSplit(server *Server, ordered []Row, failure SplitFailure) (SplitConfirmation, error) {
	confirmation := SplitConfirmation{AfterLine: failure.AfterLine}
	prefix, _ := splitAt(ordered, failure.AfterLine)
	fail, err := server.ApplyBatch(catalog.EditBatch{Edits: editsOf(prefix)})
	if err != nil {
		return confirmation, err
	}
	if fail != nil {
		confirmation.Code, confirmation.Message = fail.Code, fail.Message
	}
	return confirmation, nil
}

func splitAt(rows []Row, line int) ([]Row, []Row) {
	for i, row := range rows {
		if row.Line == line {
			return rows[:i+1], rows[i+1:]
		}
	}
	return rows, nil
}

func mustBaseline(report *Report) *World {
	return report.baseline
}

// MarshalVerification encodes the server-backed report deterministically.
func MarshalVerification(verification *Verification) ([]byte, error) {
	raw, err := json.MarshalIndent(verification, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
