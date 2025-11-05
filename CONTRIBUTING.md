# Contributing Guide

Thanks for helping us evolve the FSM toolkit! Below are the conventions we follow so the history stays clean and every change maps back to an issue.

## Commit Message Standard

Use the pattern: `type(scope): short summary (#issue)`

- **type**: one of `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `ci`, `build`, `style`, or `chore`.
- **scope**: optional module or file hint in lowercase (e.g. `storage`, `graphviz`). Skip it if it doesn’t add clarity.
- **short summary**: present-tense sentence fragment describing the change.
- **(#issue)**: reference the related GitHub issue. Always include it when a commit addresses or closes an issue (example: `(#42)`).

Examples:

```text
feat(storage): add redis backend adapter (#128)
fix: restore timer cleanup logic (#131)
docs: update otel quickstart (#135)
```

Commits without an associated issue can omit the `(#issue)` suffix, but keep the rest of the structure for consistency.

## Pull Requests

- Squash commits that share the same intent before opening the PR.
- Include a short summary and link the target issue in the PR description.
- Ensure `make test` (or `go test .`) passes locally; the CI pipeline will verify formatting, vetting, and coverage.

## Code Style

- Run `gofmt` on edited Go files.
- Prefer small, focused changes that keep existing tests green; add tests for new behaviour whenever possible.

Obrigado! 🚀
