# ADM-CC2: Admin token (32 bytes urandom)

- **Phase:** P6
- **Size:** 0.5 d
- **Status:** Backlog
- **Depends on:** CC-040, CC-042, ADM-CH1a
- **Blocks:** ADM-CC1
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `scripts/init_calaosfs` (token creation)
  - `app/context.go`, `app/context_test.go`

## Scope

- 32-byte urandom token, mode 0600, created in `scripts/init_calaosfs` at the same path (`/run/calaos/calaos-ct.token`), only if absent (so at boot; running clients keep a valid token, the old weak token stays valid until the next boot).
- Atomic creation, safe for concurrent `init_calaosfs` runs: `umask 077`, write a temp file in the same directory, `ln` it to the token path (fails if the token exists), remove the temp file.
- `?token=` stays accepted on `/events` only and is refused on every other route: ADM-CH1a found published kiosks (`4.3.1-dev.1` and later, including stable `4.4.0`) that open `/api/events?token=`, appending the raw token without percent-encoding.
- The token must be URL-safe hex, otherwise those kiosks fail auth.
- Legacy routes and response shapes stay byte-identical (CC-040 golden tests).
- Replaces `mountpoint -q "$fs"` by `[ -d "$fs" ]` in `init_calaosfs`; the former prevents `/etc/ola` creation today.

## Absorbs

- CC-052 (auth middleware tests): rotation, concurrency and envelope-body cases land in `app/context_test.go` with the 32-byte and `?token=` scoping tests.

## Acceptance

- [ ] 10 concurrent `init_calaosfs` runs leave one 0600 token and no temp file.
- [ ] Token is hex; `?token=` works on `/events` and gives 401 elsewhere.
- [ ] Token file is 0600, 32 random bytes, not rewritten when present.
- [ ] `go test -race ./app/ -run 'Token|Auth'` green, including the CC-052 checklist.
- [ ] `/etc/ola` is created on a fresh system.
