// blittermib-index walks `mibs/` and generates `mibs/INDEX.yaml` —
// the per-MIB metadata catalog used for review (license tags),
// diff-parse caching (transitive imports), and discovery surfaces.
//
// The generator is deterministic on a stable input: running it twice
// in succession produces byte-identical output, so PR diffs only show
// real corpus changes.
//
// Implementation divergence from design.md Decision 5: this v1.0
// generator does NOT call `internal/compile` (libsmi). The directory
// layout is the source of truth for PEN/vendor/file (CI Tier 2
// enforces it), the module name comes from the `MODULE-IDENTITY` line
// in the source, and the IMPORTS clause is extracted by a small
// regex. This keeps `make index` fast (file-IO only, no smidump
// per-file cost) and lets tests run without libsmi installed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := indexCmd(os.Args[1:]); err != nil {
		// Treat `-h` / `--help` as a clean exit — flag.Parse prints
		// the usage block to stderr itself, we just shouldn't dress
		// it up as an error.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "blittermib-index:", err)
		os.Exit(1)
	}
}
