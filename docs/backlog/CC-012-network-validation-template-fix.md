# CC-012: Network interface validation & template fix

- **Phase:** P1
- **Priority:** P0
- **Size:** M
- **Status:** Backlog
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

- [ ] Validate `:intf` in the handler (`app/network.go:28`): must match `^[a-zA-Z0-9_.-]{1,15}$` (kernel IFNAMSIZ limit) **and** be a member of the actual interface list already available via `models.GetAllNetInterfaces()` (`models/network.go`, used by `GET /network/list`). Reject otherwise with 400.
- [ ] Switch `html/template` → `text/template` (`models/network.go:6`); verify rendered output for a nominal config is semantically identical to before (no HTML entities).
- [ ] Add a validation function for the request body before any file is touched:
  - `IPv4` — must parse with `net.ParseCIDR` (address in CIDR form) or `net.ParseIP` + separate mask, matching what the template expects.
  - `Gateway` — `net.ParseIP`.
  - `DNSServers` — each entry `net.ParseIP`.
  - `SearchDomains` — RFC-1035-ish hostname charset `^[a-zA-Z0-9.-]+$`.
  - Reject any string field containing whitespace, `\n`, `\r`, `[`, `]`, or `=` beyond the formats above — defense in depth against directive injection even through future template edits.
- [ ] Return 400 with a field-specific message on validation failure (do not write the file).

## Out of scope

- Response envelope / central error handler — CC-040.
- Hardcoded `/usr/bin/networkctl` path and `/etc/systemd/network/` prefix as config options — acceptable as constants; do not spend time making them configurable.
- Rollback of a written-but-not-applied `.network` file — document behavior, don't build transactionality here.

## Breaking changes

Previously-accepted garbage bodies (invalid IPs, junk interface names) now return 400. Legitimate clients are unaffected.

## Acceptance criteria

- [ ] `curl -X POST .../api/network/..%2F..%2Ftmp%2Fpwn`, `.../api/network/../foo`, and an interface name of 20 chars → 400; no file created anywhere.
- [ ] Body with `"gateway": "192.168.1.1\nDNS=6.6.6.6"` → 400; no file written.
- [ ] Valid static config for a real interface produces a `.network` file byte-comparable (modulo the fixed escaping bug) to the pre-change output, and `networkctl reload` is invoked.
- [ ] A domain containing `&` renders literally (proves `text/template`).
- [ ] `go vet` green.

## Verification notes

_(filled by the implementing agent)_
