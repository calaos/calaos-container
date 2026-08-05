# CC-022: System info correctness

- **Phase:** P2
- **Priority:** P2
- **Size:** S
- **Status:** Done
- **Depends on:** P1 merged
- **Blocks:** CC-054
- **Files owned (exclusive for this phase):**
  - `models/system.go`

## Context

`SystemInfo` reporting is quietly wrong:

1. **CPU usage includes idle time.** In `getCPUUsagePercent` (`models/system.go:66-70`) the loop sums all `/proc/stat` fields into `total` but also counts the idle column into `used` (the `i < 4` window includes idle), so the reported "usage" always contains idle time — the percentage is meaningless.
2. **Swallowed parse errors.** `strconv.Atoi` errors are discarded with `_` (`:67,:97,:99`); a non-numeric `/proc` field silently yields 0 and skews the result.
3. **Inconsistent error contract.** `GetSystemInfo` (`:17-45`) guards each sub-call with `if err == nil` but returns the named `err`, which ends up as whatever the last sub-call set. It can return partially-populated data with a nil error, or a non-nil error with some fields filled.

## Work items

- [x] Fix the CPU calculation: `used = total - idle` (exclude `idle` and ideally `iowait`) per the standard `/proc/stat` delta method. A single-sample ratio is acceptable if that is the current behavior, but it must not count idle as used; document the method in a comment.
- [x] Handle every `strconv.Atoi` error (`:67,:97,:99`): on parse failure, return an error rather than silently using 0.
- [x] Make `GetSystemInfo` a single clear contract: either it fully succeeds (all fields valid, nil error) or it returns a non-nil error; do not return half-populated structs with a nil error. Pick and document.

## Out of scope

- Response envelope for `GET /system/info` — CC-040.
- Any other `models/*.go` file.

## Breaking changes

None (same JSON fields; values become correct). Consumers that displayed the bogus CPU% will now see accurate numbers.

## Acceptance criteria

- [x] On an idle host, reported CPU usage is near 0%, not ~100% (was: inflated by idle).
- [x] A malformed `/proc` line causes a returned error, not a silent 0.
- [x] `GetSystemInfo` never returns partial data with a nil error (covered by CC-054 fixtures).
- [x] `go vet` green.

## Verification notes

**Audit vs. ticket claims:** the exact defect described in item 1 ("`i < 4` window includes idle") did not literally match the code at HEAD (`i < 4` with `i` starting at 1 actually captured `user, nice, system` — idle at index 4 was already excluded from `used`). However the calculation was still wrong in the ways that matter: `used` silently dropped `irq`/`softirq`/`steal`/`guest` (undercounting usage) and, more importantly, none of the `strconv.Atoi` calls checked their error (item 2), and `GetSystemInfo`'s named-return contract (item 3) could return a partially-populated `*SystemInfo` with a nil `err` if a later sub-call's `err` happened to be nil while an earlier field write had failed. All three work items were implemented per the ticket's intent regardless of the drifted line numbers.

**Callers checked:** `git grep` found exactly one caller of `GetSystemInfo` — `app/system.go:apiSystemInfo` (route `GET /api/system/info`, `app/app.go:128`) — which already does `if err != nil { return 500 }`, so tightening the contract to nil-error-implies-fully-populated is safe and required no caller changes. No other file references `getCPUUsagePercent`/`getMemoryUsagePercent`/`getUptime` (unexported, package-local).

**Implementation:**
- `getCPUUsagePercent`/`parseCPUUsagePercent`/`cpuUsageFromFields`: parses the aggregate `cpu` line, `total` = sum of all fields, `used = total - idle - iowait` (iowait optional for older kernels lacking the field), every `strconv.Atoi` error is propagated, all-zero or too-short input now returns an error instead of a meaningless `0`. Method documented in the function's doc comment, including the explicit caveat that this is a single-sample cumulative-since-boot ratio, not a delta-based instantaneous reading (ticket explicitly allows this).
- `getMemoryUsagePercent`/`parseMemoryUsagePercent`: same treatment — `strconv.Atoi` errors propagated, missing `MemTotal`/`MemAvailable` lines now return an error instead of silently computing from a zero.
- `GetSystemInfo`: rewritten to unnamed returns; each sub-call error now returns `nil, fmt.Errorf(...)` immediately, and the success path only returns once every field is populated. Contract documented in its doc comment.

**Tests added:** `models/system_test.go`, table-driven, covering `parseCPUUsagePercent`, `cpuUsageFromFields`, and `parseMemoryUsagePercent` — idle/busy hosts, idle/iowait correctly excluded from "used" while irq/softirq/steal count towards it, missing `cpu`/`MemTotal`/`MemAvailable` lines, malformed numeric fields, all-zero input, and old-kernel `/proc/stat` lines missing `iowait`.

**Verification (in `ghcr.io/calaos/debian_deb_build_base:latest`, container `cc022-build`, matching CI's `build-vet-lint-test` job):**
- `apt-get build-dep -y .` then `make build-lib` — apt shared lib built and installed successfully.
- `go build ./...` — success.
- `go vet ./...` — clean, no issues.
- `golangci-lint run ./...` (v2.12.2, `CGO_LDFLAGS="-L./bin -lcalaos-apt"`) — `0 issues`.
- `make test` (`go test -race ./...`) — all packages pass, including the new `models` table-driven tests.
- Ran the daemon on port 8022 (`address = "127.0.0.1"`, `port = 8022`) with a token file at `/run/calaos/calaos-ct.token`, then called `GET /api/system/info` with `Authorization: Bearer <token>`:
  - Response: `{"error":false,"msg":"ok","output":{"hostname":"...","uptime":20697,"cpu_usage":3,"mem_usage":22}}`.
  - Cross-checked against the container's real `/proc/stat`/`/proc/meminfo` at request time (`cpu  2517631 12 502326 96159359 34781 0 95412 0 0 0`): total=99,309,521, idle+iowait=96,194,140, used=3,115,381 → `3,115,381*100/99,309,521 = 3` (int truncation) — matches the reported `cpu_usage` exactly, confirming the fixed formula and demonstrating the "idle host → near-0%, not ~100%" acceptance criterion on real data.
  - Also confirmed requests without a token (`401 unauthorized`) and with a wrong token (`{"error":true,"msg":"wrong token"}`) are still rejected — unrelated to this change but confirms nothing in the auth path regressed.
- Malformed-input → error (not silent 0) is exercised by the unit tests above (can't easily corrupt live `/proc` files in the container); full end-to-end fixture-based coverage of `GetSystemInfo`'s all-or-nothing contract is explicitly deferred to CC-054 per the ticket.
