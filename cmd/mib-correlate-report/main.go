/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Command mib-correlate-report compiles a directory of MIB source files
// and reports the notification-relationship inference coverage over the
// whole set: how many notifications were classified raise / clear /
// orphan and at what confidence (FR26). Run against the bundled standard
// corpus to track the engine's behaviour across releases — the golden
// tests assert correctness on the labeled oracle; this surfaces the
// distribution over the full corpus, with nothing silently omitted.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/correlate"
	"github.com/no42-org/blittermib/internal/model"
)

func main() {
	mibsDir := flag.String("mibs", "data/standard-mibs", "directory of MIB source files to compile")
	smidumpPath := flag.String("smidump", "smidump", "path to the smidump binary")
	flag.Parse()

	entries, err := os.ReadDir(*mibsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *mibsDir, err)
		os.Exit(1)
	}
	var targets []string
	for _, e := range entries {
		if !e.IsDir() {
			targets = append(targets, filepath.Join(*mibsDir, e.Name()))
		}
	}
	sort.Strings(targets)

	c := &compile.Compiler{
		Smidump: &compile.Smidump{Path: *smidumpPath, Paths: []string{*mibsDir}},
	}
	results := c.Compile(context.Background(), targets)

	var (
		smis         []*compile.SMI
		symsByModule = make(map[string][]model.Symbol)
		failed       int
		duplicates   int
	)
	for _, r := range results {
		if r.Err != nil || r.Module == nil {
			failed++
			continue
		}
		if _, seen := symsByModule[r.Module.Name]; seen {
			// Two files resolving to the same module name would inflate
			// the counts (Classify sees duplicate notifications). Keep
			// the first, skip the rest, and report it — so the tally
			// stays honest on arbitrary corpora.
			fmt.Fprintf(os.Stderr, "skipping duplicate module %s (%s)\n", r.Module.Name, r.Target)
			duplicates++
			continue
		}
		smis = append(smis, r.SMI)
		symsByModule[r.Module.Name] = r.Symbols
	}
	refsByModule := make(map[string][]model.Reference)
	for _, ref := range compile.BuildReferences(smis) {
		refsByModule[ref.SourceModule] = append(refsByModule[ref.SourceModule], ref)
	}

	modules := make([]string, 0, len(symsByModule))
	for m := range symsByModule {
		modules = append(modules, m)
	}
	sort.Strings(modules)

	cov := correlate.NewCoverage()
	for _, m := range modules {
		cov.AddModule(correlate.Classify(symsByModule[m], refsByModule[m]))
	}

	fmt.Printf("Notification Intelligence — corpus coverage report\n")
	fmt.Printf("source: %s (%d files, %d modules, %d failed, %d duplicate-name skipped)\n", *mibsDir, len(targets), len(modules), failed, duplicates)
	fmt.Printf("(distribution only; precision/recall are asserted on the labeled golden set — see `make verify`)\n\n")
	fmt.Print(cov.String())
}
