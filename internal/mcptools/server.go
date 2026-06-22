/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Package mcptools builds the read-only Model Context Protocol server over
// the blittermib MIB archive. The same wired server backs both transports:
// the stdio binary (cmd/blittermib-mcp) and the web server's network /mcp
// route. Keeping the tool set, input bounds, and wire-format views in one
// place is what keeps those two transports from drifting apart.
package mcptools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/no42-org/blittermib/internal/store"
)

// NewServer builds the MCP server with all read-only tools registered,
// querying the given store. version is advertised as the server's build
// version in the initialize handshake; callers pass their own
// linker-stamped value (the stdio binary) or the web server's version.
func NewServer(st *store.Store, version string) *mcp.Server {
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
