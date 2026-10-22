# ADM-CC2: Admin token (32 bytes urandom)

- **Phase:** P6
- **Size:** 0.5 d
- **Status:** Backlog
- **Depends on:** CC-040, CC-042, ADM-CH1a
- **Blocks:** ADM-CC1
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `calaos_install`/`init_calaosfs` token creation
  - `app/context.go`, `app/context_test.go`

## Scope

- 32-byte urandom token, mode 0600, created atomically in `init_calaosfs` at the same path (`/run/calaos/calaos-ct.token`), only if absent (so at boot; running clients keep a valid token, the old weak token stays valid until the next boot).
- `?token=` removed, or kept on `/events` only, depending on the ADM-CH1a finding on published kiosks.
- Replaces `mountpoint -q "$fs"` by `[ -d "$fs" ]` in `init_calaosfs`; the former prevents `/etc/ola` creation today.

## Absorbs

- CC-052 (auth middleware tests): rotation, concurrency and envelope-body cases land in `app/context_test.go` with the 32-byte and `?token=` scoping tests.

## Acceptance

- [ ] Token file is 0600, 32 random bytes, not rewritten when present.
- [ ] `go test -race ./app/ -run 'Token|Auth'` green, including the CC-052 checklist.
- [ ] `/etc/ola` is created on a fresh system.
