# calaos-container HTTP API

The daemon listens on `127.0.0.1:8000` (`general.address`, `general.port`). It serves two
surfaces side by side:

- the **versioned API** under `/api/v2/`: one envelope, sanitized errors, stable error codes,
  the admin routes and the upgrade jobs. Every new client uses it;
- the **legacy API**: the unprefixed `/api/...` routes, frozen in their current paths, methods,
  status codes and bodies. Old kiosks (Calaos Home desktop) and old `calaos-os` CLIs depend on
  them. They are served for at least one release after the kiosk moves to `/api/v2/`.

Nothing in this document changes a legacy response. Everything below applies to `/api/v2/` only,
unless a section says otherwise.

## Authentication (both surfaces)

Every request carries the daemon token read from `/run/calaos/calaos-ct.token` (trimmed):

```
Authorization: Bearer <token>
```

- The scheme is compared case-insensitively (old kiosks send `bearer`).
- The token is re-read when the file changes; a client that gets a bearer 401 re-reads the file
  and retries once.
- An empty or missing token file rejects every request (fail closed).
- `?token=<token>` is accepted only on the legacy `GET /api/events` WebSocket, because published
  kiosks open it without a header. It is never accepted on `/api/v2/`.

On `/api/v2/` a bearer failure answers `401` with the code `bad_bearer` (see the error
vocabulary). A `401` is never anything but `bad_bearer` or `bad_session`: a client that gets a
`401` whose `code` is not `bad_session` (including a legacy body without `code`) has a bearer
problem, not a session problem.

## Versioned API: envelope

Every `/api/v2/` response with a JSON body uses one envelope.

Success:

```json
{"error": false, "msg": "ok", "data": <payload>}
```

Error:

```json
{"error": true, "code": "upgrade_in_progress", "msg": "an upgrade is already in progress", "data": {"job_id": "3f9c2a7e5b1d4c80"}}
```

- `error` (bool) is always present.
- `code` is present on every error and absent on success. Its values form the closed vocabulary
  below. Clients branch on `code`, never on `msg`.
- `msg` is a short, stable English sentence for logs and fallbacks. It never contains a Go
  error string, subprocess output or a file path: the details go to the daemon log only. On
  `invalid_request` it may say what is wrong with the client's own input (field, interface,
  device).
- `data` carries the payload on success. On an error it is absent, or holds the code-specific
  details listed in the vocabulary.
- `Content-Type: application/json` on every enveloped response.

Two responses are not enveloped:

- `POST /api/v2/system/install/start` streams the installer log as `text/plain` once it has
  started; the errors before the stream starts are enveloped;
- errors raised by the HTTP server before routing (oversized body `413`, malformed request,
  read timeout) keep the server's plain body.

Requests that carry a body send `Content-Type: application/json`. Unknown request fields are
ignored.

## Versioned API: error vocabulary

| `code` | HTTP | Meaning | `data` |
|---|---|---|---|
| `bad_bearer` | 401 | `Authorization` missing or wrong, or no token provisioned on the box | none |
| `bad_session` | 401 | `X-Calaos-Admin-Session` present but empty, unknown, expired or invalidated | none |
| `bad_password` | 403 | wrong admin password on `admin/verify` | none |
| `not_allowed` | 403 | a session-bearing request on a bearer-only route (see Sessions) | none |
| `password_too_short` | 400 | admin password under 10 characters | `{"min_length": 10}` |
| `invalid_request` | 400 | malformed JSON, missing or invalid field, invalid `package` or interface name | none |
| `package_not_found` | 404 | the package has no pending update | none |
| `not_found` | 404 | unknown `/api/v2/` route | none |
| `method_not_allowed` | 405 | known route, wrong method | none |
| `admin_not_set` | 409 | the admin password is not defined (verify, change) | none |
| `admin_already_set` | 409 | bootstrap while an admin password exists (including a lost bootstrap race) | none |
| `upgrade_in_progress` | 409 | an upgrade runs: new upgrade, update check, reboot | `{"job_id": "..."}` once upgrade jobs exist |
| `upgrade_required` | 426 | plain `GET` on `/api/v2/events` | none |
| `rate_limited` | 429 | admin password limiter (see Rate limiting) | `{"retry_after": <seconds>}` |
| `internal` | 500 | anything else; the detail is logged by the daemon | none |

The HTTP status is informative except in two places where clients rely on it:

- `401`: bearer or session, told apart by `code` as above;
- `404` on `GET /api/v2/admin/status`, whatever the body: the daemon in memory predates the
  admin routes. An old daemon answers `404` with a plain-text body on every `/api/v2/` route
  (with a valid bearer).

`429` also sets the `Retry-After` header to the same number of seconds.

## Versioned API: request headers

| Header | Set by | Use |
|---|---|---|
| `Authorization: Bearer <token>` | every client | authentication, see above |
| `X-Calaos-Admin-Session: <session>` | calaos_server, when it relays an admin action | admin session, see Sessions. Never in the URL, never in a body |
| `X-Calaos-Client-IP: <ip>` | calaos_server, on every call it makes for a client | IP of the end client as calaos_server sees it. Written to the audit log, never used for a decision |

`X-Calaos-Client-IP` must be an IPv4 or IPv6 literal; any other value is logged as `invalid`. An
absent header is logged as `local` (the CLI). The daemon trusts nothing in it.

## Versioned API: routes

| Method | Path | Auth | Success | `data` |
|---|---|---|---|---|
| GET | `/api/v2/update/check` | bearer, session allowed | 200 | object keyed by package name: `{name, version, current_version}` |
| GET | `/api/v2/update/available` | bearer, session allowed | 200 | same as `check`, without contacting the mirrors |
| GET | `/api/v2/update/installed` | bearer, session allowed | 200 | object keyed by package name: `{name, version}` |
| GET | `/api/v2/update/status` | bearer, session allowed | 200 | `{"progress": <Status>, "job": <Job> or null}` |
| POST | `/api/v2/update/upgrade-all` | bearer, session allowed | 202 | `<Job>` |
| POST | `/api/v2/update/upgrade` | bearer, session allowed | 202 | `<Job>`; body `{"package": "<name>"}` |
| POST | `/api/v2/system/reboot` | bearer, session allowed | 200 | `null`; `409 upgrade_in_progress` while a job runs |
| POST | `/api/v2/system/restart` | bearer, session allowed | 200 | `null` (restarts the `calaos-home` kiosk) |
| GET | `/api/v2/system/info` | bearer, session allowed | 200 | `{hostname, uptime, cpu_usage, mem_usage}` |
| GET | `/api/v2/system/fs_status` | bearer, session allowed | 200 | `findmnt --json /` output |
| POST | `/api/v2/system/rollback_snapshot` | bearer only | 200 | `null` (rolls back, then reboots); `409 upgrade_in_progress` while a job runs |
| GET | `/api/v2/system/install/list_devices` | bearer only | 200 | `lsblk --json --output-all` output |
| POST | `/api/v2/system/install/start` | bearer only | 200 | `text/plain` log stream; body `{"device": "/dev/...", "confirm": true}` |
| GET | `/api/v2/system/install/status` | bearer only | 200 | `{"status": "never_run"}`, `{"status": "running"}` or `{"status": "finished", "exit_code": <int>, "log": "<text>"}`; `exit_code` is `-1` when the installer ended without an exit status (failed start, daemon restarted under it) |
| GET | `/api/v2/network/list` | bearer only | 200 | array of interfaces |
| POST | `/api/v2/network/{intf}` | bearer only | 200 | `null`; body `{ipv4, gateway, dns_servers, search_domains, dhcp}` |
| GET | `/api/v2/admin/status` | bearer, session allowed | 200 | `{"admin_set": <bool>}` |
| POST | `/api/v2/admin/verify` | bearer, session allowed | 200 | `{"session": "<hex>", "expires_in": <seconds>}` |
| POST | `/api/v2/admin/set` | see the three modes | 200 | `{"mode": "bootstrap" or "change" or "recovery"}` |
| POST | `/api/v2/admin/reset` | bearer only | 200 | `{"was_set": <bool>}` |
| POST | `/api/v2/admin/alerts/ack` | bearer only | 200 | `{"acked": <int>}`; body `{"ids": ["..."]}` |
| GET (WS) | `/api/v2/events` | bearer header only | 101 | event stream, see Events |
| GET | `/api/v2/admin/push/{code_id}` | bearer only | 200 | phase 2: `{"token": "<push token>", "message": "<text>"}`, single use (a second call gives `404 not_found`) |

"Bearer only" routes refuse a request that carries `X-Calaos-Admin-Session` (`403 not_allowed`):
the session path (calaos_server) never reaches them.

`<Status>` is the upgrade progress object of the legacy `GET /api/update/status`, unchanged
(`status`, `total_percent`, `current_pkg`, `current_step`, `current_percent`, `pkg_index`,
`pkg_count`, `packages[]`, `message`, `error`, ...).

## Sessions

An admin session is what `admin/verify` returns after a correct admin password. calaos_server
keeps none: it relays the session the browser holds, in `X-Calaos-Admin-Session`.

- **Format:** 32 bytes from the CSPRNG, hex-encoded (64 lowercase characters). Opaque to every
  client.
- **Lifetime:** 15 minutes **absolute** from issue. Using a session does not extend it.
  `expires_in` is the number of seconds left (900 at issue). The deadline is stored as wall-clock
  time and the remaining time is capped at 900 s, so a clock step backwards never lengthens a
  session.
- **Invalidation:** every `admin/set` (all three modes) and every `admin/reset` invalidate all
  sessions. In phase 2, 5 failed push codes in a session invalidate that session. An invalidated
  session answers `401 bad_session` at its next use.
- **Storage:** in `/run/calaos-container/` (0700, created by the daemon, mounted in no
  container). Sessions survive a daemon restart, including the restart after the daemon upgrades
  itself, and die at reboot. The daemon stores a SHA-256 of each session, never the session.
- **Bound:** at most 32 live sessions; issuing one more drops the oldest.
- **Validation:** whenever the header is present, on any `/api/v2/` route, before the route runs:
  - on a bearer-only route (`system/install/*`, `system/rollback_snapshot`, `network/*`,
    `admin/reset`, `admin/alerts/ack`, `admin/push/*`, recovery `admin/set`): `403 not_allowed`,
    without looking the session up;
  - elsewhere, empty, unknown, expired or invalidated: `401 bad_session`;
  - otherwise the route runs.

  A header present with an empty value counts as present. calaos_server never sends an empty
  header.

  Without the header, the bearer alone authorizes the call: that is the CLI path. calaos_server
  therefore never calls a session route without the header (see the relay table).
- **Audit id:** the first 8 hex characters of the SHA-256 of the session. It is the only form of
  a session that ever appears in a log.

## Admin routes

### `GET /api/v2/admin/status`

`{"admin_set": true}` once an admin password exists, `false` otherwise. calaos_server polls it
(start, every 60 s, on `admin_set` and `admin_reset` events). A `404` means the daemon in memory
is too old (`daemon_too_old` on the calaos_server side).

### `POST /api/v2/admin/verify`

Body `{"password": "<admin password>"}`. On success:

```json
{"error": false, "msg": "ok", "data": {"session": "9b1f...e4", "expires_in": 900}}
```

Errors: `invalid_request`, `admin_not_set`, `bad_password`, `rate_limited`.

### `POST /api/v2/admin/set`

Body `{"password": "<new admin password>", "recovery": <bool, default false>}`. The password is a
UTF-8 string of at least 10 characters (Unicode code points) and at most 1024 bytes.

The mode follows from the request, in this order:

1. **Recovery**: `"recovery": true`. Bearer only: with `X-Calaos-Admin-Session` the answer is
   `403 not_allowed`. Sets the password whether one exists or not. Only `calaos-os admin set`
   sends it (password read on stdin); calaos_server never sends `recovery`. Whoever holds the
   bearer can use it: an accepted limit, the bearer already gives root.
2. **Change**: `X-Calaos-Admin-Session` present. The session must be valid (`401 bad_session`
   otherwise). Replaces the password. In phase 2 the body also carries the push code
   (`code_id`, `code`).
3. **Bootstrap**: no session, no `recovery`. Allowed only while no password exists: the hash is
   created with `O_EXCL`, so of two concurrent bootstraps exactly one wins and the other gets
   `409 admin_already_set`.

Checks run in this order: body (`invalid_request`), session (`bad_session`, `not_allowed`), state
(`admin_already_set`, `admin_not_set`), length (`password_too_short`).

`rate_limited` is also possible in every mode (see Rate limiting).

Every successful mode invalidates all sessions and publishes the `admin_set` alert. Response:
`{"mode": "bootstrap"}` (or `change`, `recovery`). No session is returned: the client unlocks
with `admin/verify`.

### `POST /api/v2/admin/reset`

Bearer only, no body. Removes the password hash and the approvers, invalidates all sessions,
closes open consoles (phase 2), and publishes the `admin_reset` alert if a password existed.
Idempotent: `{"was_set": false}` when there was nothing to reset. Only `calaos-os admin reset`
calls it.

### `POST /api/v2/admin/alerts/ack`

Body `{"ids": ["<alert id>", ...]}`. Marks the alerts as delivered; they are never replayed
again. Unknown ids are ignored. Answer `{"acked": <number of alerts removed>}`. calaos_server
calls it after it has recorded the alert (push sent, `EventPushNotification` stored).

### Rate limiting

- One argon2id operation at a time (`verify` or `set`). A second one is refused at once with
  `429 rate_limited` (`retry_after: 1`) instead of waiting, so `admin_unlock` always answers
  quickly.
- `verify` failures are budgeted: 5 failures per rolling 60 s for the whole daemon. Successes do
  not count. Over budget, `verify` answers `429 rate_limited` with `retry_after` set to the
  seconds until the oldest failure leaves the window, before any hashing.
- The budget is global, not per client: every remote client arrives through calaos_server. A
  third party failing in a loop can therefore delay legitimate unlocks; this is accepted.

### Alerts

`admin_set` and `admin_reset` are persisted in `/mnt/calaos/admin/` when they happen, then
published on the event bus and replayed to every new `/events` subscriber until acknowledged.
An alert raised while calaos_server is stopped is delivered when it reconnects.

Delivery is at least once: if calaos_server stops between recording and acknowledging an alert,
the alert comes back. calaos_server deduplicates on `id`. At most 32 unacknowledged alerts are
kept; the oldest goes first.

### Audit

Every `/api/v2/admin/*` call and every request carrying `X-Calaos-Admin-Session` writes one line
to `/mnt/calaos/admin/audit.log` and to stdout (journald): time, client
(`X-Calaos-Client-IP`, `local` or `invalid`), method and path, result (`ok` or the error
`code`), session audit id. Never a password, a session or the bearer.

`GET admin/status` without a session is not audited (calaos_server polls it). `audit.log` is
rotated at 1 MiB, one old file kept.

## Upgrade jobs

On `/api/v2/`, `POST update/upgrade-all` and `POST update/upgrade` start a job and answer at once:

```json
HTTP/1.1 202 Accepted

{"error": false, "msg": "accepted", "data": {"id": "3f9c2a7e5b1d4c80", "kind": "upgrade", "package": "calaos-server", "status": "running", "reboot_required": false, "daemon_restart": false, "started_at": "2026-10-22T09:12:03Z"}}
```

The legacy `POST /api/update/upgrade-all` and `POST /api/update/upgrade?package=` keep their
synchronous behaviour: they answer when apt ends.

**Job object**

| Field | Type | Meaning |
|---|---|---|
| `id` | string | 16 hex characters |
| `kind` | string | `upgrade_all` or `upgrade` |
| `package` | string | for `upgrade` only |
| `status` | string | `running`, `done` or `error` |
| `error` | string | on `error`: a short summary, without apt or dpkg output (the journal has it) |
| `reboot_required` | bool | computed by the daemon from what was installed, not from snapshots |
| `daemon_restart` | bool | the run upgraded calaos-container itself: the daemon exits after the terminal event and systemd starts the new binary |
| `started_at`, `finished_at` | string | RFC 3339, UTC; `finished_at` once terminal |

**Rules**

- One upgrade at a time, whatever started it (versioned job, legacy synchronous route, CLI,
  kiosk). Every run is recorded as a job, so `update/status` shows a run started from the kiosk.
- While a run is in progress, `POST update/upgrade*`, `GET update/check`,
  `POST system/reboot` and `POST system/rollback_snapshot` answer `409 upgrade_in_progress`
  (with `data.job_id` once jobs exist).
- Legacy routes during a run keep their current answers:
  `500 {"error": true, "msg": "upgrade already in progress"}` on `upgrade*` and `update/check`,
  `"package not found"` unchanged. One deliberate exception: the legacy `POST /api/system/reboot`
  answers `409 {"error": true, "msg": "upgrade already in progress"}` during a run, so that an old
  kiosk cannot cut dpkg. Old kiosks treat any non-2xx the same way and ignore the reboot result.
  The legacy `POST /api/system/rollback_snapshot` is unchanged.
- `package` must match `^[A-Za-z0-9.+-]+$`, start with a letter or a digit, contain no `..` and
  be at most 128 characters long; otherwise `400 invalid_request`. A valid name without a
  pending update gives `404 package_not_found`, and no job.
- The last job is persisted in `/var/log/calaos-container/last_job.json` (0600). `/var/log` is its
  own btrfs subvolume: it survives daemon restarts and reboots, and no snapshot or rollback ever
  restores it. `GET update/status` returns the running job, or the last finished one, or `null`
  if none ever ran.
- The job records the kernel boot id (`/proc/sys/kernel/random/boot_id`). Once the boot id
  differs, the daemon reports `reboot_required: false` for it: the reboot has happened.
- At start, before any route is served, a persisted job still `running` (crash, OOM kill)
  becomes `error` with `error: "interrupted"` and a `finished_at`. The interrupted dpkg run itself
  is resumed by the unit's `dpkg --configure -a`.
- Progress is published as `upgrade_progress` events, each job state change as `upgrade_job`
  (see Events). The terminal `upgrade_job` is published before the daemon exits for a
  self-upgrade, and after the legacy POST that started the run (if any) has been answered.

A client following a job:

1. subscribes to `/api/v2/events`;
2. `POST`s the upgrade and keeps `data.id`;
3. renders `upgrade_progress`, and stops on the `upgrade_job` with that `id` and a terminal
   `status`;
4. after any disconnection (including the daemon's own restart), reconnects with backoff and
   reads `GET update/status`: events are not replayed, the persisted job is the source of truth.

If the terminal `upgrade_job` arrives before the POST answer, the client reads
`GET update/status` once it has the `id`.

## Events

`GET /api/v2/events` (bearer header only) and the legacy `GET /api/events` (header or
`?token=`) are WebSocket endpoints on the same bus. A plain `GET` answers `426`
(`upgrade_required` on `/api/v2/`).

Frames are text, one JSON object each, **not** the REST envelope:

```json
{"type": "upgrade_progress", "data": {...}}
```

On connect the daemon sends, in order: one `upgrade_progress` with the current `<Status>`
(snapshot), every unacknowledged alert, then live events.

| `type` | `data` | Notes |
|---|---|---|
| `upgrade_progress` | `<Status>` | Frozen: old kiosks parse it. Fields may be added; none is renamed, removed or given another meaning, and the name is never reused for another shape. `status` stays in `idle`, `upgrading`, `done`, `error` |
| `upgrade_job` | `<Job>` | On job creation and on the terminal state |
| `admin_set` | `{"id": "<16 hex>", "ts": "<RFC 3339>"}` | Alert, replayed until acknowledged |
| `admin_reset` | `{"id": "<16 hex>", "ts": "<RFC 3339>"}` | Alert, replayed until acknowledged |
| `push_code` | `{"approver_id": "...", "code_id": "..."}` | Phase 2. Carries neither token nor code; calaos_server fetches the message with `GET /api/v2/admin/push/{code_id}` |

- Clients ignore types they do not know.
- Delivery is best effort: a subscriber that falls behind loses events. Clients resynchronise
  with `GET update/status` after a reconnect.
- The daemon pings every 30 s and drops a peer silent for 60 s. On shutdown it sends a close
  frame `1001` (going away).

## Relay table (calaos_server)

calaos_server reaches the daemon only through this closed table. Each `admin` action maps to a
fixed method and path; no path, header or body field comes from the client, except the
validated `package`.

| calaos_server message | Daemon call | `X-Calaos-Admin-Session` |
|---|---|---|
| `admin_unlock {admin_pass}` | `POST /api/v2/admin/verify {"password"}` | no |
| `admin_set {new_pass}` | `POST /api/v2/admin/set {"password"}` (bootstrap) | no |
| `admin_set {new_pass, admin_session}` | `POST /api/v2/admin/set {"password"}` (change) | yes |
| `admin {action: update_status}` | `GET /api/v2/update/status` | no (household, under `secureTransport()`) |
| `admin {action: update_check}` | `GET /api/v2/update/check` | yes |
| `admin {action: update_available}` | `GET /api/v2/update/available` | yes |
| `admin {action: update_upgrade_all}` | `POST /api/v2/update/upgrade-all` | yes |
| `admin {action: update_upgrade, params: {package}}` | `POST /api/v2/update/upgrade {"package"}` | yes |
| `admin {action: system_reboot}` | `POST /api/v2/system/reboot` | yes |
| `admin {action: system_restart}` | `POST /api/v2/system/restart` | yes |
| `admin {action: system_info}` | `GET /api/v2/system/info` | yes |
| `admin {action: mcp_regenerate}` | none: handled by calaos_server (see below) | yes |
| phase 2: `approver_list`, `approver_enroll`, `approver_confirm`, `console_open` | routes named in this file when they land | yes |

calaos_server's own calls, outside the table: `GET /api/v2/admin/status`,
`POST /api/v2/admin/alerts/ack`, `GET /api/v2/events`, and in phase 2
`GET /api/v2/admin/push/{code_id}`.

Actions calaos_server serves itself while admin is set (`config get/put`, `get_mcp_info`,
`mcp_regenerate`) validate the session with `GET /api/v2/admin/status` carrying
`X-Calaos-Admin-Session`, and proceed only on `200` with `admin_set: true`. For every row marked
"yes" and for these actions, a missing or empty `admin_session` from the client is answered
`bad_session` by calaos_server itself: the daemon call is never made without the header, because
without it the bearer alone authorizes.

Never relayed, whatever the client sends: `system/install/*`, `system/rollback_snapshot`,
`system/fs_status`, `network/*`, `update/installed`, `admin/reset`, `admin/alerts/ack` and
`admin/push/*` (for a client), `admin/set` with `recovery`. The daemon refuses a session on the bearer-only ones as a second barrier.

`package` is checked by calaos_server with the same rule as the daemon (regex
`^[A-Za-z0-9.+-]+$`, first character a letter or a digit, no `..`, at most 128 characters) and
sent in the JSON body, never in the path or the query.

Every relayed call carries `X-Calaos-Client-IP`. calaos_server builds the daemon body itself from
the fields above and drops anything else the client sent.

### Relayed events

calaos_server forwards to household clients, as `admin_event {type, ts, data}`, only:

- `upgrade_progress`, `data` = `<Status>`;
- `admin_set` and `admin_reset`, with `ts` only (`id` stripped, no `data`).

`upgrade_job` is not forwarded: a client that resumes, or sees `upgrade_progress` reach `done` or
`error`, reads the job through `update_status`. `push_code` is consumed by calaos_server
(phase 2). Every other type is dropped.

### Error translation

| Daemon answer | calaos_server code |
|---|---|
| connection refused, timeout, invalid answer | `daemon_unreachable` or `admin_unavailable`, per `AdminState` |
| `401` without `code: bad_session`, after re-reading the token and one retry | `daemon_unreachable` |
| `404` on `GET /api/v2/admin/status` (any body), or any `/api/v2/` `404` whose body is not an envelope with a `code` | `daemon_too_old` |
| `bad_session`, `bad_password`, `rate_limited`, `password_too_short`, `admin_not_set`, `admin_already_set`, `upgrade_in_progress`, `not_allowed` | same code |
| `invalid_request`, `package_not_found` | same code, with the daemon `msg` (safe to display) |
| `internal`, `not_found`, `method_not_allowed`, any unknown code | `internal` |

`rate_limited` copies `retry_after`; `upgrade_in_progress` copies `job_id`. The calaos_server
vocabulary (`08_http_api.md`, CB0) therefore gains `invalid_request`, `package_not_found` and
`internal`, and cites this file's `package` rule rather than the bare regex.

## Compatibility during the upgrade

- Old kiosk or old CLI, new daemon: they use the legacy routes, which answer as before. New event
  types on `/api/events` are ignored by them.
- Old calaos_server, new daemon: it never calls the daemon; nothing changes.
- New calaos_server, old daemon still in memory: `/api/v2/` answers `404`, which means
  `daemon_too_old` until the new daemon is active.
- A daemon that serves `/api/v2/` but not yet the admin routes answers `404 not_found` on
  `admin/status`, which also reads as `daemon_too_old`.
- New CLI, old daemon still in memory (before the deferred restart): `/api/v2/` answers `404`;
  the CLI falls back to the legacy routes, and `calaos-os admin` prints that the new daemon is
  not active yet.
- The legacy `GET /api/update/status` returns `status: "idle"` whenever no run is in progress:
  the deferred restart of the package (CC-042) waits on it.

## Legacy API (frozen)

These routes answer exactly as they did before `/api/v2/`. `app/testdata/golden/` pins their
status, content type and body, and `app/legacy_golden_test.go` fails on any change. `<err>` is
the raw Go error text.

| Method | Path | Success | Error |
|---|---|---|---|
| POST | `/api/system/reboot` | 200 `{"error":false,"msg":"ok"}` | 500 `{"error":true,"msg":<err>}`; 409 `{"error":true,"msg":"upgrade already in progress"}` during a run |
| POST | `/api/system/restart` | 200 `{"error":false,"msg":"ok"}` | 500 `{"error":true,"msg":<err>}` |
| GET | `/api/system/fs_status` | 200 `{"error":false,"msg":"ok","output":<findmnt>}` | 500 `{"error":true,"msg":<err>}` |
| POST | `/api/system/rollback_snapshot` | 200 `{"error":false,"msg":"ok"}` (rolls back, then reboots, even during a run) | 500 `{"error":true,"msg":<err>}` |
| GET | `/api/system/install/list_devices` | 200 `{"error":false,"msg":"ok","output":<lsblk>}` | 500 `{"error":true,"msg":<err>}` |
| POST | `/api/system/install/start` | 200 `text/plain` log stream | 400 or 500 `{"error":true,"msg":<err>}` |
| GET | `/api/system/install/status` | 200 `{"error":false,"msg":"ok","exit_code":<int>,"log":<text>}` | 400 `{"error":true,"msg":<err>}`, including "never ran" |
| GET | `/api/system/info` | 200 `{"error":false,"msg":"ok","output":{hostname, uptime, cpu_usage, mem_usage}}` | 500 `{"error":true,"msg":<err>}` |
| GET | `/api/network/list` | 200 `{"error":false,"msg":"ok","output":[...]}` | 500 `{"error":true,"msg":<err>}` |
| POST | `/api/network/{intf}` | 200 `{"error":false,"msg":"ok"}` | 400 or 500 `{"error":true,"msg":<err>}` |
| GET | `/api/update/check` | 200 raw object keyed by package | 500 `{"error":true,"msg":<err>}` ("upgrade already in progress" during a run) |
| GET | `/api/update/available` | 200 raw object keyed by package | none |
| GET | `/api/update/installed` | 200 raw object keyed by package | 500 `{"error":true,"msg":<err>}` |
| POST | `/api/update/upgrade-all` | 200 `{"msg":"Upgrade done"}` once apt ends | 500 `{"error":true,"msg":<err>}` ("upgrade already in progress") |
| POST | `/api/update/upgrade?package=<name>` | 200 `{"msg":"Upgrade done"}` once apt ends | 500 `{"error":true,"msg":<err>}` ("package not found", "upgrade already in progress") |
| GET | `/api/update/status` | 200 raw `<Status>` | none |
| GET (WS) | `/api/events` | 101, frames as in Events | 426 `Upgrade Required` (text) on a plain `GET` |

Unknown legacy paths answer fiber's plain-text `404 Cannot GET ...` and `405 Method Not Allowed`.
A bearer failure answers `401 {"error":true,"msg":"wrong token"}`, or `"unauthorized"` when no
token is provisioned.

### Old → new mapping

| Legacy | `/api/v2/` |
|---|---|
| raw JSON on `update/*`, `{error, msg, output}` on `system/*` and `network/*` | one envelope, payload in `data` |
| `500` "upgrade already in progress" | `409 upgrade_in_progress` |
| `400` on `install/status` before any install | `200 {"status":"never_run"}`; `running` while an installer started by this daemon is alive; `finished` with `exit_code: -1` when it ended without an exit status |
| `msg` = raw Go or subprocess error | `code` + sanitized `msg`; details in the daemon log |
| fiber plain-text `404`, `405`, `426` | `not_found`, `method_not_allowed`, `upgrade_required` envelopes |
| `401 {"msg":"wrong token"}` | `401 bad_bearer` |
| `?token=` accepted | header only |
| `POST update/upgrade?package=` and `upgrade-all`, synchronous | `202` job, body `{"package"}` (ADM-CC3) |
| `GET update/status` = `<Status>` | `{"progress": <Status>, "job": <Job> or null}` |
| `/update/images` (removed earlier, CC-002) | none |
| `install/start` without `confirm` (refused since CC-013) | same rule: `"confirm": true` required |

## Rollout

The contract above is complete; the daemon implements it in steps.

| Part | Lands with |
|---|---|
| `/api/v2/` prefix, envelope, error vocabulary, `bad_bearer`, `?token=` refused on `/api/v2/`, `never_run`, `update`, `system`, `network` routes, `/api/v2/events`, `409` on `update/check`, `system/reboot` (legacy too) and `system/rollback_snapshot` during a run, `X-Calaos-Admin-Session` checks (every session unknown until ADM-CC1) | CC-040 |
| 32-byte token, `?token=` refused on every legacy route but `/api/events` | ADM-CC2 |
| `admin/*` routes, sessions, `X-Calaos-Admin-Session`, rate limiting, alerts, audit, `calaos-os admin set|reset` | ADM-CC1 |
| `202` upgrade jobs, `upgrade_job`, `job` in `update/status` and in `409` answers, interrupted jobs | ADM-CC3 |
| `push_code`, `admin/push/{code_id}`, approver and console routes | phase 2 (AP1a, CO1) |

Until ADM-CC3, `/api/v2/` has no upgrade route (`404`): the CLI upgrades through the legacy
routes, and `update/status` returns `"job": null`.
