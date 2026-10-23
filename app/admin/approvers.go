package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxApprovers      = 2
	MaxTokenBytes     = 4096
	MaxDeviceNameLen  = 64 // Unicode code points
	approversFileName = "approvers.json"
)

var (
	ErrApproverRequired = errors.New("no approver enrolled")
	ErrApproverLimit    = errors.New("too many approvers")
	ErrApproverExists   = errors.New("token already an approver")
	ErrApproverNotFound = errors.New("unknown approver")
	ErrApproverLast     = errors.New("last approver")
	// ErrApproversUnavailable: approvers.json exists but cannot be used.
	ErrApproversUnavailable = errors.New("the approvers file is unreadable, run calaos-os admin reset")
)

// Approver is a phone that receives push codes. The token is the only proof
// of the phone: there is no device secret.
type Approver struct {
	ID         string `json:"id"`
	Token      string `json:"token"`
	DeviceName string `json:"device_name"`
	CreatedAt  string `json:"created_at"`
}

// ApproverInfo is what leaves the daemon: never the token.
type ApproverInfo struct {
	ID         string `json:"id"`
	DeviceName string `json:"device_name"`
	TokenID    string `json:"token_id"`
	CreatedAt  string `json:"created_at"`
}

func (a Approver) Info() ApproverInfo {
	sum := sha256.Sum256([]byte(a.Token))
	return ApproverInfo{ID: a.ID, DeviceName: a.DeviceName, TokenID: hex.EncodeToString(sum[:])[:16], CreatedAt: a.CreatedAt}
}

// ValidToken accepts what calaos_server stores from register_push: printable
// ASCII, so it can go into a push request without escaping surprises.
func ValidToken(t string) bool {
	if t == "" || len(t) > MaxTokenBytes {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] <= ' ' || t[i] >= 0x7f {
			return false
		}
	}
	return true
}

func ValidDeviceName(n string) bool {
	if !utf8.ValidString(n) || strings.TrimSpace(n) == "" || utf8.RuneCountInString(n) > MaxDeviceNameLen {
		return false
	}
	return strings.IndexFunc(n, unicode.IsControl) < 0
}

// ValidID matches approver and code ids: 16 lowercase hex characters.
func ValidID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func newID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

type approverFile struct {
	Approvers []Approver `json:"approvers"`
}

// approverStore is guarded by Manager.mu. err is the load error, kept until
// a reset removes the file.
type approverStore struct {
	path string
	list []Approver
	err  error
}

func (as *approverStore) load() error {
	b, found, err := readSmallFile(as.path, 64*1024)
	if err != nil || !found {
		return err
	}
	var f approverFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	ids, tokens := map[string]bool{}, map[string]bool{}
	for _, a := range f.Approvers {
		if !ValidID(a.ID) || !ValidToken(a.Token) || !ValidDeviceName(a.DeviceName) || ids[a.ID] || tokens[a.Token] {
			return errors.New("invalid approver entry")
		}
		ids[a.ID], tokens[a.Token] = true, true
	}
	if len(f.Approvers) > MaxApprovers {
		return errors.New("too many approvers")
	}
	as.list = f.Approvers
	return nil
}

// save keeps memory equal to the file once the rename is done: a removed
// phone must not keep approving because only the directory sync failed.
func (as *approverStore) save(list []Approver) error {
	b, err := json.Marshal(approverFile{Approvers: list})
	if err != nil {
		return err
	}
	tmp, err := writeTemp(as.path, b)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, as.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	as.list = list
	if err := syncDir(filepath.Dir(as.path)); err != nil {
		logging.Warnf("syncing the admin directory: %v", err)
	}
	return nil
}

func (as *approverStore) find(id string) (Approver, bool) {
	for _, a := range as.list {
		if a.ID == id {
			return a, true
		}
	}
	return Approver{}, false
}

func (as *approverStore) hasToken(token string) bool {
	for _, a := range as.list {
		if a.Token == token {
			return true
		}
	}
	return false
}

func (as *approverStore) wipe() error {
	if err := os.Remove(as.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	as.list = nil
	as.err = nil
	return nil
}
