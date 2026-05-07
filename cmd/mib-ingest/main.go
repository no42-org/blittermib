// blittermib-ingest is the contributor-facing drop-folder workflow
// for adding MIBs to the corpus.
//
// A contributor copies one or more MIB files into mibs/upload/ and
// runs `make ingest`. The tool walks the upload folder, parses each
// MIB via libsmi, classifies its destination per the routing rules in
// `internal/mibcorpus` (the same rules the migrate tool uses), and
// moves the file to the canonical corpus path (vendors/{PEN}-{slug}/,
// ietf/{group}/, iana/, experimental/, or unsorted/).
//
// Files that don't parse, lack the SMI lexical marker, or whose
// destination filename already exists in the corpus stay in
// mibs/upload/ for manual review. The tool refuses to overwrite an
// existing corpus file.
//
// After all moves complete (and unless --no-index is passed), the
// tool invokes `make index` to keep mibs/INDEX.yaml in sync. Opt-in
// `--git-add` stages successfully-moved files via `git add`.
//
// Auto-commit is never offered — operators write commit messages by
// hand.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := ingestCmd(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "blittermib-ingest:", err)
		os.Exit(1)
	}
}
