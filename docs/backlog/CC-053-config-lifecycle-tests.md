# CC-053: Config & lifecycle tests

- **Phase:** P5
- **Priority:** P2
- **Size:** S
- **Status:** Backlog
- **Depends on:** CC-014 (defaults + fatal-on-error config)
- **Blocks:** —
- **Files owned (exclusive for this phase):**
  - `config/config_test.go` (new)

## Context

CC-014 added defaults (`address = 127.0.0.1`, `update_time = 12h`) and made config load/parse errors fatal, closing the "silently binds 0.0.0.0" hole. This ticket pins the config-loading contract.

## Work items

- [ ] Load a valid temp TOML → assert `general.port`, `general.address`, `general.update_time` parse correctly.
- [ ] Missing config file → `InitConfig` returns an error (or the documented fatal behavior), and the resolved address is never empty/`0.0.0.0`.
- [ ] Config present but missing `update_time` → the `12h` default applies and `time.ParseDuration` succeeds.
- [ ] Malformed TOML → `InitConfig` returns an error (not logged-and-ignored).
- [ ] `CALAOS_`-prefixed env override still works (if retained).

## Out of scope

- Production code — test file only.
- The update loop's use of `update_time` — CC-050.

## Acceptance criteria

- [ ] `go test ./config/` passes.
- [ ] The "missing file must not yield an empty bind address" invariant is asserted.
- [ ] Malformed config produces a returned error.

## Verification notes

_(filled by the implementing agent)_
