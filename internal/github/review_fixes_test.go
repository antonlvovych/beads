package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestListRelationshipPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			w.Header().Set("Link", "<"+"http://"+r.Host+r.URL.Path+"?page=2&per_page=100>; rel=\"next\"")
		}
		if page == "2" {
			_ = json.NewEncoder(w).Encode([]Issue{{Number: 2}})
			return
		}
		_ = json.NewEncoder(w).Encode([]Issue{{Number: 1}})
	}))
	defer server.Close()

	client := NewClient("token", "owner", "repo").WithBaseURL(server.URL)
	for _, get := range []struct {
		name string
		fn   func(context.Context) ([]Issue, error)
	}{
		{"sub-issues", func(ctx context.Context) ([]Issue, error) { return client.ListSubIssues(ctx, 7) }},
		{"blocked-by", func(ctx context.Context) ([]Issue, error) { return client.ListBlockedBy(ctx, 7) }},
	} {
		t.Run(get.name, func(t *testing.T) {
			issues, err := get.fn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 2 {
				t.Fatalf("issues = %+v, want both pages", issues)
			}
		})
	}
}

func TestRefScopeRejectsForeignHostAndRepository(t *testing.T) {
	scope := NewRefScope("https://api.github.com", "owner", "repo")
	for _, ref := range []string{
		"https://gitlab.com/owner/repo/-/issues/42",
		"https://gitlab.com/owner/repo/issues/42",
		"https://github.com/other/repo/issues/42",
		"https://github.com/owner/other/issues/42",
		"https://github.com/owner/repo/-/issues/42",
		"https://github.com/evil/owner/repo/issues/42",
		"https://github.com/owner/repo/pull/42",
		"https://ghe.example.com/owner/repo/issues/42",
		"ftp://github.com/owner/repo/issues/42",
		"gitlab:42",
		"42",
		"",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); ok {
			t.Errorf("IssueNumberFromRef(%q) = %d, true; want rejected", ref, number)
		}
	}
	for _, ref := range []string{
		"https://github.com/owner/repo/issues/42",
		"https://github.com/Owner/Repo/issues/42",
		"https://api.github.com/repos/owner/repo/issues/42",
		"github:42",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); !ok || number != 42 {
			t.Errorf("IssueNumberFromRef(%q) = %d, %v; want 42, true", ref, number, ok)
		}
	}
}

func TestRefScopeGitHubEnterprise(t *testing.T) {
	scope := NewRefScope("https://ghe.example.com/api/v3", "owner", "repo")
	for _, ref := range []string{
		"https://ghe.example.com/owner/repo/issues/42",
		"https://ghe.example.com/api/v3/repos/owner/repo/issues/42",
		"github:42",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); !ok || number != 42 {
			t.Errorf("IssueNumberFromRef(%q) = %d, %v; want 42, true", ref, number, ok)
		}
	}
	for _, ref := range []string{
		"https://github.com/owner/repo/issues/42",
		"https://gitlab.example.com/owner/repo/-/issues/42",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); ok {
			t.Errorf("IssueNumberFromRef(%q) = %d, true; want rejected", ref, number)
		}
	}
}

func TestRefScopeLinksSkipForeignRefs(t *testing.T) {
	scope := NewRefScope("https://api.github.com", "owner", "repo")
	child := githubIssue("bd-child", "https://github.com/owner/repo/issues/10", types.TypeTask)
	for _, ref := range []string{
		"https://gitlab.com/owner/repo/-/issues/42",
		"https://github.com/other/repo/issues/42",
	} {
		parent := githubDep("bd-parent", ref, types.DepParentChild)
		if link, ok := scope.SubIssueLinkFromParentChild(child, parent); ok {
			t.Errorf("sub-issue link from %q = %+v; want skipped", ref, link)
		}
		blocker := githubDep("bd-blocker", ref, types.DepBlocks)
		if link, ok := scope.BlockedByLinkFromBeadsDependency(child, blocker); ok {
			t.Errorf("blocked_by link from %q = %+v; want skipped", ref, link)
		}
	}
}
