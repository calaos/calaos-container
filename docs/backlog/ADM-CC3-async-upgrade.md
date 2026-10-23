# ADM-CC3: Asynchronous versioned upgrade

- **Phase:** P6
- **Size:** 3.25 d
- **Status:** Done
- **Depends on:** ADM-CC1
- **Blocks:** CB3 (calaos_base), RC1
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `models/update.go`, new `models/job.go`, `app/update.go` (versioned routes), session middleware, `cmd/calaos-os`
  - `main.go` (exit after a self-upgrade), `app/after_response.go`

## Scope

- Versioned upgrade answers 202 and creates a job; legacy synchronous upgrade is kept unchanged.
- Last result persisted; `reboot_required` computed without trusting snapshots.
- Exit 0 after a self-upgrade only once the legacy POST has answered and the terminal event is published; `Restart=always` (CC-042) relaunches the new binary.
- 409 on reboot while a job runs; session middleware; CLI follows the job over 202 and `/events`.

## Absorbs

- CC-050 (update subsystem tests): lock, present/absent package, Init/Shutdown balance and surfaced apt error become the test checklist, written on the async code.
- CC-055 (CLI client tests): `httptest`, envelope parsing, 409 mapping and timeout, written on the 202 + `/events` behaviour.

## Acceptance

- [x] Golden tests: legacy upgrade route unchanged.
- [x] A second concurrent upgrade under `/api/v2/` returns `409 upgrade_in_progress` with `job_id`.
- [x] A job persisted as `running` becomes `error` / `interrupted` at start, before any route is served.
- [x] CC-050 and CC-055 checklists covered.

## Verification notes

- Every run, legacy or `/api/v2/`, goes through `models.StartJob`: it takes `upgradeLock`, checks
  the package, records a `running` job (with the boot id) in
  `/var/log/calaos-container/last_job.json` and publishes `upgrade_job`. `Run` drives apt, the
  snapshots and the version refresh, then persists the terminal job. `Complete` resets the
  progress to idle, frees the lock and publishes the terminal `upgrade_job`. The job file is
  written to a temp file, synced, renamed, then the directory is synced.
- `/api/v2/update/upgrade*` answer `202` with the job and run it in a goroutine. The legacy routes
  run it in the handler and complete it from fasthttp's `ConnState` hook once the connection goes
  idle (answer written) or away (`Connection: close`, client gone). fiber has no after-response
  hook, and fasthttp's `Hijack` is skipped on `Connection: close` and on write errors, which would
  leave the lock held.
- Self-upgrade: `daemon_restart` is set when `calaos-container` completed in the transaction and
  `/proc/self/exe` is marked deleted. `Complete` then keeps the lock and the terminal progress,
  publishes the job, and closes `models.RestartRequested()`. `main` shuts down (the event streams
  get up to 10 s to flush the last events) and returns, so the exit status is 0 and
  `Restart=always` starts the new binary. The CC-042 deferred restart never sees the old daemon
  idle: it finds the unit stopped or running the new binary, and exits.
- `reboot_required` follows the kiosk's kernel group (`UpdateGrouping.h`) on the packages apt
  completed; it reads false once `/proc/sys/kernel/random/boot_id` differs from the job's.
- `models.Shutdown` waits for a running job (apt cannot be cancelled), within `TimeoutStopSec`.
- Accepted residual: when the binary was replaced by a run the daemon did not drive (shell
  `apt upgrade`), a job started in the instant between the deferred restart's last idle poll and
  its `systemctl restart` is cut. The unit's `dpkg --configure -a` resumes it and the job reads
  `interrupted`.
- CLI: `calaos-os upgrade` subscribes to the events, posts the job and stops on its terminal
  `upgrade_job`. On a lost connection (the daemon restarting after upgrading itself) it reconnects
  with backoff (1 s to 10 s, 2 min) and reads `GET /api/v2/update/status`. A plain `404`, or
  `404 not_found` from a daemon without jobs, falls back to the legacy synchronous flow.
- Tests (`-race`):
  - `models`: job lifecycle and persistence, `409` with the job id, check and reboot refused during
    a job, package present and absent, apt error with a sanitized summary, restart with the same
    and a new boot id, interrupted job at `Init`, corrupt job file, self-upgrade (exit only after
    the terminal event, lock kept, progress not idle), Shutdown waiting for a job, periodic check
    loop firing repeatedly, cache error surfaced;
  - `app`: `202` then the terminal job, `409` with `job_id` on upgrade, check, reboot and rollback,
    legacy answers during a job, package validation, sessions on the upgrade routes, legacy run
    recorded as a job with its frozen body, legacy and `/api/v2/` self-upgrade exits, the
    after-response hook (keep-alive, `Connection: close`, client gone), event flush at shutdown;
    goldens unchanged;
  - `main`: the exit status is 0 after a self-upgrade;
  - CLI: job followed to its end, job error, `409`, `package_not_found`, reconnection after a
    daemon restart, lost job, timeout, legacy fallback (no `/api/v2/`, `/api/v2/` without jobs,
    one package), legacy refusal and apt error, client envelope parsing.
