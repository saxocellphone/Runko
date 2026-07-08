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

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/receive"
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
	Now      func() time.Time
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
// Three sanctioned ref shapes: refs/heads/<trunk> (rejected, §6.9),
// refs/for/<trunk> (the Change funnel), and refs/workspaces/<id>/* (snapshot
// refs, §12.2 - policed since stage 12b, previously accepted
// unconditionally). Everything else (refs/tags/* etc.) is still marked skip
// - accepted unconditionally, the documented v1 permissiveness §14.10.3
// tracks for tag-namespace governance.
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
	isTrunkPush := u.Ref == "refs/heads/"+p.TrunkRef
	_, isMagicRef := receive.ParseMagicRef(u.Ref)
	if !isTrunkPush && !isMagicRef {
		return verdict{update: u, skip: true}
	}

	author := remoteUser(extraEnv)
	originWS, originBranch, originVerdict := p.resolveOrigin(ctx, u, extraEnv, author)
	if originVerdict != nil {
		return *originVerdict
	}

	changedPaths, files, err := p.diff(u.OldSHA, u.NewSHA, extraEnv)
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
		// conservative direction for a cap. A refs/for push carries no
		// workspace affinity, so a policy with RequireWorkspaceAffinity
		// correctly refuses it (§8.7: agent WRITES go through workspaces;
		// snapshot pushes carry their workspace's affinity, see
		// evaluateSnapshot).
		req.Principal = receive.Principal{IsAgent: true, Policy: pr.Policy}
		req.DiffBytes = totalContentBytes(files)
		req.ModifiesOwners = modifiesOwners(changedPaths)
	}
	return verdict{
		update: u, changedPaths: changedPaths, extraEnv: extraEnv, author: author,
		originWorkspace: originWS, originBranch: originBranch,
		decision: receive.Decide(req, p.Scanner),
	}
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
func (p *Processor) resolveOrigin(ctx context.Context, u RefUpdate, extraEnv []string, author string) (wsID, branch string, rejection *verdict) {
	for _, opt := range pushOptions(extraEnv) {
		if v, ok := strings.CutPrefix(opt, "workspace="); ok {
			wsID = v
		}
		if v, ok := strings.CutPrefix(opt, "workspace-branch="); ok {
			branch = v
		}
	}
	if wsID == "" {
		return "", "", nil
	}
	if branch == "" {
		branch = "head" // the §12.2 default branch
	}
	ws, registered, err := p.Store.GetWorkspace(ctx, wsID)
	if err != nil {
		return "", "", &verdict{update: u, evalErr: fmt.Sprintf("remote: workspace lookup failed: %v\n", err)}
	}
	if !registered {
		return "", "", &verdict{update: u, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: this push declares workspace %q as its origin, but no such workspace is registered\nremote:   -> re-attach with `runko workspace attach <id>`, or unset runko.workspace in this worktree's git config\n",
				wsID),
		}}
	}
	if author != "" && ws.Owner != "" && author != ws.Owner {
		return "", "", &verdict{update: u, decision: receive.Decision{
			Accepted: false,
			RejectionMessage: fmt.Sprintf("remote: workspace %q belongs to %s - a Change cannot claim someone else's workspace as its origin (§12.2)\nremote:   -> unset runko.workspace in this worktree's git config, or attach your own workspace\n",
				wsID, ws.Owner),
		}}
	}
	return wsID, branch, nil
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

	changedPaths, files, err := p.diff(u.OldSHA, u.NewSHA, extraEnv)
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

// commit persists an accepted verdict's Change and enqueues its webhook -
// only called once ProcessBatch has confirmed the WHOLE push is accepted.
func (p *Processor) commit(ctx context.Context, v verdict) RefResult {
	if v.evalErr != "" {
		return RefResult{Ref: v.update.Ref, Accepted: false, Message: v.evalErr}
	}
	d := v.decision

	// A stable per-Change ref, independent of whatever rotating ref the
	// client pushed to: refs/for/<trunk> is a single ref every Change (and
	// every amend of THIS Change - PushChange force-pushes it, see
	// cmd/runko/change.go) overwrites in turn. Without this, an accepted
	// Change's commit becomes unreachable - and thus GC-eligible - the
	// moment a later push moves refs/for/<trunk> on, which breaks both
	// "commits are versions of a Change" (§7.4, the Change row would point
	// at a dangling SHA) and runko-ci checkout's need to fetch a specific
	// Change by a stable path (§14.4.4).
	changeRef := "refs/changes/" + d.ChangeID + "/head"
	if _, err := p.runGit(v.extraEnv, "update-ref", changeRef, v.update.NewSHA); err != nil {
		return RefResult{Ref: v.update.Ref, Accepted: false, Message: fmt.Sprintf("remote: failed to record change ref: %v\n", err)}
	}

	base := p.computeBaseSHA(v.update, v.extraEnv)
	change, err := p.Store.CreateOrUpdateChange(ctx, d.ChangeID, base, v.update.NewSHA, changeRef, firstLine(d.CommitMessage), v.author, v.originWorkspace, v.originBranch)
	if err != nil {
		return RefResult{Ref: v.update.Ref, Accepted: false, Message: fmt.Sprintf("remote: failed to record change: %v\n", err)}
	}

	p.computeAffectedAndEnqueue(ctx, change, v.changedPaths, v.extraEnv)

	if v.update.Ref == "refs/heads/"+p.TrunkRef {
		p.ZoektIndexWorker.Trigger()
	}

	return RefResult{
		Ref: v.update.Ref, Accepted: true, ChangeID: change.ChangeKey,
		Message: fmt.Sprintf("remote: %s -> %s\n", change.ChangeKey, v.update.Ref),
	}
}

// computeBaseSHA resolves Change.BaseSHA: for a direct trunk-ref push, the
// ref's own prior value is exactly right (a genuine parent commit on
// trunk). For a magic-ref push (refs/for/<trunk>), the ref's own prior
// value is NOT the trunk commit the Change is based on - it's zero on the
// Change's first push, and the Change's own PRIOR commit on an amend/
// re-push (PushChange force-pushes the same rotating ref, see
// cmd/runko/change.go) - neither is "where this Change branched from
// trunk". The real answer is `git merge-base(newSHA, trunk tip)`.
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
func (p *Processor) computeBaseSHA(u RefUpdate, extraEnv []string) string {
	if u.Ref == "refs/heads/"+p.TrunkRef {
		if u.OldSHA == zeroOID {
			return ""
		}
		return u.OldSHA
	}
	base, err := p.runGit(extraEnv, "merge-base", u.NewSHA, "refs/heads/"+p.TrunkRef)
	if err != nil {
		// No common history yet (e.g. trunk itself is still unborn) - ""
		// matches the emptyTreeOID fallback every BaseSHA reader already uses.
		return ""
	}
	return base
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
	result := affected.Compute(projects, changedPaths, affected.Options{RootInvalidationPatterns: p.RootInvalidationPatterns})

	// Actor attribution and Change numbering need real AuthN/a persistent
	// counter, neither built yet (doc.go's scope boundary) - placeholders
	// here are informational fields on an already-durable Change, not a
	// gate on anything.
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  change.ChangeKey + "@" + change.HeadSHA,
		Type:        "change.updated",
		OccurredAt:  p.now(),
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
