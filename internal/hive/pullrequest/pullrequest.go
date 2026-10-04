// Package pullrequest resolves a branch's pull request over the forge HTTP
// clients. Unlike `gh pr view`, a failed lookup is an error and never reads as
// "no pull request".
package pullrequest

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/colonyops/hive/internal/domain/kv"
	"github.com/colonyops/hive/pkg/logutils"
)

type Key struct {
	Host   string `json:"host"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

func (k Key) cacheKey() string { return k.Host + "/" + k.Owner + "/" + k.Repo + "/" + k.Branch }

// Status keeps "no pull request" apart from a disconnected account and an
// unsupported host, which state different facts.
type Status string

const (
	StatusNone         Status = "none"
	StatusFound        Status = "found"
	StatusDisconnected Status = "disconnected"
	StatusUnsupported  Status = "unsupported"
)

// PullRequest fields below Status are set only for StatusFound. The json tags
// are camelCase because the desktop returns this type to its frontend as is.
type PullRequest struct {
	Status  Status `json:"status"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	URL     string `json:"url"`
	// ReviewDecision is APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or empty.
	ReviewDecision string `json:"reviewDecision"`
	// Checks is passing, pending, failing, or empty when no checks run.
	Checks    string `json:"checks"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Cached is true when the answer came from the cache. The desktop animates
	// only a fresh one.
	Cached bool `json:"cached"`
}

const CacheTTL = 5 * time.Minute

type Forge interface {
	Serves(host string) bool
	PullRequest(ctx context.Context, key Key) (PullRequest, error)
}

// Service asks the first forge that serves a key's host. Answers are cached in
// hive.db for CacheTTL, so both programs share them; errors are not cached.
type Service struct {
	forges []Forge
	cache  *kv.Cache[PullRequest]
}

func NewService(logger zerolog.Logger, store kv.KV, forges ...Forge) *Service {
	logger = logutils.Component(logger, "pullrequest")
	return &Service{forges: forges, cache: kv.NewCache[PullRequest](logger, store, "pullrequest", CacheTTL)}
}

// Lookup answers from the cache unless refresh is set.
func (s *Service) Lookup(ctx context.Context, key Key, refresh bool) (PullRequest, error) {
	if key.Host == "" || key.Owner == "" || key.Repo == "" || key.Branch == "" {
		return PullRequest{Status: StatusUnsupported}, nil
	}

	if !refresh {
		if view, ok := s.cache.Get(ctx, key.cacheKey()); ok {
			view.Cached = true
			return view, nil
		}
	}

	view, err := s.fetch(ctx, key)
	if err != nil {
		return PullRequest{}, err
	}
	s.cache.Set(ctx, key.cacheKey(), view)
	return view, nil
}

func (s *Service) fetch(ctx context.Context, key Key) (PullRequest, error) {
	for _, f := range s.forges {
		if f.Serves(key.Host) {
			return f.PullRequest(ctx, key)
		}
	}
	return PullRequest{Status: StatusUnsupported}, nil
}
