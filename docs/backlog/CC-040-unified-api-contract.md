# CC-040: Unified API contract (envelope, status codes, error sanitization) + CLI lockstep

> **Amended by the admin plan** ([`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)): legacy routes stay byte-identical for at least one release; the new contract lives under a versioned prefix; `docs/API.md` gains the admin section. Where the text below says otherwise, this note wins.

- **Phase:** P4
- **Priority:** P1
- **Size:** L
- **Status:** Done (contract in #3, implementation in the follow-up PR)
- **Depends on:** ALL of P1 and P2 (CC-010, CC-011, CC-012, CC-013, CC-014, CC-020, CC-021, CC-022, CC-023) — this ticket touches every HTTP handler and both CLI files, so it must run alone with respect to `app/` and `cmd/`
- **Blocks:** CC-051, CC-052, CC-055
- **Files owned (exclusive for this phase):**
  - `app/app.go`
  - `app/update.go`
  - `app/system.go`
  - `app/network.go`
  - `cmd/calaos-os/api/api.go`
  - `cmd/calaos-os/calaos-os.go`
  - `docs/API.md` (new)
  - `app/*_golden_test.go`, `app/testdata/golden/` (legacy golden tests)

## Context

The HTTP API is internally inconsistent, forcing the client SDK to special-case each endpoint and leaking internals to callers:

- **Two response shapes coexist.** Some handlers wrap results in `{error, msg, output}` (`app/network.go:20-24`, `app/system.go:66-70`); update handlers return the raw model JSON with no envelope (`app/update.go:19,23,36`). The client SDK special-cases this (`cmd/calaos-os/api/api.go:287-299` parses an envelope for network, `:198-199` parses raw for updates).
- **Duplicated 500 block.** The same `return c.Status(500).JSON(fiber.Map{"error":true,"msg":err.Error()})` is copy-pasted ~15× across every handler.
- **Raw error leakage.** `err.Error()` from subprocess stderr and JSON unmarshalling is reflected straight to clients throughout — information disclosure.
- **Wrong status codes.** "upgrade already in progress" → 500 (`app/update.go`, from `models` returning an error) should be **409 Conflict**. `GET /system/install/status` before the first install → **400** (`app/system.go:150-156`) for what is a normal "never run" state.

This ticket runs last among code changes precisely because it rewrites every `app/*.go` handler and both CLI files; doing it earlier would collide with every P1/P2 ticket.

## Work items

- [x] Define one response envelope (e.g. `{"error": bool, "msg": string, "data": <payload>}`) and apply it to every handler **under the versioned prefix**, including the update endpoints.
- [x] Add a central Fiber error handler (`fiber.Config{ErrorHandler: ...}`) and/or a helper so handlers `return err`/`return apiError(c, code, msg)` instead of repeating the 500 block ~15×. Remove all the duplicated blocks.
- [x] **Never send `err.Error()` to clients on versioned routes.** Return a generic, stable client message; log the detailed error server-side (with the request path/id). Legacy routes keep their current bodies (golden tests).
- [x] Map `models` "upgrade already in progress" to **409**; map other error classes to appropriate codes (400 validation, 404 not-found, 409 conflict, 500 internal).
- [x] `GET /system/install/status` with no prior install → **200** with `{"status":"never_run"}` (or the envelope equivalent), not 400.
- [x] Update the CLI client SDK (`cmd/calaos-os/api/api.go`) and command layer (`cmd/calaos-os/calaos-os.go`) to parse the single envelope for all endpoints; remove the per-endpoint special-casing.
- [x] Write `docs/API.md`: document every route, request/response schema, status codes, the auth header, and an **old→new mapping** table for the breaking changes (envelope, 409, never_run, removed `/update/images` from CC-002, install `confirm` field from CC-013).

## Out of scope

- Business logic in `models/*` — frozen by the time this runs; only the handler ↔ model boundary (error mapping) is touched here, not model internals.
- Auth logic — CC-011 (this ticket may restyle the 401 body to the envelope, but not change auth behavior).
- Adding tests beyond the legacy golden tests — CC-051 (CC-052 moves to ADM-CC2, CC-055 to ADM-CC3).

## Admin plan amendments

- **Versioned prefix.** The envelope, the 409, `never_run`, error sanitization and every new route (`admin/*`, jobs) live under a versioned prefix chosen in this ticket (e.g. `/api/v2/`).
- **Legacy routes untouched.** Current routes and response shapes (raw update JSON, 500 on concurrent upgrade, 400 on never-run install status, synchronous upgrade) are served byte-identical for at least one release, pinned by golden tests. Retiring them waits for CH1b.
- **`docs/API.md` admin section.** Relay table, the three `admin/set` modes, error vocabulary, session rules, `X-Calaos-Client-IP`, 202 job shape, event names. Lands as the first commit of the ticket, merged alone, so calaos_base can start CB0.
- Blocks ADM-CC2 and ADM-CC1 (in addition to CC-051, CC-052, CC-055).

## Breaking changes (versioned prefix only)

**Apply to the versioned prefix only; legacy routes are unchanged. Documented in `docs/API.md`:**
- All responses now use a single envelope (update endpoints previously returned raw JSON).
- "upgrade already in progress" is 409, not 500.
- `install/status` never-run is 200 `{"status":"never_run"}`, not 400.
- (Carried from earlier tickets, re-listed in API.md: `/update/images` removed — CC-002; install `confirm` required — CC-013.)

External clients (calaos-home web UI) keep working on the legacy routes; the in-repo CLI is updated by this ticket.

## Acceptance criteria

- [x] `git grep 'err.Error()' app/` shows no client-facing usage outside the legacy handlers pinned by golden tests.
- [x] Every versioned-prefix handler returns the same envelope shape (verify by curling each route).
- [x] `GET /api/v2/update/check` during a legacy run returns `409 upgrade_in_progress`; the legacy `POST /update/upgrade-all` still returns its current response. (A second concurrent v2 upgrade returning 409 moves to ADM-CC3, which adds the v2 upgrade routes.)
- [x] Golden tests show legacy routes and response shapes byte-identical to before.
- [x] `GET /system/install/status` on a fresh system returns 200 `never_run` under the versioned prefix (legacy unchanged).
- [ ] `calaos-os` commands work end-to-end against the updated daemon (list, check-update, upgrade, network). Partly: see verification notes; a real upgrade and `network` need a box or the RC1 VM.
- [x] `docs/API.md` exists with the old→new mapping and the admin section.
- [x] `go vet ./...` and `cd cmd/calaos-os && go vet ./...` green.

## Verification notes

- Prefix `/api/v2`. Routes: `update/{check,available,installed,status}`, `system/{reboot,restart,info,fs_status,rollback_snapshot}`, `system/install/{list_devices,start,status}`, `network/{list,:intf}`, `events`. No v2 upgrade route until ADM-CC3; the CLI upgrades through the legacy routes.
- `app/legacy_golden_test.go` + `app/testdata/golden/` (52 cases) pin status, content type and body of every legacy route. The goldens were recorded before the refactor and none changed; two were added: legacy `system/reboot` answers 409 during a run (deliberate, ADM-CH1a), legacy `rollback_snapshot` keeps rebooting during a run (accepted risk).
- `app/v2_test.go`: exact envelope per route, 409, `never_run`/`running`/`finished`, no internal text in any v2 error, `bad_bearer`, `?token=` refused on v2, session header (`bad_session`, empty header, `not_allowed` on bearer-only routes), 404/405/426 envelopes, `/api/v2/events`.
- `models` only gained `ErrUpgradeInProgress` and `ErrPackageNotFound`, with the same messages.
- CLI: `/api/v2` envelope with a fallback to the legacy routes when the daemon in memory predates `/api/v2`; `/api/v2/events`, then `/api/events`. Tests in `cmd/calaos-os/api/api_v2_test.go`.
- `go vet`, `golangci-lint` (v2.12.2) and `go test -race ./...` green in `ghcr.io/calaos/debian_deb_build_base`.
- End to end in that container (daemon + CLI): `calaos-os list`, `check-update`, `upgrade no-such-pkg` (package not found, exit 4) work; `network list` fails there only because the image has no `networkctl` (legacy fails the same way, with the raw error text).
