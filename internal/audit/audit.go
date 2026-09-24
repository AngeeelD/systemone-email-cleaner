// Package audit provides append-only JSONL audit logging and rollback verification.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Answer mirrors systemone.Answer for audit persistence. Probabilities are kept
// because the policy gates on probabilities["A"], not on Confidence: without
// them the thresholds cannot be replayed or tuned from a recorded run.
type Answer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Action describes label mutations.
type Action struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// Record is one line in audit/<run-id>.jsonl.
type Record struct {
	RunID        string            `json:"run_id"`
	MessageID    string            `json:"message_id"`
	Subject      string            `json:"subject"`
	Outcome      string            `json:"outcome"` // applied | skipped | error
	Reason       string            `json:"reason"`
	RoutingModel string            `json:"routing_model,omitempty"`
	Answers      map[string]Answer `json:"answers,omitempty"`
	LabelsBefore []string          `json:"labels_before"`
	LabelsAfter  []string          `json:"labels_after"`
	Action       *Action           `json:"action,omitempty"`
}

// Append appends rec as JSONL to audit/<run-id>.jsonl, creating dir if needed.
// File is 0644, dir 0755.
func Append(dir string, rec Record) error {
	if dir == "" {
		return fmt.Errorf("audit dir is empty")
	}
	if rec.RunID == "" {
		return fmt.Errorf("record run_id is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create audit dir: %w", err)
	}
	path := filepath.Join(dir, rec.RunID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open audit file: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write audit record: %w", err)
	}
	return nil
}

// Read returns all records for runID.
func Read(dir, runID string) ([]Record, error) {
	path := filepath.Join(dir, runID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	// Allow large lines (body preview may be 800 chars but answers etc).
	const maxCap = 2 * 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxCap)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("decode audit record: %w", err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read audit file: %w", err)
	}
	return out, nil
}

// ListRuns returns run IDs (without .jsonl) sorted lexicographically.
// Lexicographic order matches chronological for 20060102T150405Z format.
func ListRuns(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list audit dir: %w", err)
	}
	var runs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		runs = append(runs, strings.TrimSuffix(name, ".jsonl"))
	}
	sort.Strings(runs)
	return runs, nil
}

// LoadLatest returns records and runID of the most recent run.
func LoadLatest(dir string) ([]Record, string, error) {
	runs, err := ListRuns(dir)
	if err != nil {
		return nil, "", err
	}
	if len(runs) == 0 {
		return nil, "", fmt.Errorf("no audit runs in %s", dir)
	}
	latest := runs[len(runs)-1]
	recs, err := Read(dir, latest)
	if err != nil {
		return nil, "", err
	}
	return recs, latest, nil
}

// ShouldRevert reports whether a record can be safely reverted given current labels.
// It verifies the message still carries labels this run set (add). If any added
// label is missing, the user has moved it manually and we must not clobber.
func ShouldRevert(rec Record, currentLabels []string) (bool, string) {
	if rec.Outcome != "applied" {
		return false, "record not applied"
	}
	if rec.Action == nil {
		return false, "record has no action"
	}
	// Build set for quick lookup.
	curSet := make(map[string]bool, len(currentLabels))
	for _, l := range currentLabels {
		curSet[l] = true
	}
	// Every label we added must still be present.
	for _, add := range rec.Action.Add {
		if !curSet[add] {
			return false, fmt.Sprintf("label %q no longer present, skipping", add)
		}
	}
	// Every label we removed must still be absent (i.e., not re-added manually).
	for _, rem := range rec.Action.Remove {
		if curSet[rem] {
			return false, fmt.Sprintf("label %q was re-added manually, skipping", rem)
		}
	}
	return true, ""
}

// Contains helper for general use.
func contains(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}
