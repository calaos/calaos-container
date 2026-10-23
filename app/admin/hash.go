package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// HashParams are the argon2id cost parameters used for new hashes. Existing
// hashes are verified with the parameters they were created with.
type HashParams struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// DefaultHashParams is the low end of the plan's memory range: Calaos OS also
// runs on RPi 3 class boxes with 1 GiB of RAM, where the pure-Go argon2 takes
// seconds and the GC would otherwise hold two arenas at once.
var DefaultHashParams = HashParams{MemoryKiB: 32 * 1024, Time: 3, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32

	// Bounds on a stored hash, so a corrupt file cannot make verify allocate
	// gigabytes.
	maxMemoryKiB = 256 * 1024
	maxTime      = 10
	maxThreads   = 4
)

var (
	b64 = base64.RawStdEncoding

	errBadHash = errors.New("malformed admin password hash")
)

// hashPassword returns a PHC string: $argon2id$v=19$m=..,t=..,p=..$salt$key.
func hashPassword(p HashParams, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	debug.FreeOSMemory()
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, errBadHash
	}
	p, err := parseParams(parts[3])
	if err != nil {
		return false, err
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errBadHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(key))) //nolint:gosec // len(key) <= 64
	// Hands the arena back at once, so two hashes in a row never peak at 2x m.
	debug.FreeOSMemory()
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

func parseParams(s string) (HashParams, error) {
	var p HashParams
	fields := strings.Split(s, ",")
	if len(fields) != 3 {
		return p, errBadHash
	}
	vals := make([]uint64, 3)
	for i, prefix := range []string{"m=", "t=", "p="} {
		v, ok := strings.CutPrefix(fields[i], prefix)
		if !ok {
			return p, errBadHash
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return p, errBadHash
		}
		vals[i] = n
	}
	if vals[2] < 1 || vals[2] > maxThreads || vals[1] < 1 || vals[1] > maxTime ||
		vals[0] < 8*vals[2] || vals[0] > maxMemoryKiB {
		return p, errBadHash
	}
	return HashParams{MemoryKiB: uint32(vals[0]), Time: uint32(vals[1]), Threads: uint8(vals[2])}, nil //nolint:gosec // bounded above
}

// fingerprint ties a session to the password hash it was issued for, so a
// session outlives neither a password change nor a reset, even if the
// invalidation could not be written.
func fingerprint(encoded string) string {
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:16])
}
