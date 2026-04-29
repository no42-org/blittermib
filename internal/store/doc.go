// Package store persists the normalized MIB model in SQLite (via
// modernc.org/sqlite, no CGO) and exposes typed query helpers used
// by the HTTP server. Full-text search lives in an FTS5 virtual
// table populated from the symbol table.
package store
