# CC-011: Auth middleware hardening

- **Phase:** P1
- **Priority:** P0
- **Size:** S
- **Status:** Done
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

- [x] **Fail closed:** if the token file is missing, unreadable, or empty after trimming, reject every request with 401 and log a warning. An empty stored token must never validate any request.
- [x] Compare with `crypto/subtle.ConstantTimeCompare` on the byte slices.
- [x] Guard the cached token with a `sync.RWMutex` (or load via `sync.OnceValues`-style with invalidation).
- [x] Support rotation: stat the file per request and re-read when mtime/size changed (traffic is tiny; a stat per request is fine). Keep `TOKEN_FILE` const as-is (path unification with the CLI is out of scope here).
- [x] Do not leak the OS error to the client (`:39` currently returns `err.Error()`); return a generic message, log the detail.

## Out of scope

- Response envelope shape — CC-040 will restyle all error responses; keep the current `{"error":true,"msg":...}` shape for now.
- The CLI's duplicated token reading (`cmd/calaos-os/calaos-os.go:68-74`) — CC-023/CC-040 territory.
- TLS / bind address — CC-014.

## Breaking changes

None for correctly configured systems. A system with an empty token file previously (and wrongly) accepted unauthenticated requests; it will now reject everything until a token is provisioned — that is the intended fix.

## Acceptance criteria

- [x] Token file absent → any request = 401, generic message, detailed log line.
- [x] Token file empty/whitespace → request WITHOUT Authorization header = 401 (was: 200).
- [x] Valid token → 200; wrong token → 401.
- [x] `echo newtoken > /run/calaos/calaos-ct.token` is picked up without restarting the daemon.
- [x] `go test -race` on the P5 test (CC-052) will exercise concurrent first-hit caching; for now: `go vet` + manual curl matrix above.

## Verification notes

Implemented in `app/context.go`: `NewTokenMiddleware` now delegates token
loading to a new `currentToken()` helper. Cache state (`tokenStr`,
`tokenValid`, `tokenLoadPath`, `tokenModTime`, `tokenSize`) is guarded by a
new `sync.RWMutex` (`tokenMu`). Each request `os.Stat`s `tokenFile`; on a stat
error the cache is invalidated and the request rejected (401, generic `"msg":
"unauthorized"`, detail via `logging.Warnf`). When the path/mtime/size match
the cache, the cached token is reused; otherwise the file is re-read and
trimmed — an empty result after trimming clears `tokenValid` (fail closed,
so an empty stored token can never authenticate any request, including one
with no `Authorization` header) and is logged as a warning. Token comparison
uses `crypto/subtle.ConstantTimeCompare` on `[]byte`. `TOKEN_FILE`/`tokenFile`
path handling and the CLI's own token reading were left untouched, as was the
`{"error":true,"msg":...}` response shape.

Added `app/context_test.go` (minimal, per CC-011 scope — comprehensive
middleware coverage is CC-052's): `TestCurrentToken_MissingFile`,
`TestCurrentToken_EmptyFileFailsClosed`, `TestCurrentToken_ValidFile`,
`TestCurrentToken_Rotation`, and an end-to-end
`TestNewTokenMiddleware_FailsClosedAndAcceptsValidToken` using
`httptest`/`fiberApp.Test`. `app/events_test.go` (owned by another ticket)
was left untouched and continues to pass unmodified — its direct
manipulation of the package-level `tokenFile`/`tokenStr` vars remains
compatible with the new cache because invalidation also keys off
`tokenLoadPath`, so a changed `tokenFile` always forces a reload regardless
of what the test sets `tokenStr` to.

Verified inside `ghcr.io/calaos/debian_deb_build_base:latest` with the
worktree bind-mounted (host lacks `libapt-pkg-dev`/`golangci-lint`):
- `apt-get build-dep -y .`, `make build-lib`, `go build ./...`, `go vet ./...`
  — all clean.
- `golangci-lint run` (v2.12.2, `CGO_LDFLAGS="-L./bin -lcalaos-apt"`, matching
  CI) — **0 issues**.
- `make test` (`go test -race ./...`) — **all packages pass**, including the
  new `app/context_test.go` cases and the pre-existing `app/events_test.go`
  suite.
- Container build artifacts (`apt/build/`, `bin/`) were removed from the
  worktree afterwards.

Runtime curl matrix against the built daemon (`bin/calaos-container`, real
`/run/calaos/calaos-ct.token`, `GET /api/system/info`), all as expected:
- No `Authorization` header → **401**.
- Wrong token → **401**.
- Correct token → **200**.
- Token file emptied (`printf '' > .../calaos-ct.token`) while daemon kept
  running → **401** even with an empty `Authorization` header, and the
  previously-valid token was also rejected (`level=warning ... token file
  ... is empty; rejecting all requests until a token is provisioned`).
- Token file rewritten with a new value while the daemon kept running (same
  PID throughout, no restart) → old token now **401**, new token now
  **200** (rotation confirmed).
- Extra check beyond the acceptance list: token file removed entirely →
  **401** with `level=warning ... token file ... unavailable: ... no such
  file or directory`.

Note: while investigating, an editing-tool misconfiguration briefly wrote
this same change into the sibling main-repo checkout
(`/home/raoul/repos/calaos-container`) instead of this worktree; it was
caught immediately and reverted (verified via `git diff --stat -- app/context.go`
showing zero diff there) before any other work was affected. All actual
implementation and commits for CC-011 live only in this worktree /
`ticket/cc-011` branch.
