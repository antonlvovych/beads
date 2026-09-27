// Package github provides client and data types for the GitHub REST API.
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MappingConfig configures how GitHub fields map to beads fields.
type MappingConfig struct {
	PriorityMap  map[string]int    // priority label value -> beads priority (0-4)
	StateMap     map[string]string // GitHub state -> beads status
	LabelTypeMap map[string]string // type label value -> beads issue type

	// PushAssignee enables sending the bead assignee as the GitHub issue's
	// assignee set on create and update (github.push_assignee). Off by
	// default so pushes, and their persisted content hashes, are unchanged.
	PushAssignee bool
}

// DefaultMappingConfig returns the default mapping configuration.
// Uses exported mapping constants from types.go as the single source of truth.
func DefaultMappingConfig() *MappingConfig {
	// Copy PriorityMapping to avoid external modification
	priorityMap := make(map[string]int, len(PriorityMapping))
	for k, v := range PriorityMapping {
		priorityMap[k] = v
	}

	// Copy typeMapping to avoid external modification
	labelTypeMap := make(map[string]string, len(typeMapping))
	for k, v := range typeMapping {
		labelTypeMap[k] = v
	}

	return &MappingConfig{
		PriorityMap: priorityMap,
		// StateMap maps GitHub states to beads statuses
		StateMap: map[string]string{
			"open":   StatusMapping["open"],
			"closed": StatusMapping["closed"],
		},
		LabelTypeMap: labelTypeMap,
	}
}

// priorityFromLabels extracts priority from GitHub labels.
// Returns default priority (2 = medium) if no priority label found.
func priorityFromLabels(labels []string, config *MappingConfig) int {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "priority" {
			if p, ok := config.PriorityMap[strings.ToLower(value)]; ok {
				return p
			}
		}
	}
	return 2 // Default to medium
}

// statusFromLabelsAndState determines beads status from GitHub labels and state.
// GitHub's closed state takes precedence over status labels.
func statusFromLabelsAndState(labels []string, state string, config *MappingConfig) string {
	// Closed state always wins
	if state == "closed" {
		return "closed"
	}

	// Check for status label
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "status" {
			normalized := strings.ToLower(value)
			if normalized == "in_progress" {
				return "in_progress"
			}
			if normalized == "blocked" {
				return "blocked"
			}
			if normalized == "deferred" {
				return "deferred"
			}
		}
	}

	// Default: map GitHub state to beads status
	if s, ok := config.StateMap[state]; ok {
		return s
	}
	return "open"
}

// typeFromLabels extracts issue type from GitHub labels.
// Checks both scoped (type::bug) and bare (bug) labels.
// Returns "task" if no type label found.
func typeFromLabels(labels []string, config *MappingConfig) string {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "type" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
		// Also check bare labels (no prefix)
		if prefix == "" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
	}
	return "task" // Default to task
}

// GitHubIssueToBeads converts a GitHub Issue to a beads Issue.
func GitHubIssueToBeads(gh *Issue, config *MappingConfig) *IssueConversion {
	htmlURL := gh.HTMLURL
	sourceSystem := fmt.Sprintf("github:%s:%d", gh.HTMLURL, gh.Number)
	// Use a cleaner source system format if HTMLURL is not available
	if htmlURL == "" {
		sourceSystem = fmt.Sprintf("github:%d:%d", gh.ID, gh.Number)
	}

	labelNames := gh.LabelNames()

	issue := &types.Issue{
		Title:        gh.Title,
		Description:  gh.Body,
		ExternalRef:  &htmlURL,
		SourceSystem: sourceSystem,
		IssueType:    types.IssueType(typeFromLabels(labelNames, config)),
		Priority:     priorityFromLabels(labelNames, config),
		Status:       types.Status(statusFromLabelsAndState(labelNames, gh.State, config)),
		Labels:       filterNonScopedLabels(labelNames),
	}

	// Set assignee from GitHub user
	if gh.Assignee != nil {
		issue.Assignee = gh.Assignee.Login
	}

	// Set timestamps
	if gh.CreatedAt != nil {
		issue.CreatedAt = *gh.CreatedAt
	}
	if gh.UpdatedAt != nil {
		issue.UpdatedAt = *gh.UpdatedAt
	}

	return &IssueConversion{
		Issue:        issue,
		Dependencies: []DependencyInfo{},
	}
}

// BeadsIssueToGitHubFields converts a beads Issue to GitHub API update fields.
func BeadsIssueToGitHubFields(issue *types.Issue, config *MappingConfig) map[string]interface{} {
	fields := map[string]interface{}{
		"title": issue.Title,
		"body":  issue.Description,
	}

	// Build labels from type, priority, and status
	var labels []string

	// Add type label
	if issue.IssueType != "" {
		labels = append(labels, "type::"+string(issue.IssueType))
	}

	// Add priority label
	priorityLabel := priorityToLabel(issue.Priority)
	if priorityLabel != "" {
		labels = append(labels, "priority::"+priorityLabel)
	}

	// Add status label (if not open or closed - those are handled by state)
	if issue.Status == types.StatusInProgress {
		labels = append(labels, "status::in_progress")
	} else if issue.Status == types.StatusBlocked {
		labels = append(labels, "status::blocked")
	} else if issue.Status == types.StatusDeferred {
		labels = append(labels, "status::deferred")
	}

	// Add any existing non-scoped labels
	labels = append(labels, issue.Labels...)

	fields["labels"] = labels

	// Set state for closed issues
	if issue.Status == types.StatusClosed {
		fields["state"] = "closed"
	} else {
		fields["state"] = "open"
	}

	// Send the assignee set only when opted in. An empty array clears the
	// remote assignees, so unassigning a bead carries over to GitHub.
	if config != nil && config.PushAssignee {
		fields["assignees"] = desiredAssignees(issue)
	}

	return fields
}

// desiredAssignees returns the GitHub assignee set a push would send for
// issue: the bead assignee as a login, or an empty set when unassigned.
func desiredAssignees(issue *types.Issue) []string {
	if login := strings.TrimSpace(issue.Assignee); login != "" {
		return []string{login}
	}
	return []string{}
}

// remoteAssigneeLogins returns the logins currently assigned to a GitHub
// issue, falling back to the singular assignee field when the list is absent.
func remoteAssigneeLogins(remote *Issue) []string {
	logins := make([]string, 0, len(remote.Assignees))
	for _, u := range remote.Assignees {
		logins = append(logins, u.Login)
	}
	if len(logins) == 0 && remote.Assignee != nil && remote.Assignee.Login != "" {
		logins = append(logins, remote.Assignee.Login)
	}
	return logins
}

// assigneeSetsEqual reports whether a and b contain the same logins, ignoring
// order and case (GitHub logins are case-insensitive).
func assigneeSetsEqual(a, b []string) bool {
	return labelSetsEqual(lowerAll(a), lowerAll(b))
}

func lowerAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(s)
	}
	return out
}

// PushFieldsEqual reports whether a GitHub push would be a no-op by comparing
// only the fields a push can actually mutate: title, body, state, the label
// set that BeadsIssueToGitHubFields would send, and (with PushAssignee) the
// assignee set. When these already match
// the remote issue, the push is redundant and can be skipped — this is what
// prevents `bd github sync --push-only` from re-PATCHing every issue on every
// run (gastownhall/beads#4214). Mirrors linear.PushFieldsEqual.
func PushFieldsEqual(local *types.Issue, remote *Issue, config *MappingConfig) bool {
	if local == nil || remote == nil {
		return false
	}
	if local.Title != remote.Title {
		return false
	}
	if local.Description != remote.Body {
		return false
	}

	desiredState := "open"
	if local.Status == types.StatusClosed {
		desiredState = "closed"
	}
	if !strings.EqualFold(desiredState, remote.State) {
		return false
	}

	// Compare the label set we would push against the remote's current labels.
	// Both include the scoped type::/priority::/status:: labels plus any
	// non-scoped labels, so an unchanged issue produces an identical set.
	desiredLabels, _ := BeadsIssueToGitHubFields(local, config)["labels"].([]string)
	if !labelSetsEqual(desiredLabels, remote.LabelNames()) {
		return false
	}

	if config != nil && config.PushAssignee {
		return assigneeSetsEqual(desiredAssignees(local), remoteAssigneeLogins(remote))
	}
	return true
}

// PushContentHash returns a stable hex fingerprint of the fields a push would
// send to GitHub: title, body, desired state, and the order-independent label
// set produced by BeadsIssueToGitHubFields, plus the assignee set when
// PushAssignee is on (so hashes recorded with it off stay valid). The engine persists this hash in
// local_metadata after each push and compares it before fetching the remote
// issue, so an unchanged issue is skipped without any API call
// (gastownhall/beads#4214). It is derived from the same fields PushFieldsEqual
// compares, so the two agree on what "unchanged" means. Fields are
// length-prefixed before hashing so distinct field boundaries cannot collide.
func PushContentHash(local *types.Issue, config *MappingConfig) string {
	if local == nil {
		return ""
	}

	desiredState := "open"
	if local.Status == types.StatusClosed {
		desiredState = "closed"
	}

	desiredLabels, _ := BeadsIssueToGitHubFields(local, config)["labels"].([]string)
	sortedLabels := append([]string(nil), desiredLabels...)
	slices.Sort(sortedLabels)

	parts := []string{local.Title, local.Description, desiredState, strings.Join(sortedLabels, "\x00")}
	if config != nil && config.PushAssignee {
		assignees := lowerAll(desiredAssignees(local))
		slices.Sort(assignees)
		parts = append(parts, "assignees:"+strings.Join(assignees, "\x00"))
	}

	h := sha256.New()
	for _, s := range parts {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// labelSetsEqual reports whether a and b contain the same labels, ignoring
// order (GitHub does not preserve label order across a round-trip).
func labelSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// priorityToLabel converts beads priority (0-4) to GitHub priority label value.
func priorityToLabel(priority int) string {
	switch priority {
	case 0:
		return "critical"
	case 1:
		return "high"
	case 2:
		return "medium"
	case 3:
		return "low"
	case 4:
		return "none"
	default:
		return "medium"
	}
}

// filterNonScopedLabels returns only labels without scoped prefixes.
// Removes priority::*, status::*, and type::* labels.
func filterNonScopedLabels(labels []string) []string {
	var filtered []string
	for _, label := range labels {
		prefix, _ := parseLabelPrefix(label)
		// Skip scoped labels that we handle specially
		if prefix == "priority" || prefix == "status" || prefix == "type" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}
