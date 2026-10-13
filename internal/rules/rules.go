// Package rules defines per-repository classification rules in the
// maintainer-sweep overlay shape, their validation, and the hash of the
// settings that govern which evidence is collected.
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// DefaultStaleDays applies when a repository does not set stale_days.
const DefaultStaleDays = 7

// TestApproval configures detection of fork PRs waiting for test approval.
type TestApproval struct {
	MarkerChecks   []string `json:"marker_checks,omitempty"`
	Command        string   `json:"command,omitempty"`
	CIPaths        []string `json:"ci_paths,omitempty"`
	CIPathsCommand string   `json:"ci_paths_command,omitempty"`
}

// Rules are validated per-repository settings.
type Rules struct {
	TestApproval TestApproval `json:"test_approval"`
	StaleDays    int          `json:"stale_days"`
}

// Default returns the rules used when a repository sets none.
func Default() Rules {
	return Rules{StaleDays: DefaultStaleDays}
}

// Parse strictly decodes and validates rules JSON. Unknown keys are an error;
// empty input yields Default().
func Parse(data []byte) (Rules, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Default(), nil
	}
	var raw struct {
		TestApproval *TestApproval `json:"test_approval"`
		StaleDays    *int          `json:"stale_days"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Rules{}, fmt.Errorf("rules: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Rules{}, errors.New("rules: unexpected data after the JSON object")
	}
	r := Default()
	if raw.TestApproval != nil {
		r.TestApproval = *raw.TestApproval
	}
	if raw.StaleDays != nil {
		r.StaleDays = *raw.StaleDays
	}
	if err := r.Validate(); err != nil {
		return Rules{}, err
	}
	return r, nil
}

// Validate reports every rule violation at once.
func (r Rules) Validate() error {
	var errs []error
	if r.StaleDays < 1 {
		errs = append(errs, errors.New("stale_days must be at least 1"))
	}
	ta := r.TestApproval
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	if slices.ContainsFunc(ta.MarkerChecks, blank) {
		errs = append(errs, errors.New("test_approval.marker_checks entries must be non-empty"))
	}
	if slices.ContainsFunc(ta.CIPaths, blank) {
		errs = append(errs, errors.New("test_approval.ci_paths entries must be non-empty"))
	}
	if len(ta.MarkerChecks) > 0 && blank(ta.Command) {
		errs = append(errs, errors.New("test_approval.command is required with marker_checks"))
	}
	if len(ta.CIPaths) > 0 && blank(ta.CIPathsCommand) {
		errs = append(errs, errors.New("test_approval.ci_paths_command is required with ci_paths"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("rules: %w", errors.Join(errs...))
	}
	return nil
}

// JSON renders the rules as indented JSON for storage and editing.
func (r Rules) JSON() string {
	b, _ := json.MarshalIndent(r, "", "  ") // strings, slices, and ints always marshal
	return string(b)
}

// CollectionHash identifies the settings that govern which evidence is read.
// Order of list entries and stale_days do not affect it.
func (r Rules) CollectionHash() string {
	ta := r.TestApproval
	canonical := struct {
		MarkerChecks   []string `json:"marker_checks"`
		Command        string   `json:"command"`
		CIPaths        []string `json:"ci_paths"`
		CIPathsCommand string   `json:"ci_paths_command"`
	}{sortedCopy(ta.MarkerChecks), ta.Command, sortedCopy(ta.CIPaths), ta.CIPathsCommand}
	b, _ := json.Marshal(canonical) // strings and string slices always marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	slices.Sort(out)
	return out
}
