package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// captureStdout redirects os.Stdout for the duration of fn - needed since
// cmdCheckout/cmdReportCheck's --json path writes straight to os.Stdout,
// matching how a real CLI invocation behaves.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestCmdCheckoutJSONOutput(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	head := repo.Commit("one project")
	dest := filepath.Join(t.TempDir(), "checkout")

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = cmdCheckout([]string{"--remote", repo.Dir, "--dest", dest, "--rev", head, "--json"})
	})
	if cmdErr != nil {
		t.Fatalf("cmdCheckout: %v", cmdErr)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if result["rev"] != head || result["dest"] != dest {
		t.Fatalf("expected rev+dest in JSON output, got %+v", result)
	}
}

func TestCmdReportCheckJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = cmdReportCheck([]string{
			"--url", server.URL, "--name", "unit", "--external-id", "job-1",
			"--reporter", "github-actions", "--status", "completed", "--conclusion", "success", "--json",
		})
	})
	if cmdErr != nil {
		t.Fatalf("cmdReportCheck: %v", cmdErr)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if result["name"] != "unit" || result["status"] != "completed" || result["external_id"] != "job-1" {
		t.Fatalf("expected name/status/external_id in JSON output, got %+v", result)
	}
}
