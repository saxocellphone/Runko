package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/project"
	"github.com/saxocellphone/runko/platform/receive"
)

// CreateProject implements `runko project create` locally (§10.1, §17.1):
// plan + apply on top of the CURRENT local HEAD, advancing whatever branch
// is checked out - never trunk directly (§7.4: trunk is closed to direct
// push). Landing the result happens later via `runko change push` and
// review, same as any other Change.
func CreateProject(repoDir string, intent project.Intent) (rev, changeID string, err error) {
	store := gitstore.New(repoDir)
	templates := project.DefaultTemplates()

	plan, errs := project.PlanCreate(intent, templates)
	if len(errs) > 0 {
		// Surface the first validation error in the §6.5 structured shape
		// (one error per field since the multi-language work), matching how
		// the daemon's create-project flow reports the same failures.
		e := errs[0]
		msg := e.Message
		if len(errs) > 1 {
			msg = fmt.Sprintf("%s (and %d more)", msg, len(errs)-1)
		}
		return "", "", &clierr.Error{Code: e.Code, Field: e.Field, Message: msg, Suggestion: e.Suggestion}
	}

	base, err := resolveBaseOrEmpty(repoDir, store)
	if err != nil {
		return "", "", err
	}

	// Same duplicate guard the daemon's create-project flow has
	// (runkod/createproject.go, 2026-07-08 dogfood review: the CLI happily
	// committed a second "Create project checkout-api" that would thrash
	// the tree when pushed). An empty base has no projects to collide with.
	if base != "" {
		existing, err := index.Scan(store, base, nil)
		if err != nil {
			return "", "", fmt.Errorf("scan existing projects: %w", err)
		}
		for _, p := range existing {
			if p.Name == plan.EffectiveManifest.Name || p.Path == plan.Path {
				return "", "", &clierr.Error{
					Code:       "already_exists",
					Field:      "name",
					Message:    fmt.Sprintf("project %s already exists at %s", p.Name, p.Path),
					Suggestion: "pick a different name, or evolve the existing project with an ordinary change",
				}
			}
		}
	}

	// Bake the Change-Id in from birth, exactly as `change create` does
	// (changecreate.go; 2026-07-16 dogfood review papercut: create advanced
	// the branch with a trailer-less commit, so the Change identity only
	// appeared when a later amend added one - easy to push a stack with a
	// stray identity-less step). Entropy in the seed for changecreate.go's
	// reason: two clones at the same tip creating the same project name
	// must not collide on one identity.
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", "", fmt.Errorf("generate change id nonce: %w", err)
	}
	seed := strings.Join([]string{string(base), plan.Path, hex.EncodeToString(nonce)}, "|")
	changeID, msgWithID := receive.EnsureChangeID(fmt.Sprintf("Create project %s", intent.Name), seed)

	newRev, err := project.Apply(store, base, plan, core.CommitMeta{
		Message: msgWithID,
	})
	if err != nil {
		return "", "", err
	}

	// A sparse checkout materializes only cone members: without adding the
	// new project's path first, create COMMITS the files and the working
	// tree then shows nothing - a first project appearing to vanish the
	// moment it is created (found by the onboarding journey suite,
	// 2026-07-17: a `--project repo` workspace's cone holds only root
	// files). On a non-sparse checkout the add fails ("no sparse-checkout
	// to add to") and is correctly a no-op.
	//
	// In a jj colocated checkout both of the plain-git verbs below are
	// wrong: `git sparse-checkout add` is a silent no-op (jj owns
	// materialization via `jj sparse`), and `git reset --hard` rewrites
	// the checkout behind jj's back. syncWorkingTreeJJ widens the cone and
	// parks @ with `jj new` instead; plain git is unchanged.
	//
	// CommitOverlay only writes objects: jj cannot see a SHA until it is
	// reachable from a git heads ref (verified 2026-07-24). When HEAD is
	// still a branch, advance it as plain git does. When HEAD is detached
	// (normal after `change create` - @'s parent loses its bookmark), pin
	// a temporary heads ref so jj can import, then forget the bookmark once
	// @ holds the create commit (jj bookmark forget, not git update-ref -d:
	// the latter rewrites history behind jj and abandons the pin; forget is
	// local-only and does not schedule a remote deletion).
	if isJJWorkspace(repoDir) {
		// Pin (if any) must not survive any error between publish and a
		// successful `jj new` - a leaked heads ref becomes a real bookmark
		// the user did not create. defer forgets it on every return path.
		pin, err := publishCommitForJJ(repoDir, store, newRev, base)
		if err != nil {
			return "", "", err
		}
		defer dropJJProjectCreatePin(repoDir, pin)
		if err := syncWorkingTreeJJ(repoDir, plan.Path, string(newRev)); err != nil {
			return "", "", err
		}
		return string(newRev), changeID, nil
	}

	headRef, err := runGit(repoDir, "symbolic-ref", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("resolve current branch (are you in detached HEAD?): %w", err)
	}
	if err := store.UpdateRef(headRef, newRev, &base); err != nil {
		return "", "", fmt.Errorf("advance %s: %w", headRef, err)
	}
	if plan.Path != "" {
		_, _ = runGit(repoDir, "sparse-checkout", "add", plan.Path)
	}

	// CommitOverlay only writes Git objects and moves the ref - it never
	// touches the working tree or index (internal/gitstore), so bring both
	// in sync with the new commit.
	if _, err := runGit(repoDir, "reset", "--hard", string(newRev)); err != nil {
		return "", "", fmt.Errorf("sync working tree to %s: %w", newRev, err)
	}

	return string(newRev), changeID, nil
}

// jjProjectCreatePinPrefix is the bookmark namespace for temporary heads
// refs that make a CommitOverlay SHA importable by jj when git HEAD is
// detached. Nested under runko/ so it cannot collide with a user branch
// named `runko-project-create`; the per-create suffix is the short SHA so
// two concurrent creates in one checkout pin distinct refs (a fixed name
// would let B overwrite A's pin, A's `jj new` then fail, and A's cleanup
// delete B's pin). Forgotten via `jj bookmark forget` (local only - not a
// deletion to push) once @ holds the create commit, or on any error path.
const jjProjectCreatePinPrefix = "runko/project-create/"

// publishCommitForJJ makes newRev reachable from a git heads ref so
// subsequent `jj new` can resolve it. Prefer advancing the checked-out
// branch; fall back to a temporary pin when HEAD is detached. Returns the
// bookmark name to forget later, or "" when no pin was needed.
func publishCommitForJJ(repoDir string, store *gitstore.Store, newRev, base core.Revision) (pin string, err error) {
	if headRef, err := runGit(repoDir, "symbolic-ref", "HEAD"); err == nil {
		if err := store.UpdateRef(headRef, newRev, &base); err != nil {
			return "", fmt.Errorf("advance %s: %w", headRef, err)
		}
		return "", nil
	}
	// Unique per SHA: concurrent creates cannot clobber each other, and a
	// crash leaves at most an orphan pin pointing at its own create commit.
	sha := string(newRev)
	if len(sha) > 12 {
		sha = sha[:12]
	}
	pin = jjProjectCreatePinPrefix + sha
	if _, err := runGit(repoDir, "update-ref", "refs/heads/"+pin, string(newRev)); err != nil {
		return "", fmt.Errorf("pin project-create commit for jj: %w", err)
	}
	return pin, nil
}

// dropJJProjectCreatePin removes a temporary import pin. Best-effort: an
// empty name (branch was advanced instead) or a missing bookmark is fine.
// forget, not delete: these pins are local-only and must not schedule a
// remote bookmark deletion on the next push.
func dropJJProjectCreatePin(repoDir, pin string) {
	if pin == "" {
		return
	}
	_, _ = runJJ(repoDir, "bookmark", "forget", pin)
}

// syncWorkingTreeJJ is the colocated-jj counterpart of sparse-checkout add +
// reset --hard after project create. CommitOverlay only wrote objects;
// publishCommitForJJ made them reachable from a heads ref. The working copy
// still sits on the old tip (or an empty @ above it) and the new project's
// path may sit outside jj's sparse set.
//
// Widen first when the cone is restricted (jjSparsePrefixes non-nil): `jj
// sparse set --add` is additive and keeps existing patterns (verified
// 2026-07-24). Unrestricted working copies spell themselves as "." and
// jjSparsePrefixes returns nil - calling --add there would inject a second
// pattern alongside "." without materializing anything new, so it is a
// deliberate no-op rather than a narrowing. Path args are repo-relative
// because runJJStderr pins cmd.Dir to the repo (so this is correct even
// when the caller is in a subdirectory).
//
// `jj new <rev>` then parks a fresh empty @ on the create commit - the same
// shape createChangeJJ and finishJJCheckout leave, and what jjTipCommit
// expects. `jj edit` would make the create commit itself the working-copy
// commit so the next edit amends it; new is the right verb. Pin cleanup is
// the caller's responsibility (defer in CreateProject) so error paths
// between pin and new cannot leak the bookmark.
func syncWorkingTreeJJ(repoDir, path, newRev string) error {
	// Checked, not the fail-open wrapper: a sparse read that ERRORS reads as
	// "unrestricted" through the plain helper, which would skip the widen and
	// leave the new project unmaterialized - the very bug this function exists
	// to fix, re-entering through the back door.
	prefixes, err := jjSparsePrefixesChecked(repoDir)
	if err != nil {
		return err
	}
	if path != "" && prefixes != nil {
		if _, err := runJJ(repoDir, "sparse", "set", "--add", path); err != nil {
			return &clierr.Error{
				Code:       "jj_sparse_widen",
				Field:      "path",
				Message:    fmt.Sprintf("could not widen the working-copy sparse set to include %s", path),
				Suggestion: fmt.Sprintf("run `jj sparse set --add %s`, then re-check with `jj sparse list`", path),
			}
		}
	}
	if err := jjEnsureIdentity(repoDir); err != nil {
		return err
	}
	if _, err := runJJ(repoDir, "new", newRev); err != nil {
		return fmt.Errorf("sync jj working copy to %s: %w", short(newRev), err)
	}
	return nil
}

// resolveBaseOrEmpty resolves the tip to build the new project commit on.
// An unborn HEAD (a freshly `git init`'d repo with no commits yet) is the
// expected first-run state, not an error - §6.7 makes "create your first
// project" the single CTA for an empty monorepo, so project create must be
// able to create the repo's very first commit. core.MonorepoStore.CommitOverlay
// already treats an empty base as "no parent" (see internal/gitstore), so
// the only change needed here is not rejecting that case before reaching it.
//
// In a colocated jj checkout the tip is resolved from jj's working copy
// (jjTipCommit), not git HEAD - same pattern as change.go's push path.
// Colocated jj keeps git HEAD detached by design once `change create`
// (or any jj op) leaves @'s parent without a bookmark, so the symbolic-ref
// guard is a plain-git concern only. Building on a lagging git branch tip
// would also parent the create commit in the wrong place.
//
// Any other resolution failure gets a structured, resolve-or-explain error
// (§6.5) instead of git's raw "ambiguous argument ... unknown revision"
// exit-128 text.
func resolveBaseOrEmpty(repoDir string, store *gitstore.Store) (core.Revision, error) {
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code:       "not_a_repo",
			Field:      "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run `git init` (or `jj git init --colocate`) first, then retry `runko project create`",
		}
	}
	// jj mode (change.go): tip from @, not git HEAD. Detached HEAD is normal.
	if isJJWorkspace(repoDir) {
		// jjTipCommit returns @ itself when @ is non-empty. Parenting the
		// create commit on that WIP, then `jj new`, freezes the user's
		// uncommitted work into a permanent undescribed commit in the
		// pushable ancestry and leaves `jj status` empty - so the WIP both
		// pollutes the next push (RequireDescription blocks the empty
		// description) and vanishes from the working copy's view. Refuse
		// while dirty; a clean empty @ keeps the jjTipCommit path below.
		empty, err := runJJ(repoDir, "log", "--no-graph", "-r", "@", "-T", "empty")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(empty) != "true" {
			return "", &clierr.Error{
				Code:  "dirty_working_copy",
				Field: "repo",
				Message: "the working copy has uncommitted changes; project create would " +
					"freeze them into an undescribed commit in the pushable ancestry",
				// Next runko verb, not raw jj: the CLI is the basic-loop interface
				// in every checkout (jj-colocated included); jj stays surgical.
				Suggestion: "commit the work first (`runko change create -m \"...\"`), then retry `runko project create`",
			}
		}
		tip, err := jjTipCommit(repoDir)
		if err != nil {
			// Fresh colocated init with nothing finished yet - same as an
			// unborn branch: first project becomes the repo's first commit.
			var ce *clierr.Error
			if errors.As(err, &ce) && ce.Code == "no_commits" {
				return "", nil
			}
			return "", err
		}
		// jj's root revision is all-zeros; CommitOverlay wants an empty
		// base string for "no parent", not a synthetic SHA.
		if tip == "" || strings.Trim(tip, "0") == "" {
			return "", nil
		}
		return core.Revision(tip), nil
	}
	if _, err := runGit(repoDir, "symbolic-ref", "-q", "HEAD"); err != nil {
		return "", &clierr.Error{
			Code:       "detached_head",
			Field:      "repo",
			Message:    "HEAD is not on a branch (detached HEAD)",
			Suggestion: "check out a branch first, e.g. `git checkout -b my-branch`",
		}
	}
	base, err := store.ResolveRef("HEAD")
	if err != nil {
		// On a branch (checked above) but HEAD doesn't resolve to a commit:
		// an unborn branch, i.e. this repo has no commits yet. Proceed with
		// an empty base rather than erroring.
		return "", nil
	}
	return base, nil
}
