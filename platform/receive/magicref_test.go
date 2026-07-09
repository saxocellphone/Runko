package receive

import (
	"strings"
	"testing"
)

func TestParseMagicRef(t *testing.T) {
	cases := []struct {
		ref       string
		wantTrunk string
		wantOK    bool
	}{
		{"refs/for/main", "main", true},
		{"refs/for/release-1.0", "release-1.0", true},
		{"refs/for/", "", false},
		{"refs/heads/main", "", false},
		{"refs/for", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		trunk, ok := ParseMagicRef(c.ref)
		if trunk != c.wantTrunk || ok != c.wantOK {
			t.Errorf("ParseMagicRef(%q) = (%q, %v), want (%q, %v)", c.ref, trunk, ok, c.wantTrunk, c.wantOK)
		}
	}
}

func TestIsDirectTrunkPush(t *testing.T) {
	if !IsDirectTrunkPush("refs/heads/main", "main") {
		t.Fatalf("expected refs/heads/main to be a direct push to trunk main")
	}
	if IsDirectTrunkPush("refs/heads/feature", "main") {
		t.Fatalf("expected refs/heads/feature not to be a direct push to trunk main")
	}
	if IsDirectTrunkPush("refs/for/main", "main") {
		t.Fatalf("expected the magic ref not to be classified as a direct trunk push")
	}
}

func TestRejectDirectPushMessage(t *testing.T) {
	msg := RejectDirectPush("main", "https://runko.dev/docs/trunk")
	if !strings.Contains(msg, "git push origin HEAD:refs/for/main") {
		t.Fatalf("expected the exact next command in the message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "runko change push") {
		t.Fatalf("expected the CLI alternative in the message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "https://runko.dev/docs/trunk") {
		t.Fatalf("expected the docs URL in the message, got:\n%s", msg)
	}
}
