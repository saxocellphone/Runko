package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runGit runs a git subcommand rooted at dir and returns trimmed stdout.
func runGit(dir string, args ...string) (string, error) {
	return runGitEnv(dir, nil, args...)
}

// runGitEnv is runGit with extra environment variables appended - the
// watch loop's GIT_INDEX_FILE redirection (watch.go) is the one caller.
// Every invocation marks itself RUNKO_INTERNAL_GIT=1 so the advisory
// pre-commit verb nudge (doctor.go) stays silent when git runs under a
// runko verb - the nudge exists for a human or agent typing raw
// `git commit`, not for runko's own plumbing.
func runGitEnv(dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "RUNKO_INTERNAL_GIT=1")
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Env, extraEnv...)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}
