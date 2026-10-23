# ADM-CC2: Admin token (32 bytes urandom)

- **Phase:** P6
- **Size:** 0.5 d
- **Status:** Done
- **Depends on:** CC-040, CC-042, ADM-CH1a
- **Blocks:** ADM-CC1
- **Plan:** [`docs/admin/PLAN.md`](https://github.com/calaos/calaos_base/blob/docs/admin-webui-plan/docs/admin/PLAN.md)
- **Files owned:**
  - `scripts/init_calaosfs` (token creation), `scripts/tests/init_calaosfs_test.sh`
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

- [x] 10 concurrent `init_calaosfs` runs leave one 0600 token and no temp file.
- [x] Token is hex; `?token=` works on `/events` and gives 401 elsewhere.
- [x] Token file is 0600, 32 urandom bytes hex-encoded (64 chars), not rewritten when present.
- [x] `go test -race ./app/ -run 'Token|Auth'` green, including the CC-052 checklist.
- [x] `/etc/ola` is created on a fresh system.

## Verification notes

- `init_calaosfs`: token from `od -N32 /dev/urandom`, checked to be 64 hex chars before it is
  published; temp file from `mktemp` under `umask 077`, published with `ln` (never replaces),
  temp removed by an EXIT trap. The `/etc/ola` and `/var/run` blocks run under `flock`: without
  it, concurrent runs removed each other's `/etc/ola` symlink and failed on `ln -s`.
- `scripts/tests/init_calaosfs_test.sh` (root, container only, also in CI): 20 rounds of 10
  concurrent runs; each leaves one regular 0600 root 65-byte hex token, link count 1, no temp
  file, `/etc/ola` -> `/mnt/calaos/ola`; a watcher reading the file during the race never sees
  a partial or replaced token; a second batch keeps the inode and content; an old 0644 weak
  token is left untouched. Fails on the previous script, on a direct write (`>`), on `mv -f`
  instead of `ln`, and without the lock.
- Origin already compared in constant time and failed closed; kept, now in `tokenEqual`
  (an empty expected token never matches). `?token=` is read only when the path is the legacy
  `/api/events` (case-insensitive, trailing slashes ignored).
- `app/context_test.go`: `tokenEqual` cases; a source check that tokens are never compared with
  `==`/`!=`; `?token=` refused (401, exact legacy or `bad_bearer` body) on every registered route
  but `/api/events`, with a header control per route; missing, empty, whitespace and unreadable
  token files refuse every credential form; 64-hex and old weak tokens work by header and raw in
  the `/events` URL; 64 concurrent requests on a cold cache and across a rotation under `-race`.
- Goldens: `auth_query_token` is now `401 wrong token` (deliberate); added
  `events_query_token` (426, past auth) and `events_query_token_wrong` (401). All others unchanged.
- `go vet`, `golangci-lint` v2.12.2, `make test` (`-race`) and shellcheck green in
  `ghcr.io/calaos/debian_deb_build_base`.
