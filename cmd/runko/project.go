package main

import (
	"fmt"

	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/project"
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
		return "", fmt.Errorf("invalid intent: %v", errs)
	}

	base, err := store.ResolveRef("HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
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
