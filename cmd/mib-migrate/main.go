// blittermib-migrate is a one-shot migration planner for converting an
// existing flat MIB collection into the PEN-vendor / IETF-functional
// directory layout that the mib-corpus change introduces.
//
// Two subcommands:
//
//	blittermib-migrate plan  --src DIR --out FILE [--groups FILE]
//	    Walk DIR, parse each MIB via libsmi, classify per
//	    design.md Decision 9, and emit a TSV migration plan that the
//	    maintainer can review and hand-edit before applying.
//
//	blittermib-migrate apply --plan FILE [--root DIR] [--dry-run]
//	    Read the (possibly edited) plan and run `git mv` per entry,
//	    refusing to clobber existing destination paths.
//
// This is intended for the initial seed only — once the corpus is in
// place, contributors add new MIBs by hand into the right directory.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "plan":
		if err := planCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "plan:", err)
			os.Exit(1)
		}
	case "apply":
		if err := applyCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "apply:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	_, _ = fmt.Fprintln(w, `blittermib-migrate plan|apply

Subcommands:
  plan   --src DIR --out FILE [--groups FILE] [--smidump PATH] [--smilint PATH]
         Walk DIR, parse each MIB, emit a TSV migration plan.

  apply  --plan FILE [--root DIR] [--dry-run]
         Read FILE, run 'git mv' per entry under DIR.
         --dry-run prints the commands without running them.

Run with -h after a subcommand for its full flag list.`)
}
