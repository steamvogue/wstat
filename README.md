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
- **Rotation handling**: drains the old descriptor on rename/recreate and detects
  ordinary copytruncate. Concurrent copytruncate can destroy unread bytes or evade
  detection when overwritten content is identical; no reader can guarantee recovery.
  Numbered/date-suffixed and compressed history is replayed at startup with bounds.
- **Live rates, not fake spikes**: replayed history counts in totals and tables but
  never feeds the req/s rate counters.
- **Drill-down filters** (cross-filter semantics): selecting a host scopes every other
  panel; the hosts panel keeps showing all hosts with the selection marked.
- **Bounded detail**: retains up to 20,000 URLs, 20,000 clients, 2,048 hosts and
  40,000 client/host associations (64 per client). New traffic replaces older detail;
  lifetime totals continue counting every parsed web request. Go's `GOMEMLIMIT` is
  honored; actual RSS depends on the workload. See [measured results](docs/implementation/README.md).
- **Local monitoring**: sends no telemetry. Detection writes a local cache by default;
  config commands and opt-in profiles also write local files. Enabled PHP-FPM
  monitoring connects to configured Unix/TCP status endpoints.

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
wstat -n 0 -fpm=false    # follow new lines only, without PHP-FPM discovery/probes
wstat -config local.toml # use an explicit configuration file
```

Configuration lives in `./wstat.toml` (project) and
`~/.config/wstat/config.toml` (user, written by `wstat init`), with explicit
paths and vhost pins (globs allowed) overriding detection. Detection results
are cached and refreshed when config/include contents, include matches or symlink
targets change, with a five-minute expiry (`wstat config redetect` forces refresh).
Invalid or unreadable config is reported as an error; an absent optional config uses
defaults. Explicit zero/false settings are preserved.

To disable detection cache reads/writes and all PHP-FPM monitoring:

```toml
[detect]
cache = false
[fpm]
enabled = false
```

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

Filters apply to retained detail as follows. Status/bot/static intersections use
joint counters, including matching bytes, errors, latency and rates.

| View | Applied filters |
|---|---|
| Hosts | Status, bot, static; keeps all hosts selectable |
| URLs | Host, method, status, bot, static; keeps paths selectable |
| Clients | Host, status, bot, static; keeps clients selectable |
| Request stream | Host, client, path, method, status, bot, static |
| Global header | Unfiltered lifetime web totals; host count is retained hosts |

Client/path selection filters the recent stream only; method also filters URLs.
Search acts on the focused panel. Tables show at most 200 rows; the stream displays
at most 100 matching records from its 500-record ring. Eviction counters mark partial
detail: aggregates can no longer reconstruct intersections whose associations were
evicted. Seeded history counts in totals but contributes no live rate.

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

Live paths can be empty, temporarily missing or extensionless (`access_log`). Seed
reads are bounded to 256 KiB per plain file and 64 MiB decompressed per gzip file;
these bounds can yield fewer than `-n` lines. Four seed workers run at once. Lines
over 1 MiB in live files are skipped with a source warning; oversized/malformed gzip
and read failures also appear in the Services source-health view. Incomplete live
lines wait for completion.

### PHP-FPM measurements

Services (`v`) shows PHP access requests separately from web traffic: average duration,
average request memory, bad access lines and newly observed slowlog entries. Status
`slow` and observed `slowlog` counts have different scopes and are displayed separately.
Pool process memory is an estimate from `/proc`; request memory is not transferred bytes.

Access parsing follows the configured `access.format`. Supported duration units are
seconds (default), milli/milliseconds and micro/microseconds; memory uses bytes
(default), kilo/kilobytes or mega/megabytes. The parser supports the standard default
timestamp and common identity/request fields, including `%r%Q%q`. Custom timestamp
formats, unknown units/placeholders and ambiguous adjacent fields produce a visible
diagnostic. Shared access logs require a consistent format and `%n` pool attribution.
See the [PHP-FPM configuration manual](https://www.php.net/manual/en/install.fpm.configuration.php).

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
go test ./internal/store -run '^$' -bench 'BenchmarkSnapshot' -benchmem
python3 scripts/measure_workload.py /tmp/wstat --mode live --output /tmp/wstat-live
```

For local profiles, quit normally to finalize the files:

```sh
wstat -profile-after 3s -cpuprofile /tmp/wstat-cpu.pprof -heapprofile /tmp/wstat-heap.pprof
go tool pprof /tmp/wstat-cpu.pprof
```

Profiling is disabled by default. `-profile-after` delays CPU sampling past startup;
heap sampling runs after background ingestion stops. A quit before the delayed start
reports that the CPU profile did not start. Compatible CI runs the race suite; this
host's ARM64 ThreadSanitizer cannot execute due to its virtual-address layout.

The optional `samples/` directory (real production logs) enables extra corpus tests;
they self-skip when absent. See [PLAN.md](PLAN.md) for the full architecture and
roadmap (host detection engine, config wizard, php-fpm status integration, Apache
mod_status, headless JSON mode).

## License

[MIT](LICENSE)
