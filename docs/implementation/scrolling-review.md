# Panel scrolling review — 2026-10-10

Reviewed the changes for v0.2.0 on top of `404768f`, including bottom-help,
Hosts-freeze and bot-filter fixes. The evidence below records local validation;
release CI and artifact verification are recorded in the GitHub release notes.
The sections through Validation describe v0.2.0; the follow-up below records
the v0.2.1 bindings. The final section describes the v0.3.0
per-pane freeze behaviour, which supersedes those historical bindings.

## Confirmed problems and fixes

| Trigger | Previous behaviour | Corrected behaviour |
|---|---|---|
| Bot filter changes ranking | Previously selected URL followed its new rank; reproduced row 1 → row 40 | Immediate refresh; affected tables start at the top |
| New table leaders arrive | Following the former top row could hide the leaders | Row zero stays at the top; lower selections follow identity |
| Stream search then `G` / arrows | Navigation used the raw stream count instead of visible matches | Rendering and navigation use the same matching records |
| New requests arrive while scrolled | Rolling snapshot moved the request at the selected index; reproduced `/row-039` → `/row-049` | Store assigns unique stream IDs; selection follows the same request |
| Identical requests / expired selection | A content comparison cannot distinguish identical requests; stale index drifted | IDs distinguish identical requests; expiry selects the oldest visible retained request |
| Resume stream with `f` | Follow could stay paused until manually reaching the bottom | Resume immediately selects the newest visible request |
| Search in one pane | Other pane positions reset; the same query filtered unrelated panes | Query and position reset belong to the selected pane |
| Unicode search backspace | Removed one byte, potentially splitting a character | Removes the last UTF-8 rune |
| `G` in empty pane | Selection became `-1` | Selection remains zero |
| Dashboard resize / long rows | Border dimensions were subtracted twice; wrapping could push the latest request below the reserved viewport | Border-inclusive dimensions, bounded titles and exact row budgets |
| Scroll / zoom in Services | Keys moved hidden dashboard selections; pools/sources had no scroll controls | Separate Services focus, positions and zoom; pools/sources use scroll windows |

## Expected use cases

- Hosts, URLs and Clients: `g` keeps live leaders visible; lower selections
  follow their row as rankings change. If the row disappears, return to the top.
- Status/bot/static filters reset all three tables. Method resets URLs only;
  host selection resets URLs/Clients while retaining the chosen Hosts row.
  Client/path filters scope the stream and retain table selections. Sort resets
  only its table. Clearing filters resets table positions.
- Hosts `F` retains rows and values; navigation/search still work. Relevant
  filter/sort changes resume updates. It does not pause ingestion or other panes.
- Stream: Up / `g` stops follow. Down to the final visible row / `G` enables
  follow unless `f` is active. `f` pauses follow; pressing it again resumes at
  the newest visible request immediately.
- Stream search starts at the first match; arrows and `g/G` operate on matches.
  Empty results have a zero selection and show `no matches`.
- Scrolled or paused stream selection follows its ID until it leaves the recent
  snapshot (at most 100 records matching the global filters). Then it selects
  the oldest visible record without silently enabling follow. This is bounded
  recent history, not a retained log archive.
- Services: `1` pools, `2` sources, `3`/`4` stream; Tab / Shift+Tab cycles panes.
  Arrows / `j/k`, `g/G`, search and zoom operate in the visible pane. Pool/source
  identities survive insertions and reset to the top on removal. Dashboard
  table focus/selections and zoom remain independent; the stream's position is
  shared between views.
- Resize and zoom keep selections visible when the pane has content rows.
  At the minimum terminal height, a pane can have only its title; zoom provides
  content space. Help remains on the bottom row.

## Validation

Behavioural regressions are in `internal/ui/scrolling_test.go` and
`internal/ui/ui_test.go`. They cover all dashboard panes, search / cancel /
empty results, filter scopes, reorder, request rollover / duplicate requests /
expiry, freeze / resume, resize / zoom, Services pools / real source fixtures,
view switching and independent navigation state. Store stream-ID uniqueness
and immutable snapshots are checked in `internal/store/store_test.go`.

Local full Go tests and lint pass. Local race validation is unavailable because
of this host's race detector limitation, recorded in `docs/LOCAL_TOOLS.md`.
Release publication requires successful race tests on the compatible Ubuntu CI
runner; the release notes record the run for the published commit.

The compiled CLI also passed the existing Linux PTY sampler with 54,000 seeded
requests, 63 sources, a 160×50 terminal, 2 seconds of warm-up and 3 seconds of
live traffic (299 records emitted). The final request sentinel was visible and
the process exited with code zero. With `COLORTERM`, `NO_COLOR`, `TMUX` and
`WT_SESSION` unset and `TERM=xterm-256color`, its captured terminal output had
1,698 indexed colour sequences and no RGB colour sequences. This is a terminal
smoke check, not a new performance benchmark or proof of every emitted count.

## Freeze indicator follow-up (v0.2.1)

The generic header `frozen` chip represented stream auto-follow only. Pressing
`F`, then `f` twice could clear that chip while Hosts retained their frozen
snapshot. The controls are now lowercase `f` for Hosts freeze and `z` for
stream pause. `z` had no existing binding, so the path-filter key `p` stays
available. Uppercase `F` is no longer assigned. The header names each panel
and its key; titles show `[frozen] f resume` for Hosts and `[paused] z resume`
for the stream.

Regression tests exercise the independent toggle sequence, immediate Hosts
catch-up, printable/code-only lowercase events, both views and all focused
panes, path-filter preservation, search text and the longer titles across
terminal sizes/views. Full Go tests, vet and lint pass locally.

An isolated compiled-CLI PTY check with `TERM=xterm-256color` exercised
`f, z, z, f` in zoomed Hosts. Twenty records for a previously empty source
were ingested while frozen; its host stayed hidden through both stream toggles
and appeared when `f` resumed Hosts. The program exited with code
zero. This validates local behavior; it does not establish which key events
the reporting user's terminal sent. Local capture and result files are under
`/tmp/wstat-freeze-pause-bindings-check/` (temporary, not release artifacts).

## Independent pane freeze (v0.3.0)

Lowercase `f` now freezes/resumes the focused pane. Hosts, Top URLs, Clients,
Live Requests, PHP-FPM and Source Health each retain their own snapshot; several
can remain frozen together. The stream is one shared pane across both views.
Shift+F unfreezes all panes, including those in the hidden view. This key was
unassigned in v0.2.2, so there is no overlapping shortcut.

A frozen pane retains rows, metrics and order across ticks, global filter/sort
changes and ingestion. Table labels and selection markers describe its captured
filters/sort; filter maps are cloned because the live toggles mutate them.
Clearing filters keeps frozen viewport positions. User scrolling, local search,
zoom, resize and themes can redraw the retained data without replacing it.
Resuming applies current filters/sort immediately and preserves the inspected
row where it remains available. Empty panes can be frozen too.

Live Requests retain their immutable snapshot even after those records leave
both the visible recent snapshot and the underlying live buffer. `z` remains
an independent auto-follow pause: it permits live data refreshes, whereas `f`
retains data. Neither individual resume nor Shift+F clears `z`. On resume, the
stream follows the latest visible request if auto-follow is enabled; otherwise
it remaps the inspected request by ID, falling back to the oldest visible
request when that record is no longer available.

PHP-FPM snapshots copy pool values and pointed-to status counters. Source
snapshots retain the tailer's copied sources and diagnostics. Header totals,
source counts and FPM alerts continue to reflect live ingestion even while
those panes are frozen. Every frozen pane shows its own title indicator;
compact bottom help includes `f freeze` and `F thaw all` in both views.

`internal/ui/freeze_test.go` checks all table snapshots, multiple simultaneous
freezes, individual and global resume, printable/code-only/modified Shift+F,
stream buffer rollover, scrolling, search, shared views, zoom/resize, frozen
filter-map isolation, live pool alerts, real source discovery and empty panes.
Existing UI/scrolling regressions were updated for the new focused-pane binding
and explicit auto-follow pause state. Full Go tests, vet and lint pass locally;
local race checks remain unavailable for the host reason recorded above.

The compiled CLI also passed an isolated `TERM=xterm-256color` PTY smoke check.
Hosts, URLs and stream were frozen together while twenty requests were appended
to a real access log. Scrolling the frozen stream kept its data; resuming URLs
left Hosts and stream frozen. Sending uppercase `F` resumed all panes while
preserving `z` pause, and the newly ingested host/path became visible. The
process exited with code zero. Captures and results are temporarily stored in
`/tmp/wstat-panel-freeze-check/`; the helper is `/tmp/check-wstat-panel-freeze.py`
and the tested binary is `/tmp/wstat-panel-freeze`.
