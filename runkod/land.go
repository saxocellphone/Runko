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

// landIdentity is the git identity stamped as BOTH author and committer on
// every commit this server lands (and, as the committer, on the heads
// SyncChange rebases). Configured via --land-identity; the neutral
// placeholder keeps existing embedders and tests unchanged.
func (s *Server) landIdentity() land.Identity {
	if s.LandIdentity.Name != "" && s.LandIdentity.Email != "" {
		return s.LandIdentity
	}
	return land.DefaultIdentity
}

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
		return nil // base is on trunk - not stacked
	}

	// The base isn't literally on trunk. Since every land now re-mints the
	// commit under the canonical landing identity (§7.5, changelog
	// 2026-07-13), a LANDED parent's head no longer appears verbatim on
	// trunk - trunk carries the same tree under a re-stamped SHA. So a base
	// missing from trunk no longer proves the parent is pending: refuse
	// only when an OPEN change still owns this base as its head (its content
	// genuinely isn't on trunk). Otherwise the parent landed, and the
	// child's base..head delta rebases cleanly onto the tip that carries the
	// parent's content - attemptLand does exactly that below. Fail closed if
	// open changes can't be enumerated: refusing a retryable land beats
	// landing a child without its parent.
	open, err := s.Store.ListChanges(ctx, "open")
	if err != nil {
		return typedErr(http.StatusConflict, clierr.Error{
			Code: "parent_change_not_landed", Field: "change",
			Message:    fmt.Sprintf("change %s is stacked on a commit trunk does not have (base %s); open changes could not be checked: %v", key, change.BaseSHA, err),
			Suggestion: "retry the land, or rebase this change onto trunk and re-push",
		})
	}
	for _, parent := range open {
		if parent.HeadSHA == change.BaseSHA {
			return typedErr(http.StatusConflict, clierr.Error{
				Code: "parent_change_not_landed", Field: "change",
				Message:    fmt.Sprintf("change %s is stacked on unlanded change %s (base %s)", key, parent.ChangeKey, change.BaseSHA),
				Suggestion: fmt.Sprintf("land %s first, or rebase this change onto trunk and re-push", parent.ChangeKey),
			})
		}
	}
	return nil
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
		if tip, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef); err == nil {
			indexed, err := s.indexedProjectsAt(gstore, tip)
			if err != nil {
				return land.Outcome{}, fmt.Errorf("land: scan projects: %w", err)
			}
			opts.RootInvalidationPatterns = append(index.RootInvalidation(indexed), rootInvalidation...)
			opts.ProsePatterns = index.Prose(indexed)
			projects = index.AffectedProjectInfos(indexed)
		}
		// else: trunk has no commits yet - no projects exist to scan,
		// matching land.Land's own unborn-trunk bootstrap path below.

		// The change's affected set only feeds the trunk-delta intersection;
		// tiers that never consult the delta (conflict-only, never) skip the
		// per-attempt diff + compute (§13.5, 2026-07-15).
		var changeAffected affected.Result
		if land.NeedsTrunkDelta(scope) {
			changedPaths, err := gitDiffNamesOnly(s.RepoDir, diffBase, change.HeadSHA)
			if err != nil {
				return land.Outcome{}, fmt.Errorf("land: diff change: %w", err)
			}
			changeAffected = affected.Compute(projects, changedPaths, opts)
		}

		// Both land paths re-mint the commit under the canonical landing
		// identity (§7.5, changelog 2026-07-13), so authorship is uniform
		// on trunk and the mirror; only the full message must survive the
		// re-mint (the title-only fallback loses the body and, for
		// trailer-less pushes, the Change-Id link).
		meta := core.CommitMeta{Message: change.Title + "\n\nChange-Id: " + change.ChangeKey + "\n"}
		if _, _, msg, err := commitIdentity(s.RepoDir, change.HeadSHA); err == nil {
			if !strings.Contains(msg, "Change-Id: "+change.ChangeKey) {
				// Server-minted ids (trailer-less pushes) aren't in the
				// original message; the landed commit must stay linkable.
				msg = strings.TrimRight(msg, "\n") + "\n\nChange-Id: " + change.ChangeKey + "\n"
			}
			meta.Message = msg
		} else {
			log.Printf("runkod: %s: reading head commit message (landing with title-only message): %v", change.ChangeKey, err)
		}

		outcome, err := land.Land(gstore, s.RepoDir, s.TrunkRef, base, change.HeadSHA,
			scope, changeAffected, projects, opts, meta, s.landIdentity())
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
