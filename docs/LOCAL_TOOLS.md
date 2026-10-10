# Verified local tools

Verified during the 2026-10-09 quality review on this Linux/ARM64 host.

- Go: `/usr/local/go/bin/go`, version 1.26.0. Includes `go test`, `go vet`, `go tool cover`, and `go tool pprof` (all exercised).
- Lint: `$HOME/go/bin/golangci-lint`, version 2.14.0; `golangci-lint run ./...` passes.
- Workflows: `$HOME/go/bin/actionlint`; `actionlint` passes.
- Formatting: `gofmt -l .`; no differences found.
- Process sampling: `/usr/bin/pidstat` is available; use `pidstat -u -r -t -p PID 1 15` while the affected process is running. `ps -C wstat -o pid,pcpu,pmem,rss,etime` checks for a running instance. This review found none.
- `/usr/bin/strace` and `/usr/bin/hyperfine` are available; they were not needed for the measured snapshot experiment.
- Prefer `rg` for source searches.
- Terminal capabilities: `/usr/bin/tput`, verified 2026-10-10;
  `tput -T xterm-256color colors` reports 256. The compiled dashboard's PTY
  output was also checked for indexed 256-colour sequences with `COLORTERM`
  unset, using the workload sampler below.
- Python 3 (`/usr/bin/python3`), standard-library `pty`, `/proc` and `termios` are
  exercised by [the reusable workload sampler](../scripts/measure_workload.py).
  It needs Linux and a compiled wstat binary; see its `--help` for durations/profiles.
- GitHub CLI: `/usr/bin/gh`, version 2.23.0; release listing, CI inspection,
  release publication and artifact downloads are used for the release workflow.
  This version lacks `gh run list --commit`; use `gh api` with the workflow-runs
  `head_sha` query for exact-commit CI checks. Network access is required.
- Release artifacts: `python3 scripts/verify_release.py ASSET_DIR VERSION FULL_COMMIT_SHA`
  checks downloaded archive SHA-256 sums, ELF architecture/static linkage and
  Go revision metadata, then runs `--version` for the native Linux architecture.
  It stores `verification.json` and binary/build metadata in the asset directory
  for reuse in release validation.
- Terminal previews: set `WSTAT_UI_PREVIEW_DIR=/tmp/wstat-preview` when running
  `go test ./internal/ui -run TestReadabilityThemes -count=1` to capture actual
  rendered ANSI frames for all three themes. `/usr/bin/rsvg-convert` was used
  to rasterize an SVG representation of the captured frame for visual review.

The normal Go build cache is read-only in the Codex sandbox. A writable override fixes misleading package-loading failures:

```sh
GOCACHE=/tmp/wstat-review-gocache GOPROXY=off go test -count=1 ./...
GOCACHE=/tmp/wstat-review-gocache GOPROXY=off GOLANGCI_LINT_CACHE=/tmp/wstat-review-lint golangci-lint run ./...
```

`GOPROXY=off` worked because this revision's dependencies were already installed; it is not appropriate when dependencies need downloading. The FastCGI test creates a local Unix socket and needs execution outside the restricted sandbox. The race detector fails on this host both inside and outside the sandbox: `ThreadSanitizer: unsupported VMA range; Found 47 - Supported 48`. Run race checks on a compatible CI runner.

Review evidence and reusable diagnostic test sources live under `docs/reviews/2026-10-09/`. Run those diagnostic sources in an isolated copy: their passing assertions confirm existing defects, so they are not regression tests for corrected behavior.
