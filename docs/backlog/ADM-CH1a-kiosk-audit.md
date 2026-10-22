# ADM-CH1a: Kiosk (Calaos Home desktop) to calaos-container audit

- **Phase:** Admin phase 1, step S0
- **Status:** Done (audit only, no code change)
- **Feeds:** CC-040, CC2, CC2b, CC3, CH1b
- **Source audited:** `github.com/calaos/calaos_mobile` (no `calaos_home` repo exists). The kiosk is the
  `desktop.pro` build (`TARGET=calaos_home`, `desktop.pro:15`), which compiles `src/CalaosOsAPI.cpp` and
  `src/UpdateManager.cpp` (`desktop.pro:39,44`). `mobile.pro` does not.
- **Revisions read:** master `2e5e768` and every git tag. The GitHub Releases API lists no release object,
  so "published" means tags: stable `X.Y.Z`, prerelease `X.Y.Z-dev.N`.

## Version matrix

| Tags | Daemon traffic |
|---|---|
| `3.2.x` and older, `v2.x`, `v3.0.5` | none (no `CalaosOsAPI`) |
| `4.0.0` to `4.3.0`, `4.3.1-dev.0` (stable and prerelease) | `/api/system/*` and `/api/network/*` only, `Authorization` header only |
| `4.3.1-dev.1` to `4.3.1-dev.28`, `4.4.0` (stable), master | above, plus `/api/update/*` and `/api/events?token=` |

`4.3.1-dev.1` (2026-08-05) is the first tag with `UpdateManager.cpp` and `events?token=`
(commit `713fd77`, "add software update page"). `4.4.0` (2026-10-20) is the latest stable and has it.
`git diff 4.4.0 master` shows no change to `CalaosOsAPI.*` or `UpdateManager.*`: master equals `4.4.0` on this surface.
Route list per tag checked with `git grep -n '/api' <tag> -- src/CalaosOsAPI.cpp src/UpdateManager.cpp`.

## Transport and auth (all revisions with `CalaosOsAPI`)

- Base `http://127.0.0.1:8000` (`CalaosOsAPI.cpp:25`); token read once at construction from
  `/run/calaos/calaos-ct.token`, `.trimmed()` (`:7`, `:19-21`). A later token rotation is never re-read.
- Every REST call sends `Authorization: bearer <token>` (lowercase scheme) via `setCustomHeader`
  (`:68,108,269,321,384,447`). Never `?token=` on REST. The daemon compares the scheme with `ToLower`
  (`app/context.go:51-54`), so the case is not load-bearing for it, but it must stay case-insensitive.
- `?token=` is used in exactly one place: the WebSocket URL
  `ws://127.0.0.1:8000/api/events?token=<raw token>` (`UpdateManager.cpp:650-651`, from `4.3.1-dev.1`).
  The token is appended without percent-encoding. A token containing `+`, `/`, `=` or `&` would be mangled
  (fasthttp decodes `+` as a space). CC2 must emit a URL-safe alphabet (hex) or old kiosks fail auth.
- The WebSocket handshake carries no `Authorization` header (`m_ws.open(QUrl)` only).

## Routes, methods, parsed fields

`doPost`/`doGet` = system envelope (`{error,msg,output}`); `doPostRaw`/`doGetRaw` = raw document plus HTTP status.
Success means no Qt network error (any 2xx) AND a body that parses as JSON (empty body accepted,
`NetworkRequest.cpp:212-216`). Anything else is a failure.

| Route | Method | Where | Fields read |
|---|---|---|---|
| `/api/system/reboot` | POST | `CalaosOsAPI.cpp:34` | none (2xx and JSON-parsable is enough); on failure `error` (bool), `msg` |
| `/api/system/restart` | POST | `:39` | same |
| `/api/system/rollback_snapshot` | POST | `:49` | same |
| `/api/system/fs_status` | GET | `:44` | `error`, `output` (returned to caller as-is) |
| `/api/system/info` | GET | `:174` | `error`, `output` |
| `/api/network/list` | GET | `:169` | `error`, `output` |
| `/api/network/<name>` | POST | `:181` | none on success |
| `/api/system/install/list_devices` | GET | `:54` | `error`, `output` |
| `/api/system/install/start` | POST | `:64` | body streamed raw to the UI (`dataReadyRead`), HTTP success only |
| `/api/system/install/status` | GET | `:104-137` | `error`, `exit_code` (int) |
| `/api/update/check` | GET | `:186` | top-level object keyed by package name; per value `version`, `current_version`, `name` (`UpdateModel.cpp:68-105`, `UpdateManager.cpp:209`) |
| `/api/update/available` | GET | `:194` | same shape as check |
| `/api/update/installed` | GET | `:202` | object keyed by package, `name`, `version` (`UpdateModel.cpp:71-72`); any failure only marks the installed list unavailable (`UpdateManager.cpp:187-197`) |
| `/api/update/upgrade?package=<name>` | POST | `:212-218` | success/failure and `msg` on failure only |
| `/api/update/upgrade-all` | POST | `:222` | same |
| `/api/update/status` | GET | `:230` | status frame, see below |
| `/api/events` | WS | `UpdateManager.cpp:650` | see events |

Status frame (poll and WS `data`), `UpdateManager.cpp:400-498`: `status` (`idle`, `upgrading`, `done`, `error`),
`total_percent`, `current_pkg`, `current_step`, `current_percent`, `pkg_index`, `pkg_count`, `error`,
`packages[]` with `name`, `state` (`done`, `failed`, other values shown as in-progress), `percent`.

## Events

- Only `/api/events` over WS, only the type `upgrade_progress` is handled (`UpdateManager.cpp:690`).
  Any other `type`, non-JSON text, non-object JSON: silently ignored (`:686-691`). Binary frames: no handler.
- Envelope read: `{"type": string, "data": object}`; `data` is the same object as `/api/update/status`.
- First `upgrade_progress` frame after connect is consumed as the connect-time snapshot
  (`m_expectSnapshot`, `:659`, `:695-718`) and adopted only if `status == "upgrading"`, or if a resync is waiting.
- Reconnect: backoff 1 s doubling to 15 s (`:674-680`), meanwhile polls `/api/update/status` every 2 s
  during an upgrade (`:36`, `:744-765`). A WS auth failure (401) is a plain socket error: same path.
  Without a WS the kiosk still shows its own upgrades through polling, but loses upgrades started by others.

Types it would choke on:
1. A new type: nothing breaks (ignored). Safe to add `job_*`, `admin_*`, `alert_*` event names.
2. A new frame that REUSES the name `upgrade_progress` with another `data` shape. `status` missing or unknown
   (not `idle`) falls through to `applyStatusSnapshot`, which replaces the whole state with zeros
   (`:415-422`) and resets the progress bar mid-upgrade. Never repurpose that name or its fields.
3. A change in which frame is first after connect: if the daemon stops sending the `upgrade_progress`
   snapshot, the kiosk waits 3 s then falls back to one poll (`:79-83`). Degrades, does not break.
4. `status` values other than `idle|upgrading|done|error` while the kiosk owns a resync: treated as
   "in progress"; the upgrade never concludes from the stream.

## Reactions to the changes CC-040 and CC3 plan to introduce

- **Unknown fields:** ignored everywhere (`QJsonObject` accessors, missing keys decode as 0 or empty).
  Adding fields to legacy responses is safe. Dropping or renaming any field above is not.
- **New response envelope on legacy routes:** breaks. `update/*` readers treat the top-level object keys as
  package names (`UpdateManager.cpp:165`, `maybeNotifyUpdates:207`); an `{error,msg,data}` wrapper makes
  `error`, `msg`, `data` appear as three bogus packages (update badge, notification dialog, count). The system
  routes already use `output`. Legacy shapes must stay as-is.
- **202 on a POST:** treated as success the moment it returns (`NetworkRequest` accepts any 2xx;
  `upgradePackage`/`upgradeAll` callbacks only look at `ok`). `upgradeAll` then calls `finishUpgrade(true)`,
  shows "Update installed successfully" or the reboot dialog and re-fetches `available` while apt still runs
  (`:305-325`, `:539-603`). For a package queue it POSTs the next package immediately (`:362-366`), which then
  hits the in-progress guard and ends as "Upgrade failed". A 202 body that is not JSON is a parse failure.
  The legacy upgrade routes must therefore keep answering only when the transaction ends, with 200.
- **409:** not 2xx, so failure with HTTP status 409. Message is `msg` when `error:true`, otherwise Qt's generic
  `errorString`. On `update/check`, an automatic check with a message containing "in progress" is swallowed
  silently; any other error marks the daemon unavailable (`:133-154`). A manual check shows the message.
  On `upgrade`, a message containing "package not found" in a multi-package queue is counted as done
  (`:384-392`). These two substrings, in `msg`, are a de facto contract. 409 itself is not specially handled.
- **Connection severed (status 0):** the kiosk deliberately treats it as "daemon restarting after
  self-upgrade" and switches to resync via WS/poll (`:310-322`, `:369-382`). Do not turn a restart into an
  early HTTP error.
- **401:** no special handling. Reboot/restart paths ignore the result (`Application.cpp:791,799`).

## Conclusions

### 1. Can `?token=` be removed in CC2?

No. Every kiosk from `4.3.1-dev.1` through the stable `4.4.0` and master opens
`/api/events?token=` (`UpdateManager.cpp:650`) and cannot send a header on that WebSocket. Removing it on
`/events` loses live progress for all of them (they fall back to 2 s polling during their own upgrades, and
no longer see upgrades started elsewhere). On every other route no kiosk ever used `?token=`, so it can be
refused there in CC2. Plan outcome: keep it on `/events` only, remove it in CC2b one release after CH1b.
The tags before `4.3.1-dev.1` never used it. Daemon `1.7.0` rejects it, so a `4.3.1-dev.x`/`4.4.0` kiosk
talking to a `1.7.0` daemon already runs on polling only.

### 2. What must stay byte-identical for old kiosks (CC-040, CC3)

- Paths and methods of every route in the table above, unprefixed, on `127.0.0.1:8000`, with
  `Authorization: bearer <token>` case-insensitive.
- Token file path `/run/calaos/calaos-ct.token`, readable by the kiosk process; content a single URL-safe
  string (no `+ / = & %`), one trailing newline tolerated. Read once at kiosk start: rotation needs a kiosk restart.
- `GET update/check|available|installed`: top-level object keyed by package name, values with
  `version`, `current_version`, `name`; no extra top-level keys.
- `POST update/upgrade[-all]`: synchronous, 200 only when apt ends, error as 500 with `{"error":true,"msg":...}`.
  No 202 on these paths. `msg` must keep containing "in progress" (upgrade running) and "package not found".
  A refused concurrent upgrade stays an HTTP error status; 409 is acceptable as the kiosk handles any non-2xx
  identically, but the body must keep `error:true` and `msg`.
- `GET update/status` and WS `upgrade_progress`: `{type,data}` envelope, `data` fields `status`
  (`idle|upgrading|done|error`), `total_percent`, `current_pkg`, `current_step`, `current_percent`,
  `pkg_index`, `pkg_count`, `error`, `packages[].{name,state,percent}` with `state` values `done`/`failed`.
  Connect-time snapshot as first frame. Additive fields and new event types are safe.
- `system/*`, `network/*`: `{error,msg,output}` and `exit_code` on `install/status`; `install/start` streams raw text.
- Kiosk container runs as root without a userns remap (see the last section).
- Unlisted: the daemon exits and restarts during `upgrade-all` and kills the in-flight POST; CC3's "exit 0 after
  the legacy reply" must not regress the kiosk's resync logic (it tolerates both).

### 3. What CH1b must change (kiosk code: `calaos_mobile`, not `calaos_home`)

- Move `/api/events` auth to a header, or keep the query token until CC2b. A Qt `QWebSocket::open(QNetworkRequest)`
  accepts custom headers (`UpdateManager.cpp:650`, one-line change); then CC2b can drop `?token=`.
- Parse the CC-040 envelope on the versioned prefix for `update/*` and `admin/*` (today
  `UpdateManager.cpp:165` iterates top-level keys); fall back to the legacy routes when the prefix 404s.
- Use the 202 job plus `/events` terminal event for upgrades instead of the blocking POST
  (`CalaosOsAPI.cpp:208-226`, `UpdateManager.cpp:305-395`), and handle 409 with a dedicated, non-string-matched
  branch in place of the substring checks on "in progress" and "package not found" (`:135`, `:152`, `:384`).
- Re-read the token file on 401 or reconnect (today read once, `CalaosOsAPI.cpp:19-21`), as a safeguard if the daemon restarts or the
  token changes while the kiosk runs. It is not needed for the CC2 boot-time rewrite: the file is written only
  if absent, at boot, and the kiosk unit's own `ExecStartPre=init_calaosfs` creates it before launch.
- Treat only the documented `upgrade_progress` frames as snapshots and key on a new event type for jobs.
- Estimate: stays at 0.75 d. The change is local to `CalaosOsAPI.cpp` (about 40 lines) and `UpdateManager.cpp`
  (job state machine replacing the POST-result logic), plus tests in `tests/`.
  Deployment is not blocked: nothing here is a prerequisite of the daemon upgrade, the legacy routes keep serving.

## Token file mode 0600 and the kiosk container

A root-owned 0600 token works: `calaos-home.service` is a system unit, `start_calaos_home` runs
`podman run --privileged --network=host -v /run/calaos:/run/calaos` with no `--user` or `--userns`, and the
runner stage of the image has no `USER`, so the kiosk is uid 0 on the host too.
Constraint for CC2 and CC-042: the kiosk container must stay root with no userns remap. `--user`,
`--userns=auto` or a `USER` in the runner stage would give `unable to read` (`CalaosOsAPI.cpp:23`) and an
empty bearer on every call.
