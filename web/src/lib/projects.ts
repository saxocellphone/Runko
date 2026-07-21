// The repo-root project, and why it is not a peer of the others.
//
// Every folder with a PROJECT.yaml is a project (§10.3), including the
// repo root - its manifest is what makes root glue (go.mod, Makefile,
// .github/, scripts/) resolve a merge policy instead of falling to the
// fail-closed unowned-path default, and it is the only project carrying
// repo-wide machinery: root_invalidation (escalate to run_everything)
// and prose (de-escalate markdown to the root's own content check).
//
// It is therefore REAL and stays listed - you select it to get a
// root-affinity workspace (`workspace create --project repo`) - but it
// is not one service among many: longest-prefix matching puts every
// unclaimed path under it, and nothing declares a dependency ON it, so
// as a graph node it is an edgeless orphan. These helpers keep that
// distinction in one place (2026-07-20, user-raised: "repo is a project
// but it shouldn't count since it's the root").

/** The root project is the one whose path IS the repo root, never a name
 * test: `repo` is this repo's convention, not a reserved word, and
 * another monorepo may call it anything. Both spellings of the root path
 * count - the same `path == "" || path == "."` rule the daemon applies
 * when it refuses to delete the root project (runkod/deleteproject.go)
 * and when it bootstraps one (runkod/bootstraporg.go). */
export function isRootProject(p: { path: string }): boolean {
  return p.path === "" || p.path === ".";
}

/** Display order: the root project first, everything else untouched in
 * the order the server gave (the index's own). */
export function rootFirst<T extends { path: string }>(items: T[]): T[] {
  return [...items.filter(isRootProject), ...items.filter((p) => !isRootProject(p))];
}

/** The dependency graph's nodes: every project EXCEPT the root. It
 * declares no dependencies and none are declared on it, so drawing it
 * only adds a floating node to an otherwise connected DAG. */
export function graphProjects<T extends { path: string }>(items: T[]): T[] {
  return items.filter((p) => !isRootProject(p));
}
