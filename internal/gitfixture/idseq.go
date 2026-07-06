package gitfixture

import "fmt"

// IDSeq is a deterministic, seeded generator of prefixed IDs (e.g. "chg_1",
// "chg_2", ...) for tests that need stable identifiers without real randomness
// or a database (docs/design.md §28.2 rule 3).
type IDSeq struct {
	prefix string
	n      int
}

// NewIDSeq returns a sequence that yields "<prefix>_1", "<prefix>_2", ...
func NewIDSeq(prefix string) *IDSeq {
	return &IDSeq{prefix: prefix}
}

// Next returns the next ID in the sequence.
func (s *IDSeq) Next() string {
	s.n++
	return fmt.Sprintf("%s_%d", s.prefix, s.n)
}
