# gh-copilot-review

`gh-copilot-review` is a GitHub CLI extension for requesting GitHub Copilot review on a pull request and waiting for that review to finish.

It uses `cobra` for subcommands and flags, and `go-gh` for GitHub CLI integration and GraphQL access.

## Commands

- `gh copilot-review request [<pr>]`
- `gh copilot-review request [<pr>] --wait [--interval 15] [--timeout 0]`
- `gh copilot-review check [<pr>] [--interval 15] [--timeout 0] [--async]`

Without `<pr>`, the extension uses the pull request for the current branch.

Default `check` behavior is synchronous polling until Copilot is no longer pending. Use `--async` for a single poll when scripting; it exits non-zero while a review is still pending. `--interval` and `--timeout` control the polling behavior when waiting, and any explicitly provided values are still validated even with `--async`. A separate `--sync` flag is unnecessary because synchronous polling is the default.

## Local development

```sh
go mod tidy
go build
gh extension install .
```

## Examples

```sh
gh copilot-review request 17
gh copilot-review request 17 --wait --interval 10
gh copilot-review check 17
gh copilot-review check 17 --interval 10
gh copilot-review check 17 --async
```
