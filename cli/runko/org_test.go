package main

import "testing"

// TestHubBase pins the org verbs' URL derivation: a stored login points at
// the org MOUNT after signup/org-create, but the org APIs live at the hub
// root - appending /api/orgs to the mount 404ed the moment onboarding
// completed (onboarding journey suite, 2026-07-17).
func TestHubBase(t *testing.T) {
	cases := map[string]string{
		"https://host/o/acme":         "https://host",
		"https://host/o/acme/":        "https://host",
		"https://host":                "https://host",
		"https://host/":               "https://host",
		"http://127.0.0.1:8091/o/a-b": "http://127.0.0.1:8091",
	}
	for in, want := range cases {
		if got := hubBase(in); got != want {
			t.Fatalf("hubBase(%q) = %q, want %q", in, got, want)
		}
	}
}
