# CC-040: Unified API contract (envelope, status codes, error sanitization) + CLI lockstep

- **Phase:** P4
- **Priority:** P1
- **Size:** L
- **Status:** Backlog
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

## Context

The HTTP API is internally inconsistent, forcing the client SDK to special-case each endpoint and leaking internals to callers:

- **Two response shapes coexist.** Some handlers wrap results in `{error, msg, output}` (`app/network.go:20-24`, `app/system.go:66-70`); update handlers return the raw model JSON with no envelope (`app/update.go:19,23,36`). The client SDK special-cases this (`cmd/calaos-os/api/api.go:287-299` parses an envelope for network, `:198-199` parses raw for updates).
- **Duplicated 500 block.** The same `return c.Status(500).JSON(fiber.Map{"error":true,"msg":err.Error()})` is copy-pasted ~15× across every handler.
- **Raw error leakage.** `err.Error()` from subprocess stderr and JSON unmarshalling is reflected straight to clients throughout — information disclosure.
- **Wrong status codes.** "upgrade already in progress" → 500 (`app/update.go`, from `models` returning an error) should be **409 Conflict**. `GET /system/install/status` before the first install → **400** (`app/system.go:150-156`) for what is a normal "never run" state.

This ticket runs last among code changes precisely because it rewrites every `app/*.go` handler and both CLI files; doing it earlier would collide with every P1/P2 ticket.

## Work items

- [ ] Define one response envelope (e.g. `{"error": bool, "msg": string, "data": <payload>}`) and apply it to **every** handler consistently, including the update endpoints.
- [ ] Add a central Fiber error handler (`fiber.Config{ErrorHandler: ...}`) and/or a helper so handlers `return err`/`return apiError(c, code, msg)` instead of repeating the 500 block ~15×. Remove all the duplicated blocks.
- [ ] **Never send `err.Error()` to clients.** Return a generic, stable client message; log the detailed error server-side (with the request path/id).
- [ ] Map `models` "upgrade already in progress" to **409**; map other error classes to appropriate codes (400 validation, 404 not-found, 409 conflict, 500 internal).
- [ ] `GET /system/install/status` with no prior install → **200** with `{"status":"never_run"}` (or the envelope equivalent), not 400.
- [ ] Update the CLI client SDK (`cmd/calaos-os/api/api.go`) and command layer (`cmd/calaos-os/calaos-os.go`) to parse the single envelope for all endpoints; remove the per-endpoint special-casing.
- [ ] Write `docs/API.md`: document every route, request/response schema, status codes, the auth header, and an **old→new mapping** table for the breaking changes (envelope, 409, never_run, removed `/update/images` from CC-002, install `confirm` field from CC-013).

## Out of scope

- Business logic in `models/*` — frozen by the time this runs; only the handler ↔ model boundary (error mapping) is touched here, not model internals.
- Auth logic — CC-011 (this ticket may restyle the 401 body to the envelope, but not change auth behavior).
- Adding tests — CC-051/052/055.

## Breaking changes

**Significant, intentional, documented in `docs/API.md`:**
- All responses now use a single envelope (update endpoints previously returned raw JSON).
- "upgrade already in progress" is 409, not 500.
- `install/status` never-run is 200 `{"status":"never_run"}`, not 400.
- (Carried from earlier tickets, re-listed in API.md: `/update/images` removed — CC-002; install `confirm` required — CC-013.)

External clients (calaos-home web UI) must be updated in step; the in-repo CLI is updated by this ticket.

## Acceptance criteria

- [ ] `git grep 'err.Error()' app/` returns no client-facing usage (only logging).
- [ ] Every handler returns the same envelope shape (verify by curling each route).
- [ ] Second concurrent `POST /update/upgrade-all` returns **409**, not 500.
- [ ] `GET /system/install/status` on a fresh system returns 200 `never_run`.
- [ ] `calaos-os` commands work end-to-end against the updated daemon (list, check-update, upgrade, network).
- [ ] `docs/API.md` exists with the old→new mapping.
- [ ] `go vet ./...` and `cd cmd/calaos-os && go vet ./...` green.

## Verification notes

_(filled by the implementing agent)_
