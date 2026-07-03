# CC-002: Dead code & repo hygiene sweep

- **Phase:** P0
- **Priority:** P1
- **Size:** M
- **Status:** Backlog
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

- [ ] Remove `LoadFromDisk` (`models/update.go:80-83`).
- [ ] Remove `apiUpdateImages` handler (`app/update.go:26-37`) and the `/update/images` route (`app/app.go:117-119`).
- [ ] Remove `UpdateImages` from the CLI client SDK (`cmd/calaos-os/api/api.go:222-236`) and the installed-images branch of `cmdList` (`cmd/calaos-os/calaos-os.go`, around `:86-99`); `calaos-os list` now shows available updates only (or is merged into `check-update` output — keep it simple).
- [ ] Remove config keys `url_releases` and `version_file` from `calaos-container.toml` and any default/read in `config/config.go`.
- [ ] Remove `quitHeartbeat` from `app/app.go` (struct field `:22`, init `:39`, close in `Shutdown` `:152`).
- [ ] Remove unused char constants and color vars from `main.go` and `cmd/calaos-os/calaos-os.go` (keep only what is used: `errorRed`, `CharAbort`, and whatever the CLI tables actually use).
- [ ] Remove unused package-level log helpers (`log/logger.go:33-70`).
- [ ] Remove `ImageList` type; rename `CurrentVerion` → `CurrentVersion` across the repo (`models/structs/images.go:5`, `models/update.go:73`, `cmd/calaos-os/calaos-os.go:137`, any other references). **Keep the JSON tag `current_version` unchanged.**
- [ ] `git rm cmd/calaos-os/calaos-os` and add `cmd/calaos-os/calaos-os` to `.gitignore`.
- [ ] `git rm scripts/mosquitto_pre scripts/regctl`.

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

- [ ] `go build ./...` and `cd cmd/calaos-os && go build ./...` succeed.
- [ ] `git grep -l 'LoadFromDisk\|ImageList\|quitHeartbeat\|url_releases\|version_file\|CurrentVerion'` returns nothing (except this ticket file).
- [ ] `git ls-files cmd/calaos-os/calaos-os scripts/mosquitto_pre scripts/regctl` returns nothing.
- [ ] `calaos-os list` / `check-update` still work against a running daemon.

## Verification notes

_(filled by the implementing agent)_
