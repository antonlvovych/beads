package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// warningUpdateTracker returns partial-success warnings from UpdateIssue.
type warningUpdateTracker struct {
	*mockTracker
	warnings []string
}

func (w *warningUpdateTracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*TrackerIssue, error) {
	ti, err := w.mockTracker.UpdateIssue(ctx, externalID, issue)
	if ti != nil {
		ti.Warnings = w.warnings
	}
	return ti, err
}

// TestEngineSurfacesUpdateWarnings checks that warnings a tracker returns from
// a successful UpdateIssue reach the sync result, as they already do for
// CreateIssue, instead of being silently dropped.
func TestEngineSurfacesUpdateWarnings(t *testing.T) {
	ctx := context.Background()
	state := &engineUOWState{
		issues: map[string]*types.Issue{
			"bd-linked": {
				ID:          "bd-linked",
				Title:       "Already linked",
				Status:      types.StatusOpen,
				IssueType:   types.TypeTask,
				Priority:    2,
				ExternalRef: strPtr("https://test.test/EXT-LINKED"),
			},
		},
		configs: map[string]string{"issue_prefix": "bd"},
	}
	tracker := &warningUpdateTracker{
		mockTracker: newMockTracker("test"),
		warnings:    []string{"assignee dropped"},
	}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Updated != 1 {
		t.Fatalf("Stats.Updated = %d, want 1", result.Stats.Updated)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "assignee dropped") && strings.Contains(w, "bd-linked") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %#v, want the update warning tagged with bd-linked", result.Warnings)
	}
}
