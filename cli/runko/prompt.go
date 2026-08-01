// Interactive prompts for human TTY progressive disclosure (moon/Nx-style).
// Scripts and --json keep structured refusals; these helpers are only
// reached when stdin is a terminal and required flags were omitted.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/index"
)

// stdinIsTTY reports whether os.Stdin is an interactive terminal - the
// same ModeCharDevice check silenceEcho uses for password prompts.
// Tests override this so a harness TTY cannot turn scripted cases into
// interactive prompts.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// defaultWorkspaceName is the empty-input default for the name prompt.
func defaultWorkspaceName() string {
	return time.Now().Format("20060102")
}

// readLine reads one trimmed line from in (no echo manipulation - for
// non-secret prompts). Sibling of readSecret.
func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptLine prints label (and optional default in brackets), reads a
// line, and returns the default when the user accepts with Enter.
func promptLine(in *bufio.Reader, out io.Writer, label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, err := readLine(in)
	if err != nil {
		return "", err
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

// promptYesNo asks a Y/n (defaultYes) or y/N question; empty input keeps
// the default.
func promptYesNo(in *bufio.Reader, out io.Writer, label string, defaultYes bool) (bool, error) {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	fmt.Fprintf(out, "%s %s ", label, hint)
	line, err := readLine(in)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, &clierr.Error{
			Code: "invalid_input", Field: "confirm",
			Message:    fmt.Sprintf("expected y or n, got %q", line),
			Suggestion: "answer y or n, or press Enter for the default",
		}
	}
}

// promptWorkspaceName asks for a workspace name; empty input accepts def.
func promptWorkspaceName(in *bufio.Reader, out io.Writer, def string) (string, error) {
	name, err := promptLine(in, out, "Workspace name", def)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", &clierr.Error{
			Code: "missing_field", Field: "name",
			Message:    "workspace create needs a --name",
			Suggestion: "runko workspace create --name <workstream> --project <p>",
		}
	}
	return name, nil
}

// promptProjectSelect prints a numbered project list and parses a
// multi-select of indexes and/or names (e.g. "1,3" or "cli docs").
func promptProjectSelect(in *bufio.Reader, out io.Writer, projects []index.IndexedProject) ([]string, error) {
	if len(projects) == 0 {
		return nil, &clierr.Error{
			Code: "missing_field", Field: "project",
			Message:    "no projects indexed at trunk to select",
			Suggestion: "create one with `runko project create`, or pass --project / --new-path explicitly",
		}
	}
	ordered := rootProjectFirst(projects)
	fmt.Fprintln(out, "Projects at trunk:")
	for i, p := range ordered {
		label := p.Name
		if isRootProject(p) {
			label = p.Name + " (root)"
		}
		fmt.Fprintf(out, "  %d. %s\n", i+1, label)
	}
	fmt.Fprint(out, "Select projects (numbers and/or names): ")
	line, err := readLine(in)
	if err != nil {
		return nil, err
	}
	selected, err := parseProjectSelection(line, ordered)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, &clierr.Error{
			Code: "missing_field", Field: "project",
			Message:    "workspace create needs at least one --project (or --new-path)",
			Suggestion: "runko workspace create --name <workstream> --project <p>",
		}
	}
	return selected, nil
}

// parseProjectSelection turns a comma/whitespace-separated answer into
// project names. Tokens are 1-based indexes into ordered, or exact names.
func parseProjectSelection(line string, ordered []index.IndexedProject) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	byName := make(map[string]string, len(ordered))
	for _, p := range ordered {
		byName[p.Name] = p.Name
	}
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		var name string
		if n, err := strconv.Atoi(tok); err == nil {
			if n < 1 || n > len(ordered) {
				return nil, &clierr.Error{
					Code: "invalid_input", Field: "project",
					Message:    fmt.Sprintf("project index %d is out of range (1-%d)", n, len(ordered)),
					Suggestion: "pick numbers from the list, or project names (e.g. 1,3 or cli docs)",
				}
			}
			name = ordered[n-1].Name
		} else {
			name = byName[tok]
			if name == "" {
				return nil, &clierr.Error{
					Code: "invalid_input", Field: "project",
					Message:    fmt.Sprintf("unknown project %q", tok),
					Suggestion: "pick numbers from the list, or project names (e.g. 1,3 or cli docs)",
				}
			}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// requireWorkspaceCreateFlags is the non-interactive gate: scripts, pipes,
// and --json get a §6.5 missing_field instead of hanging on a prompt.
func requireWorkspaceCreateFlags(name, by string, projects, newPaths []string) error {
	if name == "" {
		return &clierr.Error{
			Code: "missing_field", Field: "name",
			Message:    "workspace create needs --name (and at least one --project or --new-path)",
			Suggestion: "runko workspace create --name <workstream> --project <p>  (or run on a TTY to be prompted)",
		}
	}
	if by == "" {
		return &clierr.Error{
			Code: "missing_field", Field: "by",
			Message:    "workspace create needs --by when signed in with a bare token",
			Suggestion: "runko workspace create --name <workstream> --project <p> --by <you>",
		}
	}
	if len(projects)+len(newPaths) == 0 {
		return &clierr.Error{
			Code: "missing_field", Field: "project",
			Message:    "workspace create needs at least one --project (or --new-path)",
			Suggestion: "runko workspace create --name <workstream> --project <p>  (or run on a TTY to be prompted)",
		}
	}
	return nil
}

// maybePromptIncludeRoot offers to add the trunk's root project (path ""
// or ".", never a reserved name - see isRootProject) when it exists and
// was not already selected. Affinity for root-owned paths is fixed at
// create time; missing it is a common whole-push refusal.
func maybePromptIncludeRoot(in *bufio.Reader, out io.Writer, selected []string, projects []index.IndexedProject) ([]string, error) {
	var root *index.IndexedProject
	for i := range projects {
		if isRootProject(projects[i]) {
			root = &projects[i]
			break
		}
	}
	if root == nil {
		return selected, nil
	}
	for _, s := range selected {
		if s == root.Name {
			return selected, nil
		}
	}
	ok, err := promptYesNo(in, out, fmt.Sprintf("Also include root project %q?", root.Name), true)
	if err != nil {
		return nil, err
	}
	if ok {
		return append(selected, root.Name), nil
	}
	return selected, nil
}
