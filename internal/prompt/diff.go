// Package prompt renders what will change and asks before anything is written.
//
// Nothing in this tool touches disk until the user has seen a diff and said yes.
// That is a safety property, but it is also the feature: users will not adopt
// something that rewrites their infrastructure opaquely.
package prompt

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ANSI colours, disabled when output is not a terminal or NO_COLOR is set.
type palette struct {
	reset, bold, dim, green, red, yellow, cyan string
}

func newPalette(color bool) palette {
	if !color {
		return palette{}
	}
	return palette{
		reset:  "\033[0m",
		bold:   "\033[1m",
		dim:    "\033[2m",
		green:  "\033[32m",
		red:    "\033[31m",
		yellow: "\033[33m",
		cyan:   "\033[36m",
	}
}

// FileChange is one file we intend to create or modify.
type FileChange struct {
	Path     string // shown to the user, relative where possible
	AbsPath  string // where it is actually written
	Original string // empty when creating a new file
	Updated  string
	Note     string // optional one-line explanation
}

// IsNew reports whether this creates a file rather than modifying one.
func (f FileChange) IsNew() bool { return f.Original == "" }

// RenderDiff writes a unified-ish diff for a set of changes.
//
// Deliberately not a full LCS diff: for new files the whole body is the change,
// and for manifest edits we only ever append, so a simple line comparison is
// both sufficient and easier to read than a general algorithm's output.
func RenderDiff(w io.Writer, changes []FileChange, color bool) {
	p := newPalette(color)

	for _, c := range changes {
		verb := "modify"
		if c.IsNew() {
			verb = "create"
		}
		fmt.Fprintf(w, "\n%s%s %s%s\n", p.bold, verb, c.Path, p.reset)
		if c.Note != "" {
			fmt.Fprintf(w, "%s%s%s\n", p.dim, c.Note, p.reset)
		}

		if c.IsNew() {
			for _, line := range strings.Split(strings.TrimRight(c.Updated, "\n"), "\n") {
				fmt.Fprintf(w, "%s+ %s%s\n", p.green, line, p.reset)
			}
			continue
		}

		renderLineDiff(w, p,
			strings.Split(c.Original, "\n"),
			strings.Split(strings.TrimRight(c.Updated, "\n"), "\n"))
	}
}

// renderLineDiff prints a real LCS-based diff with limited context.
//
// A prefix/suffix comparison is not good enough here: inserting a line into the
// middle of a JSON dependencies block made every following line look deleted and
// re-added. The diff is the user's evidence that we are not mangling their files,
// so it has to be accurate.
func renderLineDiff(w io.Writer, p palette, a, b []string) {
	const context = 3
	ops := diffOps(a, b)

	// Find which lines to show: every change, plus surrounding context.
	show := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == opEqual {
			continue
		}
		for j := i - context; j <= i+context; j++ {
			if j >= 0 && j < len(ops) {
				show[j] = true
			}
		}
	}

	gap := false
	for i, op := range ops {
		if !show[i] {
			gap = true
			continue
		}
		if gap {
			fmt.Fprintf(w, "%s  ...%s\n", p.dim, p.reset)
			gap = false
		}
		switch op.kind {
		case opEqual:
			fmt.Fprintf(w, "%s  %s%s\n", p.dim, op.text, p.reset)
		case opDelete:
			fmt.Fprintf(w, "%s- %s%s\n", p.red, op.text, p.reset)
		case opInsert:
			fmt.Fprintf(w, "%s+ %s%s\n", p.green, op.text, p.reset)
		}
	}
}

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type diffOp struct {
	kind opKind
	text string
}

// diffOps computes a line diff via the classic LCS dynamic-programming table.
// Manifest files are small, so the O(n*m) cost is irrelevant and the result is
// exact.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opEqual, a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{opDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{opInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{opDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{opInsert, b[j]})
	}
	return ops
}

// Confirm asks a yes/no question. defaultYes decides what an empty answer means.
func Confirm(r io.Reader, w io.Writer, question string, defaultYes bool) (bool, error) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	fmt.Fprintf(w, "\n%s %s ", question, suffix)

	reader := bufio.NewReader(r)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// EOF with no input (piped/non-interactive): fall back to the default
		// rather than erroring, so `signoz init < /dev/null` behaves predictably.
		if err == io.EOF {
			fmt.Fprintln(w)
			return defaultYes, nil
		}
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
