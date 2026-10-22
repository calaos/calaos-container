# ADM-CC2b: Retire `?token=` on `/events` (conditional)

- **Phase:** P6
- **Size:** 0.1 d
- **Status:** Backlog
- **Depends on:** ADM-CH1a finding (a published kiosk uses `?token=`), CH1b shipped one release earlier
- **Blocks:** —
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `app/context.go`

## Scope

Only if ADM-CH1a finds a kiosk relying on `?token=`: remove it from `/events` one release after CH1b. Out of the critical path. Close as not needed otherwise.
