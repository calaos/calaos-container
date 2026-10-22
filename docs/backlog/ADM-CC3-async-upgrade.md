# ADM-CC3: Asynchronous versioned upgrade

- **Phase:** P6
- **Size:** 3.25 d
- **Status:** Backlog
- **Depends on:** ADM-CC1
- **Blocks:** CB3 (calaos_base), RC1
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `models/update.go`, `app/update.go` (versioned routes), session middleware, `cmd/calaos-os`

## Scope

- Versioned upgrade answers 202 and creates a job; legacy synchronous upgrade is kept unchanged.
- Last result persisted; `reboot_required` computed without trusting snapshots.
- Exit 0 after a self-upgrade only once the legacy POST has answered and the terminal event is published; `Restart=always` (CC-042) relaunches the new binary.
- 409 on reboot while a job runs; session middleware; CLI follows the job over 202 and `/events`.

## Absorbs

- CC-050 (update subsystem tests): lock, present/absent package, Init/Shutdown balance and surfaced apt error become the test checklist, written on the async code.
- CC-055 (CLI client tests): `httptest`, envelope parsing, 409 mapping and timeout, written on the 202 + `/events` behaviour.

## Acceptance

- [ ] Golden tests: legacy upgrade route unchanged.
- [ ] CC-050 and CC-055 checklists covered.
