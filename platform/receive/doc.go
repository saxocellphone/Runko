// Package receive implements the single write funnel every Change and workspace
// snapshot passes through: policy check -> secret scan (gitleaks) -> Change
// create/update -> affected compute -> webhooks (docs/design.md §7.4, §11.5).
//
// This includes the magic-ref path (refs/for/<trunk> + Change-Id trailer, §11.5),
// the §6.9 pre-receive rejection UX for direct trunk pushes, and AgentPolicy
// enforcement at receive time (§8.4, §8.7). Per §28.1 this is a "discovery"
// component, not transcription - budget test tokens 1:1 with product tokens here.
//
// Scope boundary (current session): Decide() and its supporting pure logic
// (Change-Id, magic-ref, policy, rejection UX) are fully implemented and
// tested. Out of scope so far: an actual git pre-receive hook / server
// wiring Decide() to real pushes, and a real SecretScanner (gitleaks/
// trufflehog) - see secretscan.go. CreateOrUpdateChange wires accepted
// decisions to Postgres via internal/dbgen but is unverified against a live
// database, same caveat as index.Sync and stage 2.
package receive
