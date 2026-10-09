# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.1] - 2026-10-09

Performance and monitoring-correctness fixes for all twelve findings in the
project quality review. See [implementation evidence](docs/implementation/README.md)
for measurements, test coverage and remaining validation limits.

### Fixed

- Select only displayed rows and cache unchanged snapshots; remove the forced
  48 MiB Go memory limit and retire inactive rate counters.
- Admit new URLs/clients at capacity with bounded LRU retention; cap hosts and
  client/host associations, expose detail evictions and preserve selection on refresh.
- Correct joint status/bot/static filters for counts, bytes, errors, rates and latency.
- Retain descriptors across seed/follow and rename/recreate, preserve incomplete
  lines, follow extensionless/empty paths and report bounded replay/read failures.
- Count slowlogs incrementally with rotation handling; run cancellable bounded FPM
  probes outside publication locks so stalled services do not freeze the dashboard.
- Keep PHP service requests/memory separate from web totals; interpret configured
  access units explicitly, fix `/proc` RSS units and malformed-field handling.
- Reject invalid configuration, preserve explicit zero/false values, validate edits,
  honor cache/FPM controls and vhost pins, and invalidate included-config dependencies.

### Added

- Opt-in CPU/heap profiles, deterministic snapshot benchmarks and an isolated PTY
  workload sampler, with regression and bounded-retention checks.
- Implementation evidence and accurate filter, rotation, memory and local-write docs.
- Remove the unused third-party tailing dependency and its transitive dependencies.

## [0.1.0] - 2026-10-09

First stable release. Since 0.1.0-rc1: config lifecycle, detection cache,
`wstat init` wizard, and php-fpm integration.

### Added

- **php-fpm integration (P4)**: pool discovery (`/etc/php/*/fpm/pool.d`,
  `/etc/php-fpm.d`, `WSTAT_FPM_POOL_GLOB` override), a minimal internal
  FastCGI client querying `pm.status_path` (`?json&full`) over unix/tcp
  sockets, tier-2 process stats from /proc (masters/workers per pool,
  RSS, CPU), slowlog entry counting, and access-log tailing when the
  pool's `access.format` records request duration (`%d`, rendered as
  fractional seconds by PHP >= 8) — fpm access lines parse as requests
  with per-URL mean latency shown in the TOP URLS panel.
- **Services view** (`v` cycles Dashboard / Services): php-fpm pools
  (active/total, listen queue, slow requests, memory, live
  ●/disabled ○/unreachable ✗ states) and source health (live/replay per
  path). Header chip warns on listen-queue backlog or exhausted
  max_children.
- **doctor: php-fpm section** — pools, socket access with remediation
  (`usermod -aG <group>` / setfacl), status-page reachability, and the
  `pm.status_path` enable snippet when off.
- **Config lifecycle (P2)**: TOML config at `./wstat.toml` (project) and
  `$XDG_CONFIG_HOME/wstat/config.toml` (user), merged with project winning;
  schema `[source]` paths/seed_lines/`[source.vhost]` pins (path or glob →
  vhost, overriding detection and filenames), `[detect]` enabled/cache.
  Precedence: CLI globs > config paths > cached detection > fresh
  detection > default globs.
- **Detection cache**: report persisted to
  `$XDG_DATA_HOME/wstat/detect.json`, validated by host key (hostname,
  distro, Apache version + config path/mtime/size, nginx config mtime);
  stale keys re-probe automatically. `--redetect` and
  `wstat config redetect` force a refresh; cache state shown in
  `wstat doctor` and `wstat config show`.
- **`wstat init` wizard**: interactive checkbox UI over detected live
  sources (space toggle, a all/none, enter saves); writes the user config
  with `path*` globs (covering rotated/gz history) and exact vhost pins.
  Non-TTY environments fall back to selecting everything.
- **`wstat config show|redetect|edit`**: effective merged config with
  per-field origins, cache refresh, and `$EDITOR` editing with a
  template created on first use.
- Detection engine (P1): `wstat doctor` (human report) and
  `wstat detect --json` (machine report) plus zero-config runtime wiring.
  Probes platform, Apache (`apache2ctl -V`/`-M`), config trees
  (Include/IncludeOptional globs, Define + envvars `$VAR` expansion,
  VirtualHost mapping), nginx configs, per-file format fingerprinting
  (gzip-aware), permissions with distro-aware remedies, and logrotate.
  Log paths map to their exact `ServerName` from the config scan
  (`pcash.local-access.log` → `pcash.home`); empty/missing declared
  logs are waited on; rotated/gz history still replays.
- **Root package layout**: `main.go` moved to the repository root so
  `go install github.com/steamvogue/wstat@latest` resolves the module's
  root package directly.
- MVP dashboard: HOSTS / TOP URLS / CLIENTS panels, live request stream,
  header with req/s, bytes/s and status split; zoom, focus cycling.
- Zero-config discovery of Apache (Debian/RHEL) and nginx (incl. Laravel
  Forge) access logs; per-file combined-family format detection; vhost
  attribution from `vhost_combined` prefix or filename (`-ssl` variants
  merge into the domain).
- Rotation-safe tailing (rename/recreate + truncate) with exactly-once
  seed/tail handoff; rotated (`*.log.N`) and gzip (`*.log.N.gz`) history
  replay at startup, bounded at 64 MB decompression per file.
- Filters with cross-panel drill-down semantics: fuzzy search (`/`),
  host (`h`), client IP (`c`), path (`p`), status classes (`x`), method
  (`m`), bots (`b`), static assets (`t`), clear all (`X`).
- Sort cycling per panel (`s`), stream freeze (`f`), three themes (`T`),
  per-vhost color hashing, truecolor→256 downsampling, `NO_COLOR` support.
- `//path` normalization (bot probes aggregate), static-asset and bot
  detection, per-agg counters for exact filtered hit counts.
- Hard memory caps (20k URL/client rows) with overflow counters, UA
  truncation, and a 48 MB soft memory limit (~50 MB RSS measured on a
  Raspberry Pi 5 with 77 vhosts).
- Test suite: parser tables + fuzz-style edges + 590k lines/s real-corpus
  benchmark, rotation/replay/gz integration tests, store filter semantics,
  UI model keymap tests; pty end-to-end smoke harnesses.

### Fixed
- Tailers no longer start replay sources for files that rotate mid-run
  (the old content was already delivered live; rescan now adds only live
  files) — prevents double-counting across daily logrotate runs.
- Tailer switched from inotify to 250 ms polling: appends landing between
  file open and inotify watch registration were silently lost.
- Seeded history no longer feeds rate counters (no fake startup spike).
- `access.log` maps to vhost `default`; rotation suffixes stripped before
  vhost name extraction.
- vhost_combined lines with IP-literal vhosts (`127.0.0.1:80 ...`) now
  parse; IPv6 client addresses are still never mistaken for vhost prefixes.

## [0.1.0-rc1] - 2026-10-09

First release candidate: MVP dashboard, full filter layer, gz/rotated
history replay, detection engine (`doctor` / `detect --json`), root
package layout for `go install github.com/steamvogue/wstat@latest`.
