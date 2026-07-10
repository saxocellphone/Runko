package runkod

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/land"
)

// maxLandRaceRetries bounds the caller-side retry loop land.Land's own doc
// comment calls for ("this function does not loop internally; the caller
// decides retry policy - a bounded loop today, a merge queue in v1.x").
// Land races are the norm under concurrent landers (§13.5); a handful of
// attempts rides that out. If trunk is still moving after this many
// attempts, that's a signal worth surfacing to the client as
// race_retry_exhausted, not a reason to loop forever.
const maxLandRaceRetries = 5

// attemptLand runs land.Land against the current trunk tip, retrying on
// RaceRetry: each attempt re-reads the trunk tip and recomputes the trunk
// delta from scratch, since a losing attempt's view of trunk is stale by
// construction the moment it loses the compare-and-swap.
//
// base is passed to land.Land AS-IS (possibly "") - land.Land's own
// bootstrap handling (§28.3 stage 11b: landing onto an unborn trunk, the
// only way a brand-new monorepo's first Change can ever reach trunk, given
// §6.9's closed-trunk policy) specifically compares the unresolved trunk
// tip against an empty string, not emptyTreeOID. A separate diffBase
// (emptyTreeOID when base is "") is used only for gitDiffNamesOnly, which
// needs a real git object to diff against.
// refuseUnlandedParent is landChangeCore's stacked-ordering gate: a Change
// whose recorded base is a commit trunk doesn't contain is stacked on
// another pending Change (computeBaseSHA records the nearest pending
// ancestor as the base, §7.4), and attemptLand would rebase only base..head
// onto trunk - landing the child while silently DROPPING the parent's
// content it was built on. Gerrit's rule applies: ancestors land first.
// Returns nil when the Change is based directly on trunk (including the
// ""-base bootstrap case).
func (s *Server) refuseUnlandedParent(ctx context.Context, key string, change Change) *apiError {
	if change.BaseSHA == "" {
		return nil
	}
	cmd := exec.Command("git", "merge-base", "--is-ancestor", change.BaseSHA, "refs/heads/"+s.TrunkRef)
	cmd.Dir = s.RepoDir
	if cmd.Run() == nil {
		return nil // base is on trunk - not stacked (or the parent already landed)
	}

	suggestion := "land the parent change first, or rebase this change onto trunk and re-push"
	if open, err := s.Store.ListChanges(ctx, "open"); err == nil {
		for _, parent := range open {
			if parent.HeadSHA == change.BaseSHA {
				suggestion = fmt.Sprintf("land %s first, or rebase this change onto trunk and re-push", parent.ChangeKey)
				break
			}
		}
	}
	return typedErr(http.StatusConflict, clierr.Error{
		Code: "parent_change_not_landed", Field: "change",
		Message:    fmt.Sprintf("change %s is stacked on a commit trunk does not have (base %s)", key, change.BaseSHA),
		Suggestion: suggestion,
		DocURL:     "docs/design.md#74-change",
	})
}

func (s *Server) attemptLand(ctx context.Context, change Change, scope land.RevalidationScope) (land.Outcome, error) {
	gstore := gitstore.New(s.RepoDir)

	base := change.BaseSHA
	diffBase := base
	if diffBase == "" {
		diffBase = emptyTreeOID
	}

	var rootInvalidation []string
	if s.Processor != nil {
		rootInvalidation = s.Processor.RootInvalidationPatterns
	}
	opts := affected.Options{RootInvalidationPatterns: rootInvalidation}
	// Tree-declared patterns join once the scan below runs (opts is
	// re-set per attempt with the freshly scanned tree - §9.4).

	for attempt := 0; attempt < maxLandRaceRetries; attempt++ {
		var projects []affected.ProjectInfo
		if _, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef); err == nil {
			indexed, err := index.Scan(gstore, core.Revision("refs/heads/"+s.TrunkRef), nil)
			if err != nil {
				return land.Outcome{}, fmt.Errorf("land: scan projects: %w", err)
			}
			opts.RootInvalidationPatterns = append(index.RootInvalidation(indexed), rootInvalidation...)
			projects = make([]affected.ProjectInfo, len(indexed))
			for i, p := range indexed {
				projects[i] = affected.ProjectInfo{Name: p.Name, Path: p.Path, DeclaredDependencies: p.DeclaredDependencies}
			}
		}
		// else: trunk has no commits yet - no projects exist to scan,
		// matching land.Land's own unborn-trunk bootstrap path below.

		changedPaths, err := gitDiffNamesOnly(s.RepoDir, diffBase, change.HeadSHA)
		if err != nil {
			return land.Outcome{}, fmt.Errorf("land: diff change: %w", err)
		}
		changeAffected := affected.Compute(projects, changedPaths, opts)

		// A fast-forward land preserves the pushed commit verbatim; the
		// rebase path creates a NEW commit and must not degrade it - read
		// the original author and full message from the change head
		// instead of re-authoring as the machine with a title-only
		// message (observed on GitHub as history attributed to "Runko").
		meta := core.CommitMeta{Message: change.Title + "\n\nChange-Id: " + change.ChangeKey + "\n"}
		if an, ae, msg, err := commitIdentity(s.RepoDir, change.HeadSHA); err == nil {
			if !strings.Contains(msg, "Change-Id: "+change.ChangeKey) {
				// Server-minted ids (trailer-less pushes) aren't in the
				// original message; the landed commit must stay linkable.
				msg = strings.TrimRight(msg, "\n") + "\n\nChange-Id: " + change.ChangeKey + "\n"
			}
			meta = core.CommitMeta{AuthorName: an, AuthorEmail: ae, Message: msg}
		} else {
			log.Printf("runkod: %s: reading head commit identity (landing with fallback identity): %v", change.ChangeKey, err)
		}

		outcome, err := land.Land(gstore, s.RepoDir, s.TrunkRef, base, change.HeadSHA,
			scope, changeAffected, projects, opts, meta)
		if err != nil {
			return land.Outcome{}, err
		}
		if !outcome.RaceRetry {
			return outcome, nil
		}
	}
	return land.Outcome{RaceRetry: true}, nil
}

// commitIdentity reads sha's author and full message from the bare repo.
func commitIdentity(repoDir, sha string) (name, email, message string, err error) {
	cmd := exec.Command("git", "-C", repoDir, "log", "-1", "--format=%an%x00%ae%x00%B", sha)
	out, err := cmd.Output()
	if err != nil {
		return "", "", "", fmt.Errorf("git log %s: %w", sha, err)
	}
	parts := strings.SplitN(string(out), "\x00", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("git log %s: unexpected output", sha)
	}
	return parts[0], parts[1], parts[2], nil
}
