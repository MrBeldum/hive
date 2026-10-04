// Package github provides a GitHub plugin for Hive.
package github

import (
	"context"
	"os/exec"
	"sync"
	"time"

	"github.com/colonyops/hive/pkg/logutils"
	"github.com/rs/zerolog"

	"github.com/colonyops/hive/cmd/hive/internal/config"
	"github.com/colonyops/hive/cmd/hive/internal/plugins"
	"github.com/colonyops/hive/cmd/hive/internal/plugins/pluglib"
	"github.com/colonyops/hive/internal/domain/kv"
	"github.com/colonyops/hive/internal/domain/session"
	"github.com/colonyops/hive/internal/hive/gitstatus"
	"github.com/colonyops/hive/internal/hive/pullrequest"
	"github.com/colonyops/hive/internal/platform/credentials"
	"github.com/colonyops/hive/internal/platform/forge/ghclient"
)

// Deps are the engine pieces the status provider reads through.
type Deps struct {
	PullRequests *pullrequest.Service
	Branch       func(ctx context.Context, dir string) (string, error)
	// Credentials is read only to report availability.
	Credentials credentials.Store
}

// Plugin implements the GitHub plugin for Hive.
type Plugin struct {
	logger zerolog.Logger
	cfg    config.GitHubPluginConfig
	deps   Deps
	cache  *kv.Cache[prInfo]
}

// New creates a new GitHub plugin.
// If kvStore is non-nil, PR status is cached in the persistent KV store.
func New(logger zerolog.Logger, cfg config.GitHubPluginConfig, kvStore kv.KV, deps Deps) *Plugin {
	p := &Plugin{logger: logutils.Component(logger, "plugins.github"), cfg: cfg, deps: deps}
	if kvStore != nil {
		p.cache = kv.NewCache[prInfo](p.logger, kvStore, "github.pr", p.StatusCacheDuration())
	}
	return p
}

func (p *Plugin) Name() string { return "github" }

func (p *Plugin) Available() bool {
	if p.cfg.Enabled != nil && !*p.cfg.Enabled {
		return false
	}
	if credentials.HasEnvOverride(ghclient.Provider) {
		return true
	}
	if p.deps.Credentials != nil {
		if refs, err := credentials.ListProvider(p.deps.Credentials, ghclient.Provider); err == nil && len(refs) > 0 {
			return true
		}
	}
	_, err := exec.LookPath("gh")
	return err == nil
}

func (p *Plugin) Init(_ context.Context) error { return nil }
func (p *Plugin) Close() error                 { return nil }

func (p *Plugin) Commands() map[string]config.UserCommand {
	return map[string]config.UserCommand{
		"GithubOpenRepo": {Sh: "cd {{ .Path }} && gh browse", Help: "open repo in browser", Scope: []string{"sessions"}},
		"GithubOpenPR":   {Sh: "cd {{ .Path }} && gh pr view --web", Help: "view current PR in browser", Scope: []string{"sessions"}},
		"GithubPRStatus": pluglib.TmuxPopup(`cd "{{ .Path }}" && gh pr status {{ join .Args " " }}`, "show PR status [flags]"),
		"GithubPRCreate": {Sh: "cd {{ .Path }} && gh pr create --web", Help: "create PR in browser", Scope: []string{"sessions"}},
	}
}

func (p *Plugin) StatusProvider() plugins.StatusProvider {
	return p
}

// prInfo is the cached part of a pull request. Its shape predates the engine
// service and matches what is already in the kv store.
type prInfo struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
}

func (p *Plugin) RefreshStatus(ctx context.Context, sessions []*session.Session, pool *plugins.WorkerPool) (map[string]plugins.Status, error) {
	results := make(map[string]plugins.Status)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, sess := range sessions {
		wg.Add(1)
		go func(s *session.Session) {
			defer wg.Done()
			pool.Run(func() {
				status := infoToStatus(p.fetchPRInfo(ctx, s))
				if status.Label != "" {
					mu.Lock()
					results[s.ID] = status
					mu.Unlock()
				}
			})
		}(sess)
	}

	wg.Wait()
	return results, nil
}

// fetchPRInfo returns PR info, checking the cache first. A branch with no pull
// request is cached; a failed lookup is not, so the next refresh retries.
func (p *Plugin) fetchPRInfo(ctx context.Context, s *session.Session) prInfo {
	if p.cache != nil {
		if cached, ok := p.cache.Get(ctx, s.ID); ok {
			return cached
		}
	}

	info, err := p.lookup(ctx, s)
	if err != nil {
		p.logger.Debug().Err(err).Str("session", s.ID).Msg("pull request lookup failed")
		return prInfo{}
	}

	if p.cache != nil {
		p.cache.Set(ctx, s.ID, info)
	}
	return info
}

func (p *Plugin) lookup(ctx context.Context, s *session.Session) (prInfo, error) {
	if p.deps.PullRequests == nil || p.deps.Branch == nil {
		return prInfo{}, nil
	}
	branch, err := p.deps.Branch(ctx, s.Path)
	if err != nil {
		return prInfo{}, err
	}
	host, owner, repo := gitstatus.RemoteCoordinates(s.Remote)
	// The plugin keeps its own cache with the configured duration, so the
	// service's cache is bypassed.
	pr, err := p.deps.PullRequests.Lookup(ctx, pullrequest.Key{Host: host, Owner: owner, Repo: repo, Branch: branch}, true)
	if err != nil {
		return prInfo{}, err
	}
	if pr.Status != pullrequest.StatusFound {
		return prInfo{}, nil
	}
	return prInfo{Number: pr.Number, State: pr.State, IsDraft: pr.IsDraft}, nil
}

func infoToStatus(info prInfo) plugins.Status {
	if info.Number == 0 {
		return plugins.Status{}
	}

	var label string

	if info.IsDraft {
		label = "draft"
	} else {
		switch info.State {
		case "OPEN":
			label = "open"
		case "MERGED":
			label = "merged"
		case "CLOSED":
			label = "closed"
		default:
			label = info.State
		}
	}

	return plugins.Status{
		Label: label,
		Icon:  "PR",
	}
}

func (p *Plugin) StatusCacheDuration() time.Duration {
	if p.cfg.ResultsCache > 0 {
		return p.cfg.ResultsCache
	}
	return 2 * time.Minute
}
