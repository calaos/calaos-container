package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Actions a push code can be requested for (docs/API.md, Push codes).
const (
	ActionConsoleOpen    = "console_open"
	ActionAdminPassword  = "admin_password"
	ActionApproverEnroll = "approver_enroll"
	ActionApproverRemove = "approver_remove"
	// ActionNetworkConfig is reserved: no route consumes it yet.
	ActionNetworkConfig = "network_config"

	actionApproverConfirm = "approver_confirm"

	EventPushCode = "push_code"

	CodeTTL  = 60 * time.Second
	GrantTTL = 30 * time.Second

	codeInterval = 10 * time.Second
	codeBudget   = 10
	codeWindow   = time.Hour
)

var (
	ErrCodeRequired = errors.New("push code required")
	ErrCodeExpired  = errors.New("push code unknown or expired")
	ErrNotFound     = errors.New("not found")
)

// BadCodeError is a wrong push code. AttemptsLeft reaches 0 when the session
// has just been invalidated.
type BadCodeError struct{ AttemptsLeft int }

func (e *BadCodeError) Error() string {
	return fmt.Sprintf("wrong push code, %d attempts left", e.AttemptsLeft)
}

// InvalidError is a refused client input; Msg is safe to send back.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// Params are the action parameters a code is bound to.
type Params map[string]string

// CodeRef is a code as the client sends it back.
type CodeRef struct{ ID, Code string }

// CodeInfo answers a code request.
type CodeInfo struct {
	CodeID     string `json:"code_id"`
	ApproverID string `json:"approver_id"`
	ExpiresIn  int    `json:"expires_in"`
}

// PushCodeData is the push_code event: it must never carry the token or the
// code, since every /events subscriber, legacy kiosks included, receives it.
type PushCodeData struct {
	ApproverID string `json:"approver_id"`
	CodeID     string `json:"code_id"`
}

var actionParams = map[string][]string{
	ActionConsoleOpen:    nil,
	ActionAdminPassword:  {"password"},
	ActionApproverEnroll: nil,
	ActionApproverRemove: {"approver_id"},
}

// Grant actions are consumed outside a REST body (the console WebSocket), so
// the code is verified first and the grant taken later.
var grantActions = map[string]bool{ActionConsoleOpen: true}

var codePurpose = map[string]string{
	ActionConsoleOpen:     "open the web console",
	ActionAdminPassword:   "change the admin password",
	ActionApproverEnroll:  "add an approver phone",
	ActionApproverRemove:  "remove an approver phone",
	actionApproverConfirm: "enroll this phone as the admin approver",
	ActionNetworkConfig:   "change the network configuration",
}

type pushCode struct {
	id         string
	session    string // session hash
	action     string
	digits     string
	key        []byte
	mac        []byte
	approverID string
	token      string
	message    string
	expires    time.Time
	fetched    bool
	grantUntil time.Time // set once verified, for grant actions
	enrol      *Approver // the pending approver of an enrolment code
	// enrolApproved: an existing approver's code allowed this enrolment.
	enrolApproved bool
}

type codeUse int

const (
	useConsume codeUse = iota
	useGrant           // keep it as a grant for TakeGrant
	useCheck           // keep it: the caller takes it with its write
)

func (c *pushCode) dead(now time.Time) bool {
	if !c.grantUntil.IsZero() {
		return !now.Before(c.grantUntil)
	}
	return !now.Before(c.expires)
}

// codeStore is in memory only and guarded by Manager.mu.
type codeStore struct {
	byID   map[string]*pushCode
	last   map[string]time.Time // per session hash
	issued []time.Time          // whole daemon, rolling codeWindow
}

func newCodeStore() codeStore {
	return codeStore{byID: map[string]*pushCode{}, last: map[string]time.Time{}}
}

func (cs *codeStore) prune(now time.Time) {
	for id, c := range cs.byID {
		if c.dead(now) {
			delete(cs.byID, id)
		}
	}
	for k, t := range cs.last {
		if now.Sub(t) >= codeInterval {
			delete(cs.last, k)
		}
	}
	i := 0
	for i < len(cs.issued) && now.Sub(cs.issued[i]) >= codeWindow {
		i++
	}
	cs.issued = cs.issued[i:]
}

func (cs *codeStore) dropSession(hash string) {
	for id, c := range cs.byID {
		if c.session == hash {
			delete(cs.byID, id)
		}
	}
}

// clear keeps the hourly budget: invalidating sessions must not refill it.
func (cs *codeStore) clear() {
	cs.byID = map[string]*pushCode{}
	cs.last = map[string]time.Time{}
}

func ceilSeconds(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

// checkIssue applies the hourly budget, and the per-session interval when
// interval is set.
func (cs *codeStore) checkIssue(hash string, now time.Time, interval bool) error {
	cs.prune(now)
	var wait time.Duration
	if t, ok := cs.last[hash]; ok && interval {
		wait = codeInterval - now.Sub(t)
	}
	if len(cs.issued) >= codeBudget {
		if w := cs.issued[0].Add(codeWindow).Sub(now); w > wait {
			wait = w
		}
	}
	if wait > 0 {
		return &RateLimitedError{RetryAfter: ceilSeconds(wait)}
	}
	return nil
}

// bindingMAC covers the action and the canonical JSON of params (sorted keys).
func bindingMAC(key []byte, action string, p Params) []byte {
	if p == nil {
		p = Params{}
	}
	b, _ := json.Marshal(map[string]string(p))
	h := hmac.New(sha256.New, key)
	h.Write([]byte(action))
	h.Write([]byte{0})
	h.Write(b)
	return h.Sum(nil)
}

func newDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ValidCode is the shape of a code typed back: 6 ASCII digits.
func ValidCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func checkActionParams(action string, p Params) error {
	if action == ActionNetworkConfig {
		return &InvalidError{Msg: "action not available yet: " + action}
	}
	keys, ok := actionParams[action]
	if !ok {
		return &InvalidError{Msg: "unknown action"}
	}
	if len(p) != len(keys) {
		return &InvalidError{Msg: "invalid params for " + action}
	}
	for _, k := range keys {
		if _, ok := p[k]; !ok {
			return &InvalidError{Msg: "invalid params for " + action}
		}
	}
	if action == ActionAdminPassword {
		return checkLength(p["password"])
	}
	return nil
}

// issueLocked replaces the session's pending code (a verified grant stays),
// then announces the new one. dest is the phone the code goes to.
func (m *Manager) issueLocked(hash, action string, p Params, dest Approver, enrol *Approver) (*pushCode, error) {
	now := m.now()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	digits, err := newDigits()
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if _, taken := m.codes.byID[id]; taken {
		return nil, errors.New("code id collision")
	}
	for cid, old := range m.codes.byID {
		if old.session == hash && old.grantUntil.IsZero() {
			delete(m.codes.byID, cid)
		}
	}
	c := &pushCode{
		id:         id,
		session:    hash,
		action:     action,
		digits:     digits,
		key:        key,
		mac:        bindingMAC(key, action, p),
		approverID: dest.ID,
		token:      dest.Token,
		message:    fmt.Sprintf("Calaos admin code %s: %s. Valid 60 s, never share it.", digits, codePurpose[action]),
		expires:    now.Add(CodeTTL),
		enrol:      enrol,
	}
	m.codes.byID[id] = c
	m.codes.last[hash] = now
	m.codes.issued = append(m.codes.issued, now)
	m.publish(EventPushCode, PushCodeData{ApproverID: dest.ID, CodeID: id})
	return c, nil
}

// useCodeLocked checks ref against the session, the action and params. A
// mismatch of any kind is a failure of the session; the 5th one kills the
// session and its codes.
func (m *Manager) useCodeLocked(s *session, ref CodeRef, action string, p Params, use codeUse) (*pushCode, error) {
	now := m.now()
	m.codes.prune(now)
	c := m.codes.byID[ref.ID]
	if c == nil || c.session != s.Hash || !c.grantUntil.IsZero() {
		return nil, ErrCodeExpired
	}
	ok := subtle.ConstantTimeCompare([]byte(ref.Code), []byte(c.digits)) == 1
	ok = hmac.Equal(c.mac, bindingMAC(c.key, action, p)) && ok
	if ok {
		switch use {
		case useGrant:
			c.grantUntil = now.Add(GrantTTL)
			c.digits = ""
		case useConsume:
			delete(m.codes.byID, c.id)
		}
		return c, nil
	}

	left, err := m.pushCodeFailedLocked(s)
	if err != nil {
		logging.Errorf("recording a failed push code: %v", err)
	}
	return nil, &BadCodeError{AttemptsLeft: left}
}

func (m *Manager) pushCodeFailedLocked(s *session) (int, error) {
	s.PushFailures++
	if s.PushFailures >= MaxPushCodeFailures {
		m.ended(s.Hash, "bad_code")
		m.codes.dropSession(s.Hash)
		return 0, m.sessions.remove(s)
	}
	return MaxPushCodeFailures - s.PushFailures, m.sessions.save()
}

// lockSession returns with m.mu held when the session is live.
func (m *Manager) lockSession(session string) (*session, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	m.mu.Lock()
	s := m.liveSession(session)
	if s == nil {
		m.mu.Unlock()
		return nil, ErrBadSession
	}
	return s, nil
}

// Approvers lists the approvers, oldest first, without their tokens.
func (m *Manager) Approvers(session string) ([]ApproverInfo, error) {
	if _, err := m.lockSession(session); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	if m.approvers.err != nil {
		return nil, ErrApproversUnavailable
	}
	out := make([]ApproverInfo, 0, len(m.approvers.list))
	for _, a := range m.approvers.list {
		out = append(out, a.Info())
	}
	return out, nil
}

// HasApprover reports whether a code-protected action can be authorized.
func (m *Manager) HasApprover() bool {
	return m.requireApprover() == nil
}

func (m *Manager) requireApprover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requireApproverLocked()
}

func (m *Manager) requireApproverLocked() error {
	switch {
	case m.approvers.err != nil:
		return ErrApproversUnavailable
	case len(m.approvers.list) == 0:
		return ErrApproverRequired
	}
	return nil
}

// RequestCode sends a code for action to an approver, the oldest one when
// approverID is empty.
func (m *Manager) RequestCode(session, action string, p Params, approverID string) (CodeInfo, error) {
	if err := checkActionParams(action, p); err != nil {
		return CodeInfo{}, err
	}
	s, err := m.lockSession(session)
	if err != nil {
		return CodeInfo{}, err
	}
	defer m.mu.Unlock()

	if err := m.requireApproverLocked(); err != nil {
		return CodeInfo{}, err
	}
	switch action {
	case ActionApproverRemove:
		if _, ok := m.approvers.find(p["approver_id"]); !ok {
			return CodeInfo{}, ErrApproverNotFound
		}
		if len(m.approvers.list) == 1 {
			return CodeInfo{}, ErrApproverLast
		}
	case ActionApproverEnroll:
		if len(m.approvers.list) >= MaxApprovers {
			return CodeInfo{}, ErrApproverLimit
		}
	}
	dest := m.approvers.list[0]
	if approverID != "" {
		var ok bool
		if dest, ok = m.approvers.find(approverID); !ok {
			return CodeInfo{}, ErrApproverNotFound
		}
	}
	if err := m.codes.checkIssue(s.Hash, m.now(), true); err != nil {
		return CodeInfo{}, err
	}
	c, err := m.issueLocked(s.Hash, action, p, dest, nil)
	if err != nil {
		return CodeInfo{}, err
	}
	return CodeInfo{CodeID: c.id, ApproverID: dest.ID, ExpiresIn: int(CodeTTL / time.Second)}, nil
}

// VerifyCode turns a grant action's code into a grant for TakeGrant.
func (m *Manager) VerifyCode(session string, ref CodeRef) (string, int, error) {
	s, err := m.lockSession(session)
	if err != nil {
		return "", 0, err
	}
	defer m.mu.Unlock()
	m.codes.prune(m.now())
	c := m.codes.byID[ref.ID]
	if c == nil || c.session != s.Hash || !c.grantUntil.IsZero() {
		return "", 0, ErrCodeExpired
	}
	if !grantActions[c.action] {
		return "", 0, &InvalidError{Msg: "this code is checked by the route of its action"}
	}
	if _, err := m.useCodeLocked(s, ref, c.action, nil, useGrant); err != nil {
		return "", 0, err
	}
	return c.action, int(GrantTTL / time.Second), nil
}

// TakeGrant consumes a verified code of the session for action.
func (m *Manager) TakeGrant(session, codeID, action string) error {
	s, err := m.lockSession(session)
	if err != nil {
		return err
	}
	defer m.mu.Unlock()
	m.codes.prune(m.now())
	c := m.codes.byID[codeID]
	if c == nil || c.session != s.Hash || c.grantUntil.IsZero() || c.action != action {
		return ErrCodeExpired
	}
	delete(m.codes.byID, codeID)
	return nil
}

// Enroll starts an enrolment: the code goes to the new token, which is how
// the phone proves it holds it. While an approver exists, ref must be an
// approver_enroll code from it.
func (m *Manager) Enroll(session, token, deviceName string, ref CodeRef) (CodeInfo, error) {
	s, err := m.lockSession(session)
	if err != nil {
		return CodeInfo{}, err
	}
	defer m.mu.Unlock()

	if m.approvers.err != nil {
		return CodeInfo{}, ErrApproversUnavailable
	}
	if len(m.approvers.list) >= MaxApprovers {
		return CodeInfo{}, ErrApproverLimit
	}
	if m.approvers.hasToken(token) {
		return CodeInfo{}, ErrApproverExists
	}
	approved := len(m.approvers.list) > 0
	// Before the approval code is used: a refusal here must not burn it.
	// The approval code already paid the per-session interval.
	if err := m.codes.checkIssue(s.Hash, m.now(), !approved || ref.ID == ""); err != nil {
		return CodeInfo{}, err
	}
	if approved {
		if ref.ID == "" {
			return CodeInfo{}, ErrCodeRequired
		}
		if _, err := m.useCodeLocked(s, ref, ActionApproverEnroll, nil, useConsume); err != nil {
			return CodeInfo{}, err
		}
	}

	id, err := newID()
	if err != nil {
		return CodeInfo{}, err
	}
	if _, taken := m.approvers.find(id); taken {
		return CodeInfo{}, errors.New("approver id collision")
	}
	pending := &Approver{ID: id, Token: token, DeviceName: deviceName}
	c, err := m.issueLocked(s.Hash, actionApproverConfirm, nil, *pending, pending)
	if err != nil {
		return CodeInfo{}, err
	}
	c.enrolApproved = approved
	return CodeInfo{CodeID: c.id, ApproverID: id, ExpiresIn: int(CodeTTL / time.Second)}, nil
}

// Confirm stores the approver whose enrolment code ref is, and raises the
// approver_set alert so a phone enrolled behind the owner's back is seen.
func (m *Manager) Confirm(session string, ref CodeRef) (ApproverInfo, error) {
	s, err := m.lockSession(session)
	if err != nil {
		return ApproverInfo{}, err
	}
	defer m.mu.Unlock()

	if m.approvers.err != nil {
		return ApproverInfo{}, ErrApproversUnavailable
	}
	c, err := m.useCodeLocked(s, ref, actionApproverConfirm, nil, useConsume)
	if err != nil {
		return ApproverInfo{}, err
	}
	if len(m.approvers.list) >= MaxApprovers {
		return ApproverInfo{}, ErrApproverLimit
	}
	// Started while there was no approver, and another one was enrolled
	// since: nobody approved this phone.
	if !c.enrolApproved && len(m.approvers.list) > 0 {
		return ApproverInfo{}, ErrCodeRequired
	}
	if m.approvers.hasToken(c.enrol.Token) {
		return ApproverInfo{}, ErrApproverExists
	}
	a := *c.enrol
	a.CreatedAt = m.now().UTC().Format(time.RFC3339)
	list := append(append([]Approver(nil), m.approvers.list...), a)
	if err := m.approvers.save(list); err != nil {
		return ApproverInfo{}, err
	}
	info := a.Info()
	m.raiseAlertLocked(Alert{Type: EventApproverSet, DeviceName: info.DeviceName, TokenID: info.TokenID})
	return info, nil
}

// RemoveApprover needs an approver_remove code bound to approverID.
func (m *Manager) RemoveApprover(session, approverID string, ref CodeRef) error {
	s, err := m.lockSession(session)
	if err != nil {
		return err
	}
	defer m.mu.Unlock()

	if m.approvers.err != nil {
		return ErrApproversUnavailable
	}
	removed, ok := m.approvers.find(approverID)
	if !ok {
		return ErrApproverNotFound
	}
	// With no approver left, any session could enrol its own phone without
	// a code: replacing a phone is enrol first, then remove.
	if len(m.approvers.list) == 1 {
		return ErrApproverLast
	}
	if ref.ID == "" {
		return ErrCodeRequired
	}
	if _, err := m.useCodeLocked(s, ref, ActionApproverRemove, Params{"approver_id": approverID}, useConsume); err != nil {
		return err
	}
	var list []Approver
	for _, a := range m.approvers.list {
		if a.ID != approverID {
			list = append(list, a)
		}
	}
	if err := m.approvers.save(list); err != nil {
		return err
	}
	for id, c := range m.codes.byID {
		if c.approverID == approverID && c.enrol == nil {
			delete(m.codes.byID, id)
		}
	}
	info := removed.Info()
	m.raiseAlertLocked(Alert{Type: EventApproverRemoved, DeviceName: info.DeviceName, TokenID: info.TokenID})
	return nil
}

// PushPayload is what calaos_server needs to push a code. Code is in clear so
// it can encrypt it for phones registered with a push key.
type PushPayload struct {
	Token     string `json:"token"`
	Message   string `json:"message"`
	Action    string `json:"action"`
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in"`
}

// PushPayload hands a code to calaos_server, once.
func (m *Manager) PushPayload(codeID string) (PushPayload, error) {
	if m.initErr != nil {
		return PushPayload{}, m.initErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.codes.prune(now)
	c := m.codes.byID[codeID]
	if c == nil || c.fetched || !c.grantUntil.IsZero() || !m.sessions.liveHash(now, c.session) {
		return PushPayload{}, ErrNotFound
	}
	c.fetched = true
	return PushPayload{
		Token:     c.token,
		Message:   c.message,
		Action:    c.action,
		Code:      c.digits,
		ExpiresIn: ceilSeconds(c.expires.Sub(now)),
	}, nil
}
