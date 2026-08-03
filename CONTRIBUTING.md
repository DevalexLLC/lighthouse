# Contributing to Lighthouse

## Commits

Follow [Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<scope>): <subject>` with types `feat`, `fix`, `docs`, `style`,
`refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.

- Subject: imperative mood, lowercase, no trailing period, ≤72 chars.
- Body: wrap at 72 chars, explain *why*, blank line after the subject.
- Reference issues in a trailer (`Refs: #123`), not the subject.

## Ground rules

- **Builds must work offline.** Never add a build step that reaches the
  network. New Go dependencies are vendored (`make vendor`) in the same
  change. Generated protobuf code (`internal/pb/`) and the built SPA
  (`web/dist/`) are committed; regenerate with `make proto` / `make web`
  and include the diff.
- **Fail loud.** Unknown config keys are fatal. Dependencies are checked at
  startup preflight and failures name the problem. No silent fallbacks,
  no repurposed environment variables.
- **Every change is verifiable.** State how you verified it in the PR:
  the command, the broken output (if fixing), the fixed output.

## Development

```
make build test lint    # offline
make up                 # dev stack (compose base + dev overlay together)
make web                # rebuild the SPA: lints, format-checks, then builds
make web-fix            # apply oxlint autofixes and reformat
```

`make up` always composes the base stack *and* the dev overlay. Do not
`docker compose up` the base file alone in a dev environment — it silently
removes the overlay services (fake agents, their tokens, monitoring).

SPA style is enforced, not conventional: `make web` fails on any oxlint
finding or unformatted file before it rebuilds `web/dist/`, and CI's
`web-lint` job repeats both checks. That job installs from the npm registry
— it gates dev tooling, so it sits outside the offline guarantee that
`offline-build` enforces for everything shipped.
