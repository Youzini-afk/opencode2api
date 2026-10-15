package identity

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestRequestIdentityIsStablePerSessionAndFreshPerRequest(t *testing.T) {
	request := func(session string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("x-session-id", session)
		return r
	}
	body := map[string]any{"model": "test-model"}
	first := DeriveRequestIDs(request("conversation-a"), body, defaultOpenCodeVersions)
	second := DeriveRequestIDs(request("conversation-a"), body, defaultOpenCodeVersions)
	other := DeriveRequestIDs(request("conversation-b"), body, defaultOpenCodeVersions)

	if first.Session != second.Session || first.Project != second.Project || first.UserAgent != second.UserAgent {
		t.Fatalf("same session identity drifted: first=%#v second=%#v", first, second)
	}
	if first.Request == second.Request {
		t.Fatalf("new client requests reused x-opencode-request %q", first.Request)
	}
	if first.Session == other.Session || first.Project == other.Project {
		t.Fatalf("different sessions shared identity: first=%#v other=%#v", first, other)
	}
	if !canonicalSessionPattern.MatchString(first.Session) || !descendingTimePartPattern.MatchString(first.Session[4:16]) {
		t.Fatalf("session is not in canonical OpenCode format: %q", first.Session)
	}
	legacy := "ses_0123456789ab0123456789ABcd"
	migrated := CanonicalSessionID(legacy)
	if migrated == legacy || migrated != CanonicalSessionID(legacy) || !descendingTimePartPattern.MatchString(migrated[4:16]) {
		t.Fatalf("legacy hash-shaped session was not stably migrated: %q", migrated)
	}
	canonical := DeriveRequestIDs(request(first.Session), body, defaultOpenCodeVersions)
	if canonical.Session != first.Session {
		t.Fatalf("explicit OpenCode session was rehashed: %q != %q", canonical.Session, first.Session)
	}
	if !gitProjectIDPattern.MatchString(first.Project) || !gitProjectIDPattern.MatchString(other.Project) {
		t.Fatalf("derived project IDs are not lowercase SHA-1 values: %q %q", first.Project, other.Project)
	}
	if !strings.HasPrefix(first.UserAgent, "opencode/") || strings.ContainsAny(first.UserAgent, " ()\t\r\n") {
		t.Fatalf("User-Agent does not match the OpenCode CLI shape: %q", first.UserAgent)
	}
}

func TestProjectIDPreservationAndNormalization(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name     string
		header   string
		metadata string
		want     string
	}{
		{name: "global", header: "global", want: "global"},
		{name: "git SHA-1", header: valid, want: valid},
		{name: "metadata global", metadata: "global", want: "global"},
		{name: "metadata git SHA-1", metadata: valid, want: valid},
		{name: "header normalization", header: "worktree:/project", want: sha1Hex("worktree:/project")},
		{name: "metadata normalization", metadata: "metadata-project", want: sha1Hex("metadata-project")},
		{name: "header precedence", header: "global", metadata: "ignored", want: "global"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Header.Set("x-session-id", "project-test-session")
			if test.header != "" {
				r.Header.Set("x-opencode-project", test.header)
			}
			body := map[string]any{"metadata": map[string]any{"project_id": test.metadata}}
			got := DeriveRequestIDs(r, body, defaultOpenCodeVersions).Project
			if got != test.want {
				t.Fatalf("project = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRendezvousVersionStabilityAndLimitedRemapping(t *testing.T) {
	reordered := append([]string(nil), defaultOpenCodeVersions...)
	for left, right := 0, len(reordered)-1; left < right; left, right = left+1, right-1 {
		reordered[left], reordered[right] = reordered[right], reordered[left]
	}
	expanded := append(append([]string(nil), defaultOpenCodeVersions...), "1.18.19")
	changed := 0
	buckets := map[string]bool{}
	for i := 0; i < 1000; i++ {
		session := StableID("ses", "rendezvous-session-"+strconv.Itoa(i))
		original := rendezvousVersion(session, defaultOpenCodeVersions)
		if got := rendezvousVersion(session, reordered); got != original {
			t.Fatalf("version selection depends on list order for %q: %q != %q", session, got, original)
		}
		buckets[original] = true
		if rendezvousVersion(session, expanded) != original {
			changed++
		}
	}
	if len(buckets) < 2 {
		t.Fatalf("sessions did not distribute across the version pool: %#v", buckets)
	}
	if changed == 0 || changed >= 200 {
		t.Fatalf("adding one of thirteen versions remapped %d/1000 sessions; want only a minority", changed)
	}
}

func TestDefaultOpenCodeVersions(t *testing.T) {
	want := []string{
		"1.18.18", "1.18.17", "1.18.16", "1.18.15", "1.18.14", "1.18.13",
		"1.18.12", "1.18.11", "1.18.10", "1.18.9", "1.18.8", "1.18.7",
	}
	if !slices.Equal(defaultOpenCodeVersions, want) {
		t.Fatalf("default fingerprint versions = %#v, want %#v", defaultOpenCodeVersions, want)
	}
	if got := defaultOpenCodeUserAgent(); got != "opencode/latest/1.18.18/cli" {
		t.Fatalf("default User-Agent = %q", got)
	}
}
