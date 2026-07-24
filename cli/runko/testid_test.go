package main

import (
	"crypto/sha1"
	"encoding/hex"
)

// fakeChangeID returns a deterministic valid Change-Id for tests: "I" followed
// by exactly 40 lowercase hex characters (hex of sha1(seed)). The shape is
// I + [0-9a-f]{40}. Same seed always yields the same id so a test can mint
// once and assert the same value later.
func fakeChangeID(seed string) string {
	sum := sha1.Sum([]byte(seed))
	return "I" + hex.EncodeToString(sum[:])
}
