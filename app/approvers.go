package app

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/app/admin"
)

// codeFields is the push code a protected action carries in its body.
type codeFields struct {
	CodeID *string `json:"code_id"`
	Code   *string `json:"code"`
}

// check refuses a half-sent or malformed code before anything counts it as
// a failure.
func (f codeFields) check() error {
	if f.CodeID == nil && f.Code == nil {
		return nil
	}
	if f.CodeID == nil || f.Code == nil {
		return errInvalid("code_id and code go together")
	}
	if !admin.ValidID(*f.CodeID) {
		return errInvalid("invalid code_id")
	}
	if !admin.ValidCode(*f.Code) {
		return errInvalid("invalid code: 6 digits expected")
	}
	return nil
}

func (f codeFields) ref() admin.CodeRef {
	if f.CodeID == nil || f.Code == nil {
		return admin.CodeRef{}
	}
	return admin.CodeRef{ID: *f.CodeID, Code: *f.Code}
}

func (f codeFields) required() error {
	if err := f.check(); err != nil {
		return err
	}
	if f.CodeID == nil {
		return errInvalid("missing code_id and code")
	}
	return nil
}

// auditField adds key=value to the audit line. Values must be validated by
// the daemon first: never a code, a token, a device name or params.
func auditField(c *fiber.Ctx, key, value string) {
	if value == "" {
		return
	}
	f, _ := c.Locals(localAuditFields).([][2]string)
	c.Locals(localAuditFields, append(f, [2]string{key, value}))
}

func codeError(c *fiber.Ctx, err error) error {
	var bc *admin.BadCodeError
	if errors.As(err, &bc) {
		auditField(c, "attempts_left", strconv.Itoa(bc.AttemptsLeft))
	}
	return adminError(err)
}

func decodeBody(c *fiber.Ctx, v any) error {
	if err := json.Unmarshal(c.Body(), v); err != nil {
		return errInvalid("invalid JSON body")
	}
	return nil
}

func (a *AppServer) v2Approvers(c *fiber.Ctx) error {
	s, _ := adminSession(c)
	list, err := a.admin.Approvers(s)
	if err != nil {
		return adminError(err)
	}
	return sendOK(c, fiber.StatusOK, list)
}

func (a *AppServer) v2CodeRequest(c *fiber.Ctx) error {
	var b struct {
		Action     *string          `json:"action"`
		Params     *json.RawMessage `json:"params"`
		ApproverID *string          `json:"approver_id"`
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if b.Action == nil {
		return errInvalid("missing action")
	}
	var params admin.Params
	if b.Params != nil && string(*b.Params) != "null" {
		if err := json.Unmarshal(*b.Params, &params); err != nil {
			return errInvalid("invalid params: an object of strings expected")
		}
	}
	approverID := ""
	if b.ApproverID != nil {
		if !admin.ValidID(*b.ApproverID) {
			return errInvalid("invalid approver_id")
		}
		approverID = *b.ApproverID
	}

	s, _ := adminSession(c)
	info, err := a.admin.RequestCode(s, *b.Action, params, approverID)
	if err == nil || !errors.As(err, new(*admin.InvalidError)) {
		auditField(c, "action", *b.Action)
	}
	if err != nil {
		auditField(c, "approver", approverID)
		return adminError(err)
	}
	auditField(c, "approver", info.ApproverID)
	auditField(c, "code_id", info.CodeID)
	return sendOK(c, fiber.StatusOK, info)
}

func (a *AppServer) v2CodeVerify(c *fiber.Ctx) error {
	var b codeFields
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if err := b.required(); err != nil {
		return err
	}
	auditField(c, "code_id", *b.CodeID)

	s, _ := adminSession(c)
	action, expiresIn, err := a.admin.VerifyCode(s, b.ref())
	if err != nil {
		return codeError(c, err)
	}
	auditField(c, "action", action)
	return sendOK(c, fiber.StatusOK, struct {
		Action    string `json:"action"`
		ExpiresIn int    `json:"expires_in"`
	}{action, expiresIn})
}

func (a *AppServer) v2ApproverEnroll(c *fiber.Ctx) error {
	var b struct {
		Token      *string `json:"token"`
		DeviceName *string `json:"device_name"`
		codeFields
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if b.Token == nil || !admin.ValidToken(*b.Token) {
		return errInvalid("invalid token")
	}
	if b.DeviceName == nil || !admin.ValidDeviceName(*b.DeviceName) {
		return errInvalid("invalid device_name")
	}
	if err := b.check(); err != nil {
		return err
	}
	auditField(c, "action", admin.ActionApproverEnroll)
	auditField(c, "code_id", b.ref().ID)

	s, _ := adminSession(c)
	info, err := a.admin.Enroll(s, *b.Token, *b.DeviceName, b.ref())
	if err != nil {
		return codeError(c, err)
	}
	auditField(c, "approver", info.ApproverID)
	auditField(c, "enrol_code_id", info.CodeID)
	return sendOK(c, fiber.StatusOK, info)
}

func (a *AppServer) v2ApproverConfirm(c *fiber.Ctx) error {
	var b codeFields
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if err := b.required(); err != nil {
		return err
	}
	auditField(c, "action", "approver_confirm")
	auditField(c, "code_id", *b.CodeID)

	s, _ := adminSession(c)
	info, err := a.admin.Confirm(s, b.ref())
	if err != nil {
		return codeError(c, err)
	}
	auditField(c, "approver", info.ID)
	return sendOK(c, fiber.StatusOK, info)
}

func (a *AppServer) v2ApproverRemove(c *fiber.Ctx) error {
	var b struct {
		ApproverID *string `json:"approver_id"`
		codeFields
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if b.ApproverID == nil || !admin.ValidID(*b.ApproverID) {
		return errInvalid("invalid approver_id")
	}
	if err := b.check(); err != nil {
		return err
	}
	auditField(c, "action", admin.ActionApproverRemove)
	auditField(c, "approver", *b.ApproverID)
	auditField(c, "code_id", b.ref().ID)

	s, _ := adminSession(c)
	if err := a.admin.RemoveApprover(s, *b.ApproverID, b.ref()); err != nil {
		return codeError(c, err)
	}
	return sendOK(c, fiber.StatusOK, struct {
		ApproverID string `json:"approver_id"`
	}{*b.ApproverID})
}

// v2PushPayload is how calaos_server learns where to push a code: the token
// never travels on the event bus.
func (a *AppServer) v2PushPayload(c *fiber.Ctx) error {
	id := c.Params("code_id")
	if !admin.ValidID(id) {
		return adminError(admin.ErrNotFound)
	}
	token, message, err := a.admin.PushPayload(id)
	if err != nil {
		return adminError(err)
	}
	return sendOK(c, fiber.StatusOK, struct {
		Token   string `json:"token"`
		Message string `json:"message"`
	}{token, message})
}
