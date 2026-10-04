package pullrequest

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/colonyops/hive/internal/platform/credentials"
	"github.com/colonyops/hive/internal/platform/forge/ghclient"
)

// Tokens lists every token that might see a repository. An empty list means
// no account is connected.
type Tokens func(ctx context.Context) ([]string, error)

// StoredGitHubTokens reads the GitHub accounts in the credential store, and
// the HIVE_GITHUB_TOKEN override.
func StoredGitHubTokens(store credentials.Store) Tokens {
	return func(context.Context) ([]string, error) {
		return credentials.ProviderValues(store, ghclient.Provider)
	}
}

// GitHubTokensWithCLIFallback reads the credential store, and asks the gh CLI
// only when the store has nothing. The CLI runs on machines without a
// keychain, so a store that fails to read is logged and skipped rather than
// hiding the gh token behind it.
func GitHubTokensWithCLIFallback(logger zerolog.Logger, store credentials.Store, gh func(ctx context.Context) (string, error)) Tokens {
	return func(ctx context.Context) ([]string, error) {
		tokens, err := credentials.ProviderValues(store, ghclient.Provider)
		if err != nil {
			logger.Debug().Err(err).Msg("reading stored GitHub credentials")
		}
		if len(tokens) > 0 {
			return tokens, nil
		}
		token, err := gh(ctx)
		if err != nil {
			logger.Debug().Err(err).Msg("reading the gh CLI token")
			return nil, nil
		}
		return []string{token}, nil
	}
}

// GitHubForge looks a branch's pull request up on github.com. GitHub
// Enterprise is not it: an instance is reached at its own host, and nothing
// configures one.
type GitHubForge struct {
	client *ghclient.Client
	tokens Tokens
}

func NewGitHubForge(client *ghclient.Client, tokens Tokens) *GitHubForge {
	return &GitHubForge{client: client, tokens: tokens}
}

func (g *GitHubForge) Serves(host string) bool { return host == "github.com" }

func (g *GitHubForge) PullRequest(ctx context.Context, key Key) (PullRequest, error) {
	if g.client == nil || g.tokens == nil {
		return PullRequest{Status: StatusDisconnected}, nil
	}

	tokens, err := g.tokens(ctx)
	if err != nil {
		return PullRequest{}, fmt.Errorf("reading GitHub credentials: %w", err)
	}
	if len(tokens) == 0 {
		return PullRequest{Status: StatusDisconnected}, nil
	}

	ref := ghclient.BranchRef{Owner: key.Owner, Repo: key.Repo, Branch: key.Branch}
	// A repository an account cannot see resolves to a null alias, not an
	// error, so the only way to know another account can see it is to ask.
	var lastErr error
	for _, token := range tokens {
		results, err := g.client.WithTokenCopy(token).PullRequestsByBranch(ctx, []ghclient.BranchRef{ref})
		if err != nil {
			lastErr = err
			continue
		}
		if len(results) == 0 || !results[0].Found {
			continue
		}
		pr := results[0]
		return PullRequest{
			Status:         StatusFound,
			Number:         pr.Number,
			Title:          pr.Title,
			State:          pr.State,
			IsDraft:        pr.IsDraft,
			URL:            pr.URL,
			ReviewDecision: pr.ReviewDecision,
			Checks:         string(pr.Checks),
			Additions:      pr.Additions,
			Deletions:      pr.Deletions,
		}, nil
	}
	if lastErr != nil {
		return PullRequest{}, fmt.Errorf("reading the pull request for %s: %w", key.Branch, lastErr)
	}
	return PullRequest{Status: StatusNone}, nil
}
