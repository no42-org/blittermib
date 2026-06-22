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

	srv := newServer(st)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// newServer builds the MCP server with all read-only tools registered.
// Splitting this out of run() lets tests inspect the wired server.
func newServer(st *store.Store) *mcp.Server {
	h := &handlers{st: st}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "blittermib",
		Title:   "Blittermib MIB archive",
		Version: version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_mibs",
		Description: "Full-text search across the MIB symbol index (name, OID, description). Returns hits ordered best-first; rank is a bm25 score where a lower (more negative) value is a better match.",
	}, h.searchMIBs)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "lookup_oid",
		Description: "Resolve a numeric SNMP OID to its MIB symbol. Returns the exact symbol when a loaded module owns the OID, otherwise the nearest known prefix matches.",
	}, h.lookupOID)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "lookup_symbol",
		Description: "Return full detail for a symbol given its module and name: kind, syntax, access, status, OID, index columns, enum values, and description.",
	}, h.lookupSymbol)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "decode_walk",
		Description: "Decode pasted snmpwalk/snmpbulkwalk output. Resolves each line against loaded modules; unresolved OIDs carry enterprise (PEN) and nearest-module-root hints.",
	}, h.decodeWalk)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "classify_notification",
		Description: "Return the inferred notification relationship (raise/clear/orphan) with confidence and evidence for a given module and notification name.",
	}, h.classifyNotification)

	return srv
}
