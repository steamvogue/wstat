# wstat — realtime site traffic monitor (plan, rev 4 — MVP-first)

<a id="goal"></a>
## Goal

One Go binary: run `wstat` on any Apache/nginx host → instant colored TUI showing **active vhosts, top URLs, and who's pulling them (client IPs)** in realtime, with live filters. Everything else (wizard, php-fpm, detection cache) is post-MVP and must never block the fast path.

**MVP definition of done:** `go build && ./wstat` on this Pi shows `cms.local` etc. within 1 s of a request; `/`, `h`, `x`, `X` filters work; survives `logrotate -f` without lost/double-counted lines; idle CPU <3%, RSS <60 MB; works over ssh and in tmux; `NO_COLOR` respected.

---

<a id="mvp"></a>
## 0. MVP fast path (fastest to a working CLI)

### Lane 0 — wrap GoAccess, zero coding, today (~30 min)

GoAccess already is a realtime colored ncurses dashboard with panels (hosts/URLs/status), `/`-search, `s`-sort, and `--fname-as-vhost`. A wrapper script delivers a "working wstat" immediately:

```bash
#!/usr/bin/env bash
# wstat-lite: guess apache/nginx logs, launch goaccess
LOGS=$(ls /var/log/apache2/*-access.log /var/log/nginx/*-access.log 2>/dev/null)
[ -z "$LOGS" ] && { echo "no access logs found; try: wstat-lite <files...>"; exit 1; }
exec goaccess $LOGS \
  --log-format=COMBINED \
  --fname-as-vhost='(.+)-access\.log$' \
  --ignore-panel=REFERRERS --ignore-panel=KEYPHRASES
```

- Slow-start caveat: passing files parses their full history (fine on this Pi's tiny logs; bad on big Forge boxes). Instant-start variant: `tail -n 2000 -F $LOGS | goaccess --log-format=COMBINED -` (rotation-safe via `tail -F`, but loses `--fname-as-vhost` → no vhost names on stdin).
- Limits that justify building the real thing: no cross-panel drill-down filters, fixed panels, global log format (this Pi mixes combined + vhost_combined), C dependency, no php-fpm/status plane.
- Verdict: use Lane 0 today to validate the *value*; the glob-guessing logic carries over verbatim to Lane 1.

### Lane 1 — `wstat-min`, the real fast path (~1.5–2 focused days)

Descoped cut of the full plan; every line of it is reused later.

**IN (MVP scope):**
- Zero-config source guess: globs `/var/log/apache2/*access*.log`, `/var/log/httpd/*access*log*`, `/var/log/nginx/*access*.log` + CLI args override. **nginx included now** (Laravel Forge = Ubuntu + nginx + php-fpm; per-site `/var/log/nginx/<site>-access.log`, combined-equivalent format — verify exact format on first Forge host via the format fingerprint).
- Per-file format fingerprint: combined-family only (apache `combined`, `vhost_combined`, nginx `combined`/forge-like). Unknown files: skip + footer warning. Vhost attribution: `%v` from format, else filename regex.
- Tail + rotation-safe (nxadm/tail), seed last 1k lines/file so the screen isn't empty.
- Store: 60×1 s buckets, top-K LRU maps, EWMA req/s.
- UI (bubbletea v2): header (req/s, bytes/s, status split, filter chips) + **HOSTS / TOP URLS / CLIENTS** panels + **live request stream**; TAB/`1–5` focus, ENTER zoom, `j/k/g/G`; one good built-in theme, status colors + per-vhost color hash, truecolor→256 downsampling.
- Filters: `/` substring search (focused panel), `h` toggle host filter from selected row, `x` status class, `X` clear — with cross-filter semantics (a filter scopes every panel except its own).

**OUT (post-MVP, do not sneak in):** wizard, detection engine/cache, doctor, php-fpm, mod_status, Services view, backfill `--since`, fuzzy search, themes, mouse, config hot-reload, ssh, JSON out, gzip replay.

### Shortcut research (existing tools as base — verdicts)

| Tool | Verdict |
|---|---|
| **goaccess** (C, 21k★) | Best-of-class reference + Lane-0 wrapper target. Not a code base: C, no stable lib API, ncurses. Its panel design/keys are the UX baseline wstat copies (`/` search, `s` sort, `--fname-as-vhost`, `tail -F \| -` stdin pattern). |
| **ngxtop** (Python, 6.5k★) | Concept proof ("top for nginx", group-by host, filter expressions, follow mode). 49 commits, python2-primary, aging; plain table refresh, no panels/colors/TUI. Inspiration only — notably its group-by/filter CLI grammar is worth borrowing for headless mode. |
| **lnav** | Superb log *viewer*, wrong UX for a metrics dashboard. Not a base. |
| **Fork goaccess** | No — C codebase, ncurses, would cost more than building wstat-min. |
| **Go libs** | No existing Go "access-log dashboard" lib worth building on; bubbletea/lipgloss/nxadm-tail are the right assembly kit. |

Net: **wrap goaccess today (Lane 0), build wstat-min this week (Lane 1), full plan after.**

---

## 1. Verified environment

| Fact | Value |
|---|---|
| Host | Raspberry Pi 5, aarch64, 4 cores, Debian; go 1.26, rust 1.94 |
| Web server | Apache 2.4.68; logs `/var/log/apache2/*-access.log` (8 vhosts) + `other_vhosts_access.log` (vhost_combined; default-vhost traffic — parse, don't exclude) |
| Format | `combined` per-vhost files; **formats differ per file** → per-file fingerprint |
| Rotation | logrotate weekly, `.1` → `.N.gz`; live files rotate in place |
| Perms | user in `adm` → logs readable, no sudo |
| Traffic | <1 req/s sustained — perf is a non-issue at MVP scale |
| Other targets | Laravel Forge hosts: Ubuntu + nginx (`/var/log/nginx/<site>-access.log`, combined-like) + php-fpm → nginx formats are MVP scope |

## 2. Language & libraries (Go)

Go over Rust: parse headroom is irrelevant at this volume; bubbletea v2 + lipgloss v2 (cell renderer, color downsampling), bubbles v2, `nxadm/tail` (rotation-safe), `pelletier/go-toml/v2`, minimal internal FastCGI client (post-MVP). Dependency risk: `huh`/`teatest` may lag bubbletea v2 → M0-post spike decides; fallbacks cheap (hand-rolled lists, `View()` snapshots).

## 3. Post-MVP: host detection, wizard & config lifecycle

Principle: **detect → score → suggest → confirm → cache; never modify the host, never require sudo, always degrade.**

- **Providers** (each: `{kind, path, format, confidence, evidence, remediation}`, all timeout-bounded): platform probe; Apache probe (`-V`/`-M`/`-S` best-effort); config scanner; per-file log fingerprint; rotation probe; php-fpm probe; mod_status probe; permission advisor.
- **Config scanner** (`internal/apacheconf`): conservative tokenizer, `Include` expansion (depth ≤32, ≤512 files), `${VAR}` from envvars/`Define`, VirtualHost→ServerName exact map; flags piped logs (`rotatelogs`, `/proc/self/fd/1`); unknown constructs skipped + reported, never fatal.
- **Path tables:** Debian/Ubuntu, RHEL, cPanel EA4, FreeBSD, macOS, Alpine, Bitnami, httpd-Docker (stdout → stdin mode), **nginx: standard + Forge**; last-resort bounded `/var/log` scan accepting only ≥90% parse-rate files. Config-unreadable hosts: path tables + fingerprint still work, filename-regex vhosts, doctor reports degraded mode.
- **Vhost precedence:** format `%v` → config-scanned map → filename regex → `default`.
- **Cache & precedence:** `$XDG_DATA_HOME/wstat/hosts/<key>/detect.toml`; **CLI > `WSTAT_*` env > project config > user config > cache > fresh detect**; invalidate on config mtime/hash, `-V` change, glob membership (60 s), `--redetect`. Large vhost maps live in cache, not user config. `wstat detect --json` / `init --import` for fleet prep.
- **Wizard** (`wstat init`, huh-or-fallback): env summary → sources multi-select (confidence + evidence; **manual path/format entry always available**) → vhost map → services (php-fpm, mod_status) → defaults → 3 s live validation → save with diff. `--yes` for non-interactive. Plus `wstat doctor` (bug-report template) and `wstat config show|edit|set|validate|redetect` with hot reload.
- **mod_status probe order:** config-derived URLs → `127.0.0.1:<port><loc>?auto` → https (skip-verify) → ServerName vhosts; 200 ms each; failures cached with reason.

## 4. UX specification (full product)

- Views: `v` cycles **Dashboard / Services / Zoom**.
- Dashboard: header (rates, status split, filter chips, clock) + HOSTS (vhost, req/s sparkline, hits, bw, err%, last seen) + TOP URLS (latency/p95 columns only when a latency source exists) + CLIENTS (IP, hits, bytes, UA, bot flag, first/last seen) + STATUS/METHODS + LIVE REQUESTS stream (status-colored, vhost-tagged).
- Services (post-MVP): php-fpm pools (active/max, queue, slow, RSS), Apache workers (`?auto`), source health (lines/s, parse errors, drops, rotations).
- Cross-filter semantics: filter on dimension D scopes all panels **except** D's own (drill-down, selection marked).
- Keys: `TAB`/`1–6` focus · `ENTER` zoom · `v` views · `s` sort (defined per panel) · `l` log scale · `g/G` `j/k` · `f` freeze stream · `h c p x m b t` filters · `/` fuzzy (post-MVP) · `?` help · `q` quit.
- Colors: truecolor→256 downsampling, `NO_COLOR`, status colors paired with symbols, stable per-vhost hash, themes + hot reload. Responsive: <100 cols → 2 panels, <60 → 1; sparklines only for visible rows.
- stdin mode: data on stdin, keys on `/dev/tty`; no tty → headless `--output json`.

## 5. Post-MVP: php-fpm integration

Apache combined has no latency/in-flight view; FPM adds it, in three detection-selected tiers:
1. **Status page** (best): `pm.status_path` `?json&full` via internal FastCGI client on the unix socket (sets `SCRIPT_NAME`/`REQUEST_URI`/`REQUEST_METHOD`; **timeboxed spike, cross-validated against `cgi-fcgi`**, hard fallback to tier 2). Metrics: manager, active/idle/total/max, listen queue, slow requests, uptime, per-process rows.
2. **Process stats** (always, read-only): systemctl + /proc worker count, RSS, CPU%, uptime; socket backlog `ss -xl`.
3. **Logs:** pool `access.log` **only if the pool's `access.format` actually has `%d`/`%M`** (php-fpm default has neither — detection-verified before enabling latency columns); `slowlog` → Slow Requests list.
Status path disabled (default) → wizard shows exact enable snippet; degrade to 2/3. No sudo for 2–3.

## 6. Architecture (Go)

```
cmd/wstat/main.go                 run | init | detect | doctor | config
internal/config/                  TOML merge/precedence, hot reload      [post-MVP]
internal/detect/{apacheconf,fingerprint,platform}/  detection providers  [post-MVP]
internal/logsrc/                  globs, tailers, rotation, new-file watch, backfill reader
internal/parser/                  per-format byte scanners, ts parse
internal/store/                   single-owner aggregator, 1s ring, top-K LRU, EWMA, snapshots
internal/filter/                  compiled predicates (cross-filter)
internal/fpm/                     pool conf, status JSON, FCGI client, log tailing [post-MVP]
internal/proc/                    ps/ss/systemctl pollers                 [post-MVP]
internal/ui/{,panels/}            model, views, keymap, themes, wizard, sparkline
```

Pipeline: `tailers → chan Line (bounded 4k, drop+count) → parser workers (nproc−1) → chan Record → store owner → snapshot (1 Hz) → tea.Msg`; pollers emit `Metric` into the same store; UI never blocks on IO.

**Backfill/tail handoff (no double-count):** read tail chunk (≤8 MB / N lines), parse, drop older-than-window, record offset, start tailer at `SeekInfo{Offset}`; inode re-check before handoff. (MVP uses the simple case: seed last 1k lines, then follow.)

**Resource budget:** parse ≤2 µs/line · snapshot ≤5 ms · render ≤16 ms · CPU <5% at 100 req/s · RSS ~50 MB (LRU caps 20k paths/clients, 300 buckets) · stream ring 1000 lines.

## 7. Config schema (post-MVP; MVP = zero-config + flags)

```toml
[source]
paths = ["/var/log/apache2/*-access.log", "/var/log/apache2/other_vhosts_access.log"]
format = "auto"                        # auto | combined | vhost_combined | custom
[source.overrides]                     # per-path pins win over auto
"other_vhosts_access.log" = "vhost_combined"
vhost_from_filename = "(.+)-access\\.log$"
backfill = "10m"; backfill_max_bytes = 8388608

[detect]  redetect_on_config_change = true; scan_fallback = ["/var/log"]
[parse]   keep_query = false; static_ext = [".css",".js",".png",".jpg",".woff2"]
[php_fpm] enabled = "auto"; status_channel = "auto"   # socket|http|processes-only
[mod_status] enabled = "auto"; url = "auto"
[ui]      refresh_ms = 1000; theme = "default"
```

## 8. Milestones (MVP-first)

| # | Deliverable | Est. |
|---|---|---|
| **MVP** | **wstat-min (Lane 1):** globs (apache+httpd+nginx/Forge), per-file combined-family fingerprint, `%v`/filename vhosts, rotation-safe tail + 1k-line seed, store, header + HOSTS/URLS/CLIENTS + stream, colors, filters `/ h x X`, zoom, cross-filter semantics, DoD tests | **1.5–2 d** ✅ |
| P1 | Detection engine (providers, scoring, `detect --json`, `doctor`) — ✅ shipped 2026-10-09; detection cache deferred to P2 (startup detection is ~100ms) | 1.5–2 d ✅ |
| P2 | Wizard + config lifecycle (edit/set/validate/redetect, hot reload, import/export) | 1–1.5 d ✅ (shipped 2026-10-09: TOML config project+user, glob pins, detection cache w/ host key, `init` wizard + fallback, `config show/redetect/edit`) |
| P3 | Full filters (fuzzy, client/path/method/bot/static), view switcher, sort, freeze, themes | 1 d ✅ |
| P3.5 | Rotated/gz history replay (`*.log.N`, `*.log.N.gz` — replay-only sources), inotify→poll fix (missed-append race) | — ✅ |
| P4 | php-fpm (FCGI spike first) tiers 1–3 + Services view + latency columns | 1–1.5 d ✅ (shipped 2026-10-09: internal FastCGI client validated live against php-fpm 8.4; /proc tier; %d access-log latency; `v` Services view; doctor section) |
| P5 | mod_status `?auto` poller, slowlog list, backfill `--since`, polish | 1 d |
| P5.5 | Headless `wstat top` (ngxtop-style grammar, JSON output — scriptability for public users) | 0.5–1 d |
| P6 | Stretch: mod_status in-flight list (ExtendedStatus + HTML scoreboard, best-effort), `ss` connections view, GeoLite2, alerts, ssh source, nginx provider deep support | later |

## 8.1 Going public: GitHub, CI, releases

Target: publish as a general-purpose utility ("realtime per-vhost monitor for Apache/nginx hosts"). Order of operations below; the feature roadmap above feeds §8.1.5.

### 8.1.0 Decision points (resolved 2026-10-09)
- **Module path**: `github.com/steamvogue/wstat` (renamed; internal imports follow). ✅
- **Name check**: `wstat` it is — Plan 9's `wstat` syscall name is unrelated and harmless for a Go module under this path. ✅
- **LICENSE**: MIT. ✅
- Keep `PLAN.md` public (transparency is a feature) — `samples/` stays untracked (production data). ✅

### 8.1.1 Repo hygiene (before first push)
- `LICENSE` (MIT) · `README.md` (hero GIF via charmbracelet/vhs or asciinema, install: go install / release binaries / brew tap, keymap table, supported-formats matrix, filter semantics, **privacy statement** — reads logs locally, zero network calls, no telemetry, never writes to the host — comparison vs goaccess/ngxtop, dev quickstart) · `CHANGELOG.md` (Keep a Changelog) · `CONTRIBUTING.md` (short: test, lint, conventional commits) · `SECURITY.md`.
- `--version` flag populated via `-ldflags "-X main.version=…"` (goreleaser injects on tags).
- `.golangci.yml` (lean: govet, errcheck, staticcheck, ineffassign, unused).
- Issue templates: bug reports must include `wstat doctor` output (P1 makes this the bug-report envelope).
- CI green-ness verified: full suite is portable (samples/ tests self-skip when corpus absent). Note: `go test -race` cannot run on the dev Pi (kernel 6.12 arm64 exceeds the vendored TSAN VMA support — environmental); it runs on CI's older-kernel runners.

### 8.1.2 CI (GitHub Actions)
- `ci.yml` (push + PR):
  - **lint**: `gofmt -l` check, `go vet`, golangci-lint.
  - **test** matrix: `ubuntu-latest` (amd64), `go-version-file: go.mod`, `go test -race -count=1 ./...` (maintainer decision: amd64-only matrix; arm64 release binaries are cross-compiled by goreleaser).
  - **build**: `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"` (asserts the no-cgo, static-binary promise).
- `release.yml` (tag `v*`): goreleaser → GitHub Release with tarballs for linux **amd64 + arm64** (maintainer decision: drop 32-bit arm targets), sha256 checksums.
- `dependabot.yml`: go modules + actions, weekly.
- Later/optional: OpenSSF Scorecard badge, CodeQL, OSS-Fuzz (parser + future config scanner are natural fuzz targets).

### 8.1.3 Release mechanics
- goreleaser config targets linux amd64/arm64 only (maintainer decision); archives + checksums; optional own Homebrew tap repo (`homebrew-<name>`, goreleaser pushes the formula).
- Tag **v0.1.0 only after P1 + P2** (detection + wizard = "works on any host" story, which is the public pitch). An earlier `v0.1.0-rc1` tag is fine to exercise the pipeline.
- Per-release manual checklist: live run on the Pi, clean-container detection matrix (debian+apache2, httpd image, nginx/Forge-style), `logrotate -f` simulation, RSS/CPU budget check, doctor output review.

### 8.1.4 Pre-launch polish
- `?` help overlay (full keymap incl. P3 keys) — cheap, high value for first-time users.
- vhs-rendered demo GIF for the README (the single biggest adoption factor for TUI tools).
- Examples: sample `wstat.toml`, one-liner recipes (`ssh host 'wstat'`, systemd unit with `SupplementaryGroups=adm`).

### 8.1.5 Feature order for public credibility
1. **P1 detection engine + `wstat doctor`** — zero-config on *any* host is the core pitch.
2. **P2 wizard** — first-run UX when detection is ambiguous.
3. **P5.5 headless `wstat top`** (JSON/ngxtop grammar) — scriptability doubles the audience.
4. **P4 php-fpm / P5 mod_status** — the differentiation features ("htop for your webserver").
5. Launch (8.1.6) only after 1–3; 4 can land post-launch as point releases.

### 8.1.6 Launch checklist (post-v0.1.0)
- Show HN / r/selfhosted / r/golang posts with the GIF + a doctor transcript.
- PRs: awesome-selfhosted, awesome-go, charm-and-friends in-the-wild (after ~30 days stable).

## 9. Testing & validation

- **Parser:** table tests from real lines, `FuzzParseLine`, bench ≥500k lines/s, synthetic 1M-line file.
- **MVP DoD tests:** rotation integration (`logrotate -f`: no loss, no double-count), seed-then-follow exactly-once counting, cross-filter semantics table, snapshot screens at 3 widths (teatest or `View()` strings), 24 h soak on this Pi (memory flat, CPU <3% idle).
- **Post-MVP:** config-scanner goldens (Debian/RHEL/cPanel/FreeBSD/Docker) + fuzz + include cycles; fingerprint corpus + ≥90% threshold; **containerized detection matrix** (httpd:alpine, Debian+apache2, cPanel-like fixture, nginx/Forge-style layout); backfill-handoff exactly-once across rotation; FPM fixtures (pool confs, status JSON static/dynamic/ondemand, slowlog) + FCGI vs `cgi-fcgi`; wizard flow with fixture DetectReport.

## 10. Risks & notes

- **FastCGI client** = highest-uncertainty component → timeboxed spike, `cgi-fcgi` cross-check, hard fallback to process tier.
- **mod_status `?auto`** = workers/rates only; per-request in-flight needs `ExtendedStatus On` + HTML parse → stretch, labeled brittle.
- **huh/teatest on bubbletea v2** may lag → spike decides, fallbacks cheap.
- Forge log format assumed combined-equivalent → fingerprint verifies on first real Forge host; `doctor` reports actual format found.
- Config scanner heuristic by policy (skip+report, caps); piped/rotatelogs/stdout logs not tail-able → remediation text; RHEL root-only logs → ACL advice + degraded run; php-fpm default `access.format` lacks `%d/%M` → columns only when truly present; `%D` in Apache config is the cheap latency path without FPM logs.
- Second-granularity timestamps → EWMA 5 s; trust log timestamps, clamp future ts; systemd service: `SupplementaryGroups=adm www-data`.

## 11. Non-goals & distribution

**Non-goals (v1):** long-term storage, HTML reports, remote agent (ssh pipe later), writing/modifying any host file, sudo, Windows. **Distribution:** single static binary via GoReleaser (linux arm64/amd64), golangci-lint + tests in CI, no cgo; `wstat doctor` doubles as bug-report template.
