# ADM-CC1: Admin password, sessions and admin routes

- **Phase:** P6
- **Size:** 1.75 d
- **Status:** Backlog
- **Depends on:** CC-040, ADM-CC2
- **Blocks:** ADM-CC3
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - new admin package under `app/`, `cmd/calaos-os` `admin` subcommand

## Scope

- Password store in a 0700 directory, argon2id, `O_EXCL` creation, minimum length.
- Routes `GET admin/status`, `POST admin/verify`, `POST admin/set` (three modes), `POST admin/reset`, `POST admin/alerts/ack`, under the CC-040 versioned prefix.
- Sessions in `/run/calaos-container/`, surviving a daemon restart.
- Failure rate limiter with immediate 429, persisted alerts and ack, audit log.
- CLI `set`/`reset` reading the password from stdin.
- Contract: admin section of `docs/API.md` (written in CC-040).

## Acceptance

- [ ] Tests derive from the `docs/API.md` admin section.
- [ ] Sessions survive a daemon restart; limiter answers 429 at once.
- [ ] A present-but-empty `X-Calaos-Admin-Session` answers `401 bad_session` (`Request().Header.Peek` returns nil only when the header is absent).
