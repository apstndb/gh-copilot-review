# gh-copilot-review

`gh-copilot-review` is a GitHub CLI extension for requesting GitHub Copilot review on a pull request and waiting for that review to finish.

It uses `cobra` for subcommands and flags, and `go-gh` for GitHub CLI integration plus GitHub REST and GraphQL API access.

## Commands

- `gh copilot-review request [<pr>]`
- `gh copilot-review request [<pr>] --wait [--interval 15] [--timeout 0] [--backend auto|random|rest|graphql] [--rest-weight 1] [--graphql-weight 1] [--auto-adjust-weights]`
- `gh copilot-review check [<pr>] [--interval 15] [--timeout 0] [--async] [--backend auto|random|rest|graphql] [--rest-weight 1] [--graphql-weight 1] [--auto-adjust-weights]`

Without `<pr>`, the extension uses the pull request for the current branch.

Default `check` behavior is synchronous polling until Copilot is no longer pending. Use `--async` for a single poll when scripting; it exits non-zero while a review is still pending. `--interval` and `--timeout` control the polling behavior when waiting, and any explicitly provided values are still validated even with `--async`. A separate `--sync` flag is unnecessary because synchronous polling is the default.

Polling uses `--backend auto` by default:

- `auto` prefers REST for equivalent status checks and falls back between REST and GraphQL based on the selected primary backend
- `random` spreads load across REST and GraphQL with configurable weights
- `rest` and `graphql` pin polling to a single backend

Use `--rest-weight` and `--graphql-weight` to bias adaptive polling, and `--auto-adjust-weights` to scale those weights from the current GitHub rate-limit snapshot.

## Local development

```sh
go mod tidy
go test ./...
go build
gh extension install .
```

## Examples

```sh
gh copilot-review request 17
gh copilot-review request 17 --wait --interval 10
gh copilot-review request 17 --wait --backend rest
gh copilot-review check 17
gh copilot-review check 17 --interval 10
gh copilot-review check 17 --async
gh copilot-review check 17 --backend random --rest-weight 80 --graphql-weight 20
```
