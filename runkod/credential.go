// Credential hashing for self-service principals (§15.1 sign-up). Stored
// principals hold a PBKDF2-HMAC-SHA256 hash, never a plaintext token -
// unlike operator principals (--principal), whose shared-secret tokens
// live in daemon config and are compared directly (auth.go).
//
// Stdlib only (crypto/pbkdf2, Go 1.24+): the lean-dependency posture holds
// even here - no bcrypt/argon2 module for a v1 interim identity layer that
// OIDC eventually replaces (§15.1).
package runkod

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// credentialIterations follows OWASP's PBKDF2-HMAC-SHA256 guidance. The
// per-request cost is amortized by Server's verify cache (auth.go): the
// full derivation runs once per (name, password) pair per process, not on
// every API call carrying Basic credentials.
const credentialIterations = 600_000

// hashCredential returns "pbkdf2-sha256$<iterations>$<salt>$<dk>" with a
// fresh random salt - self-describing so iterations can be raised later
// without invalidating existing rows.
func hashCredential(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, credentialIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		credentialIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// verifyCredential checks password against an encoded hash in constant
// time (over the derived keys).
func verifyCredential(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// credCache remembers successfully verified (name, password) pairs so the
// expensive derivation runs once per process, not on every request - a
// browser session sends Basic credentials on EVERY call. Values are
// HMAC-SHA256 digests of the password keyed by a random per-process key
// (never the password itself); lookups compare in constant time.
type credCache struct {
	mu  sync.Mutex
	key []byte
	ok  map[string][]byte // name -> digest of the verified password
}

func (c *credCache) digest(password string) []byte {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(password))
	return mac.Sum(nil)
}

func (c *credCache) hit(name, password string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok == nil {
		return false
	}
	want, found := c.ok[name]
	return found && hmac.Equal(c.digest(password), want)
}

func (c *credCache) remember(name, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == nil {
		c.key = make([]byte, 32)
		if _, err := rand.Read(c.key); err != nil {
			return // cache disabled; verification still works, just slower
		}
	}
	if c.ok == nil || len(c.ok) > 1024 {
		c.ok = make(map[string][]byte)
	}
	c.ok[name] = c.digest(password)
}
