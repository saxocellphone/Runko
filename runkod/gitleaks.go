package runkod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/platform/receive"
)

// GitleaksScanner implements receive.SecretScanner by shelling out to a real
// gitleaks binary (docs/design.md §11.4: "integrate gitleaks/trufflehog; do
// not build bespoke heuristics") - closing the seam receive/secretscan.go
// deliberately left open. Not available in this sandbox (no gitleaks
// install here - see CLAUDE.md); tested against a scripted fake binary
// (gitleaks_test.go), same technique buildadapter/bazel uses for a real
// binary this environment doesn't have.
type GitleaksScanner struct {
	// Bin is the gitleaks executable; defaults to "gitleaks" on PATH.
	Bin string
}

// gitleaksFinding is the subset of gitleaks' JSON report fields
// (--report-format json) this scanner needs.
type gitleaksFinding struct {
	Description string `json:"Description"`
	StartLine   int    `json:"StartLine"`
	RuleID      string `json:"RuleID"`
	File        string `json:"File"`
}

// Scan writes files to a scratch directory (gitleaks scans a filesystem
// tree, not in-memory content) and runs `gitleaks detect --no-git` over it,
// parsing the JSON report back into receive.SecretFinding. --exit-code 0 is
// passed explicitly so gitleaks' exit status never signals "leaks found"
// (gitleaks defaults to exiting 1 for that) - this scanner reports findings
// via the parsed report content, not by interpreting an exit code, since
// that convention isn't guaranteed stable across gitleaks versions.
func (g GitleaksScanner) Scan(files []receive.FileContent) ([]receive.SecretFinding, error) {
	if len(files) == 0 {
		return nil, nil
	}

	dir, err := os.MkdirTemp("", "runkod-gitleaks-*")
	if err != nil {
		return nil, fmt.Errorf("gitleaks: create scratch dir: %w", err)
	}
	defer os.RemoveAll(dir)

	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, fmt.Errorf("gitleaks: stage %s: %w", f.Path, err)
		}
		if err := os.WriteFile(full, f.Content, 0o644); err != nil {
			return nil, fmt.Errorf("gitleaks: stage %s: %w", f.Path, err)
		}
	}

	reportPath := filepath.Join(dir, ".gitleaks-report.json")
	bin := g.Bin
	if bin == "" {
		bin = "gitleaks"
	}
	cmd := exec.Command(bin, "detect",
		"--no-git", "--source", dir,
		"--report-format", "json", "--report-path", reportPath,
		"--exit-code", "0",
	)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gitleaks detect: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // some gitleaks versions omit the report file entirely when clean
		}
		return nil, fmt.Errorf("gitleaks: read report: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	var results []gitleaksFinding
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("gitleaks: parse report: %w", err)
	}

	findings := make([]receive.SecretFinding, len(results))
	for i, r := range results {
		rel := strings.TrimPrefix(r.File, dir+string(filepath.Separator))
		rel = filepath.ToSlash(rel)
		findings[i] = receive.SecretFinding{
			Path: rel, Line: r.StartLine, RuleID: r.RuleID, Description: r.Description,
		}
	}
	return findings, nil
}

var _ receive.SecretScanner = GitleaksScanner{}
