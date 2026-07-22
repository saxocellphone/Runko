// Tag-namespace governance at receive (§14.10.3, decided 2026-07-10; §11.4;
// DAG stage 17). refs/tags/* historically took the funnel's unconditional
// skip - the documented v1 permissiveness. Behind the org knob
// enforce_tag_policy (OrgSettings, default OFF - the loud-opt-IN sibling of
// the default-deny posture's opt-outs), a tag write requires one of:
//
//   - the anonymous deploy token (the documented v1 operator credential),
//   - a config principal flagged admin,
//   - a named principal whose org membership role is "admin" or "releaser",
//   - a bot lane whose TagAllowlist covers the tag name (§14.10.2's
//     path-scoped pattern applied to ref namespaces).
//
// Create, move, and delete all take the same gate: a re-pointed or deleted
// release tag corrupts downstream CD exactly like a forged new one. The
// release flow (stage 17b) mints its tags through authorizeTagWrite too -
// server-created tags go through the same policy code, so there is nothing
// to bypass.
package runkod

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/saxocellphone/runko/platform/receive"
)

// tagPolicyEnforced reads the org knob, nil-safe like principalByName's
// Directory/Store fallback. Unreadable settings log and read as OFF: the
// knob's default IS the permissive posture, so failing open here preserves
// documented behavior rather than silently inventing enforcement (contrast
// the merge gate, which fails closed because its default is enforcement).
func (p *Processor) tagPolicyEnforced(ctx context.Context) bool {
	var dir Directory
	switch {
	case p.Directory != nil:
		dir = p.Directory
	case p.Store != nil:
		if d, ok := p.Store.(Directory); ok {
			dir = d
		}
	}
	if dir == nil || p.OrgName == "" {
		return false
	}
	settings, err := dir.GetOrgSettings(ctx, p.OrgName)
	if err != nil {
		log.Printf("runkod: org %q settings unavailable for enforce_tag_policy (reads as off): %v", p.OrgName, err)
		return false
	}
	return settings.EnforceTagPolicy
}

// evaluateTag gates one refs/tags/* update (stage 17). Returns a skip
// verdict (accepted, never persisted - tags are git-native, the Store holds
// no row for them) or a §6.9-style scripted rejection.
func (p *Processor) evaluateTag(ctx context.Context, u RefUpdate, extraEnv []string) verdict {
	if !p.tagPolicyEnforced(ctx) {
		return verdict{update: u, skip: true}
	}
	tagName := strings.TrimPrefix(u.Ref, "refs/tags/")
	author := remoteUser(extraEnv)
	if reject := p.authorizeTagWrite(ctx, author, remoteLane(extraEnv), tagName); reject != "" {
		return verdict{update: u, decision: receive.Decision{Accepted: false, RejectionMessage: reject}}
	}
	return verdict{update: u, skip: true}
}

// authorizeTagWrite is the one tag-policy decision (§14.10.3), shared by
// the receive funnel (evaluateTag) and the release flow (stage 17b) so
// server-minted release tags and raw pushes answer to identical rules.
// author "" = the anonymous deploy token; laneName "" = not a lane push.
// Returns "" when allowed, else the full "remote: ..." rejection script.
func (p *Processor) authorizeTagWrite(ctx context.Context, author, laneName, tagName string) string {
	if laneName != "" {
		if lane := p.laneByName(laneName); lane != nil && lane.tagAllowed(tagName) {
			return ""
		}
		return fmt.Sprintf(
			"remote: bot lane %q may not write tag %q - lanes write only the tag namespaces their tags= allowlist covers.\n", laneName, tagName)
	}
	if author == "" {
		return "" // the anonymous deploy token: the documented v1 operator credential
	}
	if pr := p.principalByName(author); pr != nil && pr.Admin {
		return ""
	}
	var dir Directory
	switch {
	case p.Directory != nil:
		dir = p.Directory
	case p.Store != nil:
		dir, _ = p.Store.(Directory)
	}
	if dir != nil {
		role, member, err := dir.OrgMemberRole(ctx, p.OrgName, author)
		if err == nil && member && (role == "admin" || role == "releaser") {
			return ""
		}
	}
	return fmt.Sprintf(`remote: this org enforces tag policy - %q may not write %s.
remote:
remote: Tags here are release surface: CD keys deploys on them.
remote:   -> cut a release instead:  runko release create --project <p>
remote:   -> or ask an org admin for the "releaser" role
remote:      (release automation gets a bot lane with tags=<glob>)
`, author, "refs/tags/"+tagName)
}
