# ADM-CC1: Admin password, sessions and admin routes

- **Phase:** P6
- **Size:** 1.75 d
- **Status:** Done
- **Depends on:** CC-040, ADM-CC2
- **Blocks:** ADM-CC3
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `app/admin/` (store), `app/admin.go` (routes, audit middleware), `cmd/calaos-os` `admin` subcommand
  - `/mnt/calaos/admin` creation in `scripts/init_calaosfs`

## Scope

- Password store in a 0700 directory, argon2id, `O_EXCL` creation, minimum length.
- Routes `GET admin/status`, `POST admin/verify`, `POST admin/set` (three modes), `POST admin/reset`, `POST admin/alerts/ack`, under the CC-040 versioned prefix.
- Sessions in `/run/calaos-container/`, surviving a daemon restart.
- Failure rate limiter with immediate 429, persisted alerts and ack, audit log.
- CLI `set`/`reset` reading the password from stdin.
- Contract: admin section of `docs/API.md` (written in CC-040).

## Acceptance

- [x] Tests derive from the `docs/API.md` admin section.
- [x] Sessions survive a daemon restart; limiter answers 429 at once.
- [x] A present-but-empty `X-Calaos-Admin-Session` answers `401 bad_session` (`Request().Header.Peek` returns nil only when the header is absent).

## Verification notes

- Store: `/mnt/calaos/admin/` holds `password.hash` (PHC argon2id, m = 64 MiB, t = 3, p = 1;
  verified with the parameters stored in the hash, bounded), `alerts.json` and `audit.log`
  (rotated at 1 MiB into `audit.log.1`), all 0600. Every file is written to a temp file, synced,
  then published: bootstrap by `link(2)`, which fails with `EEXIST` like `O_CREAT|O_EXCL` on the
  final name without ever exposing a half-written hash; change and recovery by `rename(2)`.
- `init_calaosfs` creates `/mnt/calaos/admin` under the existing `/run/calaos` flock (`umask
  077` + `mkdir`, then `chown root:root` and `chmod 0700`); a symlink or a file at that path is
  replaced, never followed. The daemon also refuses a non-directory there and tightens the mode.
- Sessions: `/run/calaos-container/sessions.json` (dir 0700) holds the SHA-256 of each session,
  its wall-clock deadline and a fingerprint of the password hash it was issued for. On load the
  remaining time is capped at 900 s and turned into a monotonic deadline. A session is live only
  while the hash on disk still matches its fingerprint, so a change or reset kills it even if the
  invalidation could not be written. Bound 32, oldest dropped. `PushCodeFailed` is the phase 2
  hook (5 failures kill the session).
- Limiter: one argon2id at a time (`verify` and `set`), refused with `429 retry_after: 1`
  without waiting; 5 `verify` failures per rolling 60 s for the daemon, checked before hashing.
- Audit: an app-level middleware before the bearer check, so refused calls on `/api/v2/admin/*`
  are audited too (`bad_bearer`). Client from `X-Calaos-Client-IP` (`local`, `invalid` or the
  normalised IP), session audit id, path sanitised. Written to the file and the daemon log.
- Alerts: persisted before they are published; replayed on `/api/v2/events` and `/api/events`
  after the `upgrade_progress` snapshot (the subscription is taken first, so none is lost).
- CLI: `calaos-os admin set` reads the password twice without echo on a terminal, else the first
  line of stdin; it sends recovery mode. `calaos-os admin reset` reminds to change the household
  password first. Both report an old daemon (no `/api/v2` or no admin routes) as not active yet.
- Tests (`-race`): `app/admin` (hash, bounds, limiter failures-only and one-at-a-time, session
  expiry, restart, reboot, clock step back, bound, fingerprint, push hook, three modes and their
  check order, concurrent bootstrap in one and in 16 daemons, alerts persist/ack/bound, audit
  format and rotation, symlinked directory); `app` (HTTP flow of every route, check order,
  empty session header, 429 with `Retry-After`, restart, audit lines, alert replay and ack over
  WebSocket on both event routes); CLI stdin reading and recovery request. Goldens unchanged.
  Mutations caught: success counted, budget unchecked, no slot, bootstrap by rename, sessions
  kept on set, no replay, no cap on load, no Retry-After, ack not persisted, length before
  session, token stored, no bound, recovery with a session, CLI without recovery.
- `init_calaosfs_test.sh`: every round checks `/mnt/calaos/admin` is `700 root root directory`
  and keeps its content across a second batch; a loose 0755 intruder-owned directory is fixed
  with its content kept; a symlink or a file is replaced without touching the target. Mutations
  caught: no chmod, no chown, no symlink check, block before the lock (`mkdir: File exists`).
