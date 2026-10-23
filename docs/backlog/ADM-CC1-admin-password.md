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

- Store: `/mnt/calaos/admin/` holds `password.hash` (PHC argon2id), `alerts.json` and `audit.log`
  (rotated at 1 MiB into `audit.log.1`), all 0600. Every file is written to a temp file, synced,
  then published: bootstrap by `link(2)`, which fails with `EEXIST` like `O_CREAT|O_EXCL` on the
  final name without ever exposing a half-written hash; change and recovery by `rename(2)`. Once
  the hash is published, a failed directory fsync is only logged.
- argon2id cost: m = 32 MiB, t = 3, p = 1, the low end of the plan's memory range. Calaos OS also
  runs on RPi 3 class boxes (1 GiB, Cortex-A53, no argon2 assembly on arm): extrapolated from x86
  (177 ms at 64 MiB in pure Go), 64 MiB would take about 2 s there. `debug.FreeOSMemory()` after
  every hash keeps the peak at about m instead of 2m. A hash keeps verifying with the parameters
  in its PHC string, so the cost can change without a migration.
- `init_calaosfs` creates `/mnt/calaos/admin` under the existing `/run/calaos` flock (`umask
  077` + `mkdir`, then `chown root:root` and `chmod 0700`). A symlink is removed, never followed;
  any other non-directory is moved to `admin.bak-<epoch>-<pid>`, never deleted. A failure there is only
  a warning: the other units waiting on the script still start, and the daemon answers `internal`
  on the admin routes. The daemon also refuses a non-directory there and tightens the mode.
- Sessions: `/run/calaos-container/sessions.json` (dir 0700) holds the SHA-256 of each session,
  its wall-clock deadline and `pw_fp`, a fingerprint of the password hash it was issued for. On
  load the remaining time is capped at 900 s and turned into a monotonic deadline. A session is
  live only while the hash on disk still matches its fingerprint (content cached on inode, size
  and mtime), so a change or reset kills it even if the invalidation could not be written; an
  unreadable hash at start leaves the file alone. Bound 32, oldest dropped. `PushCodeFailed` is
  the phase 2 hook (5 failures kill the session).
- Limiter: one argon2id at a time (`verify` and `set`), refused with `429 retry_after: 1`
  without waiting; 5 `verify` failures per rolling 60 s for the daemon, checked before hashing.
- Audit: an app-level middleware before the bearer check. Lines: time, client (from
  `X-Calaos-Client-IP`: `local`, `invalid` or the normalised IP), result, session audit id, then
  method and the path quoted and cut at 128 bytes. `bad_bearer` and `bad_session` refusals go to
  the daemon log only, one line per second with a suppressed count: 10 000 of either leave
  `audit.log` untouched.
- Alerts: persisted before they are published; replayed on `/api/v2/events` and `/api/events`
  after the `upgrade_progress` snapshot (the subscription is taken first, so none is lost); a
  pending alert a connection has not received is sent again at the next ping. A corrupt
  `alerts.json` is moved to `alerts.json.corrupt`.
- CLI: `calaos-os admin set` reads the password twice without echo on a terminal, else the first
  line of stdin; invalid UTF-8 is refused; SIGINT, SIGTERM and SIGHUP restore the terminal and
  exit 130. It sends recovery mode. `calaos-os admin reset` reminds to change the household
  password first. Both report an old daemon (no `/api/v2` or no admin routes) as not active yet.
- Tests (`-race`): `app/admin` (hash, bounds, limiter failures-only and one-at-a-time, session
  expiry, restart, reboot, clock step back, bound, fingerprint, unreadable hash, push hook, three
  modes and their check order, concurrent bootstrap in one and in 16 daemons, alerts
  persist/ack/bound/corrupt, audit format, path bound and rotation, symlinked directory); `app`
  (HTTP flow of every route, check order, empty session header, 429 with `Retry-After`, restart,
  audit lines, 10 000 unauthenticated calls, alert replay, ack and redelivery over WebSocket on
  both event routes); CLI stdin reading, UTF-8, signal restore and the recovery request. Goldens
  unchanged. Every fix and invariant was mutation-checked (see the PR).
- `init_calaosfs_test.sh`: every round checks `/mnt/calaos/admin` is `700 root root directory`
  and keeps its content across a second batch; a loose 0755 intruder-owned directory is fixed
  with its content kept; a symlink is removed without touching the target; a file is moved aside
  exactly once with its content; a failing `chmod` on that path leaves all 10 runs at exit 0 with
  the token and `/etc/ola` set up and a warning logged.
