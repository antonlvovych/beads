package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// githubConfigStore serves tracker config values for GitHub Tracker.Init.
type githubConfigStore struct {
	tracker.Store
	values map[string]string
}

func (s *githubConfigStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func TestGitHubConfigToEnvVar_PushAssignee(t *testing.T) {
	if got := githubConfigToEnvVar("github.push_assignee"); got != "GITHUB_PUSH_ASSIGNEE" {
		t.Errorf("githubConfigToEnvVar(github.push_assignee) = %q, want GITHUB_PUSH_ASSIGNEE", got)
	}
}

// TestBuildGitHubPushHooks_PushAssignee checks that the push hooks follow
// github.push_assignee: with it off an assignee-only change is invisible to
// the dedup hash and comparator; with it on the change is pushed.
func TestBuildGitHubPushHooks_PushAssignee(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_PUSH_ASSIGNEE", "")

	local := &types.Issue{Title: "T", Description: "D", IssueType: "task", Priority: 2, Status: types.StatusOpen, Assignee: "alice"}
	reassigned := *local
	reassigned.Assignee = "bob"
	remote := &tracker.TrackerIssue{Raw: &github.Issue{
		Title: "T", Body: "D", State: "open",
		Labels:    []github.Label{{Name: "type::task"}, {Name: "priority::medium"}},
		Assignees: []github.User{{Login: "alice"}},
	}}

	for _, tt := range []struct {
		value       string
		wantChanged bool
	}{{"", false}, {"true", true}} {
		t.Run("push_assignee="+tt.value, func(t *testing.T) {
			gt := &github.Tracker{}
			values := map[string]string{"github.owner": "o", "github.repo": "r", "github.push_assignee": tt.value}
			if err := gt.Init(context.Background(), &githubConfigStore{values: values}); err != nil {
				t.Fatalf("Init: %v", err)
			}
			hooks := buildGitHubPushHooks(gt)

			hashChanged := hooks.ContentHash(local) != hooks.ContentHash(&reassigned)
			if hashChanged != tt.wantChanged {
				t.Errorf("assignee-only change altered hash = %v, want %v", hashChanged, tt.wantChanged)
			}
			if !hooks.ContentEqual(local, remote) {
				t.Error("unchanged issue should compare equal")
			}
			if equal := hooks.ContentEqual(&reassigned, remote); equal == tt.wantChanged {
				t.Errorf("reassigned ContentEqual = %v, want %v", equal, !tt.wantChanged)
			}
		})
	}
}
