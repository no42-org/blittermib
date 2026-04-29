package server

import (
	"net/http"
)

// routes registers all HTTP handlers on the server's multiplexer.
//
// URL plan (see openspec design.md):
//
//	/                              landing page
//	/m                             module index
//	/m/{module}                    module page
//	/s/{module}::{name}            symbol detail (canonical)
//	/s/{name}                      disambiguation, or 302 to canonical
//	/o/{oid}                       OID lookup → 302 to canonical /s/...
//	/search                        search results page
//	/diagnostics                   parse errors and warnings
//	/api/v1/search                 palette JSON
//	/api/v1/symbol/{module}/{name} symbol detail JSON
//	/static/*                      embedded CSS, fonts, JS islands
//	/healthz                       liveness check
//	/version                       build info
func (s *Server) routes() {
	s.mux.Handle("/static/", chain(http.StripPrefix("/static/", staticHandler()), withRecover, withLogging))

	s.mux.Handle("/healthz", chain(http.HandlerFunc(s.handleHealth), withRecover, withLogging))
	s.mux.Handle("/version", chain(http.HandlerFunc(s.handleVersion), withRecover, withLogging))

	s.mux.Handle("/m/", chain(http.HandlerFunc(s.handleModule), withRecover, withLogging))
	s.mux.Handle("/s/", chain(http.HandlerFunc(s.handleSymbol), withRecover, withLogging))
	s.mux.Handle("/o/", chain(http.HandlerFunc(s.handleOID), withRecover, withLogging))
	s.mux.Handle("/search", chain(http.HandlerFunc(s.handleSearch), withRecover, withLogging))
	s.mux.Handle("/diagnostics", chain(http.HandlerFunc(s.handleDiagnostics), withRecover, withLogging))

	s.mux.Handle("/api/v1/search", chain(http.HandlerFunc(s.handleAPISearch), withRecover, withLogging))
	s.mux.Handle("/api/v1/symbol/", chain(http.HandlerFunc(s.handleAPISymbol), withRecover, withLogging))

	s.mux.Handle("/", chain(http.HandlerFunc(s.handleIndex), withRecover, withLogging))
}
