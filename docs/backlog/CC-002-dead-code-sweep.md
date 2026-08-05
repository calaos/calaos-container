# CC-002: Dead code & repo hygiene sweep

- **Phase:** P0
- **Priority:** P1
- **Size:** M
- **Status:** Done
- **Depends on:** — (first ticket to merge)
- **Blocks:** CC-001 (merge order), CC-010, CC-023
- **Files owned (exclusive for this phase):**
  - `models/update.go` (LoadFromDisk removal only)
  - `app/update.go` (apiUpdateImages removal only)
  - `app/app.go` (route + quitHeartbeat removal only)
  - `main.go`
  - `log/logger.go`
  - `models/structs/images.go`
  - `config/config.go` (dead keys only)
  - `calaos-container.toml`
  - `cmd/calaos-os/api/api.go`
  - `cmd/calaos-os/calaos-os.go`
  - `cmd/calaos-os/calaos-os` (binary — delete)
  - `.gitignore`
  - `scripts/mosquitto_pre` (delete)
  - `scripts/regctl` (delete)

## Context

The audit found substantial dead or vestigial code that pollutes every later refactoring step and would trip the lint gates introduced by CC-001:

- `models/update.go:80-83` — `LoadFromDisk` is a stub: it ignores its `filePath` argument and always returns an empty map. As a result `GET /api/update/images` (`app/update.go:26-37`) always returns `{}`. **Note:** this endpoint IS consumed by the CLI `list` command (`cmd/calaos-os/api/api.go:222-236` `UpdateImages`, used at `calaos-os.go:86`), so removal must cover the CLI too.
- `calaos-container.toml:7` `url_releases` — referenced nowhere in Go code (update path moved to apt).
- `general.version_file` — only consumed by the `LoadFromDisk` stub, effectively non-functional.
- `app/app.go:22,39,152` — `quitHeartbeat` channel is created and closed but has no consumer goroutine.
- `main.go:23-28,31-37` — unused char constants (`CharStar`, `CharCheck`, `CharWarning`, `CharArrow`, `CharVertLine`) and unused color vars (`blue`, `errorBgRed`, `green`, `cyan`, `bgCyan`); `bgCyan` is also mis-named (it is `FgWhite`). Same block duplicated in `cmd/calaos-os/calaos-os.go:22-40`.
- `log/logger.go:33-70` — package-level `Infof/Warnf/Fatalf/Tracef/Debugf/Errorf/Panicf/Printf` helpers never called anywhere.
- `models/structs/images.go:9-11` — `ImageList` type unused; `:5-6` `Image.CurrentVerion` Go identifier is misspelled (the JSON tag `current_version` is correct and must not change).
- `cmd/calaos-os/calaos-os` — an 8.6 MB compiled binary is tracked in git.
- `scripts/mosquitto_pre`, `scripts/regctl` — orphaned: not installed by `Makefile install` nor `debian/calaos-container.install`, referenced nowhere.

## Work items

- [x] Remove `LoadFromDisk` (`models/update.go:80-83`).
- [x] Remove `apiUpdateImages` handler (`app/update.go:26-37`) and the `/update/images` route (`app/app.go:117-119`).
- [x] Remove `UpdateImages` from the CLI client SDK (`cmd/calaos-os/api/api.go:222-236`) and the installed-images branch of `cmdList` (`cmd/calaos-os/calaos-os.go`, around `:86-99`); `calaos-os list` now shows available updates only (or is merged into `check-update` output — keep it simple).
- [x] Remove config keys `url_releases` and `version_file` from `calaos-container.toml` and any default/read in `config/config.go`.
- [x] Remove `quitHeartbeat` from `app/app.go` (struct field `:22`, init `:39`, close in `Shutdown` `:152`).
- [x] Remove unused char constants and color vars from `main.go` and `cmd/calaos-os/calaos-os.go` (keep only what is used: `errorRed`, `CharAbort`, and whatever the CLI tables actually use).
- [x] Remove unused package-level log helpers (`log/logger.go:33-70`).
- [x] Remove `ImageList` type; rename `CurrentVerion` → `CurrentVersion` across the repo (`models/structs/images.go:5`, `models/update.go:73`, `cmd/calaos-os/calaos-os.go:137`, any other references). **Keep the JSON tag `current_version` unchanged.**
- [x] `git rm cmd/calaos-os/calaos-os` and add `cmd/calaos-os/calaos-os` to `.gitignore`.
- [x] `git rm scripts/mosquitto_pre scripts/regctl`.

## Out of scope

- Do NOT touch the update loop, `NewVersions`, or locking (`models/update.go:15-77`) — owned by CC-010.
- Do NOT touch `scripts/pull_calaos_image` — it is installed on deployed systems; it is kept and hardened in CC-033.
- Do NOT fix CLI bugs (error ordering, timeouts) — owned by CC-023.
- Do NOT change Fiber config or `exit()` in `main.go` — owned by CC-014 (this ticket only deletes unused vars/consts in `main.go`).

## Breaking changes

- `GET /api/update/images` is **removed**. It has always returned `{}` (stub), so no functional client can depend on real data from it. Documented here for external clients (calaos-home web UI).
- `calaos-os list` no longer queries installed images.

## Removed features (for future reimplementation)

The original intent (never completed): persist installed container-image versions to `general.version_file` (`/etc/calaos-os.releases`) and serve them via `GET /update/images`, with `url_releases` pointing to the image feed (`https://releases.calaos.fr/v4/images`). The design moved to apt/dpkg-driven updates instead. A future reimplementation should read installed versions from `dpkg-query`/apt cache rather than a hand-maintained file, and reintroduce the endpoint with the unified envelope from CC-040.

## Acceptance criteria

- [x] `go build ./...` and `cd cmd/calaos-os && go build ./...` succeed.
- [x] `git grep -l 'LoadFromDisk\|ImageList\|quitHeartbeat\|url_releases\|version_file\|CurrentVerion'` returns nothing (except this ticket file).
- [x] `git ls-files cmd/calaos-os/calaos-os scripts/mosquitto_pre scripts/regctl` returns nothing.
- [x] `calaos-os list` / `check-update` still work against a running daemon.

## Verification notes

**Pre-implementation audit (git log + git grep), since the ticket's line numbers had drifted from three
intervening commits — `feat(api): add GET /api/update/installed`, `Add real-time apt upgrade progress
(libapt-pkg + WebSocket)`, and the CC-001 CI/lint gate:**

- All ten targets were confirmed still present and still dead before touching anything:
  `LoadFromDisk` (now `models/update.go:88-91`, unchanged stub), `apiUpdateImages`/`/update/images`
  route (now `app/update.go:26-36` / `app/app.go:124-126`), CLI `UpdateImages` SDK method + the
  installed-images branch of `cmdList`, `quitHeartbeat` (field/init/close, line numbers shifted but
  logic unchanged, no consumer goroutine added since), `url_releases`/`version_file` in
  `calaos-container.toml`, char/color consts in `main.go` and `cmd/calaos-os/calaos-os.go`, package-level
  log helpers in `log/logger.go`, `ImageList`, `CurrentVerion`, the tracked binary, and the two orphaned
  scripts.
- **Important finding:** `GET /api/update/installed` (from the merged `feat(api)` commit) is a
  **separate, live, unrelated** endpoint/handler (`apiUpdateInstalled`, dpkg-package-based) — not the
  same as `apiUpdateImages`/`GET /api/update/images` (the stub this ticket removes). Confirmed via
  `git grep` that they are distinct routes/handlers with no shared code; `apiUpdateInstalled` was left
  untouched.
- `config/config.go`: `defaultConfig` only ever contained `general.port`, `general.address`,
  `log.default` — it never had defaults or reads for `url_releases`/`version_file`, so there was nothing
  to remove there beyond the two lines in `calaos-container.toml`.
- No test file references any of the removed/renamed symbols (checked all `*_test.go` files).
- `.claude/skills/smoke-test/SKILL.md` (not in the ticket's file-ownership list, but not owned by any
  other ticket either) curled the now-removed `GET /api/update/images`; swapped it for
  `GET /api/update/available` so the skill keeps working for future runs.

**Implementation deviations from the literal ticket text (both directed by the assigning task, not ad hoc):**

- `calaos-os list` was rewritten (not just had a branch deleted) to call `UpdateAvailable`
  (`GET /api/update/available`, cached `NewVersions`) and render a 3-column table (Image / Current
  version / New version) — "available updates only", matching `cmdCheck`'s data shape minus the
  never-populated Source column. `cmdCheck` itself (owned by CC-023, do-not-fix-CLI-bugs) was left
  untouched, including its pre-existing Source-header/no-Source-cell mismatch.
- `.golangci.yml`: the two now-stale `unused`-linter grandfather exclusions that named the exact vars
  deleted here (`cmd/calaos-os/calaos-os.go` `var (blue|errorBgRed|bgCyan) is unused` and `main.go`
  `var (blue|errorBgRed|green|cyan|bgCyan) is unused`) were removed since those identifiers no longer
  exist. The unrelated `cmd/calaos-os/api/api.go` "call is unused" exclusion (a genuinely unused private
  helper method, not part of this ticket's scope) was left as-is.

**Verification performed** (host lacks `libapt-pkg-dev`/`golangci-lint`, so — matching CC-001's approach —
verification ran inside `ghcr.io/calaos/debian_deb_build_base:latest` with the repo bind-mounted):

- `apt-get build-dep -y .` → `make build-lib` → `go build ./...` → `(cd cmd/calaos-os && go build .)` →
  `go vet ./...` — all pass, 0 issues.
- `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` (v2.12.2, matching `.devcontainer/Dockerfile` and
  `ci.yml`) — **0 issues**.
- `make test` (`go test -race ./...`) — all packages pass (`app`, `apt`, `cmd/calaos-os/api`, `events`,
  `models`, `models/structs`; packages with no test files reported as such, not failures).
- `gofmt -l` on all touched files — clean (pre-existing unrelated formatting drift in
  `models/structs/network.go`, untouched by this ticket, left as-is).
- `git grep -l 'LoadFromDisk\|ImageList\|quitHeartbeat\|url_releases\|version_file\|CurrentVerion'` →
  only `docs/backlog/CC-002-dead-code-sweep.md`, `docs/backlog/CC-010-update-subsystem-overhaul.md`, and
  `docs/backlog/README.md` (all descriptive doc references, no code).
- `git ls-files cmd/calaos-os/calaos-os scripts/mosquitto_pre scripts/regctl` → empty.
- Build artifacts (`apt/build/`) created by the container run were removed afterward (via the same
  container, since they were root-owned); `bin/` was never created on the host since verification used
  raw `go build ./...` (matching `ci.yml`), not `make build`. `git status --short` after cleanup shows
  only the intended file changes.
- **Runtime smoke test** (`smoke-test` skill, run inside a second container instance since the host lacks
  the CGO toolchain): `make` built `bin/calaos-container` and `bin/calaos-os`; started the daemon with a
  minimal TOML config (`general.port`/`address`/`update_time` — the last is pre-existing required config,
  unrelated to this ticket's key removals); provisioned `/run/calaos/calaos-ct.token` (an externally
  supplied file per `app/context.go`, not something the daemon generates) to authenticate.
  - `GET /api/system/info` → 200.
  - `GET /api/update/available` → 200 (`null` before any check).
  - `GET /api/update/images` → **404 "Cannot GET /api/update/images"**, confirming the route removal.
  - `GET /api/update/installed` → 200 `{}` (untouched, still live).
  - `GET /api/network/list` → 500 (`networkctl` binary absent in the minimal build container — an
    environment limitation, not a regression; out of scope for this ticket).
  - `calaos-os check-update` → forced a real `apt-get update`, found and printed one real available
    update (`linux-libc-dev`).
  - `calaos-os list` → printed the same cached update in the new 3-column table with **no error**,
    confirming the installed-images branch removal did not break `list`; re-run after `check-update`
    correctly showed the now-cached available update.
