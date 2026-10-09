# Quality and CPU remediation plan

Date: 2026-10-09. Baseline: revision `335dfe2`, evaluated in [the code-quality review](reviews/2026-10-09/README.md).

Status: implemented and committed by one agent. Verification and per-finding
resolution are recorded in [implementation evidence](implementation/README.md).
Ubuntu CI, including race validation, passes for implementation commit `19ddb688`.
Optional live-FPM E2E and reproduction of the original 200% CPU incident remain
external checks. Original review artifacts are preserved.

## Objectives and decisions

- Address all 12 review findings, plus unbounded host associations and inaccurate runtime/privacy documentation.
- Reduce measured allocation/GC overhead and repeated slowlog work. Confirm the reported 200% CPU cause using the affected workload before claiming it is resolved.
- Preserve request counts across normal seed/follow transitions and supported rotations. Document unavoidable copytruncate loss windows rather than promising impossible guarantees for concurrent writer/rotation races.
- Keep web-traffic totals separate from PHP service metrics. Do not invent correlation between Apache/nginx and FPM records.
- Honor Go's native `GOMEMLIMIT` setting by removing the unconditional 48 MiB override. Do not replace it with an arbitrary universal 256 MiB limit. Bound application state independently.
- Make status/bot/static intersections exact on supported aggregate dimensions. Document the applicability of method, path, and client filters to each panel; do not silently imply arbitrary exact cross-filtering without retaining the needed dimensions.
- Keep the existing 500 ms refresh initially. Lowering the refresh rate or capping CPU is not the primary repair.
- Use meaningful behavioral and performance tests. Review diagnostic tests assert existing bugs; convert them to expected-behavior assertions rather than installing them unchanged.

## Execution sequence

### 1. Establish reproducible measurements and diagnostics

**Scope:** main/CLI, store benchmarks, FPM fixtures, review evidence. Establishes validation for findings 1, 2, 7, and 9.

- Add opt-in local CPU and heap profiling to a diagnostic build or CLI flags. Handle creation errors, stop profiling on normal shutdown, and close files reliably. Keep profiling disabled by default and avoid an HTTP profiling server.
- Convert the saved corpus benchmark into a maintained benchmark with an optional corpus and a deterministic synthetic fixture. Stabilize insertion order so the 20,000-key cap does not select different records between comparisons.
- Measure startup separately from steady-state refresh: idle with populated maps, 100 requests/s, and a burst with unique URLs/clients. Record CPU, allocated bytes, GC cycles, retained heap, snapshot latency, ingestion lag, and shutdown behavior.
- Add a synthetic large slowlog and a controllable FastCGI test server with normal, stalled, and disconnected responses. No production service changes are needed.
- Capture process CPU and a profile during the user's reported high-CPU workload when it is available. This capture must not block the independently reproduced fixes below.

**Acceptance:** baseline commands, source revision, Go version, fixture sizes, GOMAXPROCS, terminal dimensions, refresh cadence, and sample durations are recorded. Profiles separate initialization from steady-state work. Profiling disabled has no file/network side effects.

### 2. Reduce snapshot allocations and remove forced GC pressure

**Scope:** `internal/store`, `internal/ui`, `main.go`. Finding 1. Depends on step 1.

- Land the measured slice preallocation improvement first, with stable ordering and existing behavior intact.
- Remove the unconditional `debug.SetMemoryLimit(48 << 20)` call; preserve environment-configured limits.
- Implement bounded top-K selection over compact immutable candidates, avoiding formatted URL keys for discarded rows. Use a deterministic tie-breaker and preserve selected row identity as sorting changes.
- Keep shared-map access synchronized. Sort/materialize copied candidates outside the ingestion lock where this reduces contention. Never retain mutable aggregate pointers for unlocked reads.
- Reuse scratch storage only with clear ownership: published UI snapshots must remain immutable until no longer referenced.
- Consider caching only after profiling the simpler changes. Any cache key/invalidation must include ingestion, filters, sort, top-N, eviction, and rate-decay ticks. Idle rate decay and the clock must continue updating.

**Acceptance:** under a matched 48 MiB comparison, target at least 70% fewer bytes allocated per snapshot than the current baseline, grounded in the measured 75% preallocation reduction. On the same host/workload, aim to reproduce the approximately 14–15 ms snapshot result before further tuning. Treat timing as a measured target, not a portable CI limit. Correct top-K ordering, all supported sorts, empty/zero-size results, filters, rate decay, and selection behavior pass tests. Concurrent ingestion remains race-free and shows no worse lag.

### 3. Repair log handoff and bound startup replay

**Scope:** `internal/logsrc`, source classification in `internal/detect`, seed configuration in main/config. Findings 4, 5, and 9. Depends on baseline fixtures; configuration integration finishes in step 7.

- Resume at the beginning of an incomplete trailing seed line, with bounded handling of lines larger than the buffer.
- Validate inode identity at the seed-to-follow handoff. If the tail library cannot accept a retained file descriptor, add an explicit handoff/retry policy and deterministic hooks to test file replacement at each boundary. Do not use file size as proof of identity.
- Centralize replay classification around recognized rotation/compression suffixes. Treat active `access_log` and explicitly configured nonstandard live filenames as live. Cover both discovery entry points.
- Detach retained seed lines or long-lived fields from the large seed buffer at a single ownership boundary. Confirm retained-memory savings before adding string interning or pooling.
- Stop backward reading once enough complete lines are collected, subject to the byte limit. Bound concurrent seeding independently of the long-lived tail goroutines.
- Define explicit zero-seed behavior: distinguish an omitted `-n` from `-n 0`, allow `seed_lines = 0`, and skip historical replay in that mode. Preserve current default history behavior when the setting is omitted.
- Enforce gzip limits using actual decompressed bytes, including blank lines; expose truncation/read errors instead of silently treating partial history as complete.
- Coordinate rescan shutdown with worker registration so `Stop` cannot race a new worker/channel send.

**Acceptance:** tests cover partial line completion, empty files, extensionless logs, same-size/larger replacement during handoff, rename/recreate, ordinary copytruncate, replay-only files, zero seed, oversized input, malformed gzip, decompression limits, and stop during rescan. No duplicate completed records in controlled handoff fixtures. Heap profiles show that small retained fields no longer pin 256 KiB seed buffers. Replay concurrency stays within its configured/internal bound.

### 4. Make FPM polling incremental and nonblocking

**Scope:** `internal/fpm/poller.go`, `doctor.go`, `fcgi.go`, UI publication. Findings 2 and 7. Depends on step 1.

- Replace full slowlog scans with per-file inode/offset/partial-line readers. Share readers when pools use the same file and attribute entries by their parsed pool name.
- For “observed since startup” counters, establish the initial EOF baseline without reading all history. Handle startup partial records, appends, rotation, truncation, missing files, and permission failures; counters must never go negative after rotation.
- Apply per-poll byte/record budgets so one backlog does not monopolize the poller; preserve unread offsets for later polls and report lag when appropriate.
- Perform filesystem and FastCGI I/O outside the publication mutex. Publish immutable snapshots with short lock holds; the UI may read the previous snapshot during a probe.
- Start polling asynchronously, use bounded probe concurrency and an overall cycle deadline, and cancel in-flight work on shutdown. Coalesce missed ticks rather than running catch-up cycles continuously.
- Avoid requesting/parsing full worker detail when the current UI consumes only summary data.

**Acceptance:** an unchanged large slowlog incurs no repeated content scan after initialization. Appended blocks are counted once; shared logs do not double-count. A stalled pool does not block `Views`, other pools, initial UI rendering, or cancellation. Use synchronization-based tests, not fragile sleep-only timing assertions. Benchmark idle and append-only polling separately.

### 5. Fix bounded-state admission and metric ownership

**Scope:** store admission, `main.go` pipeline, FPM access/proc types, Services UI. Findings 3, 6, and 11; also host/association bounds. Depends on steps 2 and 4 for integration.

- Replace unreachable eviction with an explicit bounded admission policy: expire eligible entries first; if still full, evict the least recently used row so new live keys remain visible. Avoid a complete-map scan on every novel request.
- Apply bounds to hosts and client-to-host associations as well as URL/client maps. Keep global lifetime totals independent of detail eviction. Expose evicted/dropped detail counts and clearly label partial aggregate results when a detail budget prevents exactness.
- Introduce source/metric types so FPM service events cannot enter HTTP request/traffic-byte totals. Keep FPM request count, duration, and request-memory measurements in the Services data model.
- Remove FPM-derived latency from web URL rows unless a reliable correlation source exists. Retain service latency under its actual pool/source identity.
- Interpret FPM fields using the configured access format and explicit units; do not guess units from numeric magnitude. Report unsupported layouts rather than generating misleading metrics. Verify relevant format semantics against primary PHP documentation during implementation.
- Convert process RSS bytes to KiB using 1024, correct the stat-field length check, and clearly label estimated per-pool memory attribution.

**Acceptance:** sustained traffic beyond each cap still admits new active rows, memory plateaus, and lifetime totals remain correct. Old rows are evicted deterministically under a fake clock. One PHP request logged by both layers yields one web request plus one separate service event. Request-memory bytes never affect bandwidth. Proc unit conversions and malformed stat records are tested. Mixed-source fixture tests exercise the actual main ingestion routing.

### 6. Make filter semantics and displayed metrics consistent

**Scope:** store aggregates, filters, UI, README. Finding 8. Depends on the store/type changes in steps 2 and 5.

- Write a panel-by-filter contract before changing counters: exact supported dimensions, self-panel behavior, and global-header scope. Preserve global totals as explicitly unfiltered.
- Track joint status/bot/static counts and associated bytes/errors/rate state for supported dimensions; host-filtered client aggregates must retain the same relevant detail. Include valid status values outside 2xx–5xx in the unfiltered/other bucket policy.
- Choose compact sparse joint counters or another measured representation instead of a dense cross-product of all dimensions. Include the new counters in the memory budget from step 5.
- Apply consistent predicates to hits, bytes, errors, latency, and rates wherever advertised as filtered. Do not divide unfiltered errors by filtered hits.
- Keep unsupported client/path/method cross-panel combinations visibly scoped or unavailable. Do not manufacture exactness from the 500-record stream ring.
- Add a reference evaluator over complete small fixtures and compare every supported combination against the aggregate implementation, including host+status+bot+static intersections.

**Acceptance:** the successful-bot/failed-human counterexample returns zero for bots+errors in every applicable view. Host-filtered client rows honor their other supported filters. No impossible error percentages or negative counts appear. Unit, UI, and benchmark checks include joint filters and verify the post-step-2 allocation gains are not lost without an explained tradeoff.

### 7. Correct config failures, cache invalidation, and runtime controls

**Scope:** `internal/config`, `internal/detect`, main commands, FPM startup controls. Findings 10 and 12, plus truthful documented behavior. Depends on source and metric contracts from steps 3–6.

- Return structured configuration diagnostics. Missing optional files use defaults; unreadable files, malformed TOML, invalid values, and explicitly requested invalid paths produce actionable errors. Preserve CLI/project/user/default precedence, including explicit zero and false values.
- Validate edited configuration before reporting success. Add subprocess-level main/config command tests with isolated XDG directories.
- Cache the include dependency graph and include-pattern/directory membership relevant to Apache/nginx discovery. Detect modifications, additions, removals, and symlink target changes; use bounded expiry as a fallback. Version the cache schema and treat old/corrupt entries as a cache miss.
- Keep cache writes atomic. Make `detect.cache = false` avoid both cache reads and cache writes.
- Ensure explicit vhost pins apply to initial sources and later rescans; test source paths outside default glob directories.
- Add a documented FPM enable/disable control that governs discovery, status probes, and access/slowlog monitoring consistently. Preserve the existing default unless there is a separately justified product change.
- Document local writes, optional diagnostics, and configured Unix/TCP FPM connections accurately. Explain how to disable cache writes and FPM probes, and what remains local-only/no-telemetry.

**Acceptance:** changing only an included site file or adding/removing a matched include invalidates attribution. Invalid config cannot be reported as successfully saved. Explicit zero/false settings survive precedence. Disabled cache/FPM controls produce no corresponding writes/probes. Test read-only and malformed-cache recovery paths.

### 8. Validate the combined result and prepare the review

**Scope:** all changed components, benchmarks, CI, README/CHANGELOG. Depends on steps 2–7.

- Run formatting, build, vet, lint, unit/integration tests, and actionlint if workflows change. Run race tests on a compatible CI runner; the current ARM host fails ThreadSanitizer before tests execute.
- Run FastCGI integration against controlled fixtures and, when available, a disposable real FPM instance. Do not alter the host's production pools.
- Repeat the established workloads and profiles on the same hardware/settings. Inspect ingestion lag as well as CPU so throttling or dropped data cannot look like a performance win.
- Include saturated-map idle runs, live traffic, source rotation, filter changes, many files, growing slowlogs, failing pools, and shutdown while background work is active.
- Reassess the historical `PLAN.md` targets of idle CPU below 3%, CPU below 5% at 100 req/s, snapshot below 5 ms, and RSS around 50–60 MB. These are existing targets, not verified current outcomes. Evaluate them only against a specified fixture/hardware/terminal configuration; record misses and tradeoffs rather than silently changing targets or forcing memory pressure to meet RSS.
- Require a plateau in retained memory under bounded high-cardinality traffic and no continuing idle slowlog scans. Set stable allocation-based benchmark checks; avoid universal wall-time assertions on shared CI machines.
- Update the review with before/after evidence and resolution status for every finding. Update changelog and behavior docs, including bounded-detail semantics and any visible changes to FPM latency placement.

**Acceptance:** all applicable correctness checks pass; race status is proven on compatible CI or explicitly outstanding. Each finding links to its fix and verification. Claim the reported 200% CPU incident resolved only if the affected workload has been profiled and rechecked successfully. Otherwise report verified local improvements and leave that final incident verification open.

## Change boundaries and dependencies

Use one reviewable commit/change per numbered step where practical; split step 5 into bounded-state and FPM-routing changes if the diff becomes difficult to review. Each behavior fix includes its focused regression checks. Steps 3 and 4 are logically independent, but execution remains sequential with one agent. Avoid introducing the speculative single-owner store rewrite or additional worker pools from the historical roadmap unless profiling demonstrates a need.

Start with steps 1–2 for measurable CPU relief. The first CPU-focused checkpoint is after step 4; the full correctness checkpoint is after step 7. Step 8 verifies the integrated result rather than repeating every expensive check after unrelated documentation edits.

## AI-agent token budget

Planning estimate for the complete implementation and validation workflow, not usage already incurred: **120,000–240,000 processed tokens**, medium-low confidence. No monetary estimate or model throughput assumption is included.

Measured scope: the affected application/store/logsrc/FPM/config/detect/UI Go files and tests total **174,300 bytes**. A rough 3–5 source bytes/token conversion suggests **35,000–58,000 source tokens** for one complete read; this is a source-size approximation, not measured tokenizer usage. Reuse the existing review and read focused sections rather than reinserting the complete source for every change. The synthetic workload and regression tests are generated once and reused. Deterministic builds, tests, and corpus replay are tool execution, not a separate generation pass per file or record.

The total counts these categories across the single-agent workflow:

| Counted category | Estimated tokens |
|---|---:|
| Input/context: source, instructions, prior context replay, focused rereads, and consumed tool results | 80,000–150,000 |
| Generated code/tests/docs, tool-call arguments, and user-facing content | 20,000–40,000 |
| Reasoning tokens, where exposed by telemetry | 20,000–50,000 |
| **Total** | **120,000–240,000** |

Cached input is included in input/context; no cache-hit rate or discount is assumed. Tool output is counted when consumed as model input, not again as generated assistant output. Totals are not an output-only or exact billed-token claim. Hidden reasoning and repeated context can only be calibrated if runtime usage telemetry is available.

The same total is allocated across batches below; these are allocations of the total, not additional tokens:

| Batch | Processed-token allowance |
|---|---:|
| 1. Baselines and diagnostics | 8,000–16,000 |
| 2. Snapshot/GC improvements | 12,000–24,000 |
| 3. Log handoff/replay | 14,000–28,000 |
| 4. Incremental/nonblocking FPM polling | 12,000–24,000 |
| 5. Bounded state and metric ownership | 18,000–36,000 |
| 6. Filter correctness | 24,000–48,000 |
| 7. Configuration/cache/runtime controls | 14,000–28,000 |
| 8. Integrated checks, evidence, and review | 18,000–36,000 |

Assumptions: one implementation pass plus one focused review pass per batch; one corrective retry for straightforward batches and up to two for log handoff/filter/counter integration. The upper end allows a substantially revised counter representation or a more involved tail-library handoff, but not a wholesale architecture rewrite. After steps 1–2, compare actual telemetry with these allowances and update the remaining range; if usage is unavailable, report that and calibrate only scope/complexity, not invented token counts.

## External dependencies and unresolved evidence

- Reproducing the user's 200% CPU observation needs the original invocation, source cardinality, startup/steady-state timing, and an affected process or equivalent workload. Work on known defects can proceed independently.
- Race validation requires a compatible runner. Access to CI and queue time are external conditions, not token estimates.
- Live FPM E2E requires a disposable instance; fake FastCGI fixtures cover most implementation work without one.
- A soak long enough to cover repeated rotations, expiry, and sustained key churn is elapsed tool/runtime work. Its duration is separate from token consumption; sample/report output can be kept compact.
- Implementation and subsequent commit, push and release preparation were authorized by the user. No production configuration changes or extra agent execution were performed. Token usage telemetry was unavailable; the budget above remains a planning estimate, not measured usage.
