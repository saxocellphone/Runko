// Package index rebuilds the control-plane project index from trunk
// (docs/design.md §10.3): walk a revision's tree for PROJECT.yaml manifests
// and resolve effective owners (project-manifest owners override; otherwise
// the nearest ancestor OWNERS file; otherwise an org default) per §7.3.
//
// This is a REBUILDABLE INDEX, not a source of truth - Sync always replaces a
// monorepo's project rows wholesale rather than diffing, so "reindex" is
// always "re-derive from the tree," never "trust what's already in Postgres."
package index
