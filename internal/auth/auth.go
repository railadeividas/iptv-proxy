// Package auth holds the roles, what each role may do, and password hashing.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Roles. There is exactly one superuser: the account made on the first start.
const (
	Superuser = "superuser"
	Admin     = "admin"
	User      = "user"
)

// Perm is one thing a role may be allowed to do. Everyone signed in may browse channels
// and manage their own profiles; the rest is listed here, in one place, per role.
type Perm string

const (
	AllProfiles Perm = "profiles.all" // see and edit every user's profiles
	Sources     Perm = "sources"      // custom sources and skipped entries
	Health      Perm = "health"       // the Health page and its controls
	Settings    Perm = "settings"     // Settings pages, iptv-org sync and EPG runs
	Users       Perm = "users"        // create, edit and delete users
)

var grants = map[string][]Perm{
	User:      {},
	Admin:     {AllProfiles, Sources, Health},
	Superuser: {AllProfiles, Sources, Health, Settings, Users},
}

// Can reports whether a role has a permission.
func Can(role string, p Perm) bool {
	for _, g := range grants[role] {
		if g == p {
			return true
		}
	}
	return false
}

// Perms lists what a role may do (sent to the UI so it can hide what is off limits).
func Perms(role string) []Perm {
	return append([]Perm{}, grants[role]...)
}

// ValidRole is true for the roles that can be given to a user from the Users page.
func ValidRole(r string) bool { return r == Admin || r == User }

const iterations = 600_000

// Hash returns a salted PBKDF2-SHA256 hash of a password: "pbkdf2$iterations$salt$key".
func Hash(password string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, _ := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	return fmt.Sprintf("pbkdf2$%d$%s$%s", iterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// Verify checks a password against a Hash result.
func Verify(hash, password string) bool {
	p := strings.Split(hash, "$")
	if len(p) != 4 || p[0] != "pbkdf2" {
		return false
	}
	n, err := strconv.Atoi(p[1])
	if err != nil || n < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[2])
	want, err2 := base64.RawStdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, n, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Fingerprint is a short value that changes whenever the password does; a session is only valid
// for the password it was created with.
func Fingerprint(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return base64.RawURLEncoding.EncodeToString(sum[:6])
}

// CheckUsername allows 3 to 32 letters, digits and . _ - @
func CheckUsername(name string) error {
	if len(name) < 3 || len(name) > 32 {
		return errors.New("The user name must be 3 to 32 characters")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-@", r)) {
			return errors.New("The user name may only contain letters, digits and . _ - @")
		}
	}
	return nil
}
