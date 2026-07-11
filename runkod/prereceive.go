package runkod

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/receive"
)

// zeroOID is the all-zeros object id git's pre-receive hook uses in the
// old-sha slot for a brand-new ref.
const zeroOID = "0000000000000000000000000000000000000000"

// emptyTreeOID is git's well-known empty-tree object, present in every Git
// repository - the standard way to diff "everything in a brand-new ref"
// (there's no real old commit to diff against).
const emptyTreeOID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// RefUpdate is one line of real pre-receive hook stdin: "<old-sha> <new-sha>
// <ref-name>", git's own documented format - this is what makes the wiring
// in this file "an actual git pre-receive hook" rather than a simulation.
type RefUpdate struct {
	OldSHA, NewSHA, Ref string
}

// ParseRefUpdates parses pre-receive hook stdin.
func ParseRefUpdates(r io.Reader) ([]RefUpdate, error) {
	var updates []RefUpdate
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("runkod: malformed pre-receive line %q", line)
		}
		updates = append(updates, RefUpdate{OldSHA: fields[0], NewSHA: fields[1], Ref: fields[2]})
	}
	return updates, scanner.Err()
}

// RefResult is one ref-update's verdict, in the shape a pre-receive hook
// needs: Message is printed to the pushing client ("remote: ..." lines).
type RefResult struct {
	Ref      string
	Accepted bool
	Message  string
	ChangeID string // set when Accepted and this was a magic-ref push
}

// verdict is one ref's pure decision, computed before any Store writes -
// kept separate from committing so a whole batch can be evaluated first and
// persisted only if EVERY ref in it is accepted (real git pre-receive hooks
// are all-or-nothing across every ref in one push; a hook can't selectively
// apply half of a push).
type verdict struct {
	update       RefUpdate
	skip         bool // ref outside every sanctioned shape - accepted unconditionally, never persisted
	isSnapshot   bool // refs/workspaces/<id>/* - policed (scan + caps + registry), but no Change row
	decision     receive.Decision
	changedPaths []string
	extraEnv     []string
	author       string // pushing principal's name (REMOTE_USER); "" for the anonymous deploy token
	evalErr      string // set on an I/O failure evaluating this ref (git diff/log failed)
	advice       string // advisory remote: lines printed on ACCEPTED pushes (e.g. the near-cap split nudge)
	// origin* is push provenance for magic-ref pushes (§12.2's branch ↔
	// stack mapping): the workspace branch the pusher declared via push
	// options, validated against the registry before acceptance.
	originWorkspace string
	originBranch    string
}

func (v verdict) accepted() bool { return v.evalErr == "" && (v.skip || v.decision.Accepted) }

// Processor wires receive.Decide to a real bare repo + Store - the
// "pre-receive wiring" this stage's DAG entry names explicitly.
type Processor struct {
	RepoDir  string
	TrunkRef string
	Scanner  receive.SecretScanner
	Store    Store
	// Directory, when set (org-scoped processors, orghub.go), resolves
	// store-backed pusher identities from the SERVER-GLOBAL account view
	// instead of this org's own Store (principal.go).
	Directory Directory
	// RequireChangeWorkspace refuses refs/for pushes that declare no
	// (validated) workspace origin - changes are born in workspaces
	// (2026-07-09). The production default; cmd/runkod's
	// --allow-workspaceless-changes is the loud opt-out for the eval
	// profile, where the loop must work before any workspace exists.
	RequireChangeWorkspace bool
	Now                    func() time.Time
	// RootInvalidationPatterns mirrors runko-ci affected's own
	// --root-invalidation flag (org policy, §14.5.2) - without it, every
	// push through this daemon computed affected with the hardcoded empty
	// Options{}, silently ignoring any org root-invalidation config.
	RootInvalidationPatterns []string
	// ZoektIndexWorker, if set, is triggered whenever an accepted push
	// updates the trunk ref itself - see zoekt.go's doc comment for why
	// that's currently unreachable in practice (trunk never advances
	// through this daemon yet) but is the semantically correct hook point.
	// Nil-safe: ZoektIndexWorker.Trigger no-ops on a nil receiver.
	ZoektIndexWorker *ZoektIndexWorker
	// Mirror, when configured, is triggered after any accepted funnel push
	// - change refs just moved and the outbound mirror (§18.6 M1) carries
	// them too. Nil-safe like the zoekt worker.
	Mirror *MirrorWorker
	// OrgName stamps outgoing webhook envelopes' org_id (the org NAME, not
	// a UUID - consumers want the /o/<name> path segment). With one
	// daemon-wide --webhook-url shared by every org's OutboxWorker, an
	// unstamped envelope is unattributable (docs/migration-findings.md #13).
	OrgName string
	// MaxSnapshotDiffBytes caps the total content bytes one workspace
	// snapshot push may introduce - §12.2's backstop against build
	// artifacts/dependency trees (node_modules, target/, .venv) entering
	// snapshot commits when .gitignore hygiene fails. 0 means
	// DefaultMaxSnapshotBytes; negative disables the cap.
	MaxSnapshotDiffBytes int64
	// Principals is the same registry Server.Principals carries (§15.1
	// interim, stage 12c). The funnel resolves the pushing principal from
	// the forwarded REMOTE_USER env: agent principals get their
	// AgentPolicy enforced at receive (§8.7 - the enforcement stage 6
	// built and nothing fed until now), workspace snapshots become
	// owner-only, and accepted Changes record authored_by.
	Principals []Principal
	// BotLanes is the same registry Server.BotLanes carries (§14.10.2). The
	// funnel resolves a lane push from the forwarded REMOTE_LANE env - set
	// beside (never as) REMOTE_USER, because lanes are not principals - and
	// consults it in exactly one place: the §14.10.3 tags gate (stage 17),
	// where a lane may write the tag namespaces its TagAllowlist covers.
	BotLanes []BotLane
}

// DefaultMaxSnapshotBytes is the default snapshot size cap (§12.2): generous
// enough for any real source-code WIP, small enough that a node_modules or a
// bazel output tree slams into it immediately.
const DefaultMaxSnapshotBytes = 32 << 20 // 32 MiB

// ProcessBatch evaluates every ref update in one push, then - only if ALL
// are accepted - persists Changes and enqueues webhooks for the ones the
// funnel actually governs. This mirrors real git pre-receive semantics: one
// hook invocation's exit status decides the WHOLE push, atomically.
//
// extraEnv is forwarded from the invoking pre-receive hook's own process
// env (GIT_OBJECT_DIRECTORY / GIT_ALTERNATE_OBJECT_DIRECTORIES) - git's
// object quarantine stores an incoming push's new objects in a temporary
// area exposed via those two vars ONLY on the hook process; since this
// Processor runs inside the daemon (a different process the hook forwards
// to over HTTP, hook.go), it cannot see quarantined objects at all without
// them being explicitly passed through and merged into every git
// subprocess this evaluation shells out to.
func (p *Processor) ProcessBatch(ctx context.Context, updates []RefUpdate, extraEnv []string) []RefResult {
	verdicts := make([]verdict, len(updates))
	allAccepted := true
	for i, u := range updates {
		v := p.evaluate(ctx, u, extraEnv)
		verdicts[i] = v
		if !v.accepted() {
			allAccepted = false
		}
	}

	results := make([]RefResult, len(updates))
	for i, v := range verdicts {
		switch {
		case v.skip:
			results[i] = RefResult{Ref: v.update.Ref, Accepted: true}
		case !allAccepted:
			results[i] = RefResult{Ref: v.update.Ref, Accepted: false, Message: rejectionMessage(v)}
		case v.isSnapshot:
			// An accepted snapshot IS the durability (§12.2: the commit chain
			// on refs/workspaces/<id>/head is the content store) - no Change
			// row, no webhook; the registry row already exists.
			results[i] = RefResult{Ref: v.update.Ref, Accepted: true,
				Message: fmt.Sprintf("remote: workspace snapshot -> %s\n", v.update.Ref)}
		default:
			results[i] = p.commit(ctx, v)
		}
	}
	return results
}

// Process is ProcessBatch for the common single-ref push - what a real
// `git push origin HEAD:refs/for/main` or `git push origin main` sends.
func (p *Processor) Process(ctx context.Context, u RefUpdate, extraEnv []string) RefResult {
	return p.ProcessBatch(ctx, []RefUpdate{u}, extraEnv)[0]
}

// evaluate runs receive.Decide for one ref update without writing to Store.
// Four policed ref shapes: refs/heads/<trunk> (rejected, §6.9),
// refs/for/<trunk> (the Change funnel), refs/workspaces/<id>/* (snapshot
// refs, §12.2 - policed since stage 12b), and refs/tags/* (§14.10.3
// tag-namespace governance, stage 17 - permissive until the org's
// enforce_tag_policy knob flips, see tags.go). Everything else is still
// marked skip - accepted unconditionally.
func (p *Processor) evaluate(ctx context.Context, u RefUpdate, extraEnv []string) verdict {
	if wsID, isSnapshot := SnapshotRefWorkspaceID(u.Ref); isSnapshot {
		// Branch segment must be one conservative path segment (§12.2's
		// workspace-branches rule) - rejecting refs/workspaces/x/a/b here
		// keeps the namespace unambiguous instead of leaving nested refs
		// half-supported.
		if _, _, validBranch := SnapshotRefParts(u.Ref); !validBranch {
			return verdict{update: u, isSnapshot: true, decision: receive.Decision{
				Accepted:         false,
				RejectionMessage: fmt.Sprintf("remote: %q is not a valid snapshot ref - workspace branches are refs/workspaces/<id>/<branch>, one segment of letters, digits, dots, dashes, underscores (\"head\" is the default)\n", u.Ref),
			}}
		}
		return p.evaluateSnapshot(ctx, u, wsID, extraEnv)
	}
	if strings.HasPrefix(u.Ref, "refs/changes/") {
		// Server-owned namespace (§14.4.4): the daemon writes
		// refs/changes/<id>/head itself on every accepted push (commit()),
		// and the Store's head_sha is keyed to it. A client writing here
		// directly desynchronizes git from the Store - checkout-by-Change
		// and diffs silently follow the wrong commit (2026-07-08
		// clean-slate dogfood finding: §14.10.3's tag permissiveness was
		// covering this namespace too).
		return verdict{update: u, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: %s is server-owned - change refs are written by runkod, never pushed\nremote:   -> push your commit to refs/for/%s instead\n",
				u.Ref, p.TrunkRef),
		}}
	}
	if strings.HasPrefix(u.Ref, "refs/tags/") {
		// §14.10.3 tag-namespace governance (stage 17): gated behind the
		// org's enforce_tag_policy knob; permissive (today's documented
		// behavior) while it's off. See tags.go.
		return p.evaluateTag(ctx, u, extraEnv)
	}
	isTrunkPush := u.Ref == "refs/heads/"+p.TrunkRef
	_, isMagicRef := receive.ParseMagicRef(u.Ref)
	if !isTrunkPush && !isMagicRef {
		return verdict{update: u, skip: true}
	}

	author := remoteUser(extraEnv)
	originWS, originBranch, originWorkspace, originVerdict := p.resolveOrigin(ctx, u, extraEnv, author)
	if originVerdict != nil {
		return *originVerdict
	}

	// Changes are born in workspaces (decided 2026-07-09, superseding the
	// 2026-07-08 "recorded provenance, never an identity constraint"
	// stance): a refs/for push must declare a registered workspace origin
	// - resolveOrigin above already validated the claim (registered +
	// owner-bound), so enforcement here is just "a claim must exist".
	// `runko change push` from an attached worktree stamps it
	// automatically; plain git needs `-o workspace=<id>`. One structural
	// exemption: an UNBORN trunk (a brand-new monorepo's bootstrap/import
	// push) - workspaces need a base revision, so requiring one before
	// the first landing deadlocks every new org.
	if isMagicRef && p.RequireChangeWorkspace && originWS == "" {
		if _, err := p.runGit(extraEnv, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.TrunkRef); err == nil {
			return verdict{update: u, author: author, decision: receive.Decision{
				Accepted: false,
				RejectionMessage: "remote: changes are born in workspaces - this push declares no workspace origin (§12.2)\n" +
					"remote:   -> runko workspace create --name <n> --project <p> ... (or workspace attach <id>), then `runko change push` from that worktree\n" +
					"remote:   -> plain git: git push -o workspace=<id> " + u.Ref + "\n",
			}}
		}
	}

	// First-ever push to this magic ref arrives with old == zero; the
	// push's real delta is against trunk, not the empty tree (the
	// evaluateSnapshot fix's sibling - pre-fix, policy judged the pusher
	// as authoring the entire repository, e.g. "modifies owners" via
	// trunk's own manifests). Unborn trunk keeps the empty-tree base.
	diffBase := u.OldSHA
	if diffBase == zeroOID {
		if mb, mbErr := p.runGit(extraEnv, "merge-base", u.NewSHA, "refs/heads/"+p.TrunkRef); mbErr == nil && strings.TrimSpace(mb) != "" {
			diffBase = strings.TrimSpace(mb)
		}
	}
	changedPaths, files, err := p.diff(diffBase, u.NewSHA, extraEnv)
	if err != nil {
		return verdict{update: u, evalErr: fmt.Sprintf("remote: could not inspect push: %v\n", err)}
	}
	msg, err := p.commitMessage(u.NewSHA, extraEnv)
	if err != nil {
		return verdict{update: u, evalErr: fmt.Sprintf("remote: could not read commit message: %v\n", err)}
	}

	req := receive.PushRequest{
		Ref: u.Ref, TrunkRef: p.TrunkRef, CommitMessage: msg,
		Files: files, ChangedPaths: changedPaths,
		ChangeIDSeed: u.NewSHA,
	}
	if pr := p.principalByName(author); pr != nil && pr.IsAgent {
		// Stage 12c: the first wire-level feed for the AgentPolicy
		// enforcement stage 6 built (§8.7). DiffBytes is the sum of changed
		// files' full content - an over-count of the true diff, i.e. the
		// conservative direction for a cap.
		//
		// A refs/for push that declares a workspace origin (push options,
		// resolveOrigin above) carries that workspace's write allowlist as
		// affinity - the claim is server-validated AND owner-bound, so it
		// is exactly the workspace-affine write §8.7 wants agents making.
		// `runko change push` from an attached worktree stamps the options
		// automatically; a bare push without them still correctly refuses
		// a RequireWorkspaceAffinity policy. (Found live on the first real
		// agent-token run: agents could snapshot but never SUBMIT - the
		// policy refused their change pushes even from their own
		// workspace, because this branch predated validated provenance.)
		// Size caps are enforced PER CHANGE after series resolution
		// (enforcePerChangeCaps below), never against the whole push: a
		// push is often a stack, and measuring the stack's SUM against a
		// per-change cap punished exactly the splitting the cap exists to
		// encourage - ten small stacked changes tripped the same wall one
		// monolith did. Affinity/denylist/owners stay whole-push here
		// (the union of the members' paths - equivalent and cheaper).
		wholePush := pr.Policy
		wholePush.MaxChangedFiles, wholePush.MaxDiffBytes = 0, 0
		req.Principal = receive.Principal{IsAgent: true, Policy: wholePush}
		req.DiffBytes = totalContentBytes(files)
		req.ModifiesOwners = modifiesOwners(changedPaths)
		if originWS != "" && author != "" && originWorkspace.Owner == author {
			req.WorkspaceAffinity = originWorkspace.WriteAllowlist
		}
	}
	decision := receive.Decide(req, p.Scanner)
	if decision.Accepted && isMagicRef {
		// Landed is terminal (§7.4): a push carrying an already-landed
		// Change-Id must not zombie the landed row (new head on a landed
		// Change, stable ref overwritten) - Gerrit's "change is closed".
		// Abandoned is different: re-push is exactly how it reopens.
		if existing, known, err := p.Store.GetChange(ctx, decision.ChangeID); err != nil {
			return verdict{update: u, evalErr: fmt.Sprintf("remote: could not look up change %s: %v\n", decision.ChangeID, err)}
		} else if known && existing.State == "landed" {
			return verdict{update: u, decision: receive.Decision{
				Accepted: false,
				RejectionMessage: fmt.Sprintf("remote: change %s has already landed - landed is terminal (§7.4)\nremote:   -> start new work as a fresh change: drop the Change-Id trailer (or `runko change create`) and push again\n",
					decision.ChangeID),
			}}
		}
	}
	v := verdict{
		update: u, changedPaths: changedPaths, extraEnv: extraEnv, author: author,
		originWorkspace: originWS, originBranch: originBranch,
		decision: decision,
	}
	if decision.Accepted && isMagicRef && originWS != "" {
		if rej := p.enforceOneStackPerBranch(ctx, v); rej != nil {
			return *rej
		}
	}
	if decision.Accepted && isMagicRef {
		if rej := p.enforcePerChangeCaps(ctx, &v); rej != nil {
			return *rej
		}
	}
	return v
}

// enforcePerChangeCaps applies the agent policy's size caps to each series
// member's OWN delta (commit vs first parent) - the per-change measurement
// that makes the cap pro-stacking: one over-cap change is refused BY NAME
// with the split workflow in the refusal, while the same content as a
// stack of small changes passes. Below the hard cap, a change over HALF
// of it earns an advisory line on the accepted push (git relays remote:
// lines on success) - the nudge arrives before the wall does.
func (p *Processor) enforcePerChangeCaps(ctx context.Context, v *verdict) *verdict {
	pr := p.principalByName(v.author)
	if pr == nil || !pr.IsAgent {
		return nil
	}
	maxFiles, maxBytes := pr.Policy.MaxChangedFiles, pr.Policy.MaxDiffBytes
	if maxFiles == 0 && maxBytes == 0 {
		return nil
	}
	caps := receive.AgentPolicy{MaxChangedFiles: maxFiles, MaxDiffBytes: maxBytes}

	var advice strings.Builder
	var prev *seriesMember
	var prevPaths []string
	for _, m := range p.seriesMembers(ctx, *v, v.decision) {
		m := m
		parent := m.sha + "^"
		if _, err := p.runGit(v.extraEnv, "rev-parse", "--verify", "-q", parent); err != nil {
			parent = emptyTreeOID
		}
		paths, files, err := p.diff(parent, m.sha, v.extraEnv)
		if err != nil {
			rej := *v
			rej.evalErr = fmt.Sprintf("remote: could not measure change %s: %v\n", m.changeID, err)
			return &rej
		}
		bytes := totalContentBytes(files)

		// DAG nudge (advisory, agents): a stacked step that touches
		// nothing its parent step touches is often not a dependency at
		// all - as PARALLEL branches the two would review and land
		// independently instead of the upper one waiting out the lower.
		// Top-level-directory disjointness is a deliberately quiet proxy
		// (shared prefix = silence): it errs toward not nagging, and the
		// wording stays conditional because file overlap is not semantic
		// dependence.
		if prev != nil && disjointTopDirs(prevPaths, paths) {
			fmt.Fprintf(&advice, "remote: note: change %s (%q) touches nothing %s (%q) touches - if they are independent, put them on PARALLEL branches so neither waits for the other (runko workspace branch <name>; jj: a separate `jj new 'main@origin'` line per change)\n",
				m.changeID, m.title, prev.changeID, prev.title)
		}
		prev, prevPaths = &m, paths

		if violations := receive.EvaluatePolicy(caps, receive.PushSummary{ChangedFiles: paths, DiffBytes: bytes}); len(violations) > 0 {
			rej := *v
			var b strings.Builder
			fmt.Fprintf(&b, "remote: change %s (%q) is too big as ONE change (§8.7 agent policy):\n", m.changeID, m.title)
			for _, viol := range violations {
				fmt.Fprintf(&b, "remote:   %s\n", viol.Message)
			}
			b.WriteString("remote:   -> split it into a stack of smaller changes - one reviewable step each (jj split, or jj new between steps)\n")
			b.WriteString("remote:   -> one `runko change push` still pushes the whole stack; smaller changes scope checks narrower and land faster (§7.4)\n")
			rej.decision = receive.Decision{Accepted: false, RejectionMessage: b.String()}
			return &rej
		}

		if (maxFiles > 0 && len(paths)*2 > maxFiles) || (maxBytes > 0 && bytes*2 > maxBytes) {
			fmt.Fprintf(&advice, "remote: note: change %s (%q) touches %d files / %d bytes - over half the agent cap; consider splitting into a stack (§7.4)\n",
				m.changeID, m.title, len(paths), bytes)
		}
	}
	v.advice = advice.String()
	return nil
}

// disjointTopDirs reports whether two changed-path sets share NO top-level
// directory - the stack-shape advisory's orthogonality proxy. Empty sets
// are never "disjoint" (nothing to conclude from a rename-only or
// unmeasurable member).
func disjointTopDirs(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	tops := map[string]bool{}
	for _, p := range a {
		top, _, _ := strings.Cut(p, "/")
		tops[top] = true
	}
	for _, p := range b {
		top, _, _ := strings.Cut(p, "/")
		if tops[top] {
			return false
		}
	}
	return true
}

// enforceOneStackPerBranch is §12.2's "one workspace branch ↔ one stack"
// as an INVARIANT (2026-07-09), not just an expectation: a push claiming
// (workspace, branch) must carry every open Change of that origin in its
// series - amends, restacks, and grows all do naturally (the series walk
// spans trunk..tip, so stacking on the open head includes it); a fresh
// trunk-based line does not, and would render as a SECOND stack under one
// branch in every view (observed live: two agents sharing one owner
// account pushed unrelated work through the same workspace, and the inbox
// and workspace pages disagreed about what the branch held).
func (p *Processor) enforceOneStackPerBranch(ctx context.Context, v verdict) *verdict {
	open, err := p.Store.ListChanges(ctx, "open")
	if err != nil {
		rej := v
		rej.evalErr = fmt.Sprintf("remote: could not check the branch's open stack: %v\n", err)
		return &rej
	}
	var blocking []Change
	for _, c := range open {
		if c.OriginWorkspace == v.originWorkspace && c.OriginBranch == v.originBranch {
			blocking = append(blocking, c)
		}
	}
	if len(blocking) == 0 {
		return nil
	}
	inSeries := map[string]bool{}
	for _, m := range p.seriesMembers(ctx, v, v.decision) {
		inSeries[m.changeID] = true
	}
	for _, c := range blocking {
		if inSeries[c.ChangeKey] {
			continue
		}
		rej := v
		rej.decision = receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: workspace branch %s/%s already carries an open stack including %s (%q) - one branch, one stack (§12.2)\n"+
				"remote:   -> restack onto it and push your branch TIP so the whole stack rides along (jj rebase; runko workspace sync)\n"+
				"remote:   -> or abandon it: runko change abandon --change %s\n"+
				"remote:   -> or open a parallel line: runko workspace branch <name>\n",
				v.originWorkspace, v.originBranch, c.ChangeKey, firstLine(c.Title), c.ChangeKey),
		}
		return &rej
	}
	return nil
}

// pushOptions reads the push options git receive-pack exposed to the
// pre-receive hook (GIT_PUSH_OPTION_COUNT / GIT_PUSH_OPTION_<n>) out of the
// forwarded env - the same transport quarantine vars and REMOTE_USER ride.
func pushOptions(extraEnv []string) []string {
	byIndex := map[string]string{}
	for _, kv := range extraEnv {
		rest, ok := strings.CutPrefix(kv, "GIT_PUSH_OPTION_")
		if !ok {
			continue
		}
		idx, val, ok := strings.Cut(rest, "=")
		if !ok || idx == "COUNT" {
			continue
		}
		byIndex[idx] = val
	}
	opts := make([]string, 0, len(byIndex))
	for i := 0; ; i++ {
		val, ok := byIndex[fmt.Sprintf("%d", i)]
		if !ok {
			break
		}
		opts = append(opts, val)
	}
	return opts
}

// resolveOrigin extracts and validates the §12.2 workspace-branch
// provenance a magic-ref push may declare via push options
// (`workspace=<id>`, `workspace-branch=<name>`; `runko change push` stamps
// them from its worktree's own runko.workspace/runko.branch config).
// Returns a rejection verdict when the declared workspace doesn't exist or
// belongs to someone else - a wrong claim is a misconfigured worktree (or a
// spoof), and recording it silently would pin the Change to the wrong stack
// in every view; fail loud, same posture as snapshot pushes. No options is
// fine: provenance is advisory, plain git stays a first-class pusher.
func (p *Processor) resolveOrigin(ctx context.Context, u RefUpdate, extraEnv []string, author string) (wsID, branch string, ws Workspace, rejection *verdict) {
	for _, opt := range pushOptions(extraEnv) {
		if v, ok := strings.CutPrefix(opt, "workspace="); ok {
			wsID = v
		}
		if v, ok := strings.CutPrefix(opt, "workspace-branch="); ok {
			branch = v
		}
	}
	if wsID == "" {
		return "", "", Workspace{}, nil
	}
	if branch == "" {
		branch = "head" // the §12.2 default branch
	}
	ws, registered, err := p.Store.GetWorkspace(ctx, wsID)
	if err != nil {
		return "", "", Workspace{}, &verdict{update: u, evalErr: fmt.Sprintf("remote: workspace lookup failed: %v\n", err)}
	}
	if !registered {
		return "", "", Workspace{}, &verdict{update: u, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: this push declares workspace %q as its origin, but no such workspace is registered\nremote:   -> re-attach with `runko workspace attach <id>`, or unset runko.workspace in this worktree's git config\n",
				wsID),
		}}
	}
	if author != "" && ws.Owner != "" && author != ws.Owner {
		return "", "", Workspace{}, &verdict{update: u, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: workspace %q belongs to %s - a Change cannot claim someone else's workspace as its origin (§12.2)\nremote:   -> unset runko.workspace in this worktree's git config, or attach your own workspace\n",
				wsID, ws.Owner),
		}}
	}
	if ws.Status == "closed" {
		return "", "", Workspace{}, &verdict{update: u, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: workspace %q is closed - its task concluded, and one workspace carries one task (§12.2)\nremote:   -> start the new task in a fresh workspace: runko workspace create --name <new> --project <p> ... --by <you>\n",
				wsID),
		}}
	}
	return wsID, branch, ws, nil
}

func totalContentBytes(files []receive.FileContent) int64 {
	var total int64
	for _, f := range files {
		total += int64(len(f.Content))
	}
	return total
}

// modifiesOwners reports whether any changed path can alter ownership
// resolution (§7.3's two sources: an OWNERS file or a PROJECT.yaml's
// owners field - conservatively, ANY manifest edit counts, since parsing
// the before/after owners here would duplicate the indexer).
func modifiesOwners(changedPaths []string) bool {
	for _, path := range changedPaths {
		base := path
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			base = path[i+1:]
		}
		if base == "OWNERS" || base == "PROJECT.yaml" {
			return true
		}
	}
	return false
}

// evaluateSnapshot polices a workspace snapshot push (§12.2's "policy and
// secret scan apply BEFORE durability", closing the unconditional-accept gap
// the 12b DAG row names): the workspace must be registered, the pushing
// principal (when named, §15.1's interim registry) must be its owner, an
// agent principal's policy applies with the workspace's own affinity, the
// snapshot's content must pass the same secret scanner Changes go through,
// and total introduced bytes must fit the §12.2 size-cap backstop. An
// anonymous deploy-token push still bypasses the owner check - that token
// IS the "everyone" credential until it's retired (eval profile).
func (p *Processor) evaluateSnapshot(ctx context.Context, u RefUpdate, wsID string, extraEnv []string) verdict {
	ws, registered, err := p.Store.GetWorkspace(ctx, wsID)
	if err != nil {
		return verdict{update: u, evalErr: fmt.Sprintf("remote: workspace lookup failed: %v\n", err)}
	}
	if !registered {
		return verdict{update: u, isSnapshot: true, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: no workspace %q is registered - snapshot refs need a registry row first\nremote:   -> runko workspace create --name %s --project <name> ...\n",
				wsID, wsID),
		}}
	}

	author := remoteUser(extraEnv)
	if author != "" && ws.Owner != "" && author != ws.Owner {
		return verdict{update: u, isSnapshot: true, author: author, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: workspace %q belongs to %s - snapshots may only be pushed by their owner (§12.2)\nremote:   -> runko workspace create --name <yours> ... to get your own\n",
				wsID, ws.Owner),
		}}
	}
	if ws.Status == "closed" {
		return verdict{update: u, isSnapshot: true, author: author, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: workspace %q is closed - its task concluded, and one workspace carries one task (§12.2)\nremote:   -> start the new task in a fresh workspace: runko workspace create --name <new> --project <p> ... --by <you>\n",
				wsID),
		}}
	}

	// A FIRST push to a fresh snapshot ref arrives with old == zero; the
	// snapshot's real delta is against trunk, not the empty tree. Without
	// this, policy/caps judge the pusher as having authored the ENTIRE
	// repository - an agent's first snapshot violated affinity on any
	// file outside its cone, making agent workspaces unusable (found
	// live, first real agent-token workspace run; the stage-11b BaseSHA
	// bug's sibling). An unborn trunk keeps the empty-tree base - there
	// is genuinely nothing else the content could be a delta over.
	oldSHA := u.OldSHA
	if oldSHA == zeroOID {
		if mb, err := p.runGit(extraEnv, "merge-base", u.NewSHA, "refs/heads/"+p.TrunkRef); err == nil && strings.TrimSpace(mb) != "" {
			oldSHA = strings.TrimSpace(mb)
		}
	}
	changedPaths, files, err := p.diff(oldSHA, u.NewSHA, extraEnv)
	if err != nil {
		return verdict{update: u, evalErr: fmt.Sprintf("remote: could not inspect snapshot: %v\n", err)}
	}

	if pr := p.principalByName(author); pr != nil && pr.IsAgent {
		// A snapshot push is exactly the workspace-affine write §8.7's
		// policy wants agents making - so it carries the workspace's own
		// write allowlist as affinity, and the denylist/caps still apply.
		violations := receive.EvaluatePolicy(pr.Policy, receive.PushSummary{
			ChangedFiles:      changedPaths,
			DiffBytes:         totalContentBytes(files),
			WorkspaceAffinity: ws.WriteAllowlist,
			ModifiesOwners:    modifiesOwners(changedPaths),
		})
		if len(violations) > 0 {
			return verdict{update: u, isSnapshot: true, author: author,
				decision: receive.Decision{Accepted: false, PolicyViolations: violations}}
		}
	}

	cap := p.MaxSnapshotDiffBytes
	if cap == 0 {
		cap = DefaultMaxSnapshotBytes
	}
	if cap > 0 {
		var total int64
		for _, f := range files {
			total += int64(len(f.Content))
		}
		if total > cap {
			return verdict{update: u, isSnapshot: true, decision: receive.Decision{
				Accepted: false,
				RejectionMessage: fmt.Sprintf("remote: snapshot introduces %d bytes, over the %d-byte cap - build artifacts and dependency trees (node_modules, target/, .venv) must never enter snapshots (§12.2)\nremote:   -> add them to .gitignore and snapshot again\n",
					total, cap),
			}}
		}
	}

	findings, err := p.Scanner.Scan(files)
	if err != nil {
		return verdict{update: u, evalErr: fmt.Sprintf("remote: secret scan failed: %v\n", err)}
	}
	if len(findings) > 0 {
		return verdict{update: u, isSnapshot: true, decision: receive.Decision{Accepted: false, SecretFindings: findings}}
	}

	return verdict{update: u, isSnapshot: true, changedPaths: changedPaths, extraEnv: extraEnv, author: author,
		decision: receive.Decision{Accepted: true}}
}

func rejectionMessage(v verdict) string {
	if v.evalErr != "" {
		return v.evalErr
	}
	if !v.decision.Accepted {
		return renderRejection(v.decision)
	}
	return "remote: rejected because another ref in this push was rejected\n"
}

// commit persists an accepted verdict's Changes and enqueues their
// webhooks - only called once ProcessBatch has confirmed the WHOLE push is
// accepted. One push can carry a whole STACK (§7.4): every commit between
// trunk and the pushed tip that carries a Change-Id trailer is a series
// member and gets its Change created/updated, Gerrit's series semantics.
// This is what makes the jj/evolve workflow one push instead of N: amend
// near the root, let the client auto-rebase descendants (jj does this
// implicitly; git needs one `rebase`), push the tip once - every member's
// head and base move together, bottom-up, so each member's base resolves
// to its freshly-updated parent.
func (p *Processor) commit(ctx context.Context, v verdict) RefResult {
	if v.evalErr != "" {
		return RefResult{Ref: v.update.Ref, Accepted: false, Message: v.evalErr}
	}
	d := v.decision

	members := p.seriesMembers(ctx, v, d)
	var tip Change
	var msg strings.Builder
	msg.WriteString(v.advice) // the near-cap split nudge, when one accrued
	for _, m := range members {
		// A stable per-Change ref, independent of whatever rotating ref the
		// client pushed to: refs/for/<trunk> is a single ref every Change
		// (and every amend of THIS Change - PushChange force-pushes it, see
		// cli/runko/change.go) overwrites in turn. Without this, an accepted
		// Change's commit becomes unreachable - and thus GC-eligible - the
		// moment a later push moves refs/for/<trunk> on, which breaks both
		// "commits are versions of a Change" (§7.4, the Change row would
		// point at a dangling SHA) and runko-ci checkout's need to fetch a
		// specific Change by a stable path (§14.4.4).
		changeRef := "refs/changes/" + m.changeID + "/head"
		if _, err := p.runGit(v.extraEnv, "update-ref", changeRef, m.sha); err != nil {
			return RefResult{Ref: v.update.Ref, Accepted: false, Message: fmt.Sprintf("remote: failed to record change ref: %v\n", err)}
		}

		base := p.computeBaseSHA(ctx, RefUpdate{OldSHA: v.update.OldSHA, NewSHA: m.sha, Ref: v.update.Ref}, m.changeID, v.extraEnv)
		change, err := p.Store.CreateOrUpdateChange(ctx, m.changeID, base, m.sha, changeRef, m.title, v.author, v.originWorkspace, v.originBranch)
		if err != nil {
			return RefResult{Ref: v.update.Ref, Accepted: false, Message: fmt.Sprintf("remote: failed to record change: %v\n", err)}
		}

		p.computeAffectedAndEnqueue(ctx, change, m.changedPaths, v.extraEnv)

		if m.sha == v.update.NewSHA {
			tip = change
		} else {
			fmt.Fprintf(&msg, "remote: %s -> %s (series)\n", change.ChangeKey, changeRef)
		}
	}

	if v.update.Ref == "refs/heads/"+p.TrunkRef {
		p.ZoektIndexWorker.Trigger()
	}
	p.Mirror.Trigger()

	fmt.Fprintf(&msg, "remote: %s -> %s\n", tip.ChangeKey, v.update.Ref)
	return RefResult{
		Ref: v.update.Ref, Accepted: true, ChangeID: tip.ChangeKey,
		Message: msg.String(),
	}
}

// seriesMember is one Change-bearing commit in a pushed stack.
type seriesMember struct {
	sha, changeID, title string
	changedPaths         []string
}

// seriesMembers resolves the pushed commit's series, BOTTOM-UP (trunk-most
// first, tip last - the order that lets each member's base computation find
// its parent's freshly-written row). Rules, matching the single-Change
// receive semantics that predate series processing:
//   - only first-parent ancestors between trunk and the tip participate;
//   - a commit with no Change-Id trailer folds into the nearest descendant
//     that has one (its content is part of that Change's delta, never its
//     own Change);
//   - one Change-Id spanning several commits (the grow pattern) is one
//     Change whose head is its TOPMOST commit;
//   - a member whose Change already LANDED is skipped as history context -
//     re-submitting landed work is only an error when the TIP itself is
//     landed, which evaluate() already rejected;
//   - the tip is always a member (its trailer may have been minted
//     server-side by EnsureChangeID, so its id comes from the Decision).
//
// A direct trunk push (only reachable when §6.9's rejection is somehow
// bypassed) and any walk failure degrade to the tip alone - exactly the
// pre-series behavior.
func (p *Processor) seriesMembers(ctx context.Context, v verdict, d receive.Decision) []seriesMember {
	u := v.update
	tipOnly := []seriesMember{{sha: u.NewSHA, changeID: d.ChangeID, title: firstLine(d.CommitMessage), changedPaths: v.changedPaths}}
	if u.Ref == "refs/heads/"+p.TrunkRef {
		return tipOnly
	}
	out, err := p.runGit(v.extraEnv, "rev-list", "--first-parent", fmt.Sprintf("--max-count=%d", stackWalkLimit),
		u.NewSHA, "^refs/heads/"+p.TrunkRef)
	if err != nil {
		// Unborn trunk: the whole ancestry is the series candidate set.
		out, err = p.runGit(v.extraEnv, "rev-list", "--first-parent", fmt.Sprintf("--max-count=%d", stackWalkLimit), u.NewSHA)
	}
	if err != nil || out == "" {
		return tipOnly
	}

	seen := map[string]bool{}
	var topDown []seriesMember // near -> far; first occurrence of an id is that Change's head
	for _, sha := range strings.Split(out, "\n") {
		msg, err := p.commitMessage(sha, v.extraEnv)
		if err != nil {
			return tipOnly
		}
		id, ok := receive.ParseChangeID(msg)
		if sha == u.NewSHA {
			id, ok = d.ChangeID, true
		}
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		if sha != u.NewSHA {
			if existing, known, err := p.Store.GetChange(ctx, id); err != nil || (known && existing.State == "landed") {
				continue
			}
		}
		topDown = append(topDown, seriesMember{sha: sha, changeID: id, title: firstLine(msg)})
	}

	members := make([]seriesMember, 0, len(topDown))
	for i := len(topDown) - 1; i >= 0; i-- {
		m := topDown[i]
		if m.sha == u.NewSHA {
			m.changedPaths = v.changedPaths
		} else {
			m.changedPaths = p.commitOwnPaths(m.sha, v.extraEnv)
		}
		members = append(members, m)
	}
	return members
}

// commitOwnPaths is the changed-path set of one commit against its first
// parent (the empty tree for a root commit) - a series member's own delta
// for affected computation and webhooks.
func (p *Processor) commitOwnPaths(sha string, extraEnv []string) []string {
	parent := sha + "^"
	if _, err := p.runGit(extraEnv, "rev-parse", "--verify", "-q", sha+"^"); err != nil {
		parent = emptyTreeOID
	}
	out, err := p.runGit(extraEnv, "diff", "--name-only", parent, sha)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// computeBaseSHA resolves Change.BaseSHA: for a direct trunk-ref push, the
// ref's own prior value is exactly right (a genuine parent commit on
// trunk). For a magic-ref push (refs/for/<trunk>), the ref's own prior
// value is NOT the trunk commit the Change is based on - it's zero on the
// Change's first push, and the Change's own PRIOR commit on an amend/
// re-push (PushChange force-pushes the same rotating ref, see
// cli/runko/change.go) - neither is "where this Change branched from
// trunk". For an unstacked Change the answer is `git merge-base(newSHA,
// trunk tip)`; for a STACKED Change (the pushed commit's parent chain
// contains another pending Change's commit, §7.4) it is that nearest
// pending ancestor - recording trunk's merge-base there made every stacked
// Change's diff span the whole stack and left GetChangeStack unable to
// derive the parent relation at all (B stacked on A iff B.base_sha ==
// A.head_sha), since nothing in the receive path ever recorded a
// non-trunk base.
//
// Found via §28.3 stage 11b (land wiring): land.Land computes the trunk
// delta as diffPaths(BaseSHA, trunkTip) to decide whether checks must be
// re-run (§13.5) - a wrong-but-non-empty BaseSHA (the Change's own stale
// prior commit, or "" collapsing to the empty tree) makes that diff include
// files the Change never actually touched, so NeedsRevalidation sees a
// false intersection and every land looks like it needs revalidation, even
// a trivial fast-forward. This was invisible in every earlier stage's tests
// because they only ever exercised BaseSHA/HeadSHA at receive time, never
// fed it back into a trunk-delta diff the way land.Land does.
func (p *Processor) computeBaseSHA(ctx context.Context, u RefUpdate, changeID string, extraEnv []string) string {
	if u.Ref == "refs/heads/"+p.TrunkRef {
		if u.OldSHA == zeroOID {
			return ""
		}
		return u.OldSHA
	}
	if base, ok := p.nearestPendingChangeBase(ctx, u.NewSHA, changeID, extraEnv); ok {
		return base
	}
	base, err := p.runGit(extraEnv, "merge-base", u.NewSHA, "refs/heads/"+p.TrunkRef)
	if err != nil {
		// No common history yet (e.g. trunk itself is still unborn) - ""
		// matches the emptyTreeOID fallback every BaseSHA reader already uses.
		return ""
	}
	return base
}

// stackWalkLimit bounds nearestPendingChangeBase's ancestor walk - a stack
// deeper than this is pathological, and the merge-base fallback below it is
// merely conservative (whole-stack diff), never wrong about content.
const stackWalkLimit = 100

// nearestPendingChangeBase walks the pushed commit's first-parent ancestry
// (nearest first, stopping at trunk) looking for a commit that belongs to
// another Change this Store knows: that ancestor is the pushed Change's
// stack parent, and therefore its base (§7.4 - B stacked on A iff
// B.base_sha == A.head_sha, the relation GetChangeStack derives and
// GetChangeDiff's base..head scoping depends on). Ancestors carrying the
// pushed Change's OWN Change-Id are skipped: a same-Id commit stacked on
// itself is one Change grown by a commit, and its base must stay below the
// whole Change, not split it. The parent Change's state doesn't matter -
// even a landed/abandoned parent's commit is exactly where this Change's
// own delta starts. Unknown trailer-less ancestors keep walking: a series
// pushed without intermediate Changes lands as one delta, so the base must
// stay below it.
func (p *Processor) nearestPendingChangeBase(ctx context.Context, newSHA, changeID string, extraEnv []string) (string, bool) {
	out, err := p.runGit(extraEnv, "rev-list", "--first-parent", fmt.Sprintf("--max-count=%d", stackWalkLimit),
		newSHA+"~1", "^refs/heads/"+p.TrunkRef)
	if err != nil {
		// An UNBORN trunk makes `^refs/heads/<trunk>` a hard git error, not
		// an empty exclusion - retry with no exclusion, or every
		// pre-first-land Change silently gets base "" and stacking (plus
		// every base-scoped diff/affected/owners computation) breaks
		// exactly while a fresh monorepo bootstraps (2026-07-08 clean-slate
		// dogfood finding). seriesMembers has the same fallback.
		out, err = p.runGit(extraEnv, "rev-list", "--first-parent", fmt.Sprintf("--max-count=%d", stackWalkLimit), newSHA+"~1")
	}
	if err != nil || out == "" {
		// Root commit, or ancestry already entirely on trunk - no pending
		// ancestors to consider.
		return "", false
	}
	for _, sha := range strings.Split(out, "\n") {
		msg, err := p.commitMessage(sha, extraEnv)
		if err != nil {
			continue
		}
		key, ok := receive.ParseChangeID(msg)
		if !ok || key == changeID {
			continue
		}
		if _, known, err := p.Store.GetChange(ctx, key); err == nil && known {
			return sha, true
		}
	}
	return "", false
}

// computeAffectedAndEnqueue runs the platform-floor affected computation
// (§13.3) and enqueues a webhook envelope - the funnel's remaining two
// steps after Change persistence (§7.4, §11.5: "receive -> policy -> secret
// scan -> Change create/update -> affected compute -> webhooks"). Errors
// here are logged (not silently dropped), not fatal to the push: the Change
// is already durable, so a failed affected computation shouldn't un-accept
// an otherwise-good push - it should show up as an operational alert
// instead, which is exactly what the log line is for.
func (p *Processor) computeAffectedAndEnqueue(ctx context.Context, change Change, changedPaths []string, extraEnv []string) {
	store := &gitstore.Store{Dir: p.RepoDir, Ref: "HEAD", ExtraEnv: extraEnv}
	indexed, err := index.Scan(store, core.Revision(change.HeadSHA), nil)
	if err != nil {
		log.Printf("runkod: %s: scan projects at %s: %v", change.ChangeKey, change.HeadSHA, err)
		return
	}
	projects := make([]affected.ProjectInfo, len(indexed))
	for i, ip := range indexed {
		projects[i] = affected.ProjectInfo{Name: ip.Name, Path: ip.Path, DeclaredDependencies: ip.DeclaredDependencies}
	}
	result := affected.Compute(projects, changedPaths, affected.Options{
		RootInvalidationPatterns: append(index.RootInvalidation(indexed), p.RootInvalidationPatterns...),
		ProsePatterns:            index.Prose(indexed),
	})

	// Actor attribution and Change numbering need real AuthN/a persistent
	// counter, neither built yet (doc.go's scope boundary) - placeholders
	// here are informational fields on an already-durable Change, not a
	// gate on anything.
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		// Unique per EMISSION, not per (change, head): a same-head re-push
		// is the documented way to re-trigger CI with a full payload, and
		// consumers dedup on this id - a reused id gets silently dropped
		// as an outbox retry (migration-findings #32). Outbox retries of
		// one emission re-deliver the same payload, so they still dedup.
		DeliveryID: change.ChangeKey + "@" + change.HeadSHA + "@" + p.now().UTC().Format(time.RFC3339Nano),
		Type:       "change.updated",
		OccurredAt: p.now(),
		OrgID:      p.OrgName,
		Change: checks.WebhookChange{
			ID: change.ChangeKey, State: change.State,
			BaseSHA: change.BaseSHA, HeadSHA: change.HeadSHA, GitRef: change.GitRef,
			Title: change.Title,
			Actor: checks.WebhookActor{Type: "user", ID: "unknown"},
		},
		Affected: &checks.WebhookAffected{
			ComputationID: result.ComputationID,
			Paths:         result.Paths,
			ReasonCodes:   result.ReasonCodes,
			RunEverything: result.RunEverything,
		},
	}
	for _, pr := range result.Projects {
		env.Affected.Projects = append(env.Affected.Projects, checks.WebhookAffectedProject{Name: pr.Name, Path: pr.Path})
	}

	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		log.Printf("runkod: %s: marshal webhook envelope: %v", change.ChangeKey, err)
		return
	}
	if _, err := p.Store.EnqueueWebhook(ctx, env.Type, payload); err != nil {
		log.Printf("runkod: %s: enqueue webhook: %v", change.ChangeKey, err)
	}
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// diff returns the changed paths and, for added/modified files only (never
// deletions - secret scanning cares about content entering the repo, not
// content leaving it), their full content at newSHA.
func (p *Processor) diff(oldSHA, newSHA string, extraEnv []string) ([]string, []receive.FileContent, error) {
	from := oldSHA
	if from == zeroOID {
		from = emptyTreeOID
	}
	out, err := p.runGit(extraEnv, "diff", "--name-status", from, newSHA)
	if err != nil {
		return nil, nil, err
	}
	if out == "" {
		return nil, nil, nil
	}

	store := &gitstore.Store{Dir: p.RepoDir, Ref: "HEAD", ExtraEnv: extraEnv}
	var paths []string
	var files []receive.FileContent
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status, path := fields[0], fields[len(fields)-1]
		paths = append(paths, path)
		if strings.HasPrefix(status, "D") {
			continue
		}
		blob, err := store.GetBlob(core.Revision(newSHA), path)
		if err != nil {
			continue
		}
		files = append(files, receive.FileContent{Path: path, Content: blob.Content})
	}
	return paths, files, nil
}

func (p *Processor) commitMessage(rev string, extraEnv []string) (string, error) {
	return p.runGit(extraEnv, "log", "-1", "--format=%B", rev)
}

func (p *Processor) runGit(extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = p.RepoDir
	if extraEnv != nil {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// renderRejection turns a Decision's rejection reasons into the plain-
// language, "remote: ..." prefixed lines git relays to the pushing client
// (§6.6, §6.9) - never a raw internal error.
func renderRejection(d receive.Decision) string {
	if d.RejectionMessage != "" {
		return d.RejectionMessage
	}
	var b strings.Builder
	for _, v := range d.PolicyViolations {
		fmt.Fprintf(&b, "remote: policy violation: %s\n", v.Message)
		if v.Suggestion != "" {
			fmt.Fprintf(&b, "remote:   -> %s\n", v.Suggestion)
		}
	}
	for _, f := range d.SecretFindings {
		fmt.Fprintf(&b, "remote: possible secret in %s (line %d): %s\n", f.Path, f.Line, f.Description)
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
