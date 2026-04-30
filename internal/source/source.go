// Package source extracts slices of MIB source files for the
// symbol page's "Show full SMI source" disclosure.
//
// The slice extraction is heuristic: we read N lines starting at the
// SMI definition's source_line. SMIv2 definitions commonly span
// 5-30 lines, so a default window of 40 lines covers nearly every
// real-world case. If the heuristic ever miscalibrates, the user
// can follow the disclosure's link to /m/{module}/source for the
// full file (served via http.ServeFile, not this package).
package source

import (
	"bufio"
	"errors"
	"fmt"
	"os"
)

// ErrNotFound is returned when path is empty (the module's source
// path was never recorded — typically because libsmi resolved the
// module by name from the search path rather than from a file).
var ErrNotFound = errors.New("source not available for this module")

// DefaultWindow is the number of lines a slice extracts when the
// caller doesn't pin one explicitly. Empirically it covers the vast
// majority of OBJECT-TYPE / TC / NOTIFICATION-TYPE definitions
// without bleeding into the next definition.
const DefaultWindow = 40

// Slice returns up to lines lines from path beginning at startLine
// (1-based). Returns an empty slice with no error when path is
// empty or the file ends before startLine — symbol pages should
// hide the disclosure when the result is empty rather than show
// "source not available" as a blank panel.
//
// lines <= 0 falls back to DefaultWindow.
func Slice(path string, startLine, lines int) (string, error) {
	if path == "" {
		return "", ErrNotFound
	}
	if startLine < 1 {
		startLine = 1
	}
	if lines <= 0 {
		lines = DefaultWindow
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open source %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Many vendor MIBs ship with very long DESCRIPTION strings that
	// blow past bufio.Scanner's default 64 KB buffer. Bump it.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var buf []byte
	row := 0
	end := startLine + lines - 1
	for sc.Scan() {
		row++
		if row < startLine {
			continue
		}
		buf = append(buf, sc.Bytes()...)
		buf = append(buf, '\n')
		if row >= end {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scan source %s: %w", path, err)
	}
	return string(buf), nil
}
