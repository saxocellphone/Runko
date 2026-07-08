// runko change create / requirements - the last two §19.2 CLI stubs
// (§17.1's cheat sheet: create -> push -> requirements -> land).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/receive"
)

// CreateChange commits everything in the working tree (tracked + new
// files, the workspace-snapshot convention) as ONE commit carrying a
// Change-Id trailer - the §7.4 identity that survives amends and rebases.
// Deliberately no auto-push: `runko change push` is the explicit "submit
// for review" step (§11.5), and splitting them keeps plain-git parity
// (commit locally offline, push when ready).
func CreateChange(repoDir, message string) (changeID string, err error) {
	if message == "" {
		return "", &clierr.Error{
			Code: "missing_message", Field: "m",
			Message:    "a change needs a message",
			Suggestion: `runko change create -m "Reject invalid SKUs"`,
		}
	}
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code: "not_a_repo", Field: "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "clone the monorepo first (see `runko doctor`)",
			DocURL:     "docs/design.md#67-empty-states-and-education",
		}
	}
	if _, err := runGit(repoDir, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage changes: %w", err)
	}
	staged, err := runGit(repoDir, "diff", "--cached", "--name-only")
	if err != nil {
		return "", err
	}
	if staged == "" {
		return "", &clierr.Error{
			Code: "nothing_to_commit", Field: "repo",
			Message:    "the working tree has no changes",
			Suggestion: "edit something first, or amend HEAD with plain git if you meant to reword",
		}
	}

	// Bake the Change-Id in from the first commit (rather than letting
	// `change push` amend it in later): the id should be stable the moment
	// the Change exists locally.
	seed, _ := runGit(repoDir, "rev-parse", "HEAD") // "" on an unborn branch is a fine seed
	id, msgWithID := receive.EnsureChangeID(message, seed+"|"+staged)

	// Same identity fallback as workspace snapshot: committing must work
	// on a machine with no configured git identity.
	commitArgs := []string{"commit", "-m", msgWithID}
	if email, _ := runGit(repoDir, "config", "user.email"); email == "" {
		commitArgs = append([]string{"-c", "user.name=Runko", "-c", "user.email=runko@localhost"}, commitArgs...)
	}
	if _, err := runGit(repoDir, commitArgs...); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// headChangeID reads the Change-Id trailer from HEAD - the default target
// for `change requirements` (and anything else change-scoped run inside a
// checkout).
func headChangeID(repoDir string) (string, error) {
	msg, err := runGit(repoDir, "log", "-1", "--format=%B")
	if err != nil {
		return "", &clierr.Error{
			Code: "no_commits", Field: "repo",
			Message:    "HEAD has no commits to read a Change-Id from",
			Suggestion: "pass --change <Id> explicitly, or run `runko change create` first",
		}
	}
	id, ok := receive.ParseChangeID(msg)
	if !ok {
		return "", &clierr.Error{
			Code: "no_change_id", Field: "repo",
			Message:    "HEAD's commit message has no Change-Id trailer",
			Suggestion: "run `runko change push` once (it amends one in), or pass --change <Id>",
		}
	}
	return id, nil
}

// ChangeRequirements fetches the §13.5 merge gates for one Change.
func ChangeRequirements(ctx context.Context, client *http.Client, cred Credential, changeID string) (checks.MergeRequirements, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(cred.URL, "/")+"/api/changes/"+changeID+"/merge-requirements", nil)
	if err != nil {
		return checks.MergeRequirements{}, err
	}
	req.Header.Set("Authorization", cred.AuthHeader())
	resp, err := client.Do(req)
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("contact %s: %w", cred.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return checks.MergeRequirements{}, &clierr.Error{
			Code: "unknown_change", Field: "change",
			Message:    fmt.Sprintf("the control plane has no change %s", changeID),
			Suggestion: "did you `runko change push` yet?",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return checks.MergeRequirements{}, fmt.Errorf("merge-requirements: HTTP %d", resp.StatusCode)
	}
	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("decode response: %w", err)
	}
	return reqs, nil
}

// printRequirements renders the gates the way the web's merge-requirements
// card does: every requirement with its state, then the plain-language
// blockers (§6.6).
func printRequirements(changeID string, reqs checks.MergeRequirements) {
	if reqs.Mergeable {
		fmt.Printf("%s: ready to land\n", changeID)
	} else {
		fmt.Printf("%s: blocked from landing\n", changeID)
	}
	for _, o := range reqs.RequiredOwners {
		mark := "○ outstanding"
		for _, s := range reqs.SatisfiedOwners {
			if s == o {
				mark = "✓ approved"
			}
		}
		fmt.Printf("  owner  %-40s %s\n", o, mark)
	}
	for _, c := range reqs.RequiredChecks {
		mark := "○ not reported"
		for _, n := range reqs.PassingChecks {
			if n == c {
				mark = "✓ passing"
			}
		}
		for _, n := range reqs.FailingChecks {
			if n == c {
				mark = "✕ failing"
			}
		}
		for _, n := range reqs.PendingChecks {
			if n == c {
				mark = "● running"
			}
		}
		fmt.Printf("  check  %-40s %s\n", c, mark)
	}
	for _, b := range reqs.Blockers {
		fmt.Printf("  -> %s\n", b)
	}
}
