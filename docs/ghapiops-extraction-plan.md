# `internal/ghapiops` extraction plan

## Goal

Extract the adaptive REST/GraphQL selection core from `gh-copilot-review` into a small internal package that can be validated here before any shared-module proposal for `gh-helper`.

## Why start with an internal package

- The current adaptive backend logic has only one production consumer.
- `gh-copilot-review` and `gh-helper` do not yet share the same GitHub client stack.
- The first priority is to prove the package boundary, not to publish a reusable API too early.

## Scope of this phase

This phase keeps GitHub-specific query logic in `main.go` and moves only the reusable mechanics into `internal/ghapiops`:

1. backend selection config and validation
2. rate-limit snapshot types and cached fetcher
3. fallback runner
4. usage accounting types

## Explicit non-goals

This phase does **not** try to:

1. publish a shared module
2. unify GitHub client implementations across repositories
3. move Copilot-specific request semantics into the package
4. move polling UX or CLI flag wiring into the package

## API boundary

The internal package should stay independent from:

- Cobra
- `go-gh` client types
- repository-specific GraphQL schema models

Instead, the caller supplies:

- backend configuration
- rate-limit fetchers
- backend-specific fetch functions
- fallback-eligibility policy

## Planned evolution

### Phase 1

Use `internal/ghapiops` inside `gh-copilot-review` and keep behavior unchanged.

### Phase 2

Try one small `gh-helper` call site against the same boundary, preferably a simple read or Gemini-trigger path rather than a thread mutation flow.

### Phase 3

If the boundary holds across both tools, promote the package into a shared module. At that point, decide between:

1. a dedicated top-level repository, or
2. a nested shared module inside `gh-dev-tools`

## Acceptance criteria for this PR

- `gh-copilot-review` behavior remains unchanged
- tests pass
- the new internal package is small and focused
- no public shared-module commitment is made prematurely
