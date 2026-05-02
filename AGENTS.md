# Instructions for agents working on `gh-copilot-review`

## Build and test commands

- Build the extension from the repository root: `go build ./...`
- Run the repository test suite: `go test ./...`
- Run a single test by name: `go test ./... -run '^TestName$'`
- Install the extension into `gh` for end-to-end local checks: `go build && gh extension install .`

There is no repository-specific lint configuration or lint script checked in; rely on the existing Go toolchain commands above unless a linter config is added.

## High-level architecture

- This repository is a small Go-based GitHub CLI extension. The entire CLI currently lives in `main.go`, with `newRootCmd()` registering the `request` and `check` subcommands.
- `request` is the write path. It shells out through `go-gh` to `gh pr edit [<pr>] --add-reviewer @copilot` and can immediately hand off to the same polling flow used by `check` when `--wait` is set.
- `check` is the read/poll path. It resolves the target pull request, polls GitHub until Copilot is no longer pending by default, and supports a single-shot scripting mode via `--async`.
- Pull request selection is intentionally consistent across commands: when no `<pr>` argument is provided, `resolvePR()` uses `gh pr view --json number,url` to target the PR for the current branch.
- Review state combines two GitHub APIs:
  - local `gh` command execution to resolve the PR number and to request a reviewer
  - REST via `api.DefaultRESTClient()` for default polling, requested reviewers, review history, and rate-limit snapshots
  - GraphQL via `api.DefaultGraphQLClient()` as an alternative polling backend and adaptive fallback
- Release automation is tag-driven. `.github/workflows/release.yml` runs on `v*` tags and uses `cli/gh-extension-precompile@v2` to publish precompiled extension artifacts.

## Key conventions in this codebase

- Keep `request` and `check` behavior aligned. Both commands are expected to accept the same optional PR selector semantics, and `request --wait` should reuse the same polling path as `check` instead of maintaining separate status logic.
- `check` is synchronous by default. `--async` means "perform one poll and return a non-zero exit status while Copilot is still pending"; do not introduce a separate `--sync` mode unless the CLI contract changes.
- Polling flag validation is deliberately centralized in `shouldValidatePollingFlags()` and `validatePollingFlags()`. Explicitly provided `--interval` and `--timeout` values are still validated even when `--async` avoids repeated polling.
- Adaptive backend validation is similarly centralized: `request --wait` and `check` share `pollingConfig`, backend selection, weighted randomization, and fallback rules.
- Output and exit behavior matter:
  - pending async state returns `pendingReviewError`, which `main()` prints without the `error:` prefix before exiting with status 1
  - synchronous waiting logs progress lines to stderr
  - completed or no-longer-pending states print the final status line to stdout
- Copilot detection is based on case-insensitive login substring checks:
  - review requests match `copilot`
  - completed reviews match `copilot-pull-request-reviewer`
  Preserve that behavior unless GitHub changes the identities exposed by the API.
- When multiple Copilot reviews are returned, `fetchReviewStatus()` sorts them by `SubmittedAt` and uses the newest one as the status to report.
