package main

import (
	"fmt"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/project"
)

// CreateProject implements `runko project create` locally (§10.1, §17.1):
// plan + apply on top of the CURRENT local HEAD, advancing whatever branch
// is checked out - never trunk directly (§7.4: trunk is closed to direct
// push). Landing the result happens later via `runko change push` and
// review, same as any other Change.
func CreateProject(repoDir string, intent project.Intent) (rev string, err error) {
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
		return "", &clierr.Error{Code: e.Code, Field: e.Field, Message: msg, Suggestion: e.Suggestion}
	}

	base, err := resolveBaseOrEmpty(repoDir, store)
	if err != nil {
		return "", err
	}

	// Same duplicate guard the daemon's create-project flow has
	// (runkod/createproject.go, 2026-07-08 dogfood review: the CLI happily
	// committed a second "Create project checkout-api" that would thrash
	// the tree when pushed). An empty base has no projects to collide with.
	if base != "" {
		existing, err := index.Scan(store, base, nil)
		if err != nil {
			return "", fmt.Errorf("scan existing projects: %w", err)
		}
		for _, p := range existing {
			if p.Name == plan.EffectiveManifest.Name || p.Path == plan.Path {
				return "", &clierr.Error{
					Code:       "already_exists",
					Field:      "name",
					Message:    fmt.Sprintf("project %s already exists at %s", p.Name, p.Path),
					Suggestion: "pick a different name, or evolve the existing project with an ordinary change",
				}
			}
		}
	}

	newRev, err := project.Apply(store, base, plan, core.CommitMeta{
		Message: fmt.Sprintf("Create project %s", intent.Name),
	})
	if err != nil {
		return "", err
	}

	headRef, err := runGit(repoDir, "symbolic-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve current branch (are you in detached HEAD?): %w", err)
	}
	if err := store.UpdateRef(headRef, newRev, &base); err != nil {
		return "", fmt.Errorf("advance %s: %w", headRef, err)
	}

	// CommitOverlay only writes Git objects and moves the ref - it never
	// touches the working tree or index (internal/gitstore), so bring both
	// in sync with the new commit.
	if _, err := runGit(repoDir, "reset", "--hard", string(newRev)); err != nil {
		return "", fmt.Errorf("sync working tree to %s: %w", newRev, err)
	}

	return string(newRev), nil
}

// resolveBaseOrEmpty resolves repoDir's HEAD to build the new project commit
// on. An unborn HEAD (a freshly `git init`'d repo with no commits yet) is
// the expected first-run state, not an error - §6.7 makes "create your first
// project" the single CTA for an empty monorepo, so project create must be
// able to create the repo's very first commit. core.MonorepoStore.CommitOverlay
// already treats an empty base as "no parent" (see internal/gitstore), so
// the only change needed here is not rejecting that case before reaching it.
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
			DocURL:     "docs/design.md#67-empty-states-and-education",
		}
	}
	if _, err := runGit(repoDir, "symbolic-ref", "-q", "HEAD"); err != nil {
		return "", &clierr.Error{
			Code:       "detached_head",
			Field:      "repo",
			Message:    "HEAD is not on a branch (detached HEAD)",
			Suggestion: "check out a branch first, e.g. `git checkout -b my-branch`",
			DocURL:     "docs/design.md#69-the-closed-trunk-moment-human-git-ux",
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
