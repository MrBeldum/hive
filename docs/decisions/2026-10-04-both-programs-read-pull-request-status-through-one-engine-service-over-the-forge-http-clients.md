# Both programs read pull request status through one engine service over the forge HTTP clients

- **Status:** accepted
- **Date:** 2026-10-04
- **Amends:** [owned-github-client](2026-07-25-owned-github-client.md) (the client is no longer desktop-only)

## Context

The desktop resolved a session's pull request over its own GitHub and Gitea
HTTP clients. The CLI ran `gh pr view` in the checkout and cached whatever
came back. `gh` exits non-zero both for a branch with no pull request and for
a failed lookup, so the CLI cached a failure as "no pull request" for the
whole TTL. The desktop's path already kept those apart.

The CLI has never stored credentials. It relied on `gh`'s own login, and it
runs on machines with no OS keychain (Linux without a secret service, the
integration container).

## Decision

**`internal/hive/pullrequest` is the one pull request service.** `hive.Engine`
builds it from `Ports.Forges` and keeps it across `Reload`. It caches answers
in the `hive.db` kv store, which both programs open, so it is the only cache.
A failed lookup is not cached.
A forge is a port: GitHub's lives in the engine, over
`internal/platform/forge/ghclient`. Gitea's stays in the desktop's
`sources/gitea` until its instance bindings move, because only the desktop
knows which hosts are Gitea.

The HTTP clients move out of the desktop: `ghclient` and `giteaclient` to
`internal/platform/forge/`, and `sourcehttp`, which non-forge connectors also
use, to `internal/platform/sourcehttp`.

**Each program supplies its own GitHub tokens.** The desktop reads the
credential store and `HIVE_GITHUB_TOKEN`, as before. The CLI reads the same
two, using the desktop's ref index under the default data dir, and asks
`gh auth token` only when they yield nothing. A credential store that fails
to read is logged and skipped. That way a missing keychain does not hide the
`gh` token.

## Consequences

- A CLI user who has only `gh` keeps PR status with no setup. A desktop user
  shares their connected accounts with the CLI.
- On macOS, the first CLI read of an account the desktop stored can show a
  keychain access prompt for the `hive` binary.
- The CLI's `results_cache` sets only how often the TUI polls. The cache
  lifetime is the service's.
- The CLI shows PR status for GitHub only. Gitea follows when its connector
  moves to the engine (the sources convergence).
