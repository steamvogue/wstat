# Request text readability — 2026-10-10

The reported screenshot used faint decoration colours for URL paths and client
agents. Request text now has a separate light-grey style in amber, ocean and
mono themes. Client addresses also use that style; borders remain subdued.

Live rows use the existing per-request bot classification for a leading
`b` marker. Compact agent summaries expose browser/bot identities from long
compatibility strings, while the Clients pane retains longer platform text.

Status and byte counts retain their space. Agent text uses at most one third
of the remaining row (capped at 40 cells), with no padding beyond the summary
it needs. A complete URL has priority; agents are omitted if fitting them would
shorten a URL that otherwise fits. Short terminals omit time/client columns
before sacrificing the URL. Padding/truncation measure terminal cells rather
than rune counts, including wide Unicode characters.

## Validation

- Full Go tests, vet, lint and build pass locally.
- Regressions cover 38–248-cell stream rows, short/long/wide Unicode URLs,
  bot/human markers, missing agents, complete-URL preservation, browser/bot
  identities and render dimensions/contrast in all three themes.
- A compiled CLI in a 161×40 `TERM=xterm-256color` PTY displayed both URL proof
  strings, Chrome and Googlebot identities, and the bot marker. Both URL strings
  used foreground palette index 251 (light grey), with no RGB sequences in the
  captured frame. The process exited normally. Temporary capture/results:
  `/tmp/wstat-readability-pty/`.
- All theme frames were captured through `TestReadabilityThemes`. The amber
  frame was rasterized from its ANSI colours and visually reviewed using
  illustrative `.example` hosts; it is a fixture preview, not a server capture.

See [local tooling](../LOCAL_TOOLS.md) for the reusable theme capture command.
