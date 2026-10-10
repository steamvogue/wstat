# Panel scrolling review — 2026-10-10

Reviewed the changes for v0.2.0 on top of `404768f`, including bottom-help,
Hosts-freeze and bot-filter fixes. The evidence below records local validation;
release CI and artifact verification are recorded in the GitHub release notes.

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
