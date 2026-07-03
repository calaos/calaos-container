# CC-022: System info correctness

- **Phase:** P2
- **Priority:** P2
- **Size:** S
- **Status:** Backlog
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

- [ ] Fix the CPU calculation: `used = total - idle` (exclude `idle` and ideally `iowait`) per the standard `/proc/stat` delta method. A single-sample ratio is acceptable if that is the current behavior, but it must not count idle as used; document the method in a comment.
- [ ] Handle every `strconv.Atoi` error (`:67,:97,:99`): on parse failure, return an error rather than silently using 0.
- [ ] Make `GetSystemInfo` a single clear contract: either it fully succeeds (all fields valid, nil error) or it returns a non-nil error; do not return half-populated structs with a nil error. Pick and document.

## Out of scope

- Response envelope for `GET /system/info` — CC-040.
- Any other `models/*.go` file.

## Breaking changes

None (same JSON fields; values become correct). Consumers that displayed the bogus CPU% will now see accurate numbers.

## Acceptance criteria

- [ ] On an idle host, reported CPU usage is near 0%, not ~100% (was: inflated by idle).
- [ ] A malformed `/proc` line causes a returned error, not a silent 0.
- [ ] `GetSystemInfo` never returns partial data with a nil error (covered by CC-054 fixtures).
- [ ] `go vet` green.

## Verification notes

_(filled by the implementing agent)_
