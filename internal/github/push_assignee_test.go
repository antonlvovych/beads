package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// assigneeTestIssue is the fixture for the github.push_assignee tests. Its
// flag-off PushContentHash is pinned below so hashes persisted before the
// option existed stay valid.
func assigneeTestIssue() *types.Issue {
	return &types.Issue{
		Title:       "Fix the thing",
		Description: "Some body text",
		IssueType:   types.IssueType("task"),
		Priority:    1,
		Status:      types.StatusInProgress,
		Labels:      []string{"backend"},
		Assignee:    "alice",
	}
}

// preAssigneePushHash is PushContentHash(assigneeTestIssue()) computed on main
// before github.push_assignee was added.
const preAssigneePushHash = "5638d81250add127fa91b24bb6bfbfa44b76fda5df19cee47d585d47ac0c830a"

func pushAssigneeConfig() *MappingConfig {
	config := DefaultMappingConfig()
	config.PushAssignee = true
	return config
}

func TestBeadsIssueToGitHubFields_PushAssignee(t *testing.T) {
	t.Run("flag off sends no assignees", func(t *testing.T) {
		fields := BeadsIssueToGitHubFields(assigneeTestIssue(), DefaultMappingConfig())
		if _, ok := fields["assignees"]; ok {
			t.Errorf("assignees sent with push_assignee off: %v", fields["assignees"])
		}
	})

	t.Run("flag on sends the bead assignee", func(t *testing.T) {
		fields := BeadsIssueToGitHubFields(assigneeTestIssue(), pushAssigneeConfig())
		got, ok := fields["assignees"].([]string)
		if !ok || len(got) != 1 || got[0] != "alice" {
			t.Errorf("assignees = %#v, want [alice]", fields["assignees"])
		}
	})

	t.Run("flag on sends empty set when unassigned", func(t *testing.T) {
		issue := assigneeTestIssue()
		issue.Assignee = "  "
		fields := BeadsIssueToGitHubFields(issue, pushAssigneeConfig())
		got, ok := fields["assignees"].([]string)
		if !ok || got == nil || len(got) != 0 {
			t.Errorf("assignees = %#v, want non-nil empty slice", fields["assignees"])
		}
	})
}

func TestPushContentHash_PushAssignee(t *testing.T) {
	off := DefaultMappingConfig()
	on := pushAssigneeConfig()

	if got := PushContentHash(assigneeTestIssue(), off); got != preAssigneePushHash {
		t.Errorf("flag-off hash changed: got %s, want %s", got, preAssigneePushHash)
	}

	reassigned := assigneeTestIssue()
	reassigned.Assignee = "bob"
	if PushContentHash(reassigned, off) != PushContentHash(assigneeTestIssue(), off) {
		t.Error("flag off: assignee-only change must not change the hash")
	}

	base := PushContentHash(assigneeTestIssue(), on)
	if base == preAssigneePushHash {
		t.Error("flag on: hash must cover the assignee set")
	}
	if PushContentHash(assigneeTestIssue(), on) != base {
		t.Error("flag on: hash must be stable for an unchanged issue")
	}
	if PushContentHash(reassigned, on) == base {
		t.Error("flag on: assignee-only change must change the hash")
	}
	unassigned := assigneeTestIssue()
	unassigned.Assignee = ""
	if PushContentHash(unassigned, on) == base {
		t.Error("flag on: unassigning must change the hash")
	}
}

func TestPushFieldsEqual_PushAssignee(t *testing.T) {
	remote := func(logins ...string) *Issue {
		users := make([]User, 0, len(logins))
		for _, l := range logins {
			users = append(users, User{Login: l})
		}
		return &Issue{
			Title:     "Fix the thing",
			Body:      "Some body text",
			State:     "open",
			Labels:    []Label{{Name: "type::task"}, {Name: "priority::high"}, {Name: "status::in_progress"}, {Name: "backend"}},
			Assignees: users,
		}
	}
	unassigned := assigneeTestIssue()
	unassigned.Assignee = ""

	tests := []struct {
		name   string
		local  *types.Issue
		remote *Issue
		config *MappingConfig
		want   bool
	}{
		{"flag off ignores assignee", assigneeTestIssue(), remote("bob"), DefaultMappingConfig(), true},
		{"same assignee", assigneeTestIssue(), remote("alice"), pushAssigneeConfig(), true},
		{"login case differs", assigneeTestIssue(), remote("Alice"), pushAssigneeConfig(), true},
		{"assignee-only change", assigneeTestIssue(), remote("bob"), pushAssigneeConfig(), false},
		{"remote unassigned", assigneeTestIssue(), remote(), pushAssigneeConfig(), false},
		{"extra remote assignee", assigneeTestIssue(), remote("alice", "bob"), pushAssigneeConfig(), false},
		{"local unassigned", unassigned, remote("alice"), pushAssigneeConfig(), false},
		{"both unassigned", unassigned, remote(), pushAssigneeConfig(), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PushFieldsEqual(tt.local, tt.remote, tt.config); got != tt.want {
				t.Errorf("PushFieldsEqual = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("singular assignee fallback", func(t *testing.T) {
		r := remote()
		r.Assignee = &User{Login: "alice"}
		if !PushFieldsEqual(assigneeTestIssue(), r, pushAssigneeConfig()) {
			t.Error("remote.Assignee should count when Assignees is empty")
		}
	})
}

// issueServer records each create/update request body and replies with the
// scripted status and body for that call, defaulting to a 200/201 issue.
type issueServer struct {
	mu        sync.Mutex
	bodies    []map[string]interface{}
	responses []scriptedResponse
}

type scriptedResponse struct {
	status int
	body   string
}

func (s *issueServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		s.mu.Lock()
		call := len(s.bodies)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if call < len(s.responses) {
			w.WriteHeader(s.responses[call].status)
			_, _ = w.Write([]byte(s.responses[call].body))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(`{"id":1001,"number":42,"title":"Fix the thing","state":"open","html_url":"https://github.com/owner/repo/issues/42"}`))
	}
}

func newAssigneeTestTracker(t *testing.T, s *issueServer, config *MappingConfig) *Tracker {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)
	return &Tracker{client: newRateLimitTestClient(srv.URL), config: config}
}

const assignee422 = `{"message":"Validation Failed","errors":[{"value":"alice","resource":"Issue","field":"assignees","code":"invalid"}],"documentation_url":"https://docs.github.com/rest/issues/issues#create-an-issue","status":"422"}`

func TestTrackerPushAssignee_RequestBodies(t *testing.T) {
	ctx := context.Background()
	unassigned := assigneeTestIssue()
	unassigned.Assignee = ""

	tests := []struct {
		name   string
		config *MappingConfig
		issue  *types.Issue
		want   []interface{} // nil = field must be absent
	}{
		{"flag off", DefaultMappingConfig(), assigneeTestIssue(), nil},
		{"flag on assigned", pushAssigneeConfig(), assigneeTestIssue(), []interface{}{"alice"}},
		{"flag on unassigned", pushAssigneeConfig(), unassigned, []interface{}{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &issueServer{}
			tr := newAssigneeTestTracker(t, s, tt.config)
			if _, err := tr.CreateIssue(ctx, tt.issue); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			if _, err := tr.UpdateIssue(ctx, "42", tt.issue); err != nil {
				t.Fatalf("UpdateIssue: %v", err)
			}
			if len(s.bodies) != 2 {
				t.Fatalf("got %d requests, want 2", len(s.bodies))
			}
			for i, op := range []string{"create", "update"} {
				got, present := s.bodies[i]["assignees"]
				if tt.want == nil {
					if present {
						t.Errorf("%s: assignees sent with flag off: %v", op, got)
					}
					continue
				}
				gotList, ok := got.([]interface{})
				if !present || !ok || len(gotList) != len(tt.want) || (len(gotList) == 1 && gotList[0] != tt.want[0]) {
					t.Errorf("%s: assignees = %#v, want %#v", op, got, tt.want)
				}
			}
		})
	}
}

func TestTrackerPushAssignee_Rejected422RetriesWithoutAssignee(t *testing.T) {
	ctx := context.Background()
	for _, op := range []string{"create", "update"} {
		t.Run(op, func(t *testing.T) {
			s := &issueServer{responses: []scriptedResponse{{http.StatusUnprocessableEntity, assignee422}}}
			tr := newAssigneeTestTracker(t, s, pushAssigneeConfig())

			var ti *tracker.TrackerIssue
			var err error
			if op == "create" {
				ti, err = tr.CreateIssue(ctx, assigneeTestIssue())
			} else {
				ti, err = tr.UpdateIssue(ctx, "42", assigneeTestIssue())
			}
			if err != nil {
				t.Fatalf("%s must not fail on a rejected assignee: %v", op, err)
			}
			if len(s.bodies) != 2 {
				t.Fatalf("got %d requests, want exactly 2 (one retry)", len(s.bodies))
			}
			if _, ok := s.bodies[0]["assignees"]; !ok {
				t.Error("first request should carry assignees")
			}
			if _, ok := s.bodies[1]["assignees"]; ok {
				t.Error("retry must omit assignees")
			}
			if s.bodies[1]["title"] != "Fix the thing" {
				t.Errorf("retry dropped other fields: %v", s.bodies[1])
			}
			if len(ti.Warnings) != 1 || !strings.Contains(ti.Warnings[0], `"alice"`) {
				t.Errorf("Warnings = %#v, want one warning naming alice", ti.Warnings)
			}
		})
	}
}

func TestTrackerPushAssignee_Other422IsNotRetried(t *testing.T) {
	const title422 = `{"message":"Validation Failed","errors":[{"resource":"Issue","field":"title","code":"missing_field"}]}`
	s := &issueServer{responses: []scriptedResponse{{http.StatusUnprocessableEntity, title422}}}
	tr := newAssigneeTestTracker(t, s, pushAssigneeConfig())

	_, err := tr.CreateIssue(context.Background(), assigneeTestIssue())
	if err == nil {
		t.Fatal("expected a non-assignee 422 to fail the create")
	}
	if !strings.Contains(err.Error(), "status 422") {
		t.Errorf("error should keep the API error text, got: %v", err)
	}
	if len(s.bodies) != 1 {
		t.Errorf("got %d requests, want 1 (no retry)", len(s.bodies))
	}
}

// configStore serves tracker config values for Tracker.Init tests.
type configStore struct {
	tracker.Store
	values map[string]string
}

func (s *configStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func TestTrackerInit_PushAssignee(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_PUSH_ASSIGNEE", "")
	base := map[string]string{"github.owner": "owner", "github.repo": "repo"}
	withValue := func(v string) map[string]string {
		m := map[string]string{"github.push_assignee": v}
		for k, val := range base {
			m[k] = val
		}
		return m
	}

	tests := []struct {
		name    string
		values  map[string]string
		env     string
		want    bool
		wantErr bool
	}{
		{name: "default off", values: base},
		{name: "config true", values: withValue("true"), want: true},
		{name: "config false", values: withValue("false")},
		{name: "env fallback", values: base, env: "true", want: true},
		{name: "invalid value", values: withValue("yes please"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_PUSH_ASSIGNEE", tt.env)
			tr := &Tracker{}
			err := tr.Init(context.Background(), &configStore{values: tt.values})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error for invalid github.push_assignee")
				}
				return
			}
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if got := tr.MappingConfig().PushAssignee; got != tt.want {
				t.Errorf("PushAssignee = %v, want %v", got, tt.want)
			}
		})
	}
}
