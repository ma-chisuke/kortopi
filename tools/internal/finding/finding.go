// Package finding holds a problem that a check found in a file, and prints it
// in the one-line format that the checks of this repository share:
//
//	path:line: [rule-id] message. Fix: what to do. See ADR-0001.
package finding

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Finding is one problem in one file.
type Finding struct {
	// Path is the file, relative to the repository root, with forward slashes.
	Path string
	// Line is the 1-based line of the problem; 1 when no better line exists.
	Line int
	// Rule identifies the rule, such as "adr/title".
	Rule string
	// Message says what is wrong.
	Message string
	// Fix says what to do about it.
	Fix string
	// See names the decision record that explains the rule, such as "ADR-0001".
	See string
}

// String formats the finding as one line, without a line break.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s. Fix: %s. See %s.",
		f.Path, max(f.Line, 1), f.Rule, sentence(f.Message), sentence(f.Fix), f.See)
}

// sentence drops the final period, which String adds itself.
func sentence(s string) string {
	return strings.TrimSuffix(strings.TrimSpace(s), ".")
}

// Sort orders findings by path, then line, then rule, then message.
func Sort(findings []Finding) {
	slices.SortStableFunc(findings, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Message, b.Message),
		)
	})
}

// Write sorts the findings and writes them to w, one per line.
func Write(w io.Writer, findings []Finding) error {
	sorted := slices.Clone(findings)
	Sort(sorted)
	for _, f := range sorted {
		if _, err := fmt.Fprintln(w, f.String()); err != nil {
			return err
		}
	}
	return nil
}
