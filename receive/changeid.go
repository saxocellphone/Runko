package receive

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
)

// changeIDTrailer matches a Gerrit-style Change-Id trailer line, e.g.
// "Change-Id: I0123456789abcdef0123456789abcdef01234567" - an 'I' prefix plus
// 40 hex characters, Gerrit's own convention, adopted per §7.4/§11.5 for
// stable change identity that survives rebase/amend.
var changeIDTrailer = regexp.MustCompile(`(?m)^Change-Id: (I[0-9a-f]{40})$`)

// ParseChangeID extracts the Change-Id trailer from a commit message, if present.
func ParseChangeID(message string) (string, bool) {
	m := changeIDTrailer.FindStringSubmatch(message)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// GenerateChangeID deterministically derives a new Change-Id from seed
// material (e.g. tree SHA + author + timestamp, caller's choice), the same
// shape Gerrit's commit-msg hook produces. Deterministic so it's reproducible
// in tests without real randomness (§28.2 rule 3).
func GenerateChangeID(seed string) string {
	sum := sha1.Sum([]byte(seed))
	return "I" + hex.EncodeToString(sum[:])
}

// EnsureChangeID returns the message unchanged (with its existing Change-Id)
// if one is already present; otherwise it appends a freshly generated one as
// a trailing paragraph, matching §11.5: "pushes without one create a fresh
// Change." Mirrors what a commit-msg hook installed by `runko doctor` (§6.9)
// would have done client-side, but enforced server-side since client hooks
// are advisory only.
func EnsureChangeID(message string, seed string) (changeID string, newMessage string) {
	if id, ok := ParseChangeID(message); ok {
		return id, message
	}
	id := GenerateChangeID(seed)
	trimmed := strings.TrimRight(message, "\n")
	return id, trimmed + "\n\nChange-Id: " + id + "\n"
}
