// Package pullrequest answers "what is this branch's pull request" for both
// programs, over the forge HTTP clients rather than a forge CLI. `gh pr view`
// exits non-zero both for a branch with no pull request and for a failed
// lookup, so it cannot keep "none" apart from "the lookup failed".
package pullrequest

import (
	"context"
	"sync"
	"time"
)

// Key addresses the pull request a branch has. Host decides which forge is
// asked, because every forge spells owner and repo the same.
type Key struct {
	Host   string `json:"host"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

// Status is why there is no pull request to show, or that there is one.
// Rendering "no pull request" for a failed lookup or a disconnected account
// states a different, wrong fact, so the four stay apart.
type Status string

const (
	StatusNone         Status = "none"
	StatusFound        Status = "found"
	StatusDisconnected Status = "disconnected"
	StatusUnsupported  Status = "unsupported"
)

// PullRequest is a branch's pull request. Everything below Status is
// meaningful only for StatusFound. The json tags are camelCase because the
// desktop returns this type to its frontend as is.
type PullRequest struct {
	Status  Status `json:"status"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	URL     string `json:"url"`
	// ReviewDecision is GitHub's own vocabulary (APPROVED, CHANGES_REQUESTED,
	// REVIEW_REQUIRED), or empty when review is not required.
	ReviewDecision string `json:"reviewDecision"`
	// Checks is passing, pending, failing, or empty for a head commit with no
	// checks configured.
	Checks string `json:"checks"`
	// The pull request's own line counts, not the working tree's: those drift
	// as the branch moves on.
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	// Cached tells "this just arrived" from "this was already known". The
	// desktop animates only the former.
	Cached bool `json:"cached"`
}

// CacheTTL bounds how stale an answer may be. A lookup is a network round
// trip against a shared rate limit.
const CacheTTL = 5 * time.Minute

// Forge is one hosting service's answer for a branch. A new forge is an
// implementation of this and nothing else.
type Forge interface {
	// Serves reports whether this forge answers for a remote's host. A host no
	// forge serves makes a lookup unsupported.
	Serves(host string) bool
	PullRequest(ctx context.Context, key Key) (PullRequest, error)
}

// Service resolves branch pull requests through the first forge that serves
// the host, and caches each answer for CacheTTL. A failed lookup is returned
// as an error and is not cached.
type Service struct {
	forges []Forge

	mu     sync.Mutex
	cached map[Key]cachedPullRequest
	now    func() time.Time
}

type cachedPullRequest struct {
	view   PullRequest
	readAt time.Time
}

func NewService(forges ...Forge) *Service {
	return newService(time.Now, forges...)
}

func newService(now func() time.Time, forges ...Forge) *Service {
	return &Service{
		forges: forges,
		cached: map[Key]cachedPullRequest{},
		now:    now,
	}
}

// Lookup resolves one branch's pull request, answering from cache while the
// entry is fresh. refresh discards the cached entry first.
func (s *Service) Lookup(ctx context.Context, key Key, refresh bool) (PullRequest, error) {
	if key.Host == "" || key.Owner == "" || key.Repo == "" || key.Branch == "" {
		// A remote that named no repository, or a branch that did not resolve,
		// is not a failure to report as one.
		return PullRequest{Status: StatusUnsupported}, nil
	}

	if !refresh {
		if view, ok := s.fresh(key); ok {
			return view, nil
		}
	}

	view, err := s.fetch(ctx, key)
	if err != nil {
		return PullRequest{}, err
	}
	s.mu.Lock()
	s.cached[key] = cachedPullRequest{view: view, readAt: s.now()}
	s.mu.Unlock()
	return view, nil
}

func (s *Service) fresh(key Key) (PullRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cached[key]
	if !ok || s.now().Sub(entry.readAt) > CacheTTL {
		return PullRequest{}, false
	}
	// Stamped on the way out: the same view is fresh the first time it is
	// returned and cached after.
	view := entry.view
	view.Cached = true
	return view, true
}

func (s *Service) fetch(ctx context.Context, key Key) (PullRequest, error) {
	for _, f := range s.forges {
		if f.Serves(key.Host) {
			return f.PullRequest(ctx, key)
		}
	}
	return PullRequest{Status: StatusUnsupported}, nil
}
