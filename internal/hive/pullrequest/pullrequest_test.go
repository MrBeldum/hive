package pullrequest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/colonyops/hive/internal/platform/credentials"
	"github.com/colonyops/hive/internal/platform/forge/ghclient"
	"github.com/colonyops/hive/internal/store"
	"github.com/colonyops/hive/internal/store/db"
)

func connectedStore(t *testing.T) credentials.Store {
	t.Helper()
	store := credentials.NewMemoryStore()
	require.NoError(t, store.Set(credentials.Ref{Provider: "github", Account: "octocat"}, "tok"))
	return store
}

func graphQLServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func newTestService(t *testing.T, forges ...Forge) *Service {
	t.Helper()
	database, err := db.Open(t.TempDir(), db.OpenOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	return NewService(zerolog.Nop(), store.NewKVStore(database), forges...)
}

func gitHubForge(serverURL string, tokens Tokens) *GitHubForge {
	return NewGitHubForge(ghclient.NewClient(ghclient.WithAPIBase(serverURL)), tokens)
}

const openPRBody = `{"data":{"r0":{"pullRequests":{"nodes":[{"number":311,"state":"OPEN","isDraft":true,
  "url":"https://github.com/acme/site/pull/311","reviewDecision":"REVIEW_REQUIRED",
  "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]}}]}}}}`

var branchKey = Key{Host: "github.com", Owner: "acme", Repo: "site", Branch: "feat/bar"}

func TestLookupAnswersFromCacheUntilRefreshed(t *testing.T) {
	server, calls := graphQLServer(t, openPRBody)
	lookup := newTestService(t, gitHubForge(server.URL, StoredGitHubTokens(connectedStore(t))))

	first, err := lookup.Lookup(t.Context(), branchKey, false)
	require.NoError(t, err)
	assert.Equal(t, PullRequest{
		Status: StatusFound, Number: 311, State: "OPEN", IsDraft: true,
		URL: "https://github.com/acme/site/pull/311", ReviewDecision: "REVIEW_REQUIRED", Checks: "pending",
	}, first)

	second, err := lookup.Lookup(t.Context(), branchKey, false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), calls.Load(), "a fresh entry must not cost a second round trip")
	assert.True(t, second.Cached)

	_, err = lookup.Lookup(t.Context(), branchKey, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load())
}

func TestLookupKeepsItsEmptyAnswersDistinct(t *testing.T) {
	server, _ := graphQLServer(t, `{"data":{"r0":{"pullRequests":{"nodes":[]}}}}`)
	connected := gitHubForge(server.URL, StoredGitHubTokens(connectedStore(t)))

	none, err := newTestService(t, connected).Lookup(t.Context(), branchKey, false)
	require.NoError(t, err)
	assert.Equal(t, StatusNone, none.Status)

	disconnected, err := newTestService(t, gitHubForge(server.URL, StoredGitHubTokens(credentials.NewMemoryStore()))).
		Lookup(t.Context(), branchKey, false)
	require.NoError(t, err)
	assert.Equal(t, StatusDisconnected, disconnected.Status)

	unsupported, err := newTestService(t, connected).Lookup(t.Context(), Key{Branch: "feat/bar"}, false)
	require.NoError(t, err)
	assert.Equal(t, StatusUnsupported, unsupported.Status)

	// A remote is not evidence that its host is a forge hive can ask.
	unknownHost, err := newTestService(t, connected).Lookup(t.Context(),
		Key{Host: "git.example.test", Owner: "acme", Repo: "site", Branch: "feat/bar"}, false)
	require.NoError(t, err)
	assert.Equal(t, StatusUnsupported, unknownHost.Status)
}

// A failed lookup is an error, never a cached "none", so the next poll
// retries.
func TestLookupReportsAFailedLookupAndCachesNothing(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	lookup := newTestService(t, gitHubForge(server.URL, StoredGitHubTokens(connectedStore(t))))

	_, err := lookup.Lookup(t.Context(), branchKey, false)
	require.Error(t, err)
	_, err = lookup.Lookup(t.Context(), branchKey, false)
	require.Error(t, err)
	assert.Equal(t, int64(2), calls.Load())
}

type failingStore struct{ credentials.Store }

func (failingStore) List() ([]credentials.Ref, error) { return nil, errors.New("no keychain") }

func TestGitHubTokensWithCLIFallback(t *testing.T) {
	t.Setenv(credentials.EnvOverrideName(ghclient.Provider), "")
	ghToken := func(context.Context) (string, error) { return "gh_tok", nil }
	noGH := func(context.Context) (string, error) { return "", errors.New("gh: not logged in") }

	tests := []struct {
		name  string
		store credentials.Store
		gh    func(context.Context) (string, error)
		want  []string
	}{
		{name: "stored account wins over gh", store: connectedStore(t), gh: ghToken, want: []string{"tok"}},
		{name: "empty store falls back to gh", store: credentials.NewMemoryStore(), gh: ghToken, want: []string{"gh_tok"}},
		{name: "unreadable store falls back to gh", store: failingStore{}, gh: ghToken, want: []string{"gh_tok"}},
		{name: "nothing anywhere is disconnected", store: credentials.NewMemoryStore(), gh: noGH, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GitHubTokensWithCLIFallback(zerolog.Nop(), tt.store, tt.gh)(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGitHubTokensWithCLIFallbackPrefersTheEnvOverride(t *testing.T) {
	t.Setenv(credentials.EnvOverrideName(ghclient.Provider), "env_tok")
	gh := func(context.Context) (string, error) { return "gh_tok", nil }

	got, err := GitHubTokensWithCLIFallback(zerolog.Nop(), credentials.NewMemoryStore(), gh)(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"env_tok"}, got)
}
