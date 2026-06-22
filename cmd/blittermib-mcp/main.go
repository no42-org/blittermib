/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Command blittermib-mcp serves the blittermib MIB archive to LLM agents
// over the Model Context Protocol (stdio transport). It exposes a small
// set of read-only tools — full-text search, OID/symbol lookup, snmpwalk
// decoding, and notification classification — over the same SQLite index
// the web server reads. It never writes to or mutates the corpus.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/no42-org/blittermib/internal/mcptools"
	"github.com/no42-org/blittermib/internal/store"
)

// version is set by the linker at release time via -ldflags.
var version = "dev"

func main() {
	dataDir := flag.String("data", "./data", "blittermib data directory containing blittermib.db")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stderr, version)
		return
	}

	if err := run(*dataDir); err != nil {
		log.Fatalf("blittermib-mcp: %v", err)
	}
}

func run(dataDir string) error {
	ctx := context.Background()

	dbPath := filepath.Join(dataDir, "blittermib.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %s (run ingest first): %w", dbPath, err)
	}

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	srv := mcptools.NewServer(st, version)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
