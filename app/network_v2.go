package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"
)

const (
	netConfirmWindow = 120 * time.Second
	netRetryDelay    = 10 * time.Second
	netPendingFile   = "network-pending.json"

	evNetworkPending = "network_pending"
	evNetworkApplied = "network_applied"

	codeNetworkPending = "network_pending"
)

// Test seams: the confirmation timer and the upgrade check.
var (
	netAfterFunc = func(d time.Duration, f func()) (stop func() bool) {
		return time.AfterFunc(d, f).Stop
	}
	upgradeRunning = func() bool { return runningJobID() != "" }
	netBootClock   = models.BootClock
)

// netPending is the change waiting for its confirmation, as recorded in
// /run/calaos-container so a restarted daemon still rolls it back.
type netPending struct {
	ChangeID     string    `json:"change_id"`
	Intf         string    `json:"intf"`
	AppliedAt    time.Time `json:"applied_at"`
	Deadline     time.Time `json:"deadline"`
	SessionAudit string    `json:"session_audit"`
	Client       string    `json:"client"`
	// CLOCK_BOOTTIME deadline, in ns: unlike Deadline, no clock step moves it.
	BootDeadline time.Duration `json:"boot_deadline,omitempty"`
}

// netChanges holds at most one pending change. deadline comes from a.now(),
// so it keeps the monotonic reading: stepping the box time moves nothing.
type netChanges struct {
	mu       sync.Mutex
	path     string
	cur      *netPending
	deadline time.Time
	stop     func() bool
}

type netPendingEvent struct {
	ChangeID      string `json:"change_id"`
	Intf          string `json:"intf"`
	AppliedAt     string `json:"applied_at"`
	ConfirmWithin int    `json:"confirm_within"`
}

type netAppliedEvent struct {
	ChangeID string `json:"change_id"`
	Intf     string `json:"intf"`
}

type netStateBody struct {
	ChangeID  string `json:"change_id"`
	Intf      string `json:"intf"`
	AppliedAt string `json:"applied_at"`
	ExpiresIn int    `json:"expires_in"`
}

type netDone struct {
	ChangeID string `json:"change_id"`
	Intf     string `json:"intf"`
	State    string `json:"state"`
}

// The five static routes go before network/:intf, whose validator also
// refuses their words.
func (a *AppServer) registerNetwork(v2 fiber.Router) {
	a.net = &netChanges{path: filepath.Join(adminRuntimeDir, netPendingFile)}
	s := a.sessionAllowed

	v2.Get("/network/list", bearerOnly, a.v2NetList)
	v2.Get("/network/state", s, a.v2NetState)
	// Session only, checked after the body (docs/API.md order).
	v2.Post("/network/apply", s, a.v2NetApply)
	v2.Post("/network/confirm", s, a.netAudit, a.v2NetConfirm)
	v2.Post("/network/revert", s, a.netAudit, a.v2NetRevert)
	v2.Post("/network/trial", bearerOnly, a.netAudit, a.v2NetTrial)
	v2.Post("/network/:intf", bearerOnly, a.netNotPending, a.v2NetConfigure)
}

// netAudit writes the line of a call made with the bearer alone (the CLI):
// the global middleware only audits admin routes and session calls.
func (a *AppServer) netAudit(c *fiber.Ctx) error {
	if _, present := adminSession(c); present {
		return c.Next()
	}
	path := utils.CopyString(c.Path())
	err := c.Next()
	e := admin.AuditEntry{
		Client: auditClient(c),
		Method: c.Method(),
		Path:   path,
		Result: auditResult(c, err),
	}
	if f, ok := c.Locals(localAuditFields).([][2]string); ok {
		e.Fields = f
	}
	a.admin.Audit(e)
	return err
}

func ceilSecs(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (n *netChanges) save(p netPending) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return models.WriteFileAtomic(n.path, b, 0o600)
}

func (n *netChanges) load() (*netPending, error) {
	f, err := os.OpenFile(n.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // daemon-owned path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, err
	}
	var p netPending
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if !admin.ValidID(p.ChangeID) || models.ValidateInterfaceName(p.Intf) != nil {
		return nil, fmt.Errorf("invalid pending network change %q on %q", p.ChangeID, p.Intf)
	}
	return &p, nil
}

func (a *AppServer) netClearLocked() {
	n := a.net
	if n.stop != nil {
		n.stop()
		n.stop = nil
	}
	n.cur = nil
	if err := os.Remove(n.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logging.Errorf("removing the pending network change: %v", err)
	}
}

func (a *AppServer) netArmLocked(p *netPending, left time.Duration) {
	n := a.net
	n.cur = p
	n.deadline = a.now().Add(left)
	id := p.ChangeID
	n.stop = netAfterFunc(left, func() { a.netExpired(id) })
	a.hub.Publish(evNetworkPending, netPendingEvent{
		ChangeID:      p.ChangeID,
		Intf:          p.Intf,
		AppliedAt:     rfc3339(p.AppliedAt),
		ConfirmWithin: ceilSecs(left),
	})
}

func (a *AppServer) netExpired(id string) {
	n := a.net
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cur != nil && n.cur.ChangeID == id {
		a.netRollbackLocked("timeout")
	}
}

// netRollbackLocked reverts the pending change nobody confirmed. A trial
// that cannot be removed still wins: the change stays pending and the
// rollback is retried.
func (a *AppServer) netRollbackLocked(reason string) {
	p := *a.net.cur
	err := models.RevertNetworkTrial(p.Intf)
	if errors.Is(err, models.ErrTrialKept) {
		logging.Errorf("rolling back the network change on %s: %v", p.Intf, err)
		a.netRetryLocked()
		return
	}
	a.netClearLocked()
	a.netRolledBack(p.Intf, p.ChangeID, p.Client, p.SessionAudit, reason, err)
}

// netRetryLocked keeps a change whose rollback failed: its deadline has
// passed, so the timer and every network route try again.
func (a *AppServer) netRetryLocked() {
	n := a.net
	if n.stop != nil {
		n.stop()
	}
	n.deadline = a.now()
	id := n.cur.ChangeID
	n.stop = netAfterFunc(netRetryDelay, func() { a.netExpired(id) })
}

func (a *AppServer) netRolledBack(intf, changeID, client, session, reason string, err error) {
	result := "ok"
	if err != nil {
		logging.Errorf("rolling back the network change on %s: %v", intf, err)
		result = codeInternal
	}
	a.admin.RaiseNetworkRolledBack(intf)
	a.admin.Audit(admin.AuditEntry{
		Client:  client,
		Method:  "-",
		Path:    "-",
		Result:  result,
		Session: session,
		Fields: [][2]string{
			{"action", "network_rollback"},
			{"intf", intf},
			{"change_id", changeID},
			{"reason", reason},
		},
	})
}

// netLiveLocked is the pending change, after rolling back one whose deadline
// passed before its timer ran.
func (a *AppServer) netLiveLocked() *netPending {
	n := a.net
	if n.cur != nil && !a.now().Before(n.deadline) {
		a.netRollbackLocked("timeout")
	}
	return n.cur
}

func (a *AppServer) errNetPendingLocked(p *netPending) *apiError {
	e := newAPIError(fiber.StatusConflict, codeNetworkPending, "a network change waits for its confirmation")
	e.data = fiber.Map{"change_id": p.ChangeID, "expires_in": ceilSecs(a.net.deadline.Sub(a.now()))}
	return e
}

// resumeNetChange re-arms the timer of a change recorded before a daemon
// restart, or rolls it back when its deadline has passed. A trial without a
// valid record has no deadline left to keep: it is reverted.
func (a *AppServer) resumeNetChange() {
	n := a.net
	n.mu.Lock()
	defer n.mu.Unlock()
	p, err := n.load()
	if err != nil {
		logging.Errorf("reading the pending network change: %v", err)
		_ = os.Remove(n.path)
	}
	if p == nil {
		a.netSweepLocked()
		return
	}
	// The earlier of the wall and boot deadlines, capped: stepping the box
	// time back between restarts never lengthens the window.
	left := p.Deadline.Sub(a.now())
	if now, err := netBootClock(); err == nil && p.BootDeadline > 0 && p.BootDeadline-now < left {
		left = p.BootDeadline - now
	}
	if left > netConfirmWindow {
		left = netConfirmWindow
	}
	n.cur = p
	if left <= 0 {
		a.netRollbackLocked("restart")
		return
	}
	a.netArmLocked(p, left)
}

func (a *AppServer) netSweepLocked() {
	intfs, err := models.NetworkTrials()
	if err != nil {
		logging.Errorf("listing the network trials: %v", err)
		return
	}
	for _, intf := range intfs {
		err := models.RevertNetworkTrial(intf)
		if errors.Is(err, models.ErrTrialKept) {
			logging.Errorf("reverting the network trial of %s: %v", intf, err)
			continue
		}
		a.netRolledBack(intf, "-", "-", "-", "restart", err)
	}
}

func (a *AppServer) netNotPending(c *fiber.Ctx) error {
	a.net.mu.Lock()
	defer a.net.mu.Unlock()
	if p := a.netLiveLocked(); p != nil {
		return a.errNetPendingLocked(p)
	}
	return c.Next()
}

func (a *AppServer) v2NetState(c *fiber.Ctx) error {
	nets, err := getNetInterfaces()
	if err != nil {
		return errInternal(err)
	}
	if nets == nil {
		nets = []*structs.NetInterface{}
	}
	out := struct {
		Interfaces []*structs.NetInterface `json:"interfaces"`
		Pending    *netStateBody           `json:"pending"`
	}{Interfaces: nets}

	n := a.net
	n.mu.Lock()
	if p := a.netLiveLocked(); p != nil {
		out.Pending = &netStateBody{
			ChangeID:  p.ChangeID,
			Intf:      p.Intf,
			AppliedAt: rfc3339(p.AppliedAt),
			ExpiresIn: ceilSecs(n.deadline.Sub(a.now())),
		}
	}
	n.mu.Unlock()
	return sendOK(c, fiber.StatusOK, out)
}

type netApplyBody struct {
	Intf          *string  `json:"intf"`
	DHCP          *bool    `json:"dhcp"`
	IPv4          string   `json:"ipv4"`
	Gateway       string   `json:"gateway"`
	DNSServers    []string `json:"dns_servers"`
	SearchDomains []string `json:"search_domains"`
	codeFields
}

// params are the six strings the network_config code is bound to, verbatim:
// calaos_server builds the same ones from the same fields.
func netParams(r models.NetApply) admin.Params {
	return admin.Params{
		"intf":           r.Intf,
		"dhcp":           strconv.FormatBool(r.DHCP),
		"ipv4":           r.IPv4,
		"gateway":        r.Gateway,
		"dns_servers":    strings.Join(r.DNSServers, ","),
		"search_domains": strings.Join(r.SearchDomains, ","),
	}
}

func parseNetApply(c *fiber.Ctx) (models.NetApply, codeFields, error) {
	var b netApplyBody
	if err := decodeBody(c, &b); err != nil {
		return models.NetApply{}, b.codeFields, err
	}
	if b.Intf == nil {
		return models.NetApply{}, b.codeFields, errInvalid("missing intf")
	}
	if b.DHCP == nil {
		return models.NetApply{}, b.codeFields, errInvalid("missing dhcp")
	}
	r := models.NetApply{
		Intf:          *b.Intf,
		DHCP:          *b.DHCP,
		IPv4:          b.IPv4,
		Gateway:       b.Gateway,
		DNSServers:    b.DNSServers,
		SearchDomains: b.SearchDomains,
	}
	if err := models.ValidateNetApply(r); err != nil {
		return r, b.codeFields, errInvalid(validationMsg(err))
	}
	if err := b.check(); err != nil {
		return r, b.codeFields, err
	}

	nets, err := getNetInterfaces()
	if err != nil {
		return r, b.codeFields, errInternal(err)
	}
	for _, n := range nets {
		if n != nil && n.Name == r.Intf {
			if n.IsLoopback {
				return r, b.codeFields, errInvalid("invalid interface: the loopback cannot be configured")
			}
			return r, b.codeFields, nil
		}
	}
	return r, b.codeFields, errInvalid(fmt.Sprintf("invalid interface: %q does not exist", r.Intf))
}

func (a *AppServer) v2NetApply(c *fiber.Ctx) error { return a.netStart(c, true) }

func (a *AppServer) v2NetTrial(c *fiber.Ctx) error { return a.netStart(c, false) }

// netStart runs apply (withCode) and its bearer-only alias trial.
func (a *AppServer) netStart(c *fiber.Ctx, withCode bool) error {
	r, code, err := parseNetApply(c)
	if err != nil {
		return err
	}
	session, present := adminSession(c)
	if withCode && !present {
		return errBadSession()
	}
	auditField(c, "intf", r.Intf)

	n := a.net
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := a.netLiveLocked(); p != nil {
		return a.errNetPendingLocked(p)
	}
	if upgradeRunning() {
		return errUpgradeInProgress(nil)
	}
	sessionAudit := "-"
	if withCode {
		if err := a.admin.UseCode(session, admin.ActionNetworkConfig, netParams(r), code.ref()); err != nil {
			auditField(c, "code_id", code.ref().ID)
			return codeError(c, err)
		}
		sessionAudit = admin.SessionAuditID(session)
	}

	p, err := a.netApplyLocked(r, sessionAudit, auditClient(c))
	if err != nil {
		return errInternal(err)
	}
	auditField(c, "change_id", p.ChangeID)
	if withCode {
		auditField(c, "code_id", code.ref().ID)
	}
	return sendOK(c, fiber.StatusOK, netPendingEvent{
		ChangeID:      p.ChangeID,
		Intf:          p.Intf,
		AppliedAt:     rfc3339(p.AppliedAt),
		ConfirmWithin: int(netConfirmWindow / time.Second),
	})
}

// netApplyLocked records the change before the trial exists, so a crash at
// any point leaves at most a record whose rollback finds nothing to remove.
func (a *AppServer) netApplyLocked(r models.NetApply, sessionAudit, client string) (*netPending, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	now := a.now()
	p := &netPending{
		ChangeID:     hex.EncodeToString(raw),
		Intf:         r.Intf,
		AppliedAt:    now.UTC(),
		Deadline:     now.Add(netConfirmWindow).UTC(),
		SessionAudit: sessionAudit,
		Client:       client,
	}
	if boot, err := netBootClock(); err == nil {
		p.BootDeadline = boot + netConfirmWindow
	}
	if err := a.net.save(*p); err != nil {
		return nil, err
	}
	if err := models.WriteNetworkTrial(r.Intf, r.Config()); err != nil {
		a.netClearLocked()
		return nil, err
	}
	if err := models.ApplyNetworkTrial(r.Intf); err != nil {
		rerr := models.RevertNetworkTrial(r.Intf)
		if rerr != nil {
			logging.Errorf("reverting the failed network change on %s: %v", r.Intf, rerr)
		}
		if errors.Is(rerr, models.ErrTrialKept) {
			a.net.cur = p
			a.netRetryLocked()
		} else {
			a.netClearLocked()
		}
		return nil, err
	}
	a.netArmLocked(p, netConfirmWindow)
	return p, nil
}

// netFindLocked is the pending change named by the body.
func (a *AppServer) netFindLocked(c *fiber.Ctx) (*netPending, error) {
	var b struct {
		ChangeID *string `json:"change_id"`
	}
	if err := decodeBody(c, &b); err != nil {
		return nil, err
	}
	if b.ChangeID == nil || !admin.ValidID(*b.ChangeID) {
		return nil, errInvalid("invalid change_id")
	}
	auditField(c, "change_id", *b.ChangeID)
	p := a.netLiveLocked()
	if p == nil || p.ChangeID != *b.ChangeID {
		return nil, errInvalid("unknown or finished change_id")
	}
	return p, nil
}

func (a *AppServer) v2NetConfirm(c *fiber.Ctx) error {
	n := a.net
	n.mu.Lock()
	defer n.mu.Unlock()
	p, err := a.netFindLocked(c)
	if err != nil {
		return err
	}
	// A failed copy keeps the change pending: the timer still reverts it.
	if err := models.ConfirmNetworkTrial(p.Intf); err != nil {
		return errInternal(err)
	}
	done := *p
	a.netClearLocked()
	a.hub.Publish(evNetworkApplied, netAppliedEvent{ChangeID: done.ChangeID, Intf: done.Intf})
	return sendOK(c, fiber.StatusOK, netDone{ChangeID: done.ChangeID, Intf: done.Intf, State: "confirmed"})
}

func (a *AppServer) v2NetRevert(c *fiber.Ctx) error {
	n := a.net
	n.mu.Lock()
	defer n.mu.Unlock()
	p, err := a.netFindLocked(c)
	if err != nil {
		return err
	}
	done := *p
	auditField(c, "intf", done.Intf)
	auditField(c, "reason", "revert")
	rerr := models.RevertNetworkTrial(done.Intf)
	// A trial still in place keeps the change pending: the timer retries.
	if !errors.Is(rerr, models.ErrTrialKept) {
		a.netClearLocked()
	}
	if rerr != nil {
		return errInternal(rerr)
	}
	return sendOK(c, fiber.StatusOK, netDone{ChangeID: done.ChangeID, Intf: done.Intf, State: "reverted"})
}
