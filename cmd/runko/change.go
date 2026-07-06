package main

import (
	"fmt"

	"github.com/saxocellphone/runko/receive"
)

// PushChange implements `runko change push` (§11.5, §17.1): ensure HEAD's
// commit message carries a Change-Id trailer (amending if it doesn't), then
// push it to refs/for/<trunk> on remote - the same magic-ref path any plain
// git client can use (§6.9's "parity rule").
func PushChange(repoDir, remote, trunk string) (changeID string, err error) {
	msg, err := runGit(repoDir, "log", "-1", "--format=%B")
	if err != nil {
		return "", fmt.Errorf("read HEAD commit message: %w", err)
	}

	headSHA, err := runGit(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}

	id, newMsg := receive.EnsureChangeID(msg, headSHA)
	if newMsg != msg {
		if _, err := runGit(repoDir, "commit", "--amend", "-m", newMsg); err != nil {
			return "", fmt.Errorf("amend commit with Change-Id trailer: %w", err)
		}
	}

	if _, err := runGit(repoDir, "push", remote, "HEAD:refs/for/"+trunk); err != nil {
		return "", fmt.Errorf("push to refs/for/%s: %w", trunk, err)
	}
	return id, nil
}
