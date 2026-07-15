package project

import (
	"fmt"
	"regexp"
	"strings"
)

// PlanDelete is create's dual (§13.1, decided 2026-07-15): the name is the
// whole request, and the plan is pure - the caller supplies the tree facts
// (the project index slice, the target's file listing, every other
// manifest's content) and gets back repo-relative operations. Deleting a
// project removes its whole subtree AND strips its name from every other
// manifest's dependencies:/consumes: lists, so no dangling edge survives.
// The strips are LINE-SURGICAL, never a YAML re-marshal: manifests in a
// tree-as-truth repo are comment-heavy, and a round-trip would destroy
// them. (An emptied list drops its key line too; a comment block that
// described the dropped edge is left in place for the reviewer to see -
// the plan lands as an ordinary reviewable Change.)

// DeleteTarget is the slice of the project index PlanDelete needs.
type DeleteTarget struct {
	Name string
	Path string // repo-relative project root; "" or "." marks the root project
}

// ManifestRef is one OTHER project's manifest: repo-relative path + content.
type ManifestRef struct {
	Path    string
	Content []byte
}

// DeleteOp is one repo-relative operation of a DeletePlan.
type DeleteOp struct {
	Path    string
	Action  string // "delete" | "modify"
	Content string // the new content when Action == "modify"
}

// DeletePlan is the previewable result: the project root being removed and
// every operation the Change will carry.
type DeletePlan struct {
	Name string
	Path string
	Ops  []DeleteOp
}

// PlanDelete validates the intent and builds the plan. files is the
// target's full repo-relative file listing at the revision being planned
// against; manifests are every OTHER project's manifest.
func PlanDelete(name string, targets []DeleteTarget, files []string, manifests []ManifestRef) (DeletePlan, []ValidationError) {
	var target *DeleteTarget
	for i := range targets {
		if targets[i].Name == name {
			target = &targets[i]
			break
		}
	}
	if target == nil {
		return DeletePlan{}, []ValidationError{{
			Code: "unknown_project", Field: "name",
			Message:    fmt.Sprintf("no project named %q", name),
			Suggestion: "runko project list shows every project",
		}}
	}
	if target.Path == "" || target.Path == "." {
		return DeletePlan{}, []ValidationError{{
			Code: "root_project_immutable", Field: "name",
			Message:    "the root project is the repository's glue and cannot be deleted",
			Suggestion: "delete the projects under it instead",
		}}
	}

	plan := DeletePlan{Name: name, Path: target.Path}
	for _, f := range files {
		plan.Ops = append(plan.Ops, DeleteOp{Path: f, Action: "delete"})
	}
	for _, m := range manifests {
		stripped, changed := StripEdge(string(m.Content), name)
		if changed {
			plan.Ops = append(plan.Ops, DeleteOp{Path: m.Path, Action: "modify", Content: stripped})
		}
	}
	if len(files) == 0 {
		return DeletePlan{}, []ValidationError{{
			Code: "unknown_project", Field: "name",
			Message:    fmt.Sprintf("project %q has no files at this revision", name),
			Suggestion: "the tree may have moved - re-check runko project list",
		}}
	}
	return plan, nil
}

// edgeKeyRE matches the two edge-list keys at top level of a manifest.
var edgeKeyRE = regexp.MustCompile(`^(dependencies|consumes):\s*(#.*)?$`)

// StripEdge removes `- <name>` entries under top-level dependencies:/
// consumes: keys, dropping a key whose list empties. Exported for tests;
// exact-name matches only ("proto" never strips "proto2").
func StripEdge(manifest, name string) (string, bool) {
	lines := strings.Split(manifest, "\n")
	itemRE := regexp.MustCompile(`^\s+-\s+` + regexp.QuoteMeta(name) + `\s*(#.*)?$`)

	changed := false
	var out []string
	i := 0
	for i < len(lines) {
		if !edgeKeyRE.MatchString(lines[i]) {
			out = append(out, lines[i])
			i++
			continue
		}
		keyLine := lines[i]
		i++
		var kept []string
		removed := false
		for i < len(lines) {
			l := lines[i]
			if itemRE.MatchString(l) {
				removed = true
				changed = true
				i++
				continue
			}
			// Block members are indented list items or comments; anything
			// else (a new top-level key, EOF) ends the block.
			if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") || strings.TrimSpace(l) == "" && i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t")) {
				kept = append(kept, l)
				i++
				continue
			}
			break
		}
		hasItem := false
		for _, l := range kept {
			if strings.HasPrefix(strings.TrimSpace(l), "- ") {
				hasItem = true
				break
			}
		}
		if hasItem || !removed {
			out = append(out, keyLine)
			out = append(out, kept...)
		}
		// An emptied block drops its key line and any interior comment
		// lines with it (they described the dropped list).
	}
	return strings.Join(out, "\n"), changed
}
