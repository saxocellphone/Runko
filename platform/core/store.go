// Package core holds the shared interfaces that every other package depends on,
// per docs/design.md §9.1 ("thin core/ for interfaces") and §28.2 rule 6.
//
// MonorepoStore is the storage abstraction from §11.3. Git is the only
// implementation in v1 (§11.1); internal/gitstore implements it by shelling
// out to system git rather than using a Git-in-Go library (§28.2 rule 4).
package core

// Revision identifies a point in monorepo history (a commit SHA, or something that
// resolves to one).
type Revision string

// TreeEntry is one entry returned by GetTree.
type TreeEntry struct {
	Path string
	Mode string
	Type string // "blob" | "tree" | "commit" (submodule)
	SHA  string
}

// Blob is file content addressed by its own SHA.
type Blob struct {
	SHA     string
	Size    int64
	Content []byte
}

// FileChange is one file create/modify/delete within an Overlay.
type FileChange struct {
	Path    string
	Content []byte // ignored when Delete is true
	Delete  bool
}

// Overlay is the set of file changes applied on top of a base Revision by
// CommitOverlay - the write shape used by workspace snapshots and change refs
// alike (§11.5, §12.2).
type Overlay struct {
	Changes []FileChange
}

// CommitMeta carries the metadata for a commit produced by CommitOverlay.
type CommitMeta struct {
	AuthorName  string
	AuthorEmail string
	Message     string
}

// HistoryOptions bounds a ListHistory query.
type HistoryOptions struct {
	Since Revision
	Limit int
}

// HistoryEntry is one commit returned by ListHistory.
type HistoryEntry struct {
	Revision Revision
	Message  string
}

// MonorepoStore is the storage abstraction defined in §11.3. All reads/writes to
// monorepo content and refs go through this interface; nothing above it may assume
// a specific backend.
type MonorepoStore interface {
	ResolveRef(name string) (Revision, error)
	GetTree(rev Revision, path string) ([]TreeEntry, error)
	GetBlob(rev Revision, path string) (Blob, error)
	CommitOverlay(base Revision, overlay Overlay, meta CommitMeta) (Revision, error)
	// UpdateRef moves name to rev. If expected is non-nil, the update is a
	// compare-and-swap against the ref's current value (optimistic concurrency
	// for the receive funnel and land engine, §11.5, §13.5).
	UpdateRef(name string, rev Revision, expected *Revision) error
	ListHistory(path string, opts HistoryOptions) ([]HistoryEntry, error)
}
