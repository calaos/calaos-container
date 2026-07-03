# CC-023: CLI robustness (calaos-os)

- **Phase:** P2
- **Priority:** P2
- **Size:** S
- **Status:** Backlog
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

## Work items

- [ ] Check the POST error immediately in `cmdUpgrade` and abort before entering the poll loop; remove the shadowing.
- [ ] Handle `toCIDR` error (`:271`) — abort with a clear message on invalid IP/netmask.
- [ ] Set a default `http.Client` timeout for non-streaming calls (e.g. 30s) in `NewCalaosApi` / the request path (`api/api.go`); bound the upgrade poll loop with an overall deadline.
- [ ] Assign distinct exit codes by failure class (config/token error, network error, server error, usage error) instead of a blanket `1`.
- [ ] Fix the table headers to match populated columns (drop the blank "Source" column, or populate it).

## Out of scope

- The response-envelope / API-contract alignment (the CLI must parse the new envelope) — owned by CC-040, which updates BOTH server and client together. Do NOT change how responses are parsed here beyond what already exists; leave envelope parsing to CC-040.
- `UpdateImages` / `cmdList` images branch — already removed in CC-002.
- Duplicated token-read / color constants — CC-002 handled constants; token-path unification is not in scope.

## Breaking changes

CLI exit codes change from always-1 to differentiated codes. Scripts that only check `!= 0` are unaffected; scripts asserting `== 1` may need updating (documented in `docs/API.md` via CC-040, or a short note in the CLI `--help`).

## Acceptance criteria

- [ ] `calaos-os upgrade <pkg>` against a daemon that 500s on the POST exits immediately with an error (does not poll).
- [ ] `calaos-os network configure static --ip bogus ...` fails clearly instead of sending an empty CIDR.
- [ ] `calaos-os list` against an unreachable host times out within the configured timeout, not indefinitely.
- [ ] Distinct exit codes observable per failure class.
- [ ] `cd cmd/calaos-os && go vet ./...` green.

## Verification notes

_(filled by the implementing agent)_
