# CC-012: Network interface validation & template fix

- **Phase:** P1
- **Priority:** P0
- **Size:** M
- **Status:** Done
- **Depends on:** P0 merged
- **Blocks:** CC-051
- **Files owned (exclusive for this phase):**
  - `app/network.go`
  - `models/network.go`
  - `models/structs/network.go`

## Context

`POST /api/network/:intf` writes a systemd-networkd unit from user input with no validation:

1. **Path traversal → arbitrary file write.** `intf` comes from the URL path (`app/network.go:28`) and is interpolated into `fmt.Sprintf("/etc/systemd/network/calaos_%s.network", intf)` (`models/network.go:200`) then `os.Create`d (`:203`). An `intf` of `../../etc/cron.d/x` writes attacker-influenced content outside `/etc/systemd/network/`. The value is also passed to `networkctl status <intf>` (`:187`).
2. **Wrong template package.** The `.network` file is rendered with `html/template` (`models/network.go:6,177,211`). HTML-escaping corrupts legitimate values (`&` in a search domain becomes `&amp;`) and, critically, **does not escape newlines**.
3. **Config-directive injection.** `NetInterface` body fields (`IPv4`, `Gateway`, `DNSServers`, `SearchDomains` — `models/structs/network.go`) come straight from the JSON body (`app/network.go:29-30`). A value containing `\n` injects arbitrary `[Network]`/`[Match]` directives into the generated unit.
4. **Half-applied state.** If the file is written but `networkctl reload`/`reconfigure` fails (`models/network.go:219-232`), the system keeps the new file with no rollback — acceptable for now, but the failure must at least be surfaced clearly (it currently is returned; keep it that way).

## Work items

- [x] Validate `:intf` in the handler (`app/network.go:28`): must match `^[a-zA-Z0-9_.-]{1,15}$` (kernel IFNAMSIZ limit) **and** be a member of the actual interface list already available via `models.GetAllNetInterfaces()` (`models/network.go`, used by `GET /network/list`). Reject otherwise with 400.
- [x] Switch `html/template` → `text/template` (`models/network.go:6`); verify rendered output for a nominal config is semantically identical to before (no HTML entities).
- [x] Add a validation function for the request body before any file is touched:
  - `IPv4` — must parse with `net.ParseCIDR` (address in CIDR form) or `net.ParseIP` + separate mask, matching what the template expects.
  - `Gateway` — `net.ParseIP`.
  - `DNSServers` — each entry `net.ParseIP`.
  - `SearchDomains` — RFC-1035-ish hostname charset `^[a-zA-Z0-9.-]+$`.
  - Reject any string field containing whitespace, `\n`, `\r`, `[`, `]`, or `=` beyond the formats above — defense in depth against directive injection even through future template edits.
- [x] Return 400 with a field-specific message on validation failure (do not write the file).

## Out of scope

- Response envelope / central error handler — CC-040.
- Hardcoded `/usr/bin/networkctl` path and `/etc/systemd/network/` prefix as config options — acceptable as constants; do not spend time making them configurable.
- Rollback of a written-but-not-applied `.network` file — document behavior, don't build transactionality here.

## Breaking changes

Previously-accepted garbage bodies (invalid IPs, junk interface names) now return 400. Legitimate clients are unaffected.

## Acceptance criteria

- [x] `curl -X POST .../api/network/..%2F..%2Ftmp%2Fpwn`, `.../api/network/../foo`, and an interface name of 20 chars → 400; no file created anywhere.
- [x] Body with `"gateway": "192.168.1.1\nDNS=6.6.6.6"` → 400; no file written.
- [x] Valid static config for a real interface produces a `.network` file byte-comparable (modulo the fixed escaping bug) to the pre-change output, and `networkctl reload` is invoked.
- [x] A domain containing `&` renders literally (proves `text/template`).
- [x] `go vet` green.

## Verification notes

### What was implemented

`models/network.go` gained a validation section (all errors are `*models.ValidationError`,
`{Field, Msg}`, so the handler can map them to 400 with `errors.As`):

| Field | Rule |
| --- | --- |
| `:intf` | non-empty, ≤ 15 chars (IFNAMSIZ), `^[a-zA-Z0-9_.-]{1,15}$`, not `.`/`..`, must not start with `-`, and must appear in `GetAllNetInterfaces()` |
| `ipv4` | required unless `dhcp` is true; `netip.ParsePrefix` when it contains `/`, else `netip.ParseAddr`; must be IPv4 |
| `gateway` | optional; `netip.ParseAddr` (v4 or v6), no scope zone, no prefix length |
| `dns_servers[i]` | non-empty; `netip.ParseAddr`, no scope zone |
| `search_domains[i]` | `^[a-zA-Z0-9.-]+$`, ≤ 253 chars, labels of 1..63 chars not starting/ending with `-`, optional trailing dot |
| `ipv6`, `mac`, `state` | not rendered today, but still charset-checked so a future template edit cannot reintroduce injection |
| all of the above | no whitespace, `\n`, `\r`, `[`, `]`, `=`, `#`, `;` or control characters |

`name` in the body is ignored (it is always overwritten with the validated URL parameter).
Validation runs both in the handler (400) and at the top of `ConfigureNetInterface`
(defense in depth for any other caller) — before any `exec` or `os.Create`.

Extra hole found while writing the tests: `--all` and `-h` satisfy the regex from the ticket
and would have been passed as **options** to `networkctl status/reconfigure`. Names starting
with `-` are now rejected explicitly.

### Template swap (`html/template` → `text/template`)

Rendered both versions of the unchanged template with the same data (Debian container,
Go 1.26.5). Nominal static and DHCP configs are **byte-identical**; only values containing
HTML metacharacters differ, which is exactly the corruption being fixed:

```diff
--- nominal-static.before (html/template)   +++ nominal-static.after (text/template)
(no differences)

--- dhcp.before (html/template)             +++ dhcp.after (text/template)
(no differences)

--- amp-domain.before (html/template)
+++ amp-domain.after (text/template)
@@ -4,4 +4,4 @@
  [Network]
  Address=192.168.1.10/24
  Gateway=192.168.1.1
-Domains=a&amp;b.example.com
+Domains=a&b.example.com
```

Note the interaction with the new validation: a domain containing `&` is now rejected with
400 by the charset rule, so it can no longer reach the renderer through the API. The literal
rendering is locked down by `TestNetworkTemplateRendersLiterally` in
`models/network_validation_test.go`, which also asserts no `&amp;`/`&#` ever appears.

### Toolchain gate

Run in `ghcr.io/calaos/debian_deb_build_base:latest` (Go 1.26.5, golangci-lint 2.12.2) with
the worktree bind-mounted: `apt-get build-dep -y .`, `make build-lib`, `go build ./...`,
CLI build, `go vet ./...`, `golangci-lint run` (**0 issues**, no `.golangci.yml` change),
`make test` (race) — all green. `gofmt -l .` is clean (it also fixed the pre-existing
misalignment in `models/structs/network.go`). 49 new table-driven subtests.

### Runtime (daemon in a container, stub `/usr/bin/networkctl` logging every invocation)

| Request | Result |
| --- | --- |
| `POST /api/network/../../etc/cron.d/x` | 404 (fasthttp normalises the path away; handler never reached) |
| `POST /api/network/..%2f..%2ftmp%2fpwn` | 400 `invalid interface: must be at most 15 characters` |
| `POST /api/network/%252e%252e%252f...` | 400 (same) |
| `POST /api/network/../foo` | 404 (normalised) |
| 20-char name | 400 `must be at most 15 characters` |
| `POST /api/network/--all` | 400 `must not start with '-'` |
| `POST /api/network/eth9` (valid syntax, absent) | 400 `"eth9" does not exist` |
| `"gateway": "192.168.1.1\nDNS=6.6.6.6"` | 400 `invalid gateway: must not contain newlines` |
| `"ipv4": "192.168.1.10/24\n[Match]\nName=*"` | 400 `invalid ipv4: must not contain newlines` |
| `"dns_servers": ["9.9.9.9","evil"]` | 400 `invalid dns_servers[1]: "evil" is not a valid IP address` |
| `"search_domains": ["ok.fr\nDNS=6.6.6.6"]` | 400 `invalid search_domains[0]: must not contain newlines` |
| `"search_domains": ["a&b.example.com"]` | 400 `is not a valid domain name` |
| `{"dhcp": false}` with no `ipv4` | 400 `invalid ipv4: is required when dhcp is disabled` |

`find / -xdev` for `calaos_*`, `*.network` and `/etc/cron.d/*` before/after the whole hostile
matrix: **no new file anywhere**. `networkctl` was invoked exactly once during it
(`list --json=short`, the existence check for `eth9`) — no rejected input ever reached exec.

Nominal `POST /api/network/eth0` with `192.168.1.10/24`, gateway, 2 DNS servers and 2 search
domains → 200, and `/etc/systemd/network/calaos_eth0.network` is byte-identical (`diff`, no
output) to the pre-change `html/template` render; `networkctl status eth0 --json=pretty`,
`reload` and `reconfigure eth0` are all invoked, in that order. A DHCP body renders the
`DHCP=yes` form.

Issue 4 (half-applied state) re-checked with a stub that fails `reload`: the response is
500 `failed to reload networkd: exit status 1` and the newly written unit stays on disk. That
is unchanged, deliberate behaviour (no rollback — out of scope) and is now documented on
`ConfigureNetInterface`.

### Behaviour changes for clients

- Bodies that were silently accepted and written as junk directives (invalid/empty IPs,
  garbage domains) now return 400.
- A static (`"dhcp": false`) body without `ipv4` is now a 400 instead of writing an
  `Address=` line with an empty value.
- Configuring an interface that does not exist is now 400 instead of a 500 from `networkctl`.
- The `&`-in-a-domain case from the acceptance criteria is rejected at validation time; the
  template no longer escapes it, proven by unit test rather than through the endpoint.

### Left alone on purpose

`confFile` may come from `networkctl status`'s `NetworkFile` (which can point outside
/etc/systemd/network, e.g. a vendor unit in /usr/lib/systemd/network) — that value is not
user input, and constraining it is a behaviour change beyond this ticket.
