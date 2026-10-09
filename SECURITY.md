# Security Policy

## Design guarantees

- wstat **only reads** access-log files it is explicitly given or discovers under
  the standard log directories. It never writes, deletes, or modifies files on the
  host, and never requires elevated privileges.
- wstat makes **zero outbound network connections** — no telemetry, no update
  checks, no downloads.
- Untrusted input (log lines, filenames) is parsed defensively: bounded line
  lengths, hard caps on tracked rows, bounded gzip decompression (64 MB per file).

## Reporting a vulnerability

Please use [GitHub's private vulnerability reporting](
https://github.com/steamvogue/wstat/security/advisories/new) for this repository.
Include a reproduction (sample log line or crafted input) if possible. Expect a
response within a few days.
