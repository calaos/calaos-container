# CC-014: Server & daemon lifecycle hardening

- **Phase:** P1
- **Priority:** P0
- **Size:** M
- **Status:** Done
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

- [x] `config/config.go`: add defaults `general.address = "127.0.0.1"` and `general.update_time = "12h"`. **Make config load/parse errors fatal** (return them from `InitConfig` so `main.go:74-76` exits) rather than logging and continuing. Never allow a fallback to an empty bind address; if `general.address` ends up empty, treat it as an error or force `127.0.0.1`.
- [x] Clean up the dual JSON+TOML parse: the file is TOML — parse it as TOML and surface the real error (keep JSON only if there is a demonstrated reason; otherwise drop it).
- [x] `main.go`: fix `exit()` so the requested exit code actually propagates — print the error (colored) then `cli.Exit(code)` without calling the `Fatalln` that pre-empts it. Give distinct codes at the call sites (config vs app-init vs models-init vs run).
- [x] `app/app.go`: `Start()` should surface the listener error to the main goroutine via a channel/error return consumed alongside `handleSignals` (select on signal vs listener-error), instead of `Fatalf` in the goroutine.
- [x] Fiber config: reduce `BodyLimit` to 1 MiB (`maxFileSize` const); add a sane `IdleTimeout`; keep `ReadTimeout`. Make the CORS `AllowOrigins` derive from `general.address` (or a new optional config key) instead of a hardcoded literal.

## Out of scope

- `models/update.go` `update_time` parsing/loop — CC-010 (this ticket only provides the default value in config).
- Central error handler / response envelope — CC-040.
- Auth middleware — CC-011.
- systemd `Restart=`/`TimeoutStopSec=` — CC-042 (this ticket makes graceful shutdown reliable so those become meaningful).

## Breaking changes

- A malformed or missing config file now causes the daemon to **exit with an error** instead of starting on 0.0.0.0. This is the intended security fix; deployment must ship a valid `/etc/calaos-container.toml`.
- `BodyLimit` drops from 1 GiB to 1 MiB — no endpoint accepts a large body (install streams *out*, not in).

## Acceptance criteria

- [x] Start with no config file → daemon exits non-zero with a clear message; it never binds `0.0.0.0` (verify with `ss -ltnp`).
- [x] Start with a minimal config lacking `update_time` → uses the 12h default, starts normally.
- [x] Distinct non-zero exit codes for config-error vs listen-error (documented in the ticket's verification notes).
- [x] `kill -TERM` triggers a clean shutdown with no `Fatalf` path.
- [x] `curl --data-binary @2MB.bin ...` is rejected by the body limit.
- [x] `go vet` green.

## Verification notes

### Exit codes (`main.go`)

`exit()` no longer calls `logging.Fatalln` (which `os.Exit(1)`s before `cli.Exit`); it
logs with `Errorln` then `cli.Exit(code)`, whose panic mow.cli's flow recovers into
`os.Exit(code)`. The outer `a.Run()` error path calls `os.Exit` directly, because
outside the cli action that panic would not be recovered.

| Code | Constant         | Meaning                                                   |
| ---- | ---------------- | --------------------------------------------------------- |
| 0    | —                | clean shutdown (SIGINT/SIGTERM)                            |
| 1    | `exitUsage`      | invalid command line (`a.Run` error, outside the action)   |
| 2    | `exitConfig`     | config file missing, unparsable or invalid                 |
| 3    | `exitAppInit`    | `app.NewApp()` failed                                      |
| 4    | `exitModelsInit` | `models.Init()` failed                                     |
| 5    | `exitRuntime`    | the HTTP listener failed while running                     |

### Design decisions

- **TOML only.** The file is TOML (`calaos-container.toml`, `/etc/calaos-container.toml`);
  parsing it as JSON *and* TOML only reported an error when *both* parsers failed, which
  hid every real syntax error. Dropping the JSON parser makes the TOML error the message
  the operator sees: `failed to load config file /tmp/bad.toml: (1, 2): unexpected token
  unclosed table key, was expecting a table key`.
- **Config is committed atomically.** `InitConfig` loads defaults → TOML file → `CALAOS_*`
  env into a *fresh* koanf instance and only assigns it to `config.Config` after
  `validate()` passes, so a failed load can never leave a half-configured (empty address)
  daemon behind. `validate()` rejects an empty address, an address containing whitespace,
  and a port outside 1-65535.
- **Listener errors travel by channel.** `Start()` returns `<-chan error`; the goroutine
  sends the `Listen` failure and closes the channel. `handleSignals(listenErr)` selects on
  signal vs listener error, runs `myApp.Shutdown()` + `models.Shutdown()` in *both* cases
  and returns the error, so a listen failure still gets a graceful shutdown and then exits
  with code 5 (previously `Fatalf` in the goroutine skipped `Shutdown` entirely).
- **CORS origins are derived**, not hardcoded: `general.cors_origins` (new optional key)
  wins if set, otherwise the list is `http://<address>,http://<address>:<port>` — the bare
  origin keeps a UI served on port 80 working (the old literal), the second allows the API's
  own origin. IPv6 literals are bracketed. Origins are validated the same way fiber's cors
  middleware does, because that middleware *panics* on a malformed origin; an operator typo
  therefore becomes exit code 3 instead of a panic.
- **Fiber**: `BodyLimit` 1 GiB → 1 MiB (`maxBodySize`), `ReadTimeout` 20s kept, added
  `IdleTimeout` 75s (hijacked WebSocket connections on `/api/events` are unaffected).

### Build / static checks

Inside `ghcr.io/calaos/debian_deb_build_base:latest` with the worktree bind-mounted:
`apt-get build-dep -y .`, `make build-lib`, `go build ./...`, `go vet ./...`,
`golangci-lint run` (v2.12.2, `CGO_LDFLAGS="-L./bin -lcalaos-apt"`) → **0 issues**, and
`make test` (`go test -race ./...`) → all packages ok.

New unit tests: `config/config_test.go` (defaults incl. the 12h `update_time` and the
`127.0.0.1` address, missing file, malformed TOML, empty address, whitespace address, port
range, env override) and `app/cors_test.go` (origin derivation, IPv6, override, plus a test
asserting fiber's cors middleware accepts every origin we generate).

### Runtime acceptance (daemon run in a container, port 8014)

| Scenario                                     | Result                                                                        |
| -------------------------------------------- | ----------------------------------------------------------------------------- |
| No config file (`-c /etc/does-not-exist.toml`) | exit **2**, `✘ failed to load config file …: no such file or directory`; `ss -ltn` shows **no** listener, wildcard or otherwise |
| Malformed TOML                                | exit **2**, real TOML parser error reported                                    |
| `address = ""`                                | exit **2**, `general.address is empty; an empty address listens on all interfaces …` |
| Minimal config (`[general] port = 8014`, no `update_time`) | starts, listens on `127.0.0.1:8014` only, `GET /api/system/info` → 401 (auth), update loop runs with the 12h default |
| Port already in use                           | exit **5**, `✘ http server failed to listen on 127.0.0.1:8014: … address already in use`, after a normal shutdown |
| `curl --data-binary @2MB.bin -X POST /api/system/install/start` | **413** (1 KiB body on the same endpoint → 401, i.e. only the size is rejected) |
| `kill -TERM`                                  | `Received signal terminated - shutting down...`, exit **0**, port released, no `Fatalf` |
