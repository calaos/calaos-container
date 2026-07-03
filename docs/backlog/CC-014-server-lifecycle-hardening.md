# CC-014: Server & daemon lifecycle hardening

- **Phase:** P1
- **Priority:** P0
- **Size:** M
- **Status:** Backlog
- **Depends on:** P0 merged
- **Blocks:** CC-042, CC-053
- **Files owned (exclusive for this phase):**
  - `app/app.go`
  - `main.go`
  - `config/config.go`

## Context

Several startup/lifecycle defects combine into a "silently exposed or silently dead" daemon:

1. **Binds 0.0.0.0 on config failure.** `general.address` has a compiled-in default of `""` (`config/config.go:24`), which fasthttp binds as **all interfaces**. `calaos-container.toml:4` overrides it to `127.0.0.1`, but config load/parse errors are only **logged, not fatal** (`config/config.go:53-56`), so a missing/corrupt config silently falls back to `""` → the control API (reboot, disk-wipe, network config) is reachable from the LAN.
2. **`update_time` has no default** (`config/config.go:22-26` defines only port/address/log.default), which is what triggers the goroutine `Fatalf` addressed in CC-010. Provide a default here so a minimal config is viable.
3. **Config parsed as both JSON and TOML** (`config/config.go:48,51`) with errors surfaced only if both parsers fail — noisy-by-design and swallows single-parser errors.
4. **`exit()` is broken** (`main.go:44-47`): `logging.Fatalln` already calls `os.Exit(1)`, so `cli.Exit(exit)` on `:46` is unreachable and every caller's exit code (the `1`s at `:75,79,83,92`) is meaningless — the process always exits 1.
5. **`Fatalf` in the listener goroutine** (`app/app.go:144`): any `Listen` error `os.Exit`s, skipping `Shutdown`.
6. **Fiber config:** `BodyLimit = 1 GiB` (`app/app.go:18,46`) lets a single request force a huge allocation; only `ReadTimeout` is set (`:43`) — no `IdleTimeout`; CORS origin is a hardcoded literal `http://127.0.0.1` (`:55`) that can diverge from the configured address.

## Work items

- [ ] `config/config.go`: add defaults `general.address = "127.0.0.1"` and `general.update_time = "12h"`. **Make config load/parse errors fatal** (return them from `InitConfig` so `main.go:74-76` exits) rather than logging and continuing. Never allow a fallback to an empty bind address; if `general.address` ends up empty, treat it as an error or force `127.0.0.1`.
- [ ] Clean up the dual JSON+TOML parse: the file is TOML — parse it as TOML and surface the real error (keep JSON only if there is a demonstrated reason; otherwise drop it).
- [ ] `main.go`: fix `exit()` so the requested exit code actually propagates — print the error (colored) then `cli.Exit(code)` without calling the `Fatalln` that pre-empts it. Give distinct codes at the call sites (config vs app-init vs models-init vs run).
- [ ] `app/app.go`: `Start()` should surface the listener error to the main goroutine via a channel/error return consumed alongside `handleSignals` (select on signal vs listener-error), instead of `Fatalf` in the goroutine.
- [ ] Fiber config: reduce `BodyLimit` to 1 MiB (`maxFileSize` const); add a sane `IdleTimeout`; keep `ReadTimeout`. Make the CORS `AllowOrigins` derive from `general.address` (or a new optional config key) instead of a hardcoded literal.

## Out of scope

- `models/update.go` `update_time` parsing/loop — CC-010 (this ticket only provides the default value in config).
- Central error handler / response envelope — CC-040.
- Auth middleware — CC-011.
- systemd `Restart=`/`TimeoutStopSec=` — CC-042 (this ticket makes graceful shutdown reliable so those become meaningful).

## Breaking changes

- A malformed or missing config file now causes the daemon to **exit with an error** instead of starting on 0.0.0.0. This is the intended security fix; deployment must ship a valid `/etc/calaos-container.toml`.
- `BodyLimit` drops from 1 GiB to 1 MiB — no endpoint accepts a large body (install streams *out*, not in).

## Acceptance criteria

- [ ] Start with no config file → daemon exits non-zero with a clear message; it never binds `0.0.0.0` (verify with `ss -ltnp`).
- [ ] Start with a minimal config lacking `update_time` → uses the 12h default, starts normally.
- [ ] Distinct non-zero exit codes for config-error vs listen-error (documented in the ticket's verification notes).
- [ ] `kill -TERM` triggers a clean shutdown with no `Fatalf` path.
- [ ] `curl --data-binary @2MB.bin ...` is rejected by the body limit.
- [ ] `go vet` green.

## Verification notes

_(filled by the implementing agent)_
