# Contributing

Thanks for considering a contribution!

## Development

```sh
go build ./...            # build everything
go test -count=1 ./...    # run the suite (portable; optional samples/ corpus self-skips)
golangci-lint run         # lint (CI enforces this)
gofmt -l .                # must print nothing
```

Commits: keep the subject line imperative and under ~72 characters. Every change
that affects behavior should come with or extend a test.

## What's most useful

- **Real-world log formats**: if wstat fails to parse your access logs (footer shows
  a `bad:` count), open an issue with a sanitized sample line and your `LogFormat`.
- **Distro layouts**: detection globs currently cover Debian/Ubuntu, RHEL-family,
  and nginx/Forge. PRs adding layouts (cPanel, FreeBSD, Bitnami, …) with test
  fixtures are very welcome — see `internal/logsrc`.
- **Bugs**: include your distro, web server, how you invoked wstat, and terminal
  size; a `script(1)` transcript of the misbehavior helps enormously.

## Scope guardrails

wstat is deliberately: read-only on the host, no network calls, no telemetry, no
long-term storage. Features requiring any of those will not be accepted. See
[PLAN.md](PLAN.md) for the roadmap and architecture.
