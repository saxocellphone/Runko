// Package receive implements the single write funnel every Change and workspace
// snapshot passes through: policy check -> secret scan (gitleaks) -> Change
// create/update -> affected compute -> webhooks (docs/design.md §7.4, §11.5).
//
// This includes the magic-ref path (refs/for/<trunk> + Change-Id trailer, §11.5),
// the §6.9 pre-receive rejection UX for direct trunk pushes, and AgentPolicy
// enforcement at receive time (§8.4, §8.7). Per §28.1 this is a "discovery"
// component, not transcription - budget test tokens 1:1 with product tokens here.
package receive
