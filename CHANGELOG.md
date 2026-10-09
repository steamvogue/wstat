# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
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
- Tailer switched from inotify to 250 ms polling: appends landing between
  file open and inotify watch registration were silently lost.
- Seeded history no longer feeds rate counters (no fake startup spike).
- `access.log` maps to vhost `default`; rotation suffixes stripped before
  vhost name extraction.
