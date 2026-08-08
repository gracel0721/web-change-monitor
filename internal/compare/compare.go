// Package compare computes a content hash and a human-readable diff between
// two normalized snapshots.
package compare

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// Hash returns the sha256 hex digest of the normalized content.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Equal reports whether two normalized strings hash the same. Used as the
// fast path to decide whether a diff is needed at all.
func Equal(old, new string) bool {
	return Hash(old) == Hash(new)
}

// Diff produces a compact, human-readable unified-ish diff of two text blobs.
// Only changed lines are shown, prefixed with "-" (removed) or "+" (added).
// Returns "" when the inputs are identical.
func Diff(old, new string) string {
	if old == new {
		return ""
	}
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(old, new, true)

	var b strings.Builder
	threshold := 5 // group runs of equal text into a single "[N lines unchanged]" line
	eqRun := 0
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			lines := strings.Split(strings.TrimRight(d.Text, "\n"), "\n")
			eqRun += len(lines)
		case diffmatchpatch.DiffInsert:
			if eqRun >= threshold {
				b.WriteString("  ...\n")
			}
			eqRun = 0
			for _, line := range splitLines(d.Text) {
				b.WriteString("+ ")
				b.WriteString(line)
				b.WriteByte('\n')
			}
		case diffmatchpatch.DiffDelete:
			if eqRun >= threshold {
				b.WriteString("  ...\n")
			}
			eqRun = 0
			for _, line := range splitLines(d.Text) {
				b.WriteString("- ")
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
	out := b.String()
	return strings.TrimRight(out, "\n")
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}