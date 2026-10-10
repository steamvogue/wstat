# Public examples and privacy

- Use animal names with `.com`, `.net` or `.org` for first-party host examples
  throughout documentation, tests, fixtures, filenames and screenshots. Reuse
  the dashboard's animal names, such as `otter.net`, `redpanda.com`, `badger.org`,
  `koala.net`, `falcon.com`, `gecko.org`, `lynx.net` and `wombat.com`.
- Never copy real deployment hostnames, local aliases, client addresses or
  identifying window titles into public project material. Do not keep an
  original-to-example mapping in the repository.
- Use documentation address ranges (`192.0.2.0/24`, `198.51.100.0/24`,
  `203.0.113.0/24`, `2001:db8::/32`) for captured client examples. Loopback and
  deliberately synthetic network addresses may remain when needed by tests.
- Preserve the behavior covered by fixtures: filename versus `ServerName`
  mismatches, aliases, subdomains, wildcards, rotations and sorting still need
  distinct examples. Animal domains are illustrations, not endpoints to probe.
- Public third-party documentation, dependencies, bot-agent links and the
  author's project links may retain their actual names. Filesystem directories
  such as `~/.local/share` are not hostnames.
- Keep production sample corpora untracked. Validate edits with the existing
  tests and tools recorded in `docs/LOCAL_TOOLS.md`; record useful new local
  tools there for reuse.
