package receive

import "testing"

func TestParseChangeID(t *testing.T) {
	msg := "Reject invalid SKUs\n\nSome body text.\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567\n"
	id, ok := ParseChangeID(msg)
	if !ok {
		t.Fatalf("expected to find a Change-Id")
	}
	if id != "I0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("unexpected id: %s", id)
	}
}

func TestParseChangeIDAbsent(t *testing.T) {
	if _, ok := ParseChangeID("just a subject line\n"); ok {
		t.Fatalf("expected no Change-Id to be found")
	}
}

func TestParseChangeIDRejectsMalformed(t *testing.T) {
	cases := []string{
		"Change-Id: notthecorrectshape\n",
		"Change-Id: I0123\n",                                     // too short
		"change-id: I0123456789abcdef0123456789abcdef01234567\n", // wrong case
	}
	for _, c := range cases {
		if _, ok := ParseChangeID(c); ok {
			t.Fatalf("expected malformed trailer to be rejected: %q", c)
		}
	}
}

func TestGenerateChangeIDIsDeterministic(t *testing.T) {
	a := GenerateChangeID("seed-1")
	b := GenerateChangeID("seed-1")
	if a != b {
		t.Fatalf("expected same seed to produce same id, got %s vs %s", a, b)
	}
	c := GenerateChangeID("seed-2")
	if a == c {
		t.Fatalf("expected different seeds to produce different ids")
	}
	if _, ok := ParseChangeID("Change-Id: " + a + "\n"); !ok {
		t.Fatalf("generated id %q does not match the trailer format it must satisfy", a)
	}
}

func TestEnsureChangeIDPreservesExisting(t *testing.T) {
	msg := "Fix bug\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567\n"
	id, newMsg := EnsureChangeID(msg, "unused-seed")
	if id != "I0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected existing id preserved, got %s", id)
	}
	if newMsg != msg {
		t.Fatalf("expected message unchanged, got %q", newMsg)
	}
}

func TestEnsureChangeIDAppendsWhenMissing(t *testing.T) {
	msg := "Add checkout retry logic"
	id, newMsg := EnsureChangeID(msg, "tree-sha-author-timestamp")
	if id == "" {
		t.Fatalf("expected a generated id")
	}
	gotID, ok := ParseChangeID(newMsg)
	if !ok || gotID != id {
		t.Fatalf("expected the appended trailer to parse back to %s, got %s (ok=%v) from message:\n%s", id, gotID, ok, newMsg)
	}

	// Same seed must be reproducible (§28.2 rule 3: no real randomness).
	id2, _ := EnsureChangeID(msg, "tree-sha-author-timestamp")
	if id2 != id {
		t.Fatalf("expected deterministic id for the same seed, got %s vs %s", id, id2)
	}
}
