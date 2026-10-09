# Remediation implementation and evidence

Date: 2026-10-09. Baseline: `335dfe2`. The authorized fix plan is implemented in
the working tree. No deployment or host web/PHP configuration was changed.
The [original review](../reviews/2026-10-09/README.md) remains historical evidence.

## Finding resolution

All twelve reviewed defects have code changes and regression coverage. Incident
reproduction and compatible-runner race validation remain separate external checks.

| Finding | Implementation | Verification |
|---|---|---|
| 1. Snapshot churn / forced memory limit | Bounded top-K selection, selected-only URL labels, sorting after unlocking, immutable generation/filter cache; native `GOMEMLIMIT`; retire inactive rate aggregates | `store/topk_test.go`, `cache_test.go`, rate-decay checks, matched workloads and benchmarks below |
| 2. Repeated full slowlog scans | Shared per-path descriptor/offset/partial-line reader; start at EOF, bounded incremental reads, attributed counters | `fpm/poller_test.go`: partial append, repeat poll, shared pools, rename/truncate; 64 MiB idle benchmark |
| 3. Permanent rejection at capacity | Bounded LRU admission for URLs/clients; stale rows reclaimed on saturated admission; lifetime totals survive | `store/filters_test.go`: admission, hot-row preservation, expiry and totals; retention soak |
| 4. Partial-line seed loss / inode handoff | Seed retains descriptor and resumes before incomplete record; live reader drains old inode before replacement | `logsrc/regression_test.go`, `filetail/reader_test.go`; main seed/live count conservation |
| 5. Extensionless logs replayed once | Recognize actual rotation/gzip suffixes; other regular paths live, including empty/missing literal paths | Extensionless/empty source tests, creation test, initial/rescan vhost-pin test |
| 6. PHP contaminates web totals | Typed service records, separate ingestion owner and pool metrics; memory bytes never enter network totals | `main_test.go`: mixed web/PHP produces one web request with 100 bytes, separate PHP duration/memory and bad-line counts |
| 7. FPM stalls freeze UI | Asynchronous initial probes; collect outside publication lock; four workers, shared deadline, cancellation and deep copied views | Controlled FastCGI fixtures; stalled socket leaves `Views` responsive and `Stop` cancels it |
| 8. Incorrect filter intersections | Sparse joint status/bot/static metrics, including host/client associations; documented panel contract; global totals unfiltered | Independent full-record evaluator over 1,152 combinations, rates/seed tests, UI scope/selection checks |
| 9. Seed buffers retained by small fields | Clone retained seed lines and truncated UA strings; bounded seed concurrency/read budgets | Retained-heap profile and churn plateau below; zero seed, gzip blank-byte limit and error regressions |
| 10. Included config never invalidates cache | Versioned atomic cache, content/dependency fingerprint, include match membership, symlink target and five-minute expiry | Both server include edit/add/remove fixtures, symlink/expiry/schema checks, disabled-cache test |
| 11. RSS unit / short `/proc` fields | Convert bytes to KiB; require 22 fields and reject malformed numeric values | `fpm/proc_test.go` |
| 12. Silent invalid config / editor success | Structured read/parse/value diagnostics; optional absence distinct from explicit missing path; direct editor validation; zero/false precedence | Config fixtures and actual CLI subprocess tests for show, invalid save and explicit missing config |

Additional state bounds: 2,048 hosts, 20,000 URL and client rows, 40,000 total
client/host associations and 64 associations per client. Saturation tests prove
fresh associations are admitted, old detail is removed and global totals survive.
UI eviction counters identify partial detail. Selected row identity survives
reordering; selection clamps to visible search results.

FPM access layouts use the pool's configured format and explicit units, with
diagnostics for unsupported or ambiguous layouts. Shared files require `%n` and
consistent layouts. PHP duration and memory units were verified against the
[primary PHP manual](https://www.php.net/manual/en/install.fpm.configuration.php).
Status slow requests and slowlog observations are separate counters. Per-pool RSS
is labeled an estimate. See [behavior documentation](../../README.md).

## Matched process workloads

Hardware: Raspberry Pi 5 Model B Rev 1.0, Linux/ARM64, Go 1.26.0.
Both binaries ran with `GOMAXPROCS=4`, a 160×50 PTY, the unchanged 500 ms UI refresh,
63 generated plain access files, 46 hosts and 54,000 seed records. Explicit paths,
isolated XDG directories and a nonexistent FPM discovery glob avoid host services;
the fixed binary also honors disabled cache/FPM config. `GOMEMLIMIT` was unset;
the baseline imposed its own 48 MiB limit, the fixed binary uses Go's default.

Each mode ran once per binary: approximately three seconds of startup/warmup,
then ten seconds of steady measurement. CPU is `/proc` ticks as a percentage of
one core, so 200% would mean two cores. RSS is sampled process resident memory,
not retained heap. These are short controlled comparisons, not confidence intervals
or a reconstruction of the original incident. Raw values: [workloads.json](workloads.json).

| Mode | Baseline CPU | Fixed CPU | Baseline peak RSS | Fixed peak RSS | Baseline / fixed final-marker lag |
|---|---:|---:|---:|---:|---:|
| Idle | 16.39% | 2.80% | 53.2 MiB | 72.3 MiB | 0.326 / 0.509 s |
| 100 requests/s | 18.08% | 4.70% | 53.1 MiB | 76.8 MiB | 0.390 / 0.711 s |
| 1,000 requests/s | 28.98% | 7.40% | 54.4 MiB | 77.8 MiB | 0.759 / 0.280 s |

The live runs emitted 999 records each; burst runs emitted 9,997 baseline and 9,992
fixed records, reflecting the timed producer. Every run displayed its final marker
and exited normally. Marker visibility verifies progress rather than total count
conservation. The maintained end-to-end ingestion regression independently checks
1,000 seeded plus 600 live web requests and their exact byte totals; rotation,
partial-line and mixed-source tests exercise the other correctness boundaries.

The historical idle <3% and 100 requests/s <5% targets are met in these specific
runs. The old 50–60 MB RSS target is missed: correct joint metrics retain more
detail, and removing the forced small memory limit gives GC more headroom. Lowering
the memory limit to force RSS down would risk restoring GC pressure.

Reproduce using [the reusable Linux PTY sampler](../../scripts/measure_workload.py):

```sh
go build -o /tmp/wstat-fixed .
python3 scripts/measure_workload.py /tmp/wstat-fixed --mode idle --output /tmp/wstat-idle
python3 scripts/measure_workload.py /tmp/wstat-fixed --mode live --output /tmp/wstat-live
python3 scripts/measure_workload.py /tmp/wstat-fixed --mode burst --output /tmp/wstat-burst
```

The sampler creates disposable logs/config, samples only its child and retains JSON
and terminal evidence under `--output`. Its cleanup signals only that owned child.
The baseline binary was built from an isolated archive of `335dfe2`; no production
process was sampled, throttled or terminated.

## Snapshot, slowlog and retention measurements

Deterministic snapshot fixture: 54,000 records, 46 hosts, 20,000 retained URL rows,
11,331 clients, top 200. Three repeats, `GOMAXPROCS=4`.

| Operation | Baseline | Fixed |
|---|---|---|
| Forced rebuild including flush | 25.92–29.24 ms; ~23.38 MB, 20,060 allocations | 8.21–8.44 ms; 107,320 bytes, 215 allocations |
| Unchanged seeded snapshot cache hit | Baseline rebuilt each time | 163–172 ns, zero reported allocations |
| Idle poll on a 64 MiB slowlog | Baseline implementation rescanned full file | 2.49–2.52 µs, 432–448 bytes, three allocations |

Rebuild allocation drops about 99.5%. The historical snapshot <5 ms target is met
by cache hits but missed by a saturated full rebuild; filter changes and ongoing
live rate decay can still require rebuilding. The cache benchmark intentionally
exercises unchanged seeded state, not arbitrary live traffic. Rate/cache invalidation
tests verify that caching cannot freeze decaying live rates.

Baseline forced-rebuild runs use 20 iterations each; fixed runs use one-second
benchmark batches. Scope/cardinality is identical; timing ranges are observations,
not universal bounds. Allocation checks enforce a stable rebuild budget of 256 KiB
and at most one idle-cache allocation rather than wall-time assertions in CI.
Evidence: [baseline rebuild](snapshot-baseline-rebuild.txt),
[final snapshots](snapshot-final.txt), [slowlog](slowlog-final.txt).
Earlier [baseline](snapshot-baseline.txt) and [top-K-only](snapshot-topk.txt)
experiments are preserved as intermediate results.

The four-batch live-key churn diagnostic replaces 50,000 keys per batch. At
50k/100k/150k/200k requests, post-GC retained heap was respectively
29,814,200 / 29,841,800 / 29,898,712 / 29,896,632 bytes. Detail maps remained at
2,048 hosts, 20,000 URLs, 20,000 clients and 20,000 associations for this fixture.
This verifies a plateau over 200k requests, not an indefinite-duration production
soak. A separate saturation test exercises the full 40,000 association cap.
[Raw retention output](retention-final.txt).

```sh
GOMAXPROCS=4 go test ./internal/store -run '^$' -bench 'BenchmarkSnapshot($|Decay$)' -benchmem -count=3
WSTAT_DIAGNOSTICS=1 go test ./internal/store -run '^TestRetentionSoak$' -v
go test ./internal/fpm -run '^$' -bench SlowlogIdle -benchmem
```

Optional corpus replay uses `WSTAT_BENCH_CORPUS` with `BenchmarkSnapshotCorpus`.

## Profiles and validation

A separate fixed idle run used `--profile`, delaying CPU profiling until after the
three-second warmup. Its 10.57-second profile contains 320 ms of CPU samples. The
terminal style rendering accounts for 62.5% of cumulative samples; the short,
low-CPU run limits attribution precision. The shutdown retained-heap sample is
about 24.6 MiB, dominated by store aggregates and cloned strings, with no retained
256 KiB seed-buffer allocation visible among reported sites. This is sampled
evidence, not proof that every byte has been accounted for.

Persisted evidence: [CPU summary](cpu-profile-top.txt),
[heap summary](heap-profile-top.txt), [CPU profile](profile-cpu.pprof),
[heap profile](profile-heap.pprof), [profiled run](profile-result.json).
Profiles contain only generated fixture paths/records. Source-path prefixes were
normalized for publication; sample values, symbols and summaries are unchanged.

| Final check | Result |
|---|---|
| Full regression suite, including controlled FastCGI Unix sockets | Pass; [raw output](tests-final.txt) |
| Static build and version linker wiring (`wstat ci`) | Pass |
| `go vet ./...` | Pass |
| `golangci-lint run ./...` | Pass, zero issues |
| Formatting, `git diff --check`, `actionlint` | Pass |
| Overall statement coverage | 75.0% (application 37.1%, FPM 64.9%, store 97.2%) |
| Race suite on this host | Fails before test execution: unsupported VMA range, Found 47 / Supported 48 |
| Disposable real PHP-FPM E2E | Skipped; `WSTAT_FPM_E2E_ADDR` unset |

The existing [CI workflow](../../.github/workflows/ci.yml) runs the race suite on
Ubuntu. It has not been triggered or observed for this working-tree change.
[Local race failure](race-final.txt) is preserved; compatible-runner validation
remains outstanding. Local filesystem stalls are not made cancellable by network
probe cancellation. Concurrent copytruncate still has its inherent loss window.

The user's original 200% CPU invocation/process was unavailable. Known allocation,
GC and repeated slowlog defects are repaired and local improvements measured;
the original incident cannot yet be claimed resolved. Use the opt-in profile flags
and `pidstat` against the affected workload to finish that verification.

One agent performed the implementation. Runtime token telemetry was unavailable,
so the [planning token budget](../FIX_PLAN.md) is not presented as actual usage.
