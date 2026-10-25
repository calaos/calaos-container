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
- An empty, missing or unreadable token file rejects every request (fail closed). The token is
  compared in constant time.
- `?token=<token>` is accepted only on the legacy `GET /api/events` WebSocket, because published
  kiosks open it without a header. Every other route, legacy or `/api/v2/`, answers `401` to a
  request that only carries `?token=`.
- `init_calaosfs` writes the file at boot when it is absent: 32 bytes from `/dev/urandom`,
  hex-encoded (64 characters and a newline), owned by root, mode `0600`. Kiosks append the
  token to a URL without percent-encoding it, so it stays hex. An existing non-empty regular file
  owned by root keeps its value (only its mode is set to `0600`): after an upgrade without reboot
  the previous token stays valid until the next boot. Anything else at that path is replaced.
- Every reader must run as root on the host: the kiosk container (privileged, no `--user`, no
  userns remap), calaos_server and `calaos-os`. A kiosk run under another uid or a userns remap
  cannot read the token and sends an empty bearer.

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
| `bad_session` | 401 | `X-Calaos-Admin-Session` present but empty, unknown, expired or invalidated, or absent on a session-only route | none |
| `bad_password` | 403 | wrong admin password on `admin/verify` | none |
| `not_allowed` | 403 | a session-bearing request on a bearer-only route (see Sessions) | none |
| `password_too_short` | 400 | admin password under 10 characters | `{"min_length": 10}` |
| `invalid_request` | 400 | malformed JSON, missing or invalid field, invalid `package`, interface name, service id, log query or time value, unknown or finished `change_id` | none |
| `package_not_found` | 404 | the package has no pending update | none |
| `package_held` | 409 | single-package upgrade of a package on hold (`apt-mark hold`) | none |
| `not_found` | 404 | unknown `/api/v2/` route | none |
| `method_not_allowed` | 405 | known route, wrong method | none |
| `admin_not_set` | 409 | the admin password is not defined (verify, change) | none |
| `admin_already_set` | 409 | bootstrap while an admin password exists (including a lost bootstrap race) | none |
| `upgrade_in_progress` | 409 | an upgrade runs: new upgrade, update check, reboot, service restart, network change | `{"job_id": "..."}` when the run is a job (absent behind a version check or a reboot) |
| `upgrade_required` | 426 | plain `GET` on `/api/v2/events` | none |
| `rate_limited` | 429 | admin password limiter (see Rate limiting), push code limits (see Push codes), log query limits (see Logs) | `{"retry_after": <seconds>}` |
| `approver_required` | 409 | the action needs a push code and no approver is enrolled | none |
| `approver_limit` | 409 | enrolment while 2 approvers exist | `{"max": 2}` |
| `approver_exists` | 409 | enrolment of a token that is already an approver | none |
| `approver_not_found` | 404 | unknown `approver_id` | none |
| `approver_last` | 409 | removal of the only approver | none |
| `code_required` | 403 | a code-protected action sent without `code_id` and `code` | none |
| `bad_code` | 403 | wrong push code, or a code requested for another action or other parameters | `{"attempts_left": <int>}` |
| `code_expired` | 410 | the push code is unknown to this session, expired, used, replaced or dropped | none |
| `console_limit` | 409 | a console is opened while 2 are open or opening | `{"max": 2}` |
| `network_pending` | 409 | a network change is already waiting for its confirmation (see Network configuration) | `{"change_id": "<16 hex>", "expires_in": <seconds>}` |
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
| GET | `/api/v2/system/services` | bearer, session allowed | 200 | `{"services": [<Service>]}` (see Services) |
| POST | `/api/v2/system/services/restart` | bearer, session allowed | 200 | `{"id", "state": "restarting"}`; body `{"unit": "<id>"}` |
| GET | `/api/v2/system/time` | bearer, session allowed | 200 | `<BoxTime>` (see Box time) |
| POST | `/api/v2/system/time` | bearer, session allowed | 200 | `<BoxTime>` plus `restart_required`; body `{"timezone", "ntp", "time"}`, each optional, at least one |
| GET | `/api/v2/system/timezones` | bearer, session allowed | 200 | `{"timezones": ["Africa/Abidjan", ...]}` |
| GET | `/api/v2/logs/sources` | bearer, session allowed | 200 | `{"sources": [{"id", "label"}]}` (see Logs) |
| POST | `/api/v2/logs/query` | bearer, session allowed | 200 | `{"entries", "truncated", "complete", "next_cursor"}`; body in Logs |
| POST | `/api/v2/system/rollback_snapshot` | bearer only | 200 | `null` (rolls back, then reboots); `409 upgrade_in_progress` while a job runs |
| GET | `/api/v2/system/install/list_devices` | bearer only | 200 | `lsblk --json --output-all` output |
| POST | `/api/v2/system/install/start` | bearer only | 200 | `text/plain` log stream; body `{"device": "/dev/...", "confirm": true}` |
| GET | `/api/v2/system/install/status` | bearer only | 200 | `{"status": "never_run"}`, `{"status": "running"}` or `{"status": "finished", "exit_code": <int>, "log": "<text>"}`; `exit_code` is `-1` when the installer ended without an exit status (failed start, daemon restarted under it) |
| GET | `/api/v2/network/list` | bearer only | 200 | array of interfaces |
| GET | `/api/v2/network/state` | bearer, session allowed | 200 | `{"interfaces": [<NetInterface>], "pending": {...} or null}` (see Network configuration) |
| POST | `/api/v2/network/apply` | session only, push code | 200 | `{"change_id", "intf", "applied_at", "confirm_within"}`; body in Network configuration |
| POST | `/api/v2/network/confirm` | bearer, session allowed | 200 | `{"change_id", "intf", "state": "confirmed"}`; body `{"change_id"}` |
| POST | `/api/v2/network/revert` | bearer, session allowed | 200 | `{"change_id", "intf", "state": "reverted"}`; body `{"change_id"}` |
| POST | `/api/v2/network/trial` | bearer only | 200 | as `apply`; body as `apply` without `code_id` and `code` |
| POST | `/api/v2/network/{intf}` | bearer only | 200 | `null`; body `{ipv4, gateway, dns_servers, search_domains, dhcp}` |
| GET | `/api/v2/admin/status` | bearer, session allowed | 200 | `{"admin_set": <bool>}` |
| POST | `/api/v2/admin/verify` | bearer, session allowed | 200 | `{"session": "<hex>", "expires_in": <seconds>}` |
| POST | `/api/v2/admin/set` | see the three modes | 200 | `{"mode": "bootstrap" or "change" or "recovery"}` |
| POST | `/api/v2/admin/reset` | bearer only | 200 | `{"was_set": <bool>}` |
| POST | `/api/v2/admin/alerts/ack` | bearer only | 200 | `{"acked": <int>}`; body `{"ids": ["..."]}` |
| GET (WS) | `/api/v2/events` | bearer header only | 101 | event stream, see Events |
| GET | `/api/v2/admin/approvers` | session only | 200 | array of `<Approver>` |
| POST | `/api/v2/admin/approvers/enroll` | session only | 200 | `{"approver_id", "code_id", "expires_in"}`; body `{"token", "device_name", "code_id", "code"}` |
| POST | `/api/v2/admin/approvers/confirm` | session only | 200 | `<Approver>`; body `{"code_id", "code"}` |
| POST | `/api/v2/admin/approvers/remove` | session only | 200 | `{"approver_id"}`; body `{"approver_id", "code_id", "code"}` |
| POST | `/api/v2/admin/codes` | session only | 200 | `{"code_id", "approver_id", "expires_in"}`; body `{"action", "params", "approver_id"}` |
| POST | `/api/v2/admin/codes/verify` | session only | 200 | `{"action", "expires_in"}`; body `{"code_id", "code"}` |
| GET | `/api/v2/admin/push/{code_id}` | bearer only | 200 | `{"token": "<push token>", "message": "<text>", "action": "<action>", "code": "<6 digits>", "expires_in": <seconds>}`, single use |
| POST | `/api/v2/admin/console` | session only | 200 | `<ConsoleOpened>`; body `{"code_id", "term"}` (see Console) |

"Bearer only" routes refuse a request that carries `X-Calaos-Admin-Session` (`403 not_allowed`):
the session path (calaos_server) never reaches them. "Session only" routes need a valid
`X-Calaos-Admin-Session`; without it they answer `401 bad_session`, since a push code is bound to a
session and the bearer alone has none. `network/apply` is the one session-only route outside
`admin/*`.

`<Status>` is the upgrade progress object of the legacy `GET /api/update/status`, unchanged
(`status`, `total_percent`, `current_pkg`, `current_step`, `current_percent`, `pkg_index`,
`pkg_count`, `packages[]`, `message`, `error`, ...).

What each `<Status>` field carries, on every surface (REST, `/api/events`, `/api/v2/events`):

| Field | Content |
|---|---|
| `status` | one of `idle`, `upgrading`, `done`, `error`; anything else the daemon would produce is sent as `upgrading` |
| `current_step` | absent, or one of `pending`, `download`, `unpack`, `configure`, `install`, `done`, `error`; anything else is sent as `install` |
| `total_percent`, `current_percent`, `pkg_index`, `pkg_count`, `progress`, `progress_total` | counters, no text from apt |
| `current_pkg`, `packages[].name` | Debian package names of the transaction (no version, path or URL): the box's package inventory |
| `packages[].state`, `packages[].percent` | per-package state and counter |
| `error` | on `error`: the job's summary (`upgrade failed` or `upgrade failed: <failed packages>`), never apt or dpkg output |
| `message` | never filled: apt messages (repository URLs, archive paths) go to the daemon log only |

A relay that must not expose the inventory drops `current_pkg` and `packages[].name`; nothing
else in `<Status>` comes from apt. The legacy `POST /api/update/upgrade*` error body still carries
the raw apt error (frozen, bearer only).

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
  sessions. 5 wrong push codes in a session invalidate that session (see Push codes). An
  invalidated session answers `401 bad_session` at its next use, and its push codes die with it.
- **Storage:** in `/run/calaos-container/` (0700, created by the daemon, mounted in no
  container). Sessions survive a daemon restart, including the restart after the daemon upgrades
  itself, and die at reboot. The daemon stores a SHA-256 of each session, never the session.
- **Bound:** at most 32 live sessions; issuing one more drops the oldest.
- **Validation:** whenever the header is present, on any `/api/v2/` route, before the route runs:
  - on a bearer-only route (`system/install/*`, `system/rollback_snapshot`, `network/list`,
    `network/trial`, `network/{intf}`, `admin/reset`, `admin/alerts/ack`, `admin/push/*`, recovery
    `admin/set`): `403 not_allowed`,
    without looking the session up;
  - elsewhere, empty, unknown, expired or invalidated: `401 bad_session`;
  - otherwise the route runs.

  On a session-only route (`admin/approvers*`, `admin/codes*`, `admin/console`, `network/apply`) an
  absent header is also `401 bad_session`.

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
   otherwise). Replaces the password. The body also carries a push code for the action
   `admin_password`, bound to `{"password": "<new password>"}`: `"code_id"` and `"code"` (see
   Push codes). Without an approver the answer is `409 approver_required`, without the two fields
   `403 code_required`.
3. **Bootstrap**: no session, no `recovery`. Allowed only while no password exists: the hash is
   created with `O_EXCL`, so of two concurrent bootstraps exactly one wins and the other gets
   `409 admin_already_set`.

Checks run in this order: body (`invalid_request`), session (`bad_session`, `not_allowed`), state
(`admin_already_set`, `admin_not_set`), length (`password_too_short`), then in change mode the
approver (`approver_required`, `code_required`), the argon2id slot (`rate_limited`) and the code
(`bad_code`, `code_expired`). A refused slot leaves the code unused.

`rate_limited` is also possible in every mode (see Rate limiting).

Every successful mode invalidates all sessions and publishes the `admin_set` alert. Response:
`{"mode": "bootstrap"}` (or `change`, `recovery`). No session is returned: the client unlocks
with `admin/verify`.

### `POST /api/v2/admin/reset`

Bearer only, no body. Removes the password hash and the approvers, invalidates all sessions,
kills every open console (see Console), and publishes the `admin_reset` alert if a password existed.
Idempotent: `{"was_set": false}` when there was nothing to reset. Only `calaos-os admin reset`
calls it.

### `POST /api/v2/admin/alerts/ack`

Body `{"ids": ["<alert id>", ...]}`. Marks the alerts as delivered; they are never replayed
again. Unknown ids are ignored. Answer `{"acked": <number of alerts removed>}`. calaos_server
calls it after it has recorded the alert (push sent, `EventPushNotification` stored).

### Approvers

An approver is a phone that receives push codes: a push token registered with calaos_server
(`register_push`) and a device name. There is no device secret: the approver *is* the token,
proven by a code sent to it at enrolment. A phone whose token changes (reinstall, restore, purge)
enrols again.

- Stored in `/mnt/calaos/admin/approvers.json` (0600): id (16 hex characters), token, device
  name, enrolment time. At most 2: one is enough, the second is a spare for a dead token.
- The token never leaves the daemon but through `GET admin/push/{code_id}`, to calaos_server.
- `admin/reset` removes every approver; recovery `admin/set` keeps them. A rollback restores the
  approvers of its time, with the password.
- Without an approver, the code-protected actions answer `409 approver_required`. Enrol one: the
  first needs no code. The password can also be changed over SSH with `calaos-os admin set`, which
  keeps the approvers.
- The last approver cannot be removed (`409 approver_last`): with none left, any admin session
  could enrol its own phone without a code. Replacing a phone is enrolling the new one (with a
  code from the old one), then removing the old one. A lost phone with no spare is recovered over
  SSH: `calaos-os admin reset`, then `admin set` and a new enrolment.
- An `approvers.json` that exists but cannot be read or holds an invalid entry is a lost second
  factor, not an absent one: the file stays in place, and the approver routes and the change
  `admin/set` answer `500 internal` until `calaos-os admin reset` removes it.
- Every enrolment and every removal raises a persisted alert, `approver_set` or
  `approver_removed`, naming the phone by `device_name` and `token_id`, never by token (see
  Alerts). calaos_server pushes it to every token and shows it at the next login, like
  `admin_set`: a phone enrolled first by whoever holds the admin password is seen.

`<Approver>`:

```json
{"id": "5d0c8e1f2a3b4c6d", "device_name": "Pixel 8", "token_id": "9a41f0c2d3e4b5a6", "created_at": "2026-10-23T09:12:03Z"}
```

`token_id` is the first 16 hex characters of the SHA-256 of the token: calaos_server matches it
against its `push_tokens` to show which phone is an approver.

### Push codes

A push code is the second factor of the actions below. The daemon makes it, calaos_server pushes
it to an approver, the user types it back in the webapp.

| `action` | Consumed by | Bound to `params` |
|---|---|---|
| `console_open` | `admin/codes/verify`, then `POST admin/console` | `{}` |
| `admin_password` | `POST admin/set`, change mode | `{"password": "<new password>"}` |
| `approver_enroll` | `POST admin/approvers/enroll` while an approver exists | `{}` |
| `approver_remove` | `POST admin/approvers/remove` | `{"approver_id": "<id>"}` |
| `approver_confirm` | `POST admin/approvers/confirm` | `{}`; issued by `admin/approvers/enroll` to the new phone, not requestable through `admin/codes` |
| `network_config` | `POST network/apply` | the six strings `intf`, `dhcp`, `ipv4`, `gateway`, `dns_servers`, `search_domains`, see Network configuration |

Updates, reboot, restart, services, logs, box time and every other admin action need the admin
password only.
`approver_enroll` is bound to `{}`: the client never sees the new token (calaos_server adds it),
and the new phone proves itself with its own code.

**Enrolment and calaos_server.** Clients never send or receive a push token. calaos_server
offers `approver_candidates`, its `push_tokens` entries as `{token_id, device_name, approver}`
(`token_id` computed as in `<Approver>`, `approver` true when `GET admin/approvers` lists that
`token_id`). The client picks one by `token_id`; calaos_server resolves it to the token and the
`device_name` stored with it, and sends both to `POST admin/approvers/enroll`.

**Rules**

- 6 decimal digits from the CSPRNG, valid 60 s, single use, kept in memory only (a daemon restart
  drops them). `code_id` (16 hex characters) names a code; it is not a secret.
- A code is bound to the session that requested it, its action and its `params`: the daemon keeps
  an HMAC-SHA-256 of the canonical JSON of `params` under a key drawn for that code. The route that
  consumes it rebuilds `params` from its own body. `params` holds strings only, with exactly the
  keys of the table.
- One pending code per session: a new one (request or enrolment) replaces the previous one. A
  verified grant (`admin/codes/verify`) is not pending and stays.
- At least 10 s between two codes of a session, and at most 10 codes per rolling hour for the whole
  daemon, enrolment codes included. Beyond: `429 rate_limited`, checked before any code is used.
  An enrolment that carries an approval code is only held by the hourly budget: the approval code
  already paid the interval. The interval and the budget are in memory too: a daemon restart
  resets them.
- The code is compared in constant time. A wrong code, or a code sent to another action or with
  other `params`, answers `403 bad_code` with `attempts_left`. 5 wrong codes in a session (every
  code counts, the count is kept with the session) invalidate the session: the 5th answer is
  `bad_code` with `attempts_left: 0`, the next call gets `401 bad_session`, the session's codes are
  dropped, and the admin password is needed again.
- `admin/set` checks its code (a wrong one counts) before hashing, and uses it up only with the
  write of the new hash: a failure in between leaves it usable.
- `410 code_expired`: the `code_id` is unknown to this session, expired, used, replaced or
  dropped. Not counted as a failure.
- `invalid_request` for a `code` that is not 6 ASCII digits or a `code_id` that is not 16 hex
  characters. Not counted.

**Delivery**

1. The daemon publishes `push_code {"approver_id", "code_id"}` on the event bus (see Events). It
   carries neither the token nor the code.
2. calaos_server calls `GET /api/v2/admin/push/{code_id}` (bearer only) and gets
   `{"token": "<push token>", "message": "<text>", "action": "<action>", "code": "NNNNNN", "expires_in": <seconds>}`.
   - `message` is `Calaos admin code NNNNNN: <purpose>. Valid 60 s, never share it.`, `NNNNNN` being
     the code and `<purpose>` the one of `action` in the table below.
   - `action` is one of `console_open`, `admin_password`, `approver_enroll`, `approver_remove`,
     `approver_confirm` (the enrolment code sent to the new token) or `network_config`.
   - `code` is the 6 digits and `expires_in` the seconds left before the code expires.

   | `action` | `<purpose>` |
   |---|---|
   | `console_open` | `open the web console` |
   | `admin_password` | `change the admin password` |
   | `approver_enroll` | `add an approver phone` |
   | `approver_remove` | `remove an approver phone` |
   | `approver_confirm` | `enroll this phone as the admin approver` |
   | `network_config` | `change the network configuration` |

   One fetch per code: a second one, an unknown or an expired code answers `404 not_found`.
3. calaos_server pushes one of two forms to that one token and never forwards `push_code` to a
   client:
   - a token registered with a push key gets the notice
     `Calaos admin code to <purpose>. Open the Calaos app to read it.`, built by calaos_server from
     `action` only (`<purpose>` taken from the table above, never from `message`), plus the code
     encrypted with that key as push `data` (format and test vectors in `08_http_api.md`). The code
     is not in the text;
   - a token without a push key gets `message` as received, which carries the code in clear.

   In both forms calaos_server refuses a `message` that does not have the code message shape, so a
   process squatting on the daemon's port cannot word a push.

Events are best effort: a user who receives nothing requests a new code after 10 s.

### Approver and code routes

All but `admin/push/*` are session only.

`GET admin/approvers` lists the approvers, oldest first.

`POST admin/codes`, body `{"action": "<action>", "params": {...}, "approver_id": "<id>"}`.
`params` defaults to `{}`, `approver_id` to the oldest approver. Answer
`{"code_id": "...", "approver_id": "...", "expires_in": 60}`. Errors: `invalid_request` (unknown
or reserved action, wrong `params`), `password_too_short` (`admin_password`), `approver_required`,
`approver_not_found`, `approver_limit` (`approver_enroll` while 2 exist), `approver_last`
(`approver_remove` of the only approver), `rate_limited`, `internal` (unreadable approvers).

`POST admin/codes/verify`, body `{"code_id", "code"}`, for the actions consumed outside a REST
body: today `console_open`. Any other action answers `invalid_request` and leaves the code alone.
On success the code becomes a grant, usable once within 30 s by the same session for that action,
and the answer is `{"action": "console_open", "expires_in": 30}`. Errors: `invalid_request`,
`bad_code`, `code_expired`.

`POST admin/approvers/enroll`, body `{"token": "<push token>", "device_name": "<name>",
"code_id": "...", "code": "..."}`. The token is 1 to 4096 printable ASCII characters without
space; the device name is UTF-8, 1 to 64 characters, no control character. `code_id` and `code`
are an `approver_enroll` code from an existing approver, needed only when one exists. The daemon
sends an enrolment code to the new token and answers
`{"approver_id": "<new id>", "code_id": "...", "expires_in": 60}`; the `push_code` carries the new
id. Checks: body, session, `internal` (unreadable approvers), `approver_limit`,
`approver_exists`, `rate_limited`, then the code (`code_required`, `bad_code`, `code_expired`).

`POST admin/approvers/confirm`, body `{"code_id", "code"}` with the enrolment code. Stores the
approver, raises `approver_set` and answers its `<Approver>`. Errors: `invalid_request`,
`bad_code`, `code_expired`; `approver_limit` and `approver_exists` if another enrolment ended
first; `code_required` if the enrolment started without an approver and one was enrolled since
(nobody approved it: start again with an `approver_enroll` code).

`POST admin/approvers/remove`, body `{"approver_id", "code_id", "code"}` with an
`approver_remove` code bound to that id, from any approver, the removed one included. Pending
codes sent to the removed phone die, and `approver_removed` is raised. Answer
`{"approver_id": "..."}`. Errors: `invalid_request`, `approver_not_found`, `approver_last` (checked
before the code, which stays usable), `code_required`, `bad_code`, `code_expired`.

### Console

A root login shell on the host, served by the daemon's native pty bridge. The daemon decides who
opens a console and for how long, owns the pty and observes terminal traffic directly;
calaos_server only tunnels the browser's WebSocket to the console's private Unix socket.

**Opening**

1. The client gets a `console_open` code (`POST admin/codes`) and verifies it
   (`POST admin/codes/verify`): the code becomes a grant, usable once within 30 s by that session.
2. calaos_server calls `POST admin/console` with the **same** `X-Calaos-Admin-Session`, body
   `{"code_id": "<the verified code>", "term": "xterm-256color"}`. `term` is optional, one of
   `xterm-256color` (default), `xterm`, `vt100`. Answer `<ConsoleOpened>`:

   ```json
   {"console_id": "4fbd81b12ca13a2b", "socket": "/run/calaos-container/console/console-4fbd81b12ca13a2b.sock", "ws_path": "/ws", "ws_protocol": "calaos-console.v1", "connect_within": 30, "expires_in": 3600, "idle_timeout": 900}
   ```

   Checks run in this order: body (`invalid_request`), session (`bad_session`), a free slot
   (`console_limit`), the grant (`code_expired`), the start of the private WebSocket listener
   (`internal`). Before the grant check, the grant stays usable. The grant is bound to the session
   that verified the code: another session gets `410 code_expired` and the grant stays with its
   session. A session invalidated while its console starts gets `401 bad_session` and no console.
3. The daemon raises `console_opened` (see Alerts). It starts `/bin/login -f root` only after the
   WebSocket's valid initial message. The login process gets an explicit environment, nothing of
   the daemon's: `PATH`, `HOME=/root`, `USER=root`, `LOGNAME=root`, `SHELL=/bin/bash`,
   `LANG=C.UTF-8`, plus `TERM` from the requested `term`. The working directory is `/root`. No
   credential or command argument comes from the client: the socket's mode is the gate.

**Socket**

- `/run/calaos-container/console/console-<console_id>.sock`: Unix stream socket, owner root, mode
  `0600`, in a `0700` directory owned by root. The console listens on nothing else.
- calaos_server's container bind-mounts `/run/calaos-container/console` and nothing else of
  `/run/calaos-container`, which holds the sessions. The daemon creates the directory at start and
  never removes or replaces it, since a bind mount would keep showing the old one; it deletes the
  sockets left by a previous run inside it.
- The socket is removed when its console ends.

**WebSocket protocol (`calaos-console.v1`)**

calaos_server opens a WebSocket on the socket: HTTP upgrade on `ws_path` (`/ws`), subprotocol
`ws_protocol` (`calaos-console.v1`). It only tunnels this `GET /ws` with that subprotocol; it does
not call a console activity route or interpret terminal traffic. Host and Origin are not checked.

| Direction | Message | Meaning |
|---|---|---|
| to daemon | text JSON `{"columns": <int>, "rows": <int>}` | must be the first message; both dimensions are 1..1000; starts `/bin/login -f root` in the pty |
| to daemon | text JSON `{"type": "input", "data": "<text>"}` | input to the pty |
| to daemon | text JSON `{"type": "resize", "columns": <int>, "rows": <int>}` | resize; both dimensions are 1..1000 |
| to daemon | binary bytes | raw input to the pty |
| from daemon | binary bytes | raw output from the pty |

- After initialization, malformed JSON, unknown text message types and invalid resizes are ignored.
  There are no pause/resume messages or output flow-control commands. Empty input does not rearm the
  idle timer. Input sent before the first prompt may be discarded by `login`.
- The daemon pings every 30 s. A client that does not answer with a pong within 60 s is disconnected
  (`reason=closed`).
- Output passes through a bounded queue. A slow client applies backpressure to the pty; one write
  blocked for 10 s closes the console instead of growing daemon memory without bound.
- One client per console: a second connection is refused, and when the client disconnects the
  console ends (`reason=closed`). A console is one WebSocket; it cannot be reattached.
- Final pty output is sent before the close frame. The frame's reason text is the close reason
  token, also written to the audit log as `reason=`:

| Reason | Code | Cause |
|---|---|---|
| `closed` | `1000` | the client closed the WebSocket or stopped answering pings, or a write blocked |
| `exited` | `1000` | the shell exited |
| `expired` | `1001` | the 1 h absolute limit |
| `idle` | `1001` | the idle limit |
| `not_connected` | `1001` | no client within 30 s of the open |
| `shutdown` | `1001` | the daemon stops |
| `admin_set` | `1001` | the admin password was changed |
| `admin_reset` | `1001` | the admin access was reset |
| `bad_code` | `1001` | the session entered 5 wrong codes |

**Limits**

- At most 2 consoles at once, those being opened included: `409 console_limit`.
- 1 h after the open, absolute.
- 30 s (`connect_within`) for the WebSocket to attach; otherwise the console is killed
  (`not_connected`).
- 15 min (`idle_timeout`) without terminal input, counted from attachment. Only text `input` and
  binary input rearm it: not pings, output or resize.
- 5 wrong push codes in the session that opened it kill it; every `admin/set` and `admin/reset`
  (they invalidate every session) kill every console. The end of the session's own 15 minutes does
  not: the console has its own limits.
- A daemon that stops kills its consoles; one that dies takes the login process with it
  (`SIGKILL` from the kernel on the parent's death).
- A kill sends `SIGHUP` to the login process group and closes the pty master, hanging up the
  terminal, then sends `SIGKILL` to login after 2 s. Processes the user detached from the terminal
  (`nohup`, `setsid`, `systemd-run`) survive, as after an SSH logout.

**Trace**

- `console_opened` alert at every open (see Alerts): calaos_server pushes "Console ouverte" to
  every phone.
- Audit: the open is the audit line of `POST admin/console`, with `action=console_open`,
  `code_id=` and `console=`. The end writes
  `<time> client=<client of the open> result=ok session=<audit id> action=console_close console=<id> reason=<reason> duration=<seconds> - "-"`,
  `reason` one of `closed`, `exited`, `expired`, `idle`, `not_connected`, `shutdown`, `admin_set`,
  `admin_reset`, `bad_code`. Both lines also go to journald.
- Never any terminal content in the audit log or daemon log.

**Packaging**

The native bridge needs no separate terminal-server package.

### Rate limiting

- One argon2id operation at a time (`verify` or `set`). A second one is refused at once with
  `429 rate_limited` (`retry_after: 1`) instead of waiting, so `admin_unlock` always answers
  quickly.
- `verify` failures are budgeted: 5 failures per rolling 60 s for the whole daemon. Successes do
  not count. Over budget, `verify` answers `429 rate_limited` with `retry_after` set to the
  seconds until the oldest failure leaves the window, before any hashing.
- The budget is global, not per client: every remote client arrives through calaos_server. A
  third party failing in a loop can therefore delay legitimate unlocks; this is accepted.
- Log queries have their own limits (see Logs).

### Alerts

`admin_set`, `admin_reset`, `approver_set`, `approver_removed`, `console_opened` and
`network_rolled_back` are persisted in
`/mnt/calaos/admin/` when they happen, then
published on the event bus and replayed to every new `/events` subscriber until acknowledged. A
connected subscriber that missed one (full buffer) gets it again at the next ping (30 s).
An alert raised while calaos_server is stopped is delivered when it reconnects.

Delivery is at least once: if calaos_server stops between recording and acknowledging an alert,
the alert comes back. calaos_server deduplicates on `id`. At most 32 unacknowledged alerts are
kept; the oldest goes first.

### Audit

Every `/api/v2/admin/*` call and every request carrying `X-Calaos-Admin-Session`, except those
refused with `bad_bearer` or `bad_session` (below), writes one line to `/mnt/calaos/admin/audit.log` and to stdout (journald): time, client
(`X-Calaos-Client-IP`, `local` or `invalid`), result (`ok` or the error `code`), session audit id,
then method and path (quoted, cut at 128 bytes). Never a password, a session or the bearer.

The approver and code routes add, when known, `action=`, `approver=` (id), `code_id=` and, on
`bad_code`, `attempts_left=`, before the method. Never the code, the token, the device name or
`params`.

The other routes add these fields before the method, and never a value that could carry a secret:

| Route | Fields |
|---|---|
| `POST system/services/restart` | `unit=<id>` |
| `POST logs/query` | `sources=<ids joined by +>`, `lines=`, `dir=`, `text=yes` or `text=no`; never the text |
| `POST system/time` | `tz=`, `ntp=`, `time=set`; never the time |
| `POST network/apply`, `POST network/trial` | `intf=`, `change_id=`; `code_id=` on `apply` |
| `POST network/confirm` | `change_id=` |
| `POST network/revert` | `change_id=`, `intf=`, `reason=revert` |

A rollback of a pending network change by the timeout or by a daemon restart past the deadline
writes one more line, in the form of the `console_close` line, with the `client` and the session
audit id kept in the pending record:
`<time> client=<client> result=ok session=<audit id> action=network_rollback intf=<intf> change_id=<id> reason=<reason> - "-"`,
`reason` being `timeout` or `restart`. An explicit `POST network/revert` is its own audit line,
with `reason=revert`.
`POST network/trial`, `network/confirm` and `network/revert` are audited with the bearer alone
too (the CLI), as `client=local session=-` unless `X-Calaos-Client-IP` is set.

A call refused with `bad_bearer` or `bad_session` goes to the daemon log only, at most one line per
second: anything on the host can send the first, calaos_server relays a household client's guessed
session on `update_status`, and in `audit.log` they would rotate the real entries away.

`GET admin/status` without a session is not audited (calaos_server polls it). `audit.log` is
rotated at 1 MiB, one old file kept.

## Services

The daemon reports the state of a closed list of Calaos OS units and restarts some of them. The
list is a static table in the daemon: a client names an `id`, never a unit.

| `id` | Unit | `kind` | `restartable` |
|---|---|---|---|
| `calaos-server` | `calaos-server.service` | `service` | yes |
| `calaos-home` | `calaos-home.service` | `service` | yes |
| `mosquitto` | `mosquitto.service` | `service` | yes |
| `zigbee2mqtt` | `zigbee2mqtt.service` | `service` | yes |
| `influxdb` | `influxdb.service` | `service` | yes |
| `grafana` | `grafana.service` | `service` | yes |
| `hue2mqtt` | `hue2mqtt.service` | `service` | yes |
| `envoy` | `envoy.service` | `service` | yes |
| `olad` | `olad.service` | `service` | yes |
| `timesyncd` | `systemd-timesyncd.service` | `service` | yes |
| `calaos-ddns` | `calaos-ddns.service` (run by `calaos-ddns.timer`) | `oneshot` | yes (starts it) |
| `calaos-container` | `calaos-container.service` | `service` | no |
| `haproxy` | `haproxy.service` | `service` | no |
| `networkd` | `systemd-networkd.service` | `service` | no |
| `ssh` | `ssh.service` | `service` | no |

The units that are not restartable are status only: the daemon may be running apt, haproxy carries
the admin session, and `systemd-networkd` is reloaded by the network routes under their own guard.

### `GET /api/v2/system/services`

```json
{"error": false, "msg": "ok", "data": {"services": [{"id": "mosquitto", "unit": "mosquitto.service", "kind": "service", "restartable": true, "active_state": "active", "sub_state": "running", "enabled": "enabled", "since": "2026-10-25T07:41:12Z"}]}}
```

- One entry per table row, in the order of the table above. `restartable` is a boolean, `kind`
  `"service"` or `"oneshot"`.
- `active_state` is systemd's `ActiveState` (`active`, `inactive`, `failed`, `activating`,
  `deactivating`, `reloading`), or `"not-found"` when the unit is not installed (`LoadState` is
  `not-found`). `sub_state` is systemd's `SubState`, `""` for `not-found`.
- `enabled` is systemd's `UnitFileState` string (`enabled`, `disabled`, `static`, `indirect`, ...),
  `""` when empty or `not-found`.
- `since` is when the unit last entered the `active` state (`ActiveEnterTimestamp`), RFC 3339 UTC,
  or `null` when it never did.
- The state is read in one `systemctl show` call for all units, and cached for 1 s.

### `POST /api/v2/system/services/restart`

Body `{"unit": "<id>"}`. Answer `{"id": "<id>", "state": "restarting"}`.

- `unit` is an `id` of the table with `restartable: true`; anything else, including a unit name or a
  status-only id, is `400 invalid_request` ("unknown or non-restartable service").
- `409 upgrade_in_progress` while an upgrade run is in progress, for every unit (the same guard as
  `system/reboot`).
- The daemon answers first, then restarts the unit about 500 ms later (`systemctl restart
  --no-block`, `start` for a `oneshot`), with a 10 s limit on the command. The answer says nothing
  about the outcome: a client reads it by polling `GET system/services`. A client that restarts
  `calaos-server` loses its connection within seconds.
- Audit field `unit=<id>`. `POST /api/v2/system/restart` and the legacy `POST /api/system/restart`
  (restart of the `calaos-home` kiosk) are unchanged.

## Logs

The daemon reads the systemd journal (`journalctl -o json`, as root) and returns bounded,
filtered, redacted entries. It never streams. The journal is only read.

### `GET /api/v2/logs/sources`

`{"sources": [{"id", "label"}]}`, in the order of the table below. `label` is a fixed English text,
for a client that has no translation of the `id`:

| `id` | `label` |
|---|---|
| `calaos-server` | Calaos server |
| `calaos-home` | Calaos Home (kiosk) |
| `haproxy` | haproxy |
| `mosquitto` | Mosquitto |
| `zigbee2mqtt` | Zigbee2MQTT |
| `influxdb` | InfluxDB |
| `grafana` | Grafana |
| `hue2mqtt` | hue2mqtt |
| `envoy` | Envoy |
| `calaos-container` | calaos-container |
| `olad` | OLA (olad) |
| `timesyncd` | Time sync (timesyncd) |
| `calaos-ddns` | Calaos DDNS |
| `networkd` | Network (networkd) |
| `ssh` | SSH |
| `kernel` | Kernel |
| `system` | Whole system |

Entries are matched as follows.

| `id` | Journal entries |
|---|---|
| `calaos-server`, `calaos-home`, `haproxy`, `mosquitto`, `zigbee2mqtt`, `influxdb`, `grafana`, `hue2mqtt`, `envoy` | the unit of the same name, and the container of the same name |
| `calaos-container`, `olad`, `timesyncd`, `calaos-ddns`, `networkd`, `ssh` | the unit of the Services table |
| `kernel` | kernel messages |
| `system` | no unit filter |

The haproxy process logs to a UDP port with no listener on the box: only the lines of its container
runtime appear under `haproxy`.

### `POST /api/v2/logs/query`

```json
{"sources": ["calaos-server"], "priority": "warning", "since": "2026-10-25T08:00:00Z", "until": "2026-10-25T09:00:00Z",
 "text": "wago", "lines": 200, "cursor": "<journal cursor>", "direction": "backward"}
```

Every field is optional.

| Field | Rule |
|---|---|
| `sources` | 1 to 8 ids of the list above; default `["system"]`; unknown id: `invalid_request` |
| `priority` | the maximum level shown: a name (`emerg`, `alert`, `crit`, `err`, `warning`, `notice`, `info`, `debug`) or a JSON number `0` to `7`; a numeric string such as `"3"` is refused |
| `since`, `until` | RFC 3339 times; `since` at or after `until` is `invalid_request`. The daemon parses them and passes its own formatting to `journalctl` |
| `text` | at most 256 characters, matched as a case-insensitive substring by the daemon: never a regular expression, never on a command line |
| `lines` | 1 to 1000; default 200 |
| `cursor` | a journal cursor from an earlier answer, at most 512 printable ASCII characters, passed to `journalctl` as the single argument `--after-cursor=<value>` or `--before-cursor=<value>` |
| `direction` | `backward` (default): the newest `lines` entries before `until` or `cursor`; `forward`: the oldest `lines` entries after `cursor` |

- A `backward` query without `since` covers the last 7 days. A `forward` query without `cursor` and
  without `since` is `invalid_request`.
- An unknown source, a bad `priority`, `cursor`, `lines` or time, a `text` that is too long: all
  `400 invalid_request`.

Answer:

```json
{"error": false, "msg": "ok", "data": {"entries": [{"cursor": "s=...", "ts": "2026-10-25T08:12:03.412Z", "source": "calaos-server", "unit": "calaos-server.service", "priority": 4, "pid": 812, "message": "..."}], "truncated": false, "complete": true, "next_cursor": "s=..."}}
```

- `entries` are oldest first, in both directions. `ts` is RFC 3339 UTC with milliseconds,
  `priority` an integer 0 to 7, `source` the id the entry matched, `unit` the systemd unit or
  `kernel`, `pid` is present when the journal has one.
- `message` is cut at 2048 bytes, and then the entry carries `"cut": true`. It is redacted
  (below).
- `priority` in the answer is always a number.
- The answer is at most 1 MiB: `truncated: true` when that budget cut it. The cut drops the entries
  farthest from the start point (the newest for `forward`, the oldest for `backward`).
- Every query scans at most 5000 journal entries or 5 s. `complete: false` when the scan ended
  before `lines` entries matched; the answer is still `200` with the entries read so far, never
  `internal`.
- `next_cursor` is the cursor of the last journal entry scanned in the direction of the query
  (the newest for `forward`, the oldest for `backward`), matching or not, so that a client always
  makes progress; `null` when nothing was scanned. When `truncated` is true it is the cursor of the
  last entry kept. A client continues with `cursor: next_cursor` and the same `direction`.
- To follow the journal, a client repeats a `forward` query with the `next_cursor` it received.

**Limits**

- 2 queries per second per session, one more bucket of 2 per second shared by every call without a
  session (the CLI), and 4 `journalctl` processes at a time for the whole daemon: beyond,
  `429 rate_limited` with `retry_after: 1`.
- A query that outlives 5 s is stopped, with its whole process group, and answers as above
  (`complete: false`).
- A `journalctl` failure is `500 internal`; its stderr goes to the daemon log only.

**Redaction**

The daemon masks `message` before anything leaves. It is best effort: it catches the credentials a
driver prints in a URL or a `key=value` pair, not every secret. In this order, case-insensitive:

1. userinfo in a URL, `://user:pass@` becomes `://***@`;
2. a query parameter whose name contains `pass`, `pwd`, `token`, `key`, `secret`, `session`, `auth`
   or `user`: the value becomes `***`;
3. a key followed by `=` or `:` (a JSON `"key": "value"` included), the value becoming `***`, for
   `cn_pass`, `cn_user`, `calaos_password`, `calaos_user`, `password`, `passwd`, `pass`, `pwd`,
   `secret`, `token`, `api_key`, `apikey`, `auth_token`, `mcp_token`, `mcp_service_token`,
   `smtp_password`, `influxdb_token`, `admin_pass`, `new_pass`, `admin_session`,
   `x-calaos-admin-session`, `session`, `psk`, `private_key`, `push_key` and `code`;
4. `Authorization: Bearer <value>` becomes `authorization: bearer ***`;
5. `Calaos admin code NNNNNN` becomes `Calaos admin code ******`.

The query `text` is matched against the redacted message and is never logged.

## Box time

`<BoxTime>`:

```json
{"now": "2026-10-25T09:12:03Z", "timezone": "Europe/Paris", "ntp": true, "synchronized": true, "rtc": "2026-10-25T09:12:03Z"}
```

`ntp` tells whether the NTP client (`systemd-timesyncd`) is enabled, `synchronized` whether the
clock is synchronized, `rtc` is the hardware clock or `null`.

`GET /api/v2/system/time` answers a `<BoxTime>`. `GET /api/v2/system/timezones` answers
`{"timezones": [...]}`, the output of `timedatectl list-timezones` (cached).

`POST /api/v2/system/time`, body `{"timezone": "Europe/Paris", "ntp": false, "time": "2026-10-25T09:12:03Z"}`,
each field optional, at least one (an empty body is `400 invalid_request`, "no field to set").
Answer: the new `<BoxTime>` plus `"restart_required": <bool>`, true when `timezone` changed.

- The changes apply in this order: `timezone` (it must be in the list of `timezones`), then `ntp`
  (`timedatectl set-ntp`, which starts or stops `systemd-timesyncd`), then `time`.
- `time` is RFC 3339 with a year from 2020 to 2100. It is refused with `invalid_request`
  ("ntp is active") when NTP is still on after the `ntp` step.
- Each `timedatectl` command has 10 s. The first step that fails stops the sequence and answers
  `500 internal` with `data: {"applied": ["timezone"], "failed": "ntp"}`; the steps before it stay
  applied, and `GET system/time` shows the real state.
- The daemon does not write `/etc/timezone` and sets no `TZ`: the processes of the box read the
  zone from `/etc/localtime`, and a container sees the change when it is recreated, hence
  `restart_required`.
- Audit fields `tz=`, `ntp=`, `time=set`, for the applied steps only.

Sessions, push codes, consoles and the pending network change measure their deadlines on the
monotonic clock: stepping the box time does not stretch or shorten them.

## Network configuration

A change of the network configuration is applied as a trial, and kept only when a client confirms
it from the new address: otherwise it reverts by itself, and a reboot reverts it too.

**Model** (systemd-networkd; Calaos OS has no Wi-Fi stack)

- **Trial:** `/run/systemd/network/00-calaos-trial-<intf>.network` with `[Match] Name=<intf>`.
  networkd reads `/run/systemd/network` and the `00-` prefix sorts before `calaos_*` and
  `config.network`, so the trial wins. `/run` is empty after a reboot or a power cut, which reverts
  the change.
- **Confirm:** the same content is written atomically (temporary file in the same directory,
  `fsync`, `rename`) to `/etc/systemd/network/calaos_<intf>.network`, the trial is removed and
  `networkctl reload` runs.
- **Revert** (explicit, timeout or restart past the deadline): the trial is removed, then
  `networkctl reload` and `networkctl reconfigure <intf>`.
- The daemon only writes `00-calaos-trial-<intf>.network` and `calaos_<intf>.network`. It never
  edits a file whose `[Match]` is not exactly the interface, and never touches `config.network`.
- The static template writes `DHCP=no` and a static IPv4 address, which also stops DHCPv6;
  router advertisements are still accepted.

**Pending change**

- One change at a time. It is recorded in `/run/calaos-container/network-pending.json`
  (`change_id`, `intf`, `applied_at`, `deadline`, `session_audit`, `client`), mounted in no
  container. `session_audit` is the audit id of the applying session (`-` for the CLI) and `client`
  the `X-Calaos-Client-IP` of the apply (`local` for the CLI).
- The window is 120 s from `applied_at` (`confirm_within`). A daemon that restarts re-arms the timer
  with the remaining time and publishes `network_pending` once more, with the original `applied_at`
  and `confirm_within` set to the seconds left; one that finds the deadline passed rolls back at
  once.
- At the end of the window, and when a restarted daemon finds the deadline already passed, the
  daemon reverts, deletes the record and raises the persisted alert
  `network_rolled_back {id, ts, intf}` (see Alerts).
- While a change is pending, `POST /api/v2/network/{intf}` and `POST network/trial` answer
  `409 network_pending`. The legacy `POST /api/network/{intf}` is not refused: it writes
  `/etc`, and the trial keeps winning until the confirmation or the rollback.

**Interface names**

`apply`, `confirm`, `revert`, `state`, `list` and `trial` are not valid interface names (nor `.`,
`..`): `POST network/{intf}` answers `400 invalid_request` for them, and the five static routes
below (`GET state`; `POST apply`, `confirm`, `revert` and `trial`) are registered before it, so that
`POST network/apply` with a session reaches `apply`.

### `GET /api/v2/network/state`

```json
{"error": false, "msg": "ok", "data": {"interfaces": [{"name": "eth0", "ipv4": "192.168.1.10/24", "gateway": "192.168.1.1", "mac": "...", "state": "routable", "dhcp": false, "dns_servers": ["192.168.1.1"], "search_domains": ["lan"]}], "pending": {"change_id": "5d0c8e1f2a3b4c6d", "intf": "eth0", "applied_at": "2026-10-25T09:12:03Z", "expires_in": 87}}}
```

`interfaces` holds the objects of `GET network/list`; `pending` is `null` when no change waits.
`expires_in` is the seconds left.

### `POST /api/v2/network/apply`

Session only, with a push code of the action `network_config`.

```json
{"intf": "eth0", "dhcp": false, "ipv4": "192.168.1.10/24", "gateway": "192.168.1.1",
 "dns_servers": ["192.168.1.1"], "search_domains": ["lan"], "code_id": "...", "code": "..."}
```

Answer `{"change_id": "<16 hex>", "intf": "eth0", "applied_at": "<RFC 3339 UTC>", "confirm_within": 120}`.

Checks run in this order:

1. the body (`invalid_request`): the validators of `network/{intf}`, plus: `intf` exists and is not
   the loopback; IPv4 only, with no leading zero in any octet: `ipv4` is a CIDR with an explicit
   prefix (`a.b.c.d/n`; a bare address is refused here, unlike `network/{intf}`), `gateway` and
   each `dns_servers` entry an IPv4 address; at most 4 `dns_servers` and at most 4
   `search_domains`; `ipv4` is required when `dhcp` is false; `gateway` is optional (no `Gateway=` line is written when it is empty); with
   `dhcp: true`, `ipv4`, `gateway`, `dns_servers` and `search_domains` must be absent or empty;
2. the session (`bad_session`);
3. no other pending change: `409 network_pending` with `{change_id, expires_in}`. The code stays
   usable;
4. no upgrade run: `409 upgrade_in_progress`;
5. the code: `approver_required`, `code_required`, `bad_code`, `code_expired`.

The code is bound to the **verbatim validated strings** of the body, six keys, all always present
(`""` when absent), no normalisation of an address on either side:

| Key | Value |
|---|---|
| `intf` | as sent |
| `dhcp` | `"true"` or `"false"` |
| `ipv4`, `gateway` | as sent |
| `dns_servers` | the list joined by `,` |
| `search_domains` | the list joined by `,` |

Applying: the trial is written and recorded, `networkctl reload` and `reconfigure <intf>` run, the
timer starts from `applied_at`, and `network_pending {change_id, intf, applied_at, confirm_within}`
is published. If `reload` or `reconfigure` fails the daemon reverts at once and answers
`500 internal`. The box may switch address before the answer reaches the client.

### `POST /api/v2/network/confirm`, `POST /api/v2/network/revert`

Body `{"change_id": "<16 hex>"}`. Any **live** session may call them, not only the one that applied (it may have
lost its connection with the old address and unlocked again from the new one); no code. So may the
bearer alone (the CLI).

- `confirm` makes the change permanent as above, deletes the record and publishes
  `network_applied {change_id, intf}`. Answer `{"change_id", "intf", "state": "confirmed"}`.
- `revert` reverts it and answers `{"change_id", "intf", "state": "reverted"}`.
- A `change_id` that is unknown, finished (confirmed, reverted or rolled back) or not 16 hex is
  `400 invalid_request`.

### `POST /api/v2/network/trial`

Bearer only (`403 not_allowed` with a session), never relayed. For `calaos-os network`, run as root
on the box: no code. Body as `apply` without `code_id` and `code`; same checks, trial, timer,
events, rollback and answer as `apply`. The CLI then calls `state`, `confirm` and `revert` with the
bearer alone.

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
| `error` | string | on `error`: a short summary, without apt or dpkg output (the journal has it): `interrupted`, `upgrade failed`, or `upgrade failed: <failed packages>` |
| `reboot_required` | bool | computed by the daemon from what was installed (removals excluded), not from snapshots: a kernel, firmware or microcode package (`linux-image*`, `linux-firmware*`, `firmware-linux`, `firmware-linux-nonfree`, `intel-microcode`, `amd64-microcode`, `raspberrypi-kernel`, `raspberrypi-bootloader`, `raspi-firmware`) |
| `daemon_restart` | bool | the run replaced the running calaos-container binary: the daemon exits after the terminal event and systemd starts the new binary, whose `dpkg --configure -a` finishes a half-configured package |
| `started_at`, `finished_at` | string | RFC 3339, UTC; `finished_at` once terminal |

**Rules**

- One upgrade at a time, whatever started it (versioned job, legacy synchronous route, CLI,
  kiosk). Every run is recorded as a job, so `update/status` shows a run started from the kiosk.
- While a run is in progress, `POST update/upgrade*`, `GET update/check`,
  `POST system/reboot` and `POST system/rollback_snapshot` answer `409 upgrade_in_progress`
  (with `data.job_id`).
- Legacy routes during a run keep their current answers:
  `500 {"error": true, "msg": "upgrade already in progress"}` on `upgrade*` and `update/check`,
  `"package not found"` unchanged. One deliberate exception: the legacy `POST /api/system/reboot`
  answers `409 {"error": true, "msg": "upgrade already in progress"}` during a run, so that an old
  kiosk cannot cut dpkg. Old kiosks treat any non-2xx the same way and ignore the reboot result.
  The legacy `POST /api/system/rollback_snapshot` is unchanged.
- `package` must match `^[A-Za-z0-9][A-Za-z0-9.+-]*$` (the first character is a letter or a
  digit), contain no `..` and be at most 128 characters long; otherwise `400 invalid_request`.
  The legacy `POST /api/update/upgrade?package=` applies the same rule and answers its
  `"package not found"` body. A valid name without a
  pending update gives `404 package_not_found`, and no job.
- A package on hold (`apt-mark hold`) is not an available update: `update/check` and
  `update/available` omit it, on both the versioned and the legacy routes, and `upgrade-all`
  keeps it back. `POST /api/v2/update/upgrade` on it answers `409 package_held`, and no job;
  the legacy `upgrade?package=` answers its `"package not found"` body. A hold set after the
  last check makes the job fail. A hold released after the last check keeps answering
  `409 package_held` until a new `update/check`. An apt pin with a negative priority already hides the
  version from apt itself.
- The last job is persisted in `/var/log/calaos-container/last_job.json` (0600, in a 0700
  directory owned by the daemon; neither is used through a symlink). `/var/log` is its
  own btrfs subvolume: it survives daemon restarts and reboots, and no snapshot or rollback ever
  restores it. `GET update/status` returns the running job, or the last finished one, or `null`
  if none ever ran.
- The job records the kernel boot id (`/proc/sys/kernel/random/boot_id`, else the `btime` of
  `/proc/stat`). Once the boot id differs, the daemon reports `reboot_required: false` for it: the
  reboot has happened. A job started while the previous one still requires a reboot on the same
  boot requires it too, whatever it installs.
- At start, before any route is served, a persisted job still `running` (crash, OOM kill)
  becomes `error` with `error: "interrupted"` and a `finished_at`. The interrupted dpkg run itself
  is resumed by the unit's `dpkg --configure -a`.
- Progress is published as `upgrade_progress` events, each job state change as `upgrade_job`
  (see Events). The terminal `upgrade_job` is published before the daemon exits for a
  self-upgrade. A legacy run completes (lock released, terminal `upgrade_job`) before its POST is
  answered, so the next POST of a kiosk queue never finds it in progress; the daemon exits only
  after that POST has been answered, on a connection it closes.
- After a self-upgrade, until it exits, the daemon keeps the run's lock (runs, checks and reboots
  are refused as during a run, without `job_id`) and the progress stays at its terminal `status`: the
  deferred restart of the package (CC-042) never sees it idle, so it never restarts it a second
  time.
- A daemon asked to stop while a job runs waits up to 25 s for apt to end; past that the job is
  cut and reads `interrupted` at the next start. A legacy synchronous run keeps its request in
  flight, so the HTTP shutdown waits on it unbounded: the unit's 30 s stop timeout cuts it, with
  the same outcome.

A client following a job:

1. subscribes to `/api/v2/events`;
2. `POST`s the upgrade and keeps `data.id`;
3. renders `upgrade_progress`, and stops on the `upgrade_job` with that `id` and a terminal
   `status`;
4. after any disconnection (including the daemon's own restart), reconnects with backoff and
   reads `GET update/status`: events are not replayed, the persisted job is the source of truth;
5. also reads `GET update/status` now and then (`calaos-os`: every 15 s), since a subscriber that
   falls behind loses events, the terminal `upgrade_job` included.

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
| `approver_set` | `{"id", "ts", "device_name", "token_id"}` | Alert, replayed until acknowledged: an approver was enrolled. Never the token |
| `approver_removed` | `{"id", "ts", "device_name", "token_id"}` | Alert, replayed until acknowledged: an approver was removed. Never the token |
| `console_opened` | `{"id", "ts", "session_audit_id"}` | Alert, replayed until acknowledged: a console was opened by the session with that audit id |
| `network_pending` | `{"change_id", "intf", "applied_at", "confirm_within"}` | A network change was applied and waits for its confirmation. Not replayed |
| `network_applied` | `{"change_id", "intf"}` | The pending network change was confirmed. Not replayed |
| `network_rolled_back` | `{"id", "ts", "intf"}` | Alert, replayed until acknowledged: a network change was reverted |
| `push_code` | `{"approver_id": "...", "code_id": "..."}` | Carries neither token nor code; calaos_server fetches the message with `GET /api/v2/admin/push/{code_id}`. Not replayed |

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
| `admin_set {new_pass, admin_session, code_id, code}` | `POST /api/v2/admin/set {"password", "code_id", "code"}` (change) | yes |
| `admin {action: update_status}` | `GET /api/v2/update/status` | no (household, under `secureTransport()`) |
| `admin {action: update_check}` | `GET /api/v2/update/check` | yes |
| `admin {action: update_available}` | `GET /api/v2/update/available` | yes |
| `admin {action: update_upgrade_all}` | `POST /api/v2/update/upgrade-all` | yes |
| `admin {action: update_upgrade, params: {package}}` | `POST /api/v2/update/upgrade {"package"}` | yes |
| `admin {action: system_reboot}` | `POST /api/v2/system/reboot` | yes |
| `admin {action: system_restart}` | `POST /api/v2/system/restart` | yes |
| `admin {action: system_info}` | `GET /api/v2/system/info` | yes |
| `admin {action: mcp_regenerate}` | none: handled by calaos_server (see below) | yes |
| `admin {action: approver_list}` | `GET /api/v2/admin/approvers` | yes |
| `admin {action: approver_candidates}` | none: served by calaos_server from `push_tokens` and `GET /api/v2/admin/approvers`, `[{"token_id", "device_name", "approver"}]` | yes |
| `admin {action: approver_enroll, params: {token_id, code_id, code}}` | `POST /api/v2/admin/approvers/enroll {"token", "device_name", "code_id", "code"}`; calaos_server resolves `token_id` against its `push_tokens` and sends that entry's token and stored device name. An unknown `token_id` is `invalid_request`; the client never sends a token or a device name | yes |
| `admin {action: approver_confirm, params: {code_id, code}}` | `POST /api/v2/admin/approvers/confirm {"code_id", "code"}` | yes |
| `admin {action: approver_remove, params: {approver_id, code_id, code}}` | `POST /api/v2/admin/approvers/remove {"approver_id", "code_id", "code"}` | yes |
| `admin {action: code_request, params: {code_action, code_params, approver_id}}` | `POST /api/v2/admin/codes {"action", "params", "approver_id"}`; `params` rebuilt per action from the Push codes table | yes |
| `admin {action: code_verify, params: {code_id, code}}` | `POST /api/v2/admin/codes/verify {"code_id", "code"}` | yes |
| `admin {action: console_open}` | `POST /api/v2/admin/console {"code_id", "term"}`, then the console socket (see Console); the session is the one that verified the code | yes |
| `admin {action: services_list}` | `GET /api/v2/system/services` | yes |
| `admin {action: services_restart, params: {unit}}` | `POST /api/v2/system/services/restart {"unit"}` | yes |
| `admin {action: logs_sources}` | `GET /api/v2/logs/sources` | yes |
| `admin {action: logs_query, params}` | `POST /api/v2/logs/query`, body rebuilt field by field from the fields of Logs | yes |
| `admin {action: time_get}` | `GET /api/v2/system/time` | yes |
| `admin {action: time_zones}` | `GET /api/v2/system/timezones` | yes |
| `admin {action: time_set, params: {timezone, ntp, time}}` | `POST /api/v2/system/time {"timezone", "ntp", "time"}` | yes |
| `admin {action: network_state}` | `GET /api/v2/network/state` | yes |
| `admin {action: network_apply, params: {intf, dhcp, ipv4, gateway, dns_servers, search_domains, code_id, code}}` | `POST /api/v2/network/apply` with the same fields | yes |
| `admin {action: network_confirm, params: {change_id}}` | `POST /api/v2/network/confirm {"change_id"}` | yes |
| `admin {action: network_revert, params: {change_id}}` | `POST /api/v2/network/revert {"change_id"}` | yes |

calaos_server's own calls, outside the table: `GET /api/v2/admin/status`,
`POST /api/v2/admin/alerts/ack`, `GET /api/v2/events` and `GET /api/v2/admin/push/{code_id}`.
For a console it only tunnels `GET /ws` with subprotocol `calaos-console.v1` (see Console).

Actions calaos_server serves itself while admin is set (`config get/put`, `get_mcp_info`,
`mcp_regenerate`, `config_list`, `config_set`, `config_backup`) validate the session with `GET /api/v2/admin/status` carrying
`X-Calaos-Admin-Session`, and proceed only on `200` with `admin_set: true`. For every row marked
"yes" and for these actions, a missing or empty `admin_session` from the client is answered
`bad_session` by calaos_server itself: the daemon call is never made without the header, because
without it the bearer alone authorizes.

Never relayed, whatever the client sends: `system/install/*`, `system/rollback_snapshot`,
`system/fs_status`, `network/list`, `network/trial`, `network/{intf}`, `update/installed`,
`admin/reset`, `admin/alerts/ack`, `admin/push/*` and `admin/set` with `recovery`. The daemon
refuses a session on the bearer-only ones as a second barrier.

`package` is checked by calaos_server with the same rule as the daemon (regex
`^[A-Za-z0-9.+-]+$`, first character a letter or a digit, no `..`, at most 128 characters) and
sent in the JSON body, never in the path or the query.

Every relayed call carries `X-Calaos-Client-IP`. calaos_server builds the daemon body itself from
the fields above and drops anything else the client sent.

### Relayed events

calaos_server forwards to household clients, as `admin_event {type, ts, data}`, only:

- `upgrade_progress`, `data` = `<Status>`;
- `network_pending`, with `ts`, `intf`, `change_id`, `applied_at` and `confirm_within`;
  `network_applied`, with `ts`, `intf` and `change_id`;
- `network_rolled_back`, with `ts` and `intf`, acknowledged like the other alerts and stored as the
  `EventPushNotification` shown at login for the banner; no phone push;
- `admin_set`, `admin_reset`, `approver_set`, `approver_removed` and `console_opened`, with `ts`
  only (`id` stripped, no `data`). For the approver alerts calaos_server also pushes "approver
  phone <device_name> enrolled" (or removed) to every token and stores the
  `EventPushNotification` shown at login; for `console_opened` it pushes "Console ouverte" the same
  way.

`upgrade_job` is not forwarded: a client that resumes, or sees `upgrade_progress` reach `done` or
`error`, reads the job through `update_status`. `push_code` is consumed by calaos_server,
never forwarded. Every other type is dropped.

### Error translation

| Daemon answer | calaos_server code |
|---|---|
| connection refused, timeout, invalid answer | `daemon_unreachable` or `admin_unavailable`, per `AdminState` |
| `401` without `code: bad_session`, after re-reading the token and one retry | `daemon_unreachable` |
| `404` on `GET /api/v2/admin/status` (any body), or any `/api/v2/` `404` whose body is not an envelope with a `code` | `daemon_too_old` |
| `bad_session`, `bad_password`, `rate_limited`, `password_too_short`, `admin_not_set`, `admin_already_set`, `upgrade_in_progress`, `not_allowed`, `approver_required`, `approver_limit`, `approver_exists`, `approver_not_found`, `approver_last`, `code_required`, `bad_code`, `code_expired`, `console_limit`, `network_pending` | same code |
| `invalid_request`, `package_not_found`, `package_held` | same code, with the daemon `msg` (safe to display) |
| `not_found` on a relay-table route (the path is fixed by the table, so the route does not exist in a daemon that predates it) | `daemon_too_old`, for that action only |
| `internal`, `not_found` on any other call (`admin/push/{code_id}`, ...), `method_not_allowed`, any unknown code | `internal` |

`rate_limited` copies `retry_after`; `upgrade_in_progress` copies `job_id`; `bad_code` copies
`attempts_left`; `approver_limit` and `console_limit` copy `max`; `network_pending` copies
`change_id` and `expires_in`. The calaos_server
vocabulary (`08_http_api.md`, CB0) therefore gains `invalid_request`, `package_not_found`,
`package_held` and `internal`, and cites this file's `package` rule rather than the bare regex.

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

These routes answer exactly as they did before `/api/v2/`, except that `?token=` now only
authenticates `/api/events` (see Authentication). `app/testdata/golden/` pins their status,
content type and body, and `app/legacy_golden_test.go` fails on any change. `<err>` is the raw
Go error text.

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

`POST /api/network/{intf}` and `POST /api/v2/network/{intf}` write
`/etc/systemd/network/calaos_<intf>.network` for that interface, atomically, and never the file
shared by every interface (`en*`). Their bodies and statuses are unchanged. They stay bearer only,
apply at once and have no confirmation or rollback.
The words `apply`, `confirm`, `revert`, `state`, `list` and `trial` are refused as interface names
by the validator the legacy route shares: it still answers `400`, with the raw error text of the
validator as `msg` for those six words.

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
| approvers, push codes, `push_code`, `approver_set` and `approver_removed` alerts, `admin/push/{code_id}`, code required on change `admin/set` | AP1a, released together with AP1b and WA3: `admin/p7` merges to main only then, since against an older calaos_server the change-mode `admin/set` fails (`internal`) |
| `admin/console`, the native pty WebSocket bridge and `console_opened` | CO1 |

A daemon that serves `/api/v2/` but predates ADM-CC3 answers `404 not_found` on the upgrade
routes: the CLI then upgrades through the legacy routes, as it does against a daemon without
`/api/v2/`.
