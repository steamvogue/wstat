# Code quality and CPU review — 2026-10-09

Reviewed revision: `335dfe2`. Scope: all eight internal packages, application wiring, tests, configuration, and CI; 7,488 Go lines including tests. Application source was not changed. Experiments ran in `/tmp/wstat-review/source`.

Implementation sequence, acceptance checks, and token budgets are in the [remediation plan](../../FIX_PLAN.md).

The subsequent implementation addresses all 12 findings. See the
[resolution table and validation evidence](../../implementation/README.md).
This document preserves the original review and baseline line references.

The package boundaries and ordinary test coverage provide a useful foundation, but there are significant performance and monitoring-correctness defects. Lint success does not cover these behavioral failures.

## Validation

| Check | Result |
|---|---|
| `go build` | Pass |
| `go vet ./...` | Pass |
| `golangci-lint run ./...` | Pass, zero issues |
| `gofmt -l .` | Pass |
| `actionlint` | Pass |
| `go test -count=1 -coverprofile=... ./...` | Pass outside sandbox; local Unix-socket test is blocked inside it |
| Overall statement coverage | 68.6%; main has 0%, FPM 49.4%, other packages 72.1–94.5% |
| Race detector | Cannot execute on this ARM64 host: unsupported VMA range, both inside and outside sandbox |
| Live FPM E2E | Not run; opt-in test needs `WSTAT_FPM_E2E_ADDR` |
| Targeted defect reproductions | Confirmed map saturation, combined-filter mismatch, partial startup-line loss, and extensionless live-log misclassification |

No running `wstat` process was found in the host process list. Consequently, this review identifies and measures CPU contributors but does not establish the cause of the reported production 200% CPU reading. A conventional per-process 200% reading means approximately two logical CPUs are busy, rather than an impossible utilization percentage.

## Measured CPU/allocation findings

The benchmark loads the repository's access-log samples through the actual seed/parser/store pipeline, then repeatedly requests the same top-200 snapshot without new ingestion. It uses 63 sources, 53,945 seeded records, 46 hosts, 20,000 URL rows, and 11,331 clients. Sources are replay-only for this experiment. The Go benchmark reports GOMAXPROCS=4. Each configuration ran three batches of 20 snapshots, without CPU profiling enabled for the timing comparison.

| Configuration | Time/snapshot, observed range | Median | Bytes/snapshot | GC cycles/snapshot |
|---|---:|---:|---:|---:|
| Current code, 48 MiB | 28.4–33.8 ms | 31.6 ms | 23,970,944 | 2.45 |
| Current code, 256 MiB experiment | 22.0–22.7 ms | 22.1 ms | 23,970,944 | 0.95–1.00 |
| Preallocated slices, 48 MiB experiment | 14.3–14.5 ms | 14.3 ms | 5,873,144 | 0.45–0.50 |
| Preallocated slices, 256 MiB experiment | 13.0–13.5 ms | 13.4 ms | 5,873,144 | 0.20 |

At the nominal two-refreshes-per-second cadence, the baseline is about 48 MB/s of snapshot allocations. Actual cadence also includes processing time. Preallocation cut allocated bytes by about 75% and median snapshot time by about 55% at the existing limit. This is a snapshot microbenchmark, not a measurement of end-to-end CPU savings. Concurrent seed delivery can change which URL keys occupy the capped map; timings are local observations, not portable guarantees.

The experimental patch only adds the following after `s.maybeFlushLocked(now)` in `Store.Snapshot`:

```go
hostsRows = make([]Row, 0, len(s.hosts))
urlRows = make([]Row, 0, len(s.urls))
clientRows = make([]Row, 0, len(s.clients))
```

The existing store tests and diagnostic tests passed on the experimental copy. This is a useful first optimization, but top-K selection and reusable storage should reduce allocations further, especially when filters match few rows. The preallocation experiment retains the existing complete sorting and locking behavior.

A separate CPU/heap-profile run attributed about 92% of allocated bytes to `Snapshot`, with substantial CPU time in memory copying and GC scanning. That run included setup and both memory limits, so its percentages are not production attribution. Profiles remain at `/tmp/wstat-review/cpu.pprof` and `/tmp/wstat-review/heap.pprof` with matching `/tmp/wstat-review/store.test`; they are temporary local artifacts. Raw repeat timings and diagnostic source files are saved beside this report.

## Prioritized findings and fixes

1. **P1 — Snapshot allocation churn is amplified by the hard-coded memory limit.** `internal/store/store.go:391` constructs all rows, allocates a display key for every URL, sorts everything, and only then retains 200 rows. This happens on the 500 ms UI timer (`internal/ui/ui.go:20`) while holding the ingestion mutex. `main.go:31` unconditionally sets a 48 MiB soft limit. The corpus measurements above confirm the interaction. Fix: preallocate as a first step, then select only the best K rows, materialize strings only for retained rows, reuse buffers safely, and move sorting outside the store lock using a consistent copied snapshot. Cache unchanged results while still updating rate decay. Remove the unconditional limit or make it explicitly configurable; choose any default from measured live-memory requirements and available headroom. **Setting `GOMEMLIMIT=256MiB` alone cannot fix this build: main overwrites it.** Go's [GC guide](https://go.dev/doc/gc-guide#Memory_limit) describes thrashing near the memory limit and specifically cautions against baking limits into input-dependent CLI tools; [SetMemoryLimit documentation](https://pkg.go.dev/runtime/debug#SetMemoryLimit) explains the environment variable's initial-setting semantics.

2. **P1 — Entire slow logs are rescanned every poll.** `internal/fpm/doctor.go:15` scans from byte zero to EOF, despite its “bounded scan from the end” comment. `internal/fpm/poller.go:115` invokes it for each pool every two seconds; initialization scans twice. Cost grows with accumulated historical log size even on an idle web server. A poll that takes longer than its interval can keep the poller almost continually occupied. Fix: keep inode/offset/partial-line state and process only appended bytes; handle rotation/truncation and share readers for duplicate slowlog paths. Bound initial scanning. This path was inspected but not profiled against the user's actual slow logs.

3. **P1 — URL/client tracking stops admitting new keys permanently at capacity.** Insertion refuses new keys at `len >= 20000`, but `evictLocked` (`internal/store/store.go:348`, `:355`) only runs its deletion loops at `len > 20000`, an unreachable state through ordinary insertion. The reproduction fills the maps, ages all rows, invokes eviction, and confirms that a fresh URL/client is still rejected. Fix: evict at capacity before rejecting new insertions, or maintain a periodic bounded TTL/LRU policy; expose overflow counts to users. Simply changing `>` to `>=` still requires considering admission timing and stale-state policy.

4. **P1 — Startup drops the prefix of an incomplete log line.** `internal/logsrc/logsrc.go:193` discards the unfinished seed line but returns EOF as the resume offset at line 201. A file containing `one\npart`, followed by an append of `ial\n`, emits `ial` instead of `partial`; reproduced. Fix: resume at the beginning of the unfinished line, with a defined policy for oversized lines. Also test replacement between seed and open: the current size comparison does not prove inode identity.

5. **P1 — Active logs without a `.log` suffix are replayed once and never followed.** `internal/logsrc/logsrc.go:93` and `internal/detect/report.go:150` classify every other filename as replay-only. An active `access_log`, included by the httpd default glob, is misclassified; reproduced. Fix: recognize actual rotation/compression suffixes and default other configured/readable source paths to live.

6. **P1 — FPM access records contaminate web-traffic totals.** `main.go:109` adds FPM access logs to the same source stream as Apache/nginx and feeds both into the same store at lines 136–138. When a PHP request appears in both logs, global requests count it twice. Additionally, `internal/fpm/access.go:93` maps PHP request-memory usage into `Record.Bytes`, which the store interprets as transferred bytes. Fix: distinguish source/measurement types, keep FPM service statistics separate, and only enrich web requests when correlation is well-defined. Do not mix request memory and network bytes. This finding follows the application data flow; no live FPM workload was available.

7. **P2 — Slow or unreachable FPM pools can freeze the dashboard.** `internal/fpm/poller.go:75` holds the same write lock needed by `Views` throughout file scans and serial FastCGI queries. A connected but unresponsive socket can consume its two-second I/O deadline per pool; connection establishment has its own timeout. The ordinary dashboard also calls `Views` from its header. Initial polling runs synchronously before the UI starts. Fix: collect results outside the publication lock, use bounded concurrency and cancellation, and atomically swap a completed snapshot. Keep the most recent status available while probes run.

8. **P2 — Combined filters produce incorrect counts.** `internal/store/store.go:147` calculates status-filtered hits and then overwrites them with the bot count; subtracting all static hits is also not a true intersection. Reproduction: one successful bot request plus one failed human request produces one host hit for “bots + errors”, while the exact stream correctly has zero. With a host selected, client counts bypass other filters at line 435. Rate/bytes/errors remain unfiltered, so displayed error percentages can also become inconsistent with filtered hits. Some internal comments admit approximation, but the README promises exactness too broadly. Fix: track compact joint counters for supported intersections and corresponding metrics, or clearly limit and label supported filter semantics.

9. **P2 — Seed substrings retain large backing buffers.** `internal/logsrc/logsrc.go:184` splits a string holding up to 256 KiB; parser fields are subslices, and store keys/fields retain them. Even a small retained field can keep a complete source buffer live. Fix: copy retained seed lines or clone/intern long-lived fields at the ownership boundary, validated with an in-use heap profile. This is a retention mechanism identified from code; its isolated memory contribution was not measured. The seed reader also reads its full 256 KiB budget even for `-n 1`.

10. **P2 — Detection cache ignores included configuration changes.** `internal/detect/cache.go:36` fingerprints the root Apache/nginx config, not the included site files or their directory membership, and has no TTL. Adding or editing a vhost can leave attribution/source discovery stale indefinitely. Fix: fingerprint the discovered include graph and relevant directories, or combine explicit invalidation with bounded expiry. `wstat config redetect` is the existing manual workaround.

11. **P2 — FPM RSS has the wrong unit.** `readProcStat` returns bytes, but `internal/fpm/proc.go:47` divides by 4096 into a field named `RSSKB`; on a 4 KiB-page host this reports one quarter of the intended KiB. Fix: divide returned bytes by 1024 and test conversion separately. The parser also checks `len(f) < 21` before indexing `f[21]`; use a minimum of 22 fields.

12. **P2 — Configuration errors are silently discarded.** `internal/config/config.go:92` ignores parse/read errors, and `main.go`'s config editor then prints “config saved” merely because `Load` reports the same path. Invalid edits can silently fall back to defaults. Fix: propagate diagnostics, distinguish absent files from invalid/unreadable files, and validate the edited TOML before reporting success. Add main/config command tests; application wiring currently has no statement coverage.

Further maintenance concerns: host keys and each client's host map lack the URL/client caps; automatic FPM polling and detection-cache writes contradict the README's blanket “zero network calls” and “never writes” promises. Reconcile documentation with supported behavior and provide explicit controls.

## Suggested CPU repair sequence

1. Capture a profile during the actual problem, distinguishing startup replay from steady-state CPU. Sample the specific process with `pidstat -u -r -t -p PID 1 15`. The current program has no CPU-profile flag; add an opt-in `runtime/pprof` file output or use a diagnostic build. Capture a heap profile and GC metrics alongside CPU.
2. Apply the measured snapshot preallocation improvement, then implement top-K/reuse and shorter lock duration. Preserve tie ordering, selected rows, filtering, and rate decay in tests. Raise/remove the hard-coded memory limit only with an explicit memory-headroom decision; 256 MiB above was an experiment, not a universally safe setting.
3. Replace repeated full slowlog scans with incremental reads and move I/O outside the UI-facing mutex.
4. Bound replay concurrency and avoid scanning all compressed rotations unless requested. Gzip replay currently scans up to roughly 64 MiB of nonempty line content per archive to return its final N lines; reducing N does not avoid that decompression work.
5. Add performance checks for idle refresh at full cardinality, concurrent ingestion plus snapshots, many seeded sources, and large slowlogs. Re-run race tests on compatible CI. Measure before/after CPU, allocation rate, live heap, and ingestion lag on the affected workload.

For immediate workload reduction without code changes, explicitly select only the needed **live `.log` files**, omit rotated/gzip globs, and use `-n 1` to retain fewer seed records. This can reduce aggregation work but does not eliminate the fixed seed-buffer read or automatic FPM polling. `-n 0` and `source.seed_lines = 0` do not disable replay in this revision because the overrides accept only positive values. No current setting disables FPM polling.

## Reusing the evidence

`store_audit_test.go.txt` and `logsrc_audit_test.go.txt` contain temporary diagnostic tests and the corpus benchmark. Copy them to their corresponding internal package directories in a disposable checkout, removing the `.txt` suffix. Make the repository's optional `samples/` corpus available at that checkout's root. Run:

```sh
go test ./internal/store ./internal/logsrc -run TestAudit -v
go test ./internal/store -run '^$' -bench BenchmarkAuditSnapshotCorpus -benchtime=20x -count=3 -v
```

These diagnostic tests assert that the reviewed defects are present; invert them into proper expected-behavior regression tests when implementing fixes. Timing evidence is in `benchmark-baseline.txt` and `benchmark-preallocated.txt`. Tool locations and host limitations are recorded in `../../LOCAL_TOOLS.md` relative to this report's directory, or directly at `docs/LOCAL_TOOLS.md` from the repository root.
