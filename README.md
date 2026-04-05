# gh-copilot-review

`gh-copilot-review` is a GitHub CLI extension for requesting GitHub Copilot review on a pull request and waiting for that review to finish.

It uses `cobra` for subcommands and flags, and `go-gh` for GitHub CLI integration and GraphQL access.

## Commands

- `gh copilot-review request [<pr>]`
- `gh copilot-review request [<pr>] --wait [--interval 15] [--timeout 0]`
- `gh copilot-review wait [<pr>] [--interval 15] [--timeout 0] [--once]`

Without `<pr>`, the extension uses the pull request for the current branch.

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
gh copilot-review wait 17 --once
```
