package gitea

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/colonyops/hive/internal/hive/pullrequest"
)

// The status bar renders the view, not the forge, so a Gitea session's badge
// has to arrive in exactly the shape GitHub's does.
func TestForgeAnswersAGiteaSessionInTheSameView(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v1/repos/acme/site":
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case "/api/v1/repos/acme/site/pulls/main/feat%2Fbar":
			_, _ = w.Write([]byte(`{"number":12,"title":"Add the thing","state":"open","draft":false,
				"html_url":"https://git.example.com/acme/site/pulls/12","additions":31,"deletions":4,
				"head":{"ref":"feat/bar","sha":"cafe"},"requested_reviewers":[{"login":"hubot"}]}`))
		case "/api/v1/repos/acme/site/commits/cafe/status":
			_, _ = w.Write([]byte(`{"state":"failure","total_count":3}`))
		case "/api/v1/repos/acme/site/pulls/12/reviews":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	defer server.Close()

	forge, host := connectedPullRequests(t, server.URL, "octocat")
	lookup := pullrequest.NewService(pullrequest.NewGitHubForge(nil, nil), forge)

	view, err := lookup.Lookup(t.Context(),
		pullrequest.Key{Host: host, Owner: "acme", Repo: "site", Branch: "feat/bar"}, false)
	require.NoError(t, err)
	assert.Equal(t, pullrequest.PullRequest{
		Status: pullrequest.StatusFound, Number: 12, Title: "Add the thing", State: "OPEN",
		URL: "https://git.example.com/acme/site/pulls/12", ReviewDecision: "REVIEW_REQUIRED",
		Checks: "failing", Additions: 31, Deletions: 4,
	}, view)
}

// A branch with no pull request on an instance that answered is "none"; the
// same branch on a host nobody has connected is unsupported, since nothing
// identifies that host as a forge the app can ask.
func TestForgeSeparatesABranchWithNoneFromAnUnservedHost(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v1/repos/acme/site":
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case "/api/v1/repos/acme/site/pulls":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	defer server.Close()

	forge, host := connectedPullRequests(t, server.URL, "octocat")
	lookup := pullrequest.NewService(pullrequest.NewGitHubForge(nil, nil), forge)

	none, err := lookup.Lookup(t.Context(),
		pullrequest.Key{Host: host, Owner: "acme", Repo: "site", Branch: "feat/bar"}, false)
	require.NoError(t, err)
	assert.Equal(t, pullrequest.StatusNone, none.Status)

	unsupported, err := lookup.Lookup(t.Context(),
		pullrequest.Key{Host: "git.other.test", Owner: "acme", Repo: "site", Branch: "feat/bar"}, false)
	require.NoError(t, err)
	assert.Equal(t, pullrequest.StatusUnsupported, unsupported.Status)
}
