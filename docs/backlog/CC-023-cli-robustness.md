# CC-023: CLI robustness (calaos-os)

- **Phase:** P2
- **Priority:** P2
- **Size:** S
- **Status:** Done
- **Depends on:** CC-002 (dead-code sweep already touched these files)
- **Blocks:** CC-055
- **Files owned (exclusive for this phase):**
  - `cmd/calaos-os/calaos-os.go`
  - `cmd/calaos-os/api/api.go`

## Context

The CLI client has several robustness gaps:

1. **Error checked too late in `cmdUpgrade`** (`cmd/calaos-os/calaos-os.go:165-208`): the return of `UpdatePackage`/`UpgradePackages` is stored in `err` but not checked before the status-polling loop starts (`:179`); it is only checked at `:206`, after the loop. If the POST fails the loop still spins. A shadowed `status, err :=` (`:180`) compounds the confusion.
2. **Ignored conversion error** (`:271`): `ipCIDR, _ := toCIDR(*ip, *netmask)` — an invalid IP/mask silently sends an empty CIDR to the server.
3. **No HTTP timeout** (`cmd/calaos-os/api/api.go:97`): `NewCalaosApi` is always constructed without `SetTimeout`, so `apiClient.Timeout` defaults to 0 = infinite. A hung daemon hangs the CLI forever; the upgrade poll loop (`:179-204`) has no wall-clock bound either.
4. **Undifferentiated exit codes**: every `exit(err, ...)` call passes hardcoded `1` (`:83,88,115,120,162,182,207,226,268,282,300,309`) — a token-missing failure is indistinguishable from a network failure to a calling shell script.
5. Cosmetic: table headers declare more columns than rows populate (`cmdList` 3 vs 2, `cmdCheck` 4 vs 3) — the "Source" column is permanently blank.

## Audit (re-verified against current code before implementing)

The ticket's line references predate CC-002 (dead-code sweep) and CC-014
(daemon lifecycle hardening, which also rewrote `cmdUpgrade` to be
event-driven over `/api/events` WebSocket instead of a `status, err :=`
HTTP poll loop). Re-checked each item against `main` at the time this
ticket was picked up (commit `e5b4638`):

1. **Error checked too late in `cmdUpgrade`.** The literal bug described
   (shadowed `status, err :=`, unchecked POST error before a poll loop) no
   longer exists — CC-014 replaced the poll loop with a `select` over the
   WebSocket event channel, the POST-result channel, and a grace-period
   timer. However the *spirit* of the bug was still present: on
   `postErr = <-postCh`, the code applied the same 3s grace period whether
   the POST succeeded or failed, so a daemon that rejected the upgrade
   outright (e.g. 500) still took ~3s to report the failure instead of
   erroring immediately. Fixed: `postErr != nil` now breaks the loop right
   away (no event will ever come for a request that never started), the
   grace period only applies when the POST actually succeeded.
2. **Ignored `toCIDR` error.** Still present exactly as described
   (`cmd/calaos-os/calaos-os.go`, `cmdNetConfigureStatic`). Fixed.
3. **No HTTP timeout.** Still present — `NewCalaosApi` always built an
   `http.Client{Timeout: 0}` unless a caller passed `SetTimeout`, and none
   of the CLI call sites did. Fixed. The upgrade loop also had no overall
   wall-clock bound beyond the per-event grace period; added one.
4. **Undifferentiated exit codes.** Still present, all call sites used a
   hardcoded `1`. Fixed.
5. **Table header/row column mismatch.** `cmdList` (3 headers / 3 populated
   fields) was already fixed by CC-002 — no change needed there. `cmdCheck`
   still declared a 4th "Source" header with only 3 populated fields; fixed
   by dropping the column (no `Source` field exists on `structs.Image`, and
   that struct is outside this ticket's owned files / CC-040's envelope
   scope).

Also noted per CC-002/out-of-scope: `UpdateImages` / the `cmdList` images
branch were already removed; `list` already uses `UpdateAvailable`.

## Work items

- [x] Check the POST error immediately in `cmdUpgrade` and abort before entering the poll loop; remove the shadowing. (Shadowing was already gone via CC-014's rewrite; the remaining gap — a fixed grace period applied even to outright POST failures — is fixed.)
- [x] Handle `toCIDR` error (`:271`) — abort with a clear message on invalid IP/netmask.
- [x] Set a default `http.Client` timeout for non-streaming calls (e.g. 30s) in `NewCalaosApi` / the request path (`api/api.go`); bound the upgrade poll loop with an overall deadline.
- [x] Assign distinct exit codes by failure class (config/token error, network error, server error, usage error) instead of a blanket `1`.
- [x] Fix the table headers to match populated columns (drop the blank "Source" column, or populate it).

## Out of scope

- The response-envelope / API-contract alignment (the CLI must parse the new envelope) — owned by CC-040, which updates BOTH server and client together. Do NOT change how responses are parsed here beyond what already exists; leave envelope parsing to CC-040.
- `UpdateImages` / `cmdList` images branch — already removed in CC-002.
- Duplicated token-read / color constants — CC-002 handled constants; token-path unification is not in scope.

## Breaking changes

CLI exit codes change from always-1 to differentiated codes. Scripts that only check `!= 0` are unaffected; scripts asserting `== 1` may need updating (documented in `docs/API.md` via CC-040, or a short note in the CLI `--help`).

## Acceptance criteria

- [x] `calaos-os upgrade <pkg>` against a daemon that 500s on the POST exits immediately with an error (does not poll).
- [x] `calaos-os network configure static ...` fails clearly instead of sending an empty CIDR. (The CLI's actual arg shape is positional — `network configure static INTERFACE IPV4 NETMASK [GATEWAY] [DNS...]`, no `--ip` flag exists — tested with a bogus positional `IPV4`/`NETMASK`.)
- [x] `calaos-os list` against an unreachable host times out within the configured timeout, not indefinitely.
- [x] Distinct exit codes observable per failure class.
- [x] `cd cmd/calaos-os && go vet ./...` green.

## Verification notes

All verification below was run in a fresh container from
`ghcr.io/calaos/debian_deb_build_base:latest` (matching CI's
`.github/workflows/ci.yml` job), with the Go 1.26.5 toolchain baked into
the image at `/usr/local/go/bin` put ahead of Debian's `golang-go` (1.19)
on `PATH`, as CI does. Container/host artifacts were chowned back to the
host uid/gid before the container was removed.

**Build / vet / lint / test**
- `make build-lib` — apt C++ lib builds and installs cleanly.
- `go build ./...` (server) and `cd cmd/calaos-os && go build .` (CLI) — both clean.
- `go vet ./...` at repo root and in `cmd/calaos-os` — no issues.
- `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` (v2.12.2) — **0 issues**.
  The grandfathered exclusions in `.golangci.yml` for `cmd/calaos-os/api/api.go`
  (errcheck `resp.Body.Close`, gosec `G402`, unused `call is unused`) and
  `cmd/calaos-os/calaos-os.go` (gosec `G101`) are all still needed: none of
  this ticket's changes touched the `InsecureSkipVerify` option, the unused
  `call` helper, the unchecked `resp.Body.Close()` in `makeHttpCall`, or the
  `TOKEN_FILE` constant that trips `G101`. Per instructions, `.golangci.yml`
  was left untouched.
- `make test` (`go test -race ./...`) — all packages pass, including two new
  test files: `cmd/calaos-os/calaos-os_test.go` (`toCIDR`, `classifyAPIErr`,
  exit-code distinctness) and additions to `cmd/calaos-os/api/api_test.go`
  (`NewCalaosApi` default-timeout wiring + override, and a hung-server test
  proving a call actually aborts within the configured timeout rather than
  hanging).

**Runtime (daemon on port 8000 inside container, container published as host
127.0.0.1:8023, token provisioned at `/run/calaos/calaos-ct.token`)**
- `calaos-os list` / `check-update` / `network list` against the live daemon:
  correct output; `check-update`'s table now shows exactly 3 columns (no
  blank "Source"); `network list` surfaced a daemon-side `networkctl` exec
  failure (expected — no systemd-networkd in this minimal container) as
  exit code **4** (server error), confirming daemon-reached-but-failed is
  classified correctly.
- `calaos-os upgrade nonexistent-package-xyz` against the live daemon: POST
  rejected with `"package not found"`; CLI exited in **~8ms** with exit code
  **4** — confirms it no longer waits out the old 3s grace period on a
  rejected upgrade (item 1) and exit codes are differentiated (item 4).
- `calaos-os network configure static eth0 bogus-ip 255.255.255.0` and
  `... eth0 192.168.1.10 bogus-mask`: both fail immediately with
  `invalid ip : ...` / `invalid mask : ...` and exit code **1** (usage),
  never reaching the network — confirms item 2.
- Token file removed: `calaos-os list` exits with `unable to read token: ...`
  and exit code **2** (config).
- Daemon killed (down-daemon scenario): `calaos-os list`, `check-update`,
  and `upgrade somepkg` (EventsWS dial failure path) all fail with
  `connection refused` in ~6-8ms and exit code **3** (network) — confirms
  the CLI fails fast rather than hanging against a down daemon, and that
  `EventsWS`'s dial error is classified as a network error too.
- Deterministic timeout proof (not relying on external network black-hole
  behavior inside the sandbox): a throwaway test pointed `NewCalaosApi` at
  a private, unroutable address with `SetTimeout(3*time.Second)`; the call
  blocked for exactly ~3.00s then returned `context deadline exceeded
  (Client.Timeout exceeded while awaiting headers)` — confirms the default
  timeout (30s in production, overridden here for a fast test) actually
  bounds the call. This throwaway test was run and discarded, not committed;
  the committed, fast/deterministic equivalent is
  `TestCallTimesOutOnHungServer` in `cmd/calaos-os/api/api_test.go`.

Exit code scheme added to `cmd/calaos-os/calaos-os.go`: `ExitUsage=1`,
`ExitConfig=2`, `ExitNetwork=3`, `ExitServer=4` — mirrors the existing
`exitUsage/exitConfig/exitAppInit/exitModelsInit/exitRuntime` pattern already
used by the daemon's own `main.go`.
