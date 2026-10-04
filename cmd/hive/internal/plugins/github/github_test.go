package github

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/colonyops/hive/cmd/hive/internal/config"
	"github.com/colonyops/hive/cmd/hive/internal/plugins"
	"github.com/colonyops/hive/internal/domain/session"
	"github.com/colonyops/hive/internal/hive/pullrequest"
)

type stubForge struct {
	got pullrequest.Key
	pr  pullrequest.PullRequest
	err error
}

func (f *stubForge) Serves(host string) bool { return host == "github.com" }

func (f *stubForge) PullRequest(_ context.Context, key pullrequest.Key) (pullrequest.PullRequest, error) {
	f.got = key
	return f.pr, f.err
}

func newTestPlugin(forge *stubForge) *Plugin {
	return New(zerolog.Nop(), config.GitHubPluginConfig{}, nil, Deps{
		PullRequests: pullrequest.NewService(forge),
		Branch:       func(context.Context, string) (string, error) { return "feat/bar", nil },
	})
}

func refresh(t *testing.T, p *Plugin) map[string]plugins.Status {
	t.Helper()
	sessions := []*session.Session{{ID: "s1", Path: "/tmp/s1", Remote: "git@github.com:acme/site.git"}}
	got, err := p.RefreshStatus(t.Context(), sessions, plugins.NewWorkerPool(1))
	require.NoError(t, err)
	return got
}

func TestRefreshStatusLooksUpTheSessionBranchOnItsRemote(t *testing.T) {
	forge := &stubForge{pr: pullrequest.PullRequest{Status: pullrequest.StatusFound, Number: 7, State: "OPEN", IsDraft: true}}

	got := refresh(t, newTestPlugin(forge))

	assert.Equal(t, pullrequest.Key{Host: "github.com", Owner: "acme", Repo: "site", Branch: "feat/bar"}, forge.got)
	assert.Equal(t, map[string]plugins.Status{"s1": {Label: "draft", Icon: "PR"}}, got)
}

func TestRefreshStatusShowsNothingWithoutAPullRequest(t *testing.T) {
	for _, forge := range []*stubForge{
		{pr: pullrequest.PullRequest{Status: pullrequest.StatusNone}},
		{pr: pullrequest.PullRequest{Status: pullrequest.StatusDisconnected}},
		{err: errors.New("bad credentials")},
	} {
		assert.Empty(t, refresh(t, newTestPlugin(forge)))
	}
}
