# CC-011: Auth middleware hardening

- **Phase:** P1
- **Priority:** P0
- **Size:** S
- **Status:** Backlog
- **Depends on:** P0 merged
- **Blocks:** CC-052
- **Files owned (exclusive for this phase):**
  - `app/context.go`

## Context

`NewTokenMiddleware` (`app/context.go:18-55`) is the **only** authentication layer in front of endpoints that can reboot the host, wipe a disk (`/system/install/start`), and rewrite network config. It has four defects:

1. **Auth bypass with an empty token file.** The middleware caches `tokenStr` and only reads the file while `tokenStr == ""` (`:33`). If `/run/calaos/calaos-ct.token` exists but is empty (or whitespace), `tokenStr` stays `""` after trim (`:42`), and the comparison `token != tokenStr` (`:46`) **accepts a request with no Authorization header** (parsed `token` is also `""`). Empty file ⇒ fully unauthenticated API.
2. **Data race.** `tokenStr` is a package global (`:15`) read and lazily written by concurrent Fiber handlers with no synchronization.
3. **No rotation.** Once cached, the token is never re-read (`:33`); rotating the file requires a daemon restart.
4. **Non-constant-time comparison** (`:46`) — a timing side-channel; minor on loopback, relevant if the daemon is ever bound beyond it (see CC-014).

## Work items

- [ ] **Fail closed:** if the token file is missing, unreadable, or empty after trimming, reject every request with 401 and log a warning. An empty stored token must never validate any request.
- [ ] Compare with `crypto/subtle.ConstantTimeCompare` on the byte slices.
- [ ] Guard the cached token with a `sync.RWMutex` (or load via `sync.OnceValues`-style with invalidation).
- [ ] Support rotation: stat the file per request and re-read when mtime/size changed (traffic is tiny; a stat per request is fine). Keep `TOKEN_FILE` const as-is (path unification with the CLI is out of scope here).
- [ ] Do not leak the OS error to the client (`:39` currently returns `err.Error()`); return a generic message, log the detail.

## Out of scope

- Response envelope shape — CC-040 will restyle all error responses; keep the current `{"error":true,"msg":...}` shape for now.
- The CLI's duplicated token reading (`cmd/calaos-os/calaos-os.go:68-74`) — CC-023/CC-040 territory.
- TLS / bind address — CC-014.

## Breaking changes

None for correctly configured systems. A system with an empty token file previously (and wrongly) accepted unauthenticated requests; it will now reject everything until a token is provisioned — that is the intended fix.

## Acceptance criteria

- [ ] Token file absent → any request = 401, generic message, detailed log line.
- [ ] Token file empty/whitespace → request WITHOUT Authorization header = 401 (was: 200).
- [ ] Valid token → 200; wrong token → 401.
- [ ] `echo newtoken > /run/calaos/calaos-ct.token` is picked up without restarting the daemon.
- [ ] `go test -race` on the P5 test (CC-052) will exercise concurrent first-hit caching; for now: `go vet` + manual curl matrix above.

## Verification notes

_(filled by the implementing agent)_
