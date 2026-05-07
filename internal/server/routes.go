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
//	/imprint                       operator disclosure (§ 5 TMG)
//	/privacy                       data-handling notice (GDPR)
//	/healthz                       liveness check
//	/version                       build info
func (s *Server) routes() {
	s.mux.Handle("/static/", chain(http.StripPrefix("/static/", staticHandler()), withLogging, withRecover))

	s.mux.Handle("/healthz", chain(http.HandlerFunc(s.handleHealth), withLogging, withRecover))
	s.mux.Handle("/version", chain(http.HandlerFunc(s.handleVersion), withLogging, withRecover))
	s.mux.Handle("/imprint", chain(http.HandlerFunc(s.handleImprint), withLogging, withRecover))
	s.mux.Handle("/privacy", chain(http.HandlerFunc(s.handlePrivacy), withLogging, withRecover))

	s.mux.Handle("/m/", chain(http.HandlerFunc(s.handleModule), withLogging, withRecover))
	s.mux.Handle("/s/", chain(http.HandlerFunc(s.handleSymbol), withLogging, withRecover))
	s.mux.Handle("/o/", chain(http.HandlerFunc(s.handleOID), withLogging, withRecover))
	s.mux.Handle("/search", chain(http.HandlerFunc(s.handleSearch), withLogging, withRecover))
	s.mux.Handle("/diagnostics", chain(http.HandlerFunc(s.handleDiagnostics), withLogging, withRecover))
	s.mux.Handle("/tree", chain(http.HandlerFunc(s.handleTree), withLogging, withRecover))
	s.mux.Handle("/tree/", chain(http.HandlerFunc(s.handleTree), withLogging, withRecover))

	s.mux.Handle("/api/v1/search", chain(http.HandlerFunc(s.handleAPISearch), withLogging, withRecover))
	s.mux.Handle("/api/v1/symbol/", chain(http.HandlerFunc(s.handleAPISymbol), withLogging, withRecover))
	s.mux.Handle("/api/v1/tree", chain(http.HandlerFunc(s.handleAPITree), withLogging, withRecover))
	s.mux.Handle("/api/v1/tree/fragment", chain(http.HandlerFunc(s.handleAPITreeFragment), withLogging, withRecover))

	s.mux.Handle("/", chain(http.HandlerFunc(s.handleIndex), withLogging, withRecover))
}
