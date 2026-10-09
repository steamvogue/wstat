# wstat

**Realtime per-vhost traffic monitor for Apache and nginx — a dashboard in your terminal.**

`wstat` tails your web server's access logs and shows, live: which **hosts (vhosts)** are
being hit, which **URLs** are being pulled, **who** is pulling them (client IPs / user
agents), and a colorized stream of every request — with drill-down filters, sorted
panels, and rotation-safe tailing. One static binary, zero configuration on standard
Debian/Ubuntu/RHEL/nginx/Laravel Forge layouts.

```
 VIEW: DASHBOARD                    10:42:07  hosts:8  4.2 req/s  18.2K/s  2xx 87% 4xx 9%
 ┌ HOSTS ────────────────────────────┬ TOP URLS ────────────────────────────┐
 │ cms.local   3.1/s  ▁▂▄▆█  1.2k 2% │ GET /panel/login      210   18ms     │
 │ dev.local   0.8/s  ▁▁▂▁▂   340 0% │ GET /api/nodes        188            │
 ├ CLIENTS (who's pulling) ─────────┼ STATUS / METHODS ─────────────────────┤
 │ 192.168.100.219   410 hits   1% ↑ │ 2xx 87%  3xx 2%  4xx 9%  5xx 2%      │
 ├ LIVE REQUESTS ────────────────────┴──────────────────────────────────────┤
 │ 12:46:31 cms.local 192.168.100.219 GET /panel/site 200 4.2K             │
 └──────────────────────────────────────────────────────────────────────────┘
```

## Highlights

- **Zero config**: discovers logs in the standard Apache (`/var/log/apache2`,
  `/var/log/httpd`) and nginx (`/var/log/nginx`, Laravel Forge) layouts, detects the
  combined-family format per file, and derives the vhost from the log line or filename
  (`example.com-access.log` → `example.com`; `-ssl-access.log` merges into the domain).
- **Rotation-proof**: follows logrotate rename/recreate and copytruncate without losing
  or double-counting a single line. Rotated (`*.log.1`) and compressed
  (`*.log.N.gz`) history is replayed at startup (bounded), so panels start populated.
- **Live rates, not fake spikes**: replayed history counts in totals and tables but
  never feeds the req/s rate counters.
- **Drill-down filters** (cross-filter semantics): selecting a host scopes every other
  panel; the hosts panel keeps showing all hosts with the selection marked.
- **Fast and light**: ~590k lines/s parse on real production logs, zero allocations on
  the hot path, hard caps on tracked rows, a soft memory limit — ~50 MB RSS on a
  Raspberry Pi 5 with 77 vhosts of history.
- **Privacy by design**: reads access logs locally, makes **zero network calls**, sends
  **no telemetry**, and **never writes** to your host.

## Install

```sh
go install github.com/steamvogue/wstat@latest
```

or grab a static binary (linux amd64 / arm64)
from the [releases](https://github.com/steamvogue/wstat/releases) page.

From source:

```sh
git clone https://github.com/steamvogue/wstat
cd wstat && go build -o wstat .
```

## Usage

```sh
wstat                    # auto-detect host layout (Apache/nginx config scan + globs)
wstat init               # pick sources interactively; writes your config
wstat doctor             # what wstat found on this host — attach to bug reports
wstat detect --json      # machine-readable detection report
wstat config show        # effective config + origins + detection cache state
wstat config edit        # edit the user config ($EDITOR)
wstat 'samples/*access*' # explicit globs (multiple allowed)
wstat -n 5000            # seed more history lines per file
```

Configuration lives in `./wstat.toml` (project) and
`~/.config/wstat/config.toml` (user, written by `wstat init`), with explicit
paths and vhost pins (globs allowed) overriding detection. Detection results
are cached and refreshed automatically when your Apache/nginx config or
version changes (`wstat config redetect` forces it).

Zero-config detection scans your Apache/nginx configuration (via `apache2ctl -V` /
`/etc/nginx/nginx.conf`, includes, `envvars`/`Define` expansion) to attribute each log
to its exact `ServerName` — e.g. `pcash.local-access.log` → `pcash.home` even when the
filename would guess wrong — and falls back to the standard log globs everywhere else.
Files declared in config but empty or missing are waited on, so idle vhosts appear the
moment they receive traffic.

Works great over ssh (`ssh host wstat`) and in tmux. Needs read access to the log
files — on Debian/Ubuntu, membership in the `adm` group is usually enough; no sudo.

### Keymap

| Key | Action |
|---|---|
| `tab` / `1`–`4` | focus panel (hosts / urls / clients / stream) |
| `enter` | zoom focused panel · `esc` back |
| `j` `k` `g` `G` | move selection |
| `/` | fuzzy search in focused panel (`enter` apply, `esc` cancel) |
| `h` | toggle host filter on selected row (drills into every other panel) |
| `c` | toggle client-IP filter (from clients panel) |
| `p` | toggle path filter (from urls panel) |
| `x` | cycle status filter (all → 4xx-5xx → 2xx-3xx) |
| `m` | cycle method filter (all → GET → POST → HEAD) |
| `b` | cycle bot filter (all → bots → humans) |
| `t` | hide static assets (.css/.js/images/…) |
| `s` | cycle panel sort (rate → hits → errors → bytes) |
| `f` | freeze the stream auto-follow |
| `T` | cycle theme (amber / ocean / mono; honors `NO_COLOR`) |
| `X` | clear all filters |
| `q` / `ctrl+c` | quit |

Filter semantics: host, status, bot and static filters are exact everywhere
(per-dimension counters); method is exact on URLs and stream; client/path are exact on
the stream and their own panels. Active filters show as chips in the header.

## Supported log formats

Combined-family formats are parsed natively per file (a host may mix them):

| Format | Example |
|---|---|
| Apache `combined` | `%h %l %u %t "%r" %>s %O "referer" "ua"` |
| Apache `vhost_combined` | leading `vhost:port` per line |
| nginx `combined` / Laravel Forge | same layout as Apache combined |
| Rotated & gz history | `*.log.1`, `*.log.N.gz` replayed at startup |

Weird input is handled, not fatal: `-` byte counts, missing referer/UA, escaped quotes,
IPv6 clients, binary TLS-handshake garbage lines (counted as bad, shown in the footer).

## How it compares

| | wstat | goaccess | ngxtop |
|---|---|---|---|
| Realtime TUI | ✅ | ✅ (ncurses) | ✅ (plain tables) |
| Starts instantly (no full-file parse) | ✅ | ❌ | ✅ |
| Live req/s rates & cross-panel drill-down | ✅ | limited | ❌ |
| Raw request stream | ✅ | ❌ | ❌ |
| gz/rotated history replay | ✅ | manual | ❌ |
| Single static binary, no runtime deps | ✅ | C build | Python |

## Development

```sh
go build ./... && go test -race -count=1 ./...   # tests are portable
golangci-lint run
```

The optional `samples/` directory (real production logs) enables extra corpus tests;
they self-skip when absent. See [PLAN.md](PLAN.md) for the full architecture and
roadmap (host detection engine, config wizard, php-fpm status integration, Apache
mod_status, headless JSON mode).

## License

[MIT](LICENSE)
