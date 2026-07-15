/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/no42-org/blittermib/internal/correlate"
	"github.com/no42-org/blittermib/internal/eventconf"
	"github.com/no42-org/blittermib/internal/iana"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/source"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/web"
)

// underMIBRoot reports whether the absolute form of `p` lives at
// or under the absolute form of the server's MIB corpus root.
// Empty `p` and an empty root are rejected; resolving `.` ancestor
// segments is handled by `filepath.Abs` + `filepath.Rel`.
//
// Symlink semantics:
//   - When `p` exists on disk, its symlinks are resolved via
//     `filepath.EvalSymlinks` before the prefix check; the root
//     is resolved symmetrically so a `/var` → `/private/var`
//     rewrite on macOS doesn't make a real file under
//     `/var/folders/.../mibs` falsely escape.
//   - When `p` doesn't exist, `EvalSymlinks` fails and the raw
//     abs path is checked against the raw abs root. There's no
//     symlink to escape through (the path resolves to nothing),
//     and the lexical-prefix check is sufficient. The caller's
//     follow-up `os.Open` distinguishes "stale recorded path"
//     (410) from "path was unsafe" (404).
//
// Used by the module-download endpoints as a defense-in-depth
// guard against any future writer that might let a module's
// `source_path` be set to an arbitrary file. Today libsmi only
// reports paths under the configured MIB root, but this guard
// shrinks the blast radius of a regression.
func (s *Server) underMIBRoot(p string) bool {
	if p == "" || s.mibsDir == "" {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	resolved := false
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
		resolved = true
	}
	rabs, err := filepath.Abs(s.mibsDir)
	if err != nil {
		return false
	}
	if resolved {
		// File symlinks were followed; the root must be canonicalised
		// to match. Without this, a real file under a root whose
		// abs path traverses a parent symlink (macOS `/var` →
		// `/private/var`) lexically diverges from the unresolved
		// root and the prefix check would reject it.
		if real, err := filepath.EvalSymlinks(rabs); err == nil {
			rabs = real
		}
	}
	return isUnderRoot(rabs, abs)
}

// isUnderRoot reports whether `abs` is at or under `root`, both
// expected as cleaned absolute paths. Rejects any post-relativisation
// path containing a `..` component (a `..foo` basename is fine; an
// actual escape segment isn't).
func isUnderRoot(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// setAttachmentDisposition writes the `Content-Disposition: attachment`
// header with both the legacy `filename=` parameter (ASCII-only,
// non-printable bytes mapped to `_`) and the RFC 5987 `filename*=`
// parameter (UTF-8 percent-encoded). Defense-in-depth against any
// downstream writer that lets a non-ASCII byte slip into the module
// name; today `validModuleName` already rejects those at handler
// entry, but the dual-parameter form is the standards-compliant
// shape modern clients prefer.
func setAttachmentDisposition(w http.ResponseWriter, filename string) {
	ascii := strings.Map(func(r rune) rune {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' {
			return r
		}
		return '_'
	}, filename)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename=%q; filename*=UTF-8''%s`, ascii, url.PathEscape(filename)))
}

// validModuleName reports whether s matches the SMI module-name
// grammar from RFC 1212 §4.1.6 / RFC 2578 §3.1: leading letter
// followed by letters / digits / hyphens. Used to gate echoed
// query params before they flow into rendered URLs.
func validModuleName(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// --- ops endpoints ---------------------------------------------------

// handleHealth is PURE LIVENESS: "is the process able to serve HTTP?"
// It MUST NOT depend on the store or the corpus — a liveness probe
// that fails during a long boot-time corpus load restarts the very pod
// that is making progress (the CrashLoop this split exists to fix).
// Readiness (corpus loaded, store usable) lives at /readyz.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

// handleReady is READINESS: "has the initial corpus load completed and
// does the store answer?" 503 {"status":"loading"} while the boot-time
// load is still running; once the gate opens, a per-request store check
// guards against a broken store without re-latching the gate. Note the
// gate opens when the load ATTEMPT completes — per-file compile errors
// surface in logs/diagnostics, matching the old /healthz store-only
// contract, rather than holding the whole pod not-ready over one
// broken MIB.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "loading",
			"version": s.version,
		})
		return
	}
	if _, err := s.store.CountModules(r.Context()); err != nil {
		http.Error(w, "store unhealthy", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s.version + "\n"))
}

func (s *Server) handleImprint(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusOK, web.Imprint())
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusOK, web.Privacy())
}

// --- page handlers ---------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	modCount, _ := s.store.CountModules(ctx)
	symCount, _ := s.store.CountSymbols(ctx)
	if modCount == 0 {
		render(w, r, http.StatusOK, web.LandingEmpty(s.mibsDir))
		return
	}
	render(w, r, http.StatusOK, web.Landing(modCount, symCount, s.uploadsEnabled))
}

func (s *Server) handleModule(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/m")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		s.handleModuleIndex(w, r)
		return
	}
	// Slash-first dispatch — three branches:
	//   /m/{name}                → workspace empty
	//   /m/{name}/source         → raw MIB source
	//   /m/{name}/{oid…}         → workspace with selection
	//
	// The earlier suffix-first check (`HasSuffix("/source")`) caught
	// /m/{name}/{oid}/source as a source request with the OID
	// embedded in the module name, which then 404'd. The slash-first
	// split makes the source endpoint exactly `/m/{name}/source`
	// and the workspace path everything else.
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		s.handleWorkspace(w, r, rest, "")
		return
	}
	name, tail := rest[:i], rest[i+1:]
	switch tail {
	case "source":
		s.handleModuleSource(w, r, name)
		return
	case "download":
		s.handleModuleDownload(w, r, name)
		return
	case "download.zip":
		s.handleModuleBundle(w, r, name)
		return
	case "events.xml":
		s.handleModuleEvents(w, r, name)
		return
	}
	s.handleWorkspace(w, r, name, tail)
}

// handleModuleSource serves the raw MIB source file for a module as
// text/plain. http.ServeFile streams the file (handles range,
// etag, and if-modified-since for free) — better than reading
// the whole MIB into memory before writing.
func (s *Server) handleModuleSource(w http.ResponseWriter, r *http.Request, name string) {
	mod, err := s.store.GetModule(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if mod.SourcePath == "" {
		// Module is loaded but libsmi resolved it without a file
		// path (e.g. embedded module).
		s.notFound(w, r)
		return
	}
	if !s.underMIBRoot(mod.SourcePath) {
		s.notFound(w, r)
		return
	}
	// Pre-set the headers — http.ServeFile leaves them alone if
	// they're already populated. .mib files would otherwise default
	// to application/octet-stream which would prompt downloads.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, mod.SourcePath) // #nosec G703 -- guarded by underMIBRoot above
}

// handleModuleDownload serves the single MIB source file as a
// `text/plain` attachment named `{name}.mib`. The filename uses
// the module name (rather than the on-disk basename) because the
// embedded bundle stages files without extensions
// (`{data}/standard-mibs/IF-MIB`); `IF-MIB.mib` is what
// downstream tools (`smilint`, `snmptranslate`) expect.
//
// The path-traversal guard is the only difference from
// `handleModuleSource` beyond headers — `mod.SourcePath` came
// from the parser, which traces back to one of the configured
// roots, but a future writer (API ingest, migration tool) could
// regress that guarantee. 404 on miss matches the
// "module not found" outcome rather than leaking that the path
// existed but was unsafe.
//
// File handling is open-once: we open the source descriptor before
// committing headers and serve from the descriptor via
// `http.ServeContent`. This avoids the TOCTOU window where a Stat
// success would commit `text/plain; attachment` headers, then a
// later `http.ServeFile` could 404 — leaving the client with the
// download-as-`{name}.mib` headers but the framework's HTML 404
// body. With a held descriptor any racing unlink leaves us reading
// from the original inode.
func (s *Server) handleModuleDownload(w http.ResponseWriter, r *http.Request, name string) {
	if !validModuleName(name) {
		s.notFound(w, r)
		return
	}
	mod, err := s.store.GetModule(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if mod.SourcePath == "" {
		s.notFound(w, r)
		return
	}
	if !s.underMIBRoot(mod.SourcePath) {
		s.notFound(w, r)
		return
	}
	f, err := os.Open(mod.SourcePath) // #nosec G703 -- guarded by underMIBRoot above
	if err != nil {
		// File recorded in DB but gone from disk — distinguish
		// from "module never existed" so the user can see what
		// happened. Don't echo the recorded path back: it leaks
		// server-side filesystem layout to an unauthenticated
		// client.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte("module source no longer readable\n"))
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setAttachmentDisposition(w, mod.Name+".mib")
	http.ServeContent(w, r, mod.Name+".mib", info.ModTime(), f)
}

// handleModuleEvents serves the module's notifications as an OpenNMS
// eventconf XML attachment (`{name}.events.xml`). A module with no
// notifications returns 404 — there's nothing to export. The UEI base
// defaults to `uei.opennms.org/traps/{name}` and is overridable with
// `?uei=`; `?parms=position` forces legacy positional varbind tokens
// instead of the OID-based hybrid.
func (s *Server) handleModuleEvents(w http.ResponseWriter, r *http.Request, name string) {
	if !validModuleName(name) {
		s.notFound(w, r)
		return
	}
	if _, err := s.store.GetModule(r.Context(), name); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}

	ueibase := "uei.opennms.org/traps/" + name
	if q := r.URL.Query().Get("uei"); q != "" {
		if !validUEIBase(q) {
			http.Error(w, "invalid uei parameter", http.StatusBadRequest)
			return
		}
		ueibase = q
	}
	forcePositional := r.URL.Query().Get("parms") == "position"

	notifs, err := s.store.ListNotificationsWithObjects(r.Context(), name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(notifs) == 0 {
		s.notFound(w, r)
		return
	}

	// Attach inferred relationships so the export can emit alarm-data —
	// unless suppressed (?alarms=off) or below the High-confidence gate.
	// Only High-confidence relationships emit alarm-data; lower-confidence
	// inferences are exported as plain events (FR20, FR23).
	if r.URL.Query().Get("alarms") != "off" {
		rels, err := s.store.ListRelationships(r.Context(), name)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		// Only High-confidence relationships emit alarm-data.
		high := make(map[string]correlate.Relationship, len(rels))
		for _, rel := range rels {
			if rel.Confidence == correlate.ConfHigh {
				high[rel.Notification] = rel
			}
		}
		for i := range notifs {
			rel, ok := high[notifs[i].Symbol.Name]
			if !ok {
				continue
			}
			er := eventconf.Relationship{
				AlarmType: alarmType(rel.Class),
				Provenance: fmt.Sprintf("Notification Intelligence: inferred %s — %s; confidence %s",
					rel.Class, rel.Evidence.String(), rel.Confidence),
			}
			if rel.Class == correlate.ClassClear {
				// Keep only raises that are themselves High, so the
				// clear-key resolves to an emitted reduction-key. A clear
				// with no High raise to resolve is exported as a plain
				// event rather than a clear that clears nothing.
				for _, raise := range rel.Clears {
					if _, ok := high[raise]; ok {
						er.Clears = append(er.Clears, raise)
					}
				}
				if len(er.Clears) == 0 {
					continue
				}
			}
			notifs[i].Relationship = er
		}
	}

	events := eventconf.FromModule(name, notifs, eventconf.Options{
		UEIBase:         ueibase,
		ForcePositional: forcePositional,
	})
	out, err := eventconf.Marshal(events, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setAttachmentDisposition(w, name+".events.xml")
	// #nosec G705 -- `out` is generated eventconf XML served as an application/xml attachment with X-Content-Type-Options: nosniff; it is never interpreted as HTML, so the taint-flagged write is not an XSS vector.
	_, _ = w.Write(out)
}

// alarmType maps an inferred classification onto the OpenNMS
// alarm-data/@alarm-type value, or "" when unclassified.
func alarmType(c correlate.Classification) string {
	switch c {
	case correlate.ClassRaise:
		return eventconf.AlarmTypeRaise
	case correlate.ClassClear:
		return eventconf.AlarmTypeClear
	case correlate.ClassOrphan:
		return eventconf.AlarmTypeNotification
	}
	return ""
}

// validUEIBase reports whether s is a plausible event UEI base — the
// charset OpenNMS UEIs use (alphanumerics plus `. _ - / :`), with no
// spaces or control characters, AND at least one alphanumeric so a
// punctuation-only value (e.g. `/` or `:::`) can't normalize to an
// empty base and emit a malformed `<uei>/name`. Gates the echoed
// `?uei=` override before it flows into the generated document.
func validUEIBase(s string) bool {
	hasAlnum := false
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			hasAlnum = true
		case c >= 'a' && c <= 'z':
			hasAlnum = true
		case c >= '0' && c <= '9':
			hasAlnum = true
		case c == '.' || c == '_' || c == '-' || c == '/' || c == ':':
		default:
			return false
		}
	}
	return hasAlnum
}

// handleModuleBundle streams a ZIP containing the module + its
// transitive IMPORTS closure. Layout is flat — one
// `{ModuleName}.mib` per loaded module — with a `MISSING.txt`
// manifest at the root listing closure entries that could not be
// resolved (never loaded, or loaded with a source file that has
// since gone unreadable). The manifest is always emitted (even
// when no entries are missing) so machine consumers can detect
// a successful bundle without inferring from absence.
//
// `archive/zip` writes directly to the ResponseWriter, so the
// transfer streams without buffering. The trade-off is that
// once `WriteHeader(200)` ships, an error mid-walk produces a
// truncated ZIP — we log and let the client see CRC failures on
// extract rather than send a 5xx mid-body. Filesystem read
// errors on already-staged MIBs are rare in practice.
//
// 404 cases (refused before any bytes are committed):
//   - The root module is not in the store.
//   - The root module's `source_path` is recorded but resolves
//     outside the configured roots. This matches the spec's
//     "Both endpoints SHALL refuse to serve files whose recorded
//     `source_path` does not resolve under one of the configured
//     root directories ... returning 404 in that case." Closure
//     entries (i.e. transitive imports) are demoted to MISSING.txt
//     rather than refusing the whole bundle.
func (s *Server) handleModuleBundle(w http.ResponseWriter, r *http.Request, name string) {
	if !validModuleName(name) {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()

	// Pre-walk the root before committing any response state — the
	// bundle endpoint must return 404 (not 200 with a MISSING.txt
	// stub) when the root module's source path resolves outside the
	// configured roots.
	rootMod, err := s.store.GetModule(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if rootMod.SourcePath != "" && !s.underMIBRoot(rootMod.SourcePath) {
		s.notFound(w, r)
		return
	}

	closure, err := s.store.ListImportClosure(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	shippable, missings := s.partitionClosure(closure)

	w.Header().Set("Content-Type", "application/zip")
	setAttachmentDisposition(w, name+"-bundle.zip")

	zw := zip.NewWriter(w)
	defer func() {
		if err := zw.Close(); err != nil {
			slog.Warn("bundle: zip close", "module", name, "err", err)
		}
	}()

	extra, ok := copyMIBsToZip(ctx, zw, shippable, "bundle")
	missings = append(missings, extra...)
	if !ok {
		return
	}

	// MISSING.txt is always emitted, even when len(missings) == 0,
	// so machine consumers can rely on `unzip -l | grep MISSING.txt`
	// rather than inferring from absence whether the bundle was
	// complete.
	var buf strings.Builder
	fmt.Fprintf(&buf, "# Missing imports — modules referenced by %s and its dependencies\n", name)
	fmt.Fprintln(&buf, "# but not currently loaded into blittermib (or whose source")
	fmt.Fprintln(&buf, "# files were unreadable at download time).")
	fmt.Fprintf(&buf, "# Generated: %s\n", time.Now().UTC().Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&buf, "# Root module: %s\n\n", name)
	if len(missings) == 0 {
		fmt.Fprintln(&buf, "# (no missing imports — every closure entry was shipped)")
	}
	for _, m := range missings {
		fmt.Fprintf(&buf, "%s\n", m.Module)
		if m.ImportedBy != "" {
			fmt.Fprintf(&buf, "  imported by: %s\n", m.ImportedBy)
		}
		if len(m.Symbols) > 0 {
			fmt.Fprintf(&buf, "  symbols:     %s\n", strings.Join(m.Symbols, ", "))
		}
		fmt.Fprintf(&buf, "  reason:      %s\n\n", m.Reason)
	}
	if err := writeZipString(zw, "MISSING.txt", buf.String(), time.Now()); err != nil {
		slog.Warn("bundle: write MISSING.txt", "module", name, "err", err)
		return
	}
}

func (s *Server) handleModuleIndex(w http.ResponseWriter, r *http.Request) {
	mods, err := s.store.ListModules(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, web.ModuleIndex(mods))
}

// handleWorkspace serves the 3-pane workspace shell at /m/{name}
// and /m/{name}/{oid}. When oid is empty the right pane shows an
// empty-state hint; when oid resolves to a symbol the right pane
// renders the compact detail body plus an OID-decode breadcrumb;
// when oid is non-empty but doesn't match anything in the module
// the workspace renders without a selection plus a soft missing-OID
// notice.
//
// The `?sel=` query parameter splits SCOPE from SELECTION:
//
//   - The OID baked into the URL path is the SCOPE — it drives the
//     list pane's symbol set and the scope breadcrumb.
//   - `?sel=…` is the SELECTION — the symbol whose detail renders
//     in the right pane. When omitted, the scope-OID auto-selects
//     (matching the legacy single-OID behavior and keeping deep-
//     links to `/m/{name}/{oid}` working unchanged).
//
// Splitting them lets the handoff's "click a column → right pane
// updates, list stays put" workflow round-trip cleanly through
// the URL: clicking a leaf row stays on `/m/{name}/{scope}` and
// only updates `?sel`. See `web.WorkspaceRowURL` for the helper
// that builds those leaf-vs-container URLs from the templates.
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request, name, oid string) {
	ctx := r.Context()
	mod, err := s.store.GetModule(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	// SELECTION = `?sel=…` if provided, otherwise the path-OID auto-
	// selects so `/m/{name}/{oid}` deep-links keep working.
	selectionOID := r.URL.Query().Get("sel")
	if selectionOID == "" {
		selectionOID = oid
	}

	syms, err := s.store.ListSymbolsByModule(ctx, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	view, err := s.buildWorkspaceView(ctx, mod, syms, oid)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.resolveSelection(ctx, view, selectionOID); err != nil {
		s.internalError(w, r, err)
		return
	}

	// Partial navigation (the A/B contract — see the
	// workspace-partial-nav design): in-workspace htmx requests get
	// only the panes a click changes — the detail section always, plus
	// the list section out-of-band when the scope changed. The tree is
	// never part of a partial response; workspace.js (expandTreeTo)
	// synchronizes it in place, which preserves its DOM and scroll
	// position. History
	// restores (htmx cache misses) and plain requests always receive
	// the full document.
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		if curModule, curScope, ok := workspaceRef(r.Header.Get("HX-Current-URL")); ok {
			if curModule != name {
				// Cross-module partials shouldn't occur (module
				// switches navigate natively); a stale or hand-crafted
				// request gets a full client reload rather than panes
				// swapped into the wrong module's chrome.
				w.Header().Set("HX-Refresh", "true")
				w.WriteHeader(http.StatusOK)
				return
			}
			render(w, r, http.StatusOK, web.WorkspacePartial(view, curScope != oid))
			return
		}
		// HX-Current-URL absent or unparseable: we can't tell A from
		// B, and a full document swapped into #workspace-detail would
		// be wrong — serve case B (list + detail), the safe superset.
		render(w, r, http.StatusOK, web.WorkspacePartial(view, true))
		return
	}

	render(w, r, http.StatusOK, web.Workspace(view))
}

// buildWorkspaceView assembles the workspace's render model for one
// module: the (optionally OID-scoped) list rows, family counts, the
// module list for the picker, download affordances, and the TC
// type-defs bar. The left-pane tree is the global OID-tree island
// (client-side, oid_node-backed) — no server-rendered tree rows.
// Selection resolution is layered on top by resolveSelection.
func (s *Server) buildWorkspaceView(ctx context.Context, mod *model.Module, syms []model.Symbol, oid string) (*web.WorkspaceView, error) {
	name := mod.Name

	counts, err := s.store.CountByFamily(ctx, name)
	if err != nil {
		return nil, err
	}

	allModules, err := s.store.ListModules(ctx)
	if err != nil {
		return nil, err
	}

	// When the URL specifies an OID, narrow the center-pane list to
	// symbols at or under that OID. The "View all in module" link
	// in the list-pane chrome navigates back to the unscoped URL.
	listRows := syms
	if oid != "" {
		listRows = listRows[:0:0]
		for i := range syms {
			if web.OIDUnderPrefix(syms[i].OID, oid) {
				listRows = append(listRows, syms[i])
			}
		}
	}
	// Baseline for the Scoped flag: no-OID symbols (TCs, some groups)
	// can never pass OIDUnderPrefix, so comparing against len(syms)
	// would mark a module-root scope as "narrowed" on any module that
	// defines a TC. Count only OID-bearing symbols.
	oidBearing := 0
	for i := range syms {
		if syms[i].OID != "" {
			oidBearing++
		}
	}

	// Scoped iff the scope OID actually narrows the list (computed from the
	// at-or-under count, BEFORE the scope-root row is stripped below).
	scoped := oid != "" && len(listRows) < oidBearing

	// When genuinely scoped to a sub-container, drop the scope-root row
	// itself from the list — it is already the breadcrumb's current crumb
	// (and the auto-selected detail), so listing it again just duplicates
	// it. Unscoped / module-root views keep every row. Guard: if stripping
	// the root would leave nothing (a deep-link scoped directly to a leaf
	// OID, whose only row IS the root), keep the root so the list isn't
	// empty.
	if scoped {
		kept := listRows[:0:0]
		for i := range listRows {
			if listRows[i].OID != oid {
				kept = append(kept, listRows[i])
			}
		}
		if len(kept) > 0 {
			listRows = kept
		}
	}

	// Pre-compute disk-availability so the module-info bar can hide
	// download affordances when the source has disappeared. A single
	// stat per render is cheap; doing it from inside the templ would
	// pull I/O into the rendering layer. Errors (including ENOENT)
	// flatten to "not downloadable".
	downloadable := false
	if mod.SourcePath != "" && s.underMIBRoot(mod.SourcePath) {
		if _, err := os.Stat(mod.SourcePath); err == nil { // #nosec G703 -- guarded by underMIBRoot above
			downloadable = true
		}
	}

	// Pre-compute the bundle's `.mib` file count from the IMPORTS
	// closure so the module-info bar can advertise an accurate
	// number — using `len(mod.Imports)` would count flat
	// per-symbol imports (e.g. each `Counter32`, `Integer32`,
	// `TimeTicks` from SNMPv2-SMI as a separate entry), which
	// massively over-counts. The bundle endpoint ships one `.mib`
	// per loaded closure entry; this counts the same set so the
	// displayed number matches what the user actually downloads.
	// Errors collapse to 0 so the templ can suppress the count
	// gracefully — closure walks should not be load-bearing for
	// rendering the workspace itself.
	bundleFileCount := 0
	if downloadable {
		closure, err := s.store.ListImportClosure(ctx, name)
		if err != nil {
			slog.Warn("workspace: import-closure count failed", "module", name, "err", err)
		} else {
			for _, e := range closure {
				if e.Loaded && e.SourcePath != "" && s.underMIBRoot(e.SourcePath) {
					bundleFileCount++
				}
			}
		}
	}

	// Whether to surface the eventconf export link — derived from the
	// already-loaded module symbols, so no extra query. Covers both
	// SMIv2 NOTIFICATION-TYPE and SMIv1 TRAP-TYPE.
	hasNotifications := false
	for i := range syms {
		if syms[i].Kind == model.KindNotificationType || syms[i].Kind == model.KindTrapType {
			hasNotifications = true
			break
		}
	}

	// Per-notification classifications for the inline list-row badges.
	var relationships map[string]correlate.Relationship
	if hasNotifications {
		if rels, err := s.store.ListRelationships(ctx, name); err != nil {
			slog.Warn("workspace: relationship lookup failed", "module", name, "err", err)
		} else if len(rels) > 0 {
			relationships = make(map[string]correlate.Relationship, len(rels))
			for _, rel := range rels {
				relationships[rel.Notification] = rel
			}
		}
	}

	return &web.WorkspaceView{
		Module:             mod,
		Counts:             counts,
		ListRows:           listRows,
		Modules:            allModules,
		ScopeOID:           oid,
		Scoped:             scoped,
		ModuleDownloadable: downloadable,
		TypeDefs:           web.CollectTypeDefs(syms),
		BundleFileCount:    bundleFileCount,
		HasNotifications:   hasNotifications,
		Relationships:      relationships,
	}, nil
}

// resolveSelection resolves the `sel=` / path-OID selection into
// view.Selected (or view.MissingOID when nothing matches), decodes
// the OID path the scope breadcrumb derives from, and pre-expands
// the tree spine down to the selection. A no-op when selectionOID
// is empty.
func (s *Server) resolveSelection(ctx context.Context, view *web.WorkspaceView, selectionOID string) error {
	name := view.Module.Name

	if selectionOID != "" {
		// `sel=` may be either an OID (digits + dots, the common
		// case) or a symbol name (textual conventions and other
		// no-OID symbols ride in by name). SMI names always start
		// with a letter, so the first-char digit check is enough
		// to disambiguate. Name-keyed lookups go through
		// GetSymbol(module, name) so a TC click resolves to its
		// row even when the path-OID slot is empty.
		var sym *model.Symbol
		var lookupErr error
		if web.SelectorLooksLikeOID(selectionOID) {
			sym, lookupErr = s.store.GetSymbolByOID(ctx, selectionOID)
		} else {
			sym, lookupErr = s.store.GetSymbol(ctx, name, selectionOID)
		}
		switch {
		case errors.Is(lookupErr, store.ErrNotFound):
			view.MissingOID = selectionOID
		case lookupErr != nil:
			return lookupErr
		default:
			selected, err := s.buildSymbolView(ctx, sym)
			if err != nil {
				return err
			}
			// NOTIFICATION-TYPE / TRAP-TYPE OBJECTS clause —
			// outbound references of kind RefNotificationObject.
			// Surfaced in the workspace right pane as clickable
			// links so a reader can jump from "what does linkDown
			// carry?" straight to ifAdminStatus's detail. (The /s/
			// deep-link page deliberately stays without this block.)
			if sym.Kind == model.KindNotificationType || sym.Kind == model.KindTrapType {
				outRefs, err := s.store.ListReferencesFrom(ctx, sym.ModuleName, sym.Name)
				if err != nil {
					return err
				}
				selected.NotifyObjects, selected.TrapIndex = s.buildNotifyVarbinds(ctx, outRefs)
			}
			view.Selected = selected
			// SelectionOID is the OID the tree island expands the spine
			// down to (data-tree-focus). Empty for no-OID symbols (TCs),
			// which leave the tree at the apex.
			view.SelectionOID = sym.OID
			// view.OIDPath is still decoded (the scope breadcrumb
			// derives from it via `web.ScopeBreadcrumb`); the
			// right-pane no longer renders an "OID decode"
			// section, but the breadcrumb still needs the chain.
			if sym.OID != "" {
				path, err := s.store.OIDPath(ctx, sym.OID)
				if err != nil {
					return err
				}
				view.OIDPath = path
			}
		}
	}

	// The tree spine is expanded client-side by the tree.js island
	// (workspace mode walks the selection OID's prefixes), so there is no
	// server-side pre-expansion here — the tree is the global OID trie,
	// not a per-module render.
	return nil
}

// workspaceRef extracts the module name and scope OID from a
// workspace URL (the htmx HX-Current-URL header). ok is false for
// URLs outside the `/m/{module}[/{scope}]` shape — the caller then
// falls back to a safe default rather than guessing.
func workspaceRef(raw string) (module, scope string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	rest, found := strings.CutPrefix(u.Path, "/m/")
	if !found || rest == "" {
		return "", "", false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], rest[i+1:], true
	}
	return rest, "", true
}

func (s *Server) handleSymbol(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	if rest == "" {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	module, name, ok := splitQualified(rest)
	if !ok {
		s.handleSymbolDisambiguation(w, r, rest)
		return
	}
	sym, err := s.store.GetSymbol(ctx, module, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	view, err := s.buildSymbolView(ctx, sym)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, web.SymbolDetail(view))
}

// handleSymbolDisambiguation handles the `/s/{name}` form (no
// `Module::` prefix). One match → 302 to the canonical URL; multiple
// matches → chooser page; zero → 404. Spec R5 / spec scenario
// "Search by exact symbol".
func (s *Server) handleSymbolDisambiguation(w http.ResponseWriter, r *http.Request, name string) {
	matches, err := s.store.LookupByName(r.Context(), name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	switch len(matches) {
	case 0:
		s.notFound(w, r)
	case 1:
		// Single-match redirect lands in the workspace selection so
		// `/s/{name}` is consistent with the other Phase-3 nav
		// surfaces (search hits, ⌘K palette, /o/{oid}). Symbols
		// without an OID still resolve to /s/... via the helper.
		// #nosec G710 -- target is an internal /m/... URL built from DB-owned identifiers; no user-supplied scheme/host.
		http.Redirect(w, r, string(web.WorkspaceSymbolURL(matches[0].ModuleName, matches[0].Name, matches[0].OID)), http.StatusFound)
	default:
		render(w, r, http.StatusOK, web.SymbolDisambiguation(name, matches))
	}
}

// buildSymbolView assembles the right-pane symbol view shared by the
// workspace selection branch and the /s/ deep-link page: the
// in-context block, table columns (tables only), inbound references,
// and the SMI source slice for the "Show full SMI source" disclosure.
// The workspace path layers its notification-varbind extras on top so
// the two surfaces can't drift on the shared pieces.
func (s *Server) buildSymbolView(ctx context.Context, sym *model.Symbol) (*web.SymbolView, error) {
	v := &web.SymbolView{Symbol: sym}
	// Resolve a bare TC syntax (e.g. `InetAddressType`) to its defining
	// TEXTUAL-CONVENTION so the syntax chip can link to it and, when the
	// object declares no inline enumeration, its enumerated values surface
	// under this object too.
	if tc, err := s.store.ResolveSyntaxTC(ctx, sym); err != nil {
		slog.WarnContext(ctx, "symbol view: syntax TC resolution failed", "module", sym.ModuleName, "name", sym.Name, "err", err)
	} else if tc != nil {
		v.SyntaxRef = tc
		if len(sym.EnumValues) == 0 && len(tc.EnumValues) > 0 {
			sym.EnumValues = tc.EnumValues
			v.EnumInherited = true
		}
	}
	if sym.Kind == model.KindNotificationType || sym.Kind == model.KindTrapType {
		if rel, err := s.store.GetRelationship(ctx, sym.ModuleName, sym.Name); err == nil {
			v.Relationship = rel
		} else {
			slog.WarnContext(ctx, "symbol view: relationship lookup failed", "module", sym.ModuleName, "name", sym.Name, "err", err)
		}
	}
	v.Context = s.buildSymbolContext(ctx, sym)
	if sym.Kind == model.KindTable {
		v.Columns = s.buildTableColumns(ctx, sym)
	}
	usedBy, err := s.store.ListReferencesTo(ctx, sym.ModuleName, sym.Name)
	if err != nil {
		return nil, err
	}
	v.UsedBy = usedBy
	if mod, err := s.store.GetModule(ctx, sym.ModuleName); err == nil && mod.SourcePath != "" && sym.SourceLine > 0 {
		if slice, err := source.Slice(mod.SourcePath, sym.SourceLine, source.DefaultWindow); err == nil && slice != "" {
			v.SourceText = slice
			v.SourcePath = mod.SourcePath
		}
	}
	// OID collision: OTHER symbols the module defines at this symbol's OID
	// (a MIB bug). Drives the detail-pane warning on both the workspace and
	// the /s/ page. A well-formed module returns just this symbol → none.
	if peers, err := s.store.SymbolsAtOID(ctx, sym.ModuleName, sym.OID); err == nil {
		for i := range peers {
			if peers[i].Name != sym.Name {
				v.CollisionSiblings = append(v.CollisionSiblings, web.OIDCollision{
					Name: peers[i].Name, Kind: peers[i].Kind,
				})
			}
		}
	} else {
		slog.WarnContext(ctx, "symbol view: collision lookup failed", "module", sym.ModuleName, "oid", sym.OID, "err", err)
	}
	return v, nil
}

// buildSymbolContext computes the in-context block for a symbol —
// "Column N of X table, Indexed by Y, Augments Z" — entirely from
// stored data (parent_oid, IndexColumns, Augments).
func (s *Server) buildSymbolContext(ctx context.Context, sym *model.Symbol) *web.SymbolContext {
	out := &web.SymbolContext{}
	any := false

	// Walk up to find the table-entry parent (if column) or table parent.
	if sym.ParentOID != "" {
		parent, err := s.store.GetSymbolByOID(ctx, sym.ParentOID)
		if err == nil {
			switch {
			case parent.Kind == model.KindTableEntry:
				// We're a column. The table is parent's parent.
				if parent.ParentOID != "" {
					if grand, err := s.store.GetSymbolByOID(ctx, parent.ParentOID); err == nil && grand.Kind == model.KindTable {
						out.ParentTable = &web.SymbolRef{Module: grand.ModuleName, Name: grand.Name}
						out.ColumnNumber = lastOIDSegment(sym.OID)
						any = true
					}
				}
				// Inherit the index columns from the entry.
				for _, idx := range parent.IndexColumns {
					out.IndexedBy = append(out.IndexedBy, web.SymbolRef{Module: parent.ModuleName, Name: idx})
				}
				if len(parent.IndexColumns) > 0 {
					any = true
				}
			case parent.Kind == model.KindTable && sym.Kind == model.KindTableEntry:
				// We're an entry — point to the parent table.
				out.ParentTable = &web.SymbolRef{Module: parent.ModuleName, Name: parent.Name}
				any = true
			}
		}
	}

	// Direct entry-row data.
	if sym.Kind == model.KindTableEntry {
		for _, idx := range sym.IndexColumns {
			out.IndexedBy = append(out.IndexedBy, web.SymbolRef{Module: sym.ModuleName, Name: idx})
		}
		if len(sym.IndexColumns) > 0 {
			any = true
		}
	}

	if sym.Augments != "" {
		mod, name, ok := splitQualified(sym.Augments)
		if !ok {
			mod, name = sym.ModuleName, sym.Augments
		}
		out.Augments = &web.SymbolRef{Module: mod, Name: name}
		any = true
	}

	if !any {
		return nil
	}
	return out
}

// buildNotifyVarbinds resolves each RefNotificationObject reference
// into the rich shape the trap-simulator modal needs: the
// varbind's OID, syntax, snmptrap type letter, JSON-encoded enum
// values (when the syntax is enumerated), and a column-vs-scalar
// flag. Also derives the row-identity strategy across all
// resolved varbinds — when every column varbind shares a parent
// table-entry whose INDEX clause is a single INTEGER column, the
// modal renders one labeled input; otherwise it falls back to a
// raw-suffix text input.
//
// References to symbols that aren't loaded are skipped (the
// reference still rendered in earlier UIs as a clickable link
// even when the target wasn't loaded; here we drop them, since
// the modal needs the syntax to know what type letter to use,
// and the link rendering happens via the existing notify-object
// templ markup).
func (s *Server) buildNotifyVarbinds(ctx context.Context, refs []model.Reference) ([]web.NotifyVarbind, web.TrapIndexStrategy) {
	out := make([]web.NotifyVarbind, 0, len(refs))
	var sharedEntryOID string
	allColumns := true
	allScalar := true
	conflictingEntries := false

	for _, ref := range refs {
		if ref.Kind != model.RefNotificationObject {
			continue
		}
		target, err := s.store.GetSymbol(ctx, ref.TargetModule, ref.TargetName)
		if err != nil {
			// Unloaded varbind target — render a placeholder entry so
			// the modal can still emit something sensible (with
			// snmptrap letter "s") and the user knows what's
			// missing.
			out = append(out, web.NotifyVarbind{
				Module: ref.TargetModule,
				Name:   ref.TargetName,
			})
			allColumns = false
			allScalar = false
			continue
		}
		vb := web.NotifyVarbind{
			Module:         target.ModuleName,
			Name:           target.Name,
			OID:            target.OID,
			Syntax:         target.Syntax,
			TrapTypeLetter: web.TrapTypeLetter(target.Syntax),
			IsColumn:       target.Kind == model.KindColumn,
		}
		// A varbind whose SYNTAX is an imported INTEGER-enum TC (e.g.
		// `InetAddressType`, `TruthValue`) carries no inline enumeration —
		// borrow it from the TC so the simulator renders a value dropdown
		// instead of a free-text box. BITS TCs are excluded: they store
		// their named bits in the same EnumValues field, but a bit-string
		// is neither a single-choice integer nor snmptrap type `i`, so
		// inheriting them would render a misleading single-select and
		// mistype the varbind (see the `i` forcing below).
		if len(target.EnumValues) == 0 {
			if tc, err := s.store.ResolveSyntaxTC(ctx, target); err != nil {
				slog.WarnContext(ctx, "trap-simulator: syntax TC resolution failed", "module", target.ModuleName, "name", target.Name, "err", err)
			} else if tc != nil && !isBitsSyntax(tc.Syntax) {
				target.EnumValues = tc.EnumValues
			}
		}
		if len(target.EnumValues) > 0 {
			if buf, err := json.Marshal(target.EnumValues); err == nil {
				vb.EnumValuesJSON = string(buf)
			} else {
				slog.Warn("trap-simulator: marshal enum values",
					"module", target.ModuleName,
					"name", target.Name,
					"err", err,
				)
			}
			// Enum-valued symbols are always INTEGER subtypes per
			// SMI; force the trap type letter even if the syntax
			// is something `TrapTypeLetter` doesn't recognise (a
			// vendor-named TC, an obscure subtype, etc.).
			vb.TrapTypeLetter = "i"
		}
		out = append(out, vb)

		if target.Kind == model.KindColumn {
			allScalar = false
			// Walk one parent — should be the table-entry — and
			// pin the entry's OID for the shared-parent check.
			if target.ParentOID != "" {
				if sharedEntryOID == "" {
					sharedEntryOID = target.ParentOID
				} else if sharedEntryOID != target.ParentOID {
					conflictingEntries = true
				}
			} else {
				conflictingEntries = true
			}
		} else {
			allColumns = false
		}
	}

	// Decide the index strategy.
	if len(out) == 0 {
		// No varbinds (e.g. authenticationFailure, coldStart,
		// warmStart in SNMPv2-MIB). The simulator has no row
		// identity to prompt for; the trap is sent with just
		// its OID. Returning "scalar-only" suppresses both the
		// single-int input and the raw-suffix fallback in the
		// modal.
		return out, web.TrapIndexStrategy{Mode: "scalar-only"}
	}
	if allScalar {
		return out, web.TrapIndexStrategy{Mode: "scalar-only"}
	}
	if allColumns && !conflictingEntries && sharedEntryOID != "" {
		entry, err := s.store.GetSymbolByOID(ctx, sharedEntryOID)
		// Defensive nil-entry guard. The store contract today
		// returns a non-nil pointer when err is nil, but a future
		// store change could surface a (nil, nil) path; without
		// the guard the next access panics.
		if err == nil && entry != nil && len(entry.IndexColumns) >= 1 {
			// Walk every index column in INDEX-clause order and
			// classify each one. SMIv2's IMPLIED keyword applies
			// only to the LAST column (RFC 2578 §7.7) — middle
			// variable-length columns must be length-prefixed
			// regardless, otherwise the encoder has no way to
			// delimit them. The `impliedForCol` argument carries
			// that "last column only" constraint into each
			// classification.
			//
			// If any column fails to classify (unknown syntax,
			// unloaded symbol, empty BITS list, etc.) the entire
			// INDEX clause drops to raw-suffix — partial
			// classification would compose a malformed suffix
			// downstream.
			cols := make([]web.TrapIndexColumn, 0, len(entry.IndexColumns))
			classified := true
			for i, colName := range entry.IndexColumns {
				isLast := i == len(entry.IndexColumns)-1
				impliedForCol := isLast && entry.IndexImplied
				col, ok := s.classifyIndexColumn(ctx, entry.ModuleName, colName, impliedForCol)
				if !ok {
					classified = false
					break
				}
				cols = append(cols, col)
			}
			if classified && len(cols) > 0 {
				return out, web.TrapIndexStrategy{
					Mode:    "indexed",
					Columns: cols,
				}
			}
		}
	}
	return out, web.TrapIndexStrategy{Mode: "raw-suffix"}
}

// classifyIndexColumn classifies a single index column's syntax
// into a `web.TrapIndexColumn` descriptor. The `impliedForCol`
// argument is the IsImplied value to attach when the syntax is
// variable-length: in a multi-column INDEX clause, only the LAST
// column may inherit the parent entry's IMPLIED bit; middle
// variable columns must always force `IsImplied=false` so they
// length-prefix on the wire.
//
// Returns ok=false when the column's symbol can't be loaded, the
// syntax doesn't match any classifier branch, or a degenerate
// case (empty BITS list) makes the descriptor unusable. Callers
// drop the entire INDEX clause to raw-suffix on a single false
// — partial classification would yield a malformed suffix.
func (s *Server) classifyIndexColumn(
	ctx context.Context,
	moduleName, columnName string,
	impliedForCol bool,
) (web.TrapIndexColumn, bool) {
	idx, err := s.store.GetSymbol(ctx, moduleName, columnName)
	if err != nil || idx == nil {
		return web.TrapIndexColumn{}, false
	}
	switch {
	case isInetAddressTypeSyntax(idx.Syntax):
		// RFC 4001 InetAddressType — enumerated integer. The
		// modal renders a `<select>` with the standard enum
		// options instead of a plain numeric input. Caught
		// before `isIntegerSyntax` so the descriptor preserves
		// the InetAddressType-ness for the templ branch.
		return web.TrapIndexColumn{
			Name:   columnName,
			Syntax: "InetAddressType",
		}, true
	case isIntegerSyntax(idx.Syntax):
		return web.TrapIndexColumn{
			Name:   columnName,
			Syntax: "INTEGER",
		}, true
	case isIPAddressSyntax(idx.Syntax):
		return web.TrapIndexColumn{
			Name:   columnName,
			Syntax: "IpAddress",
		}, true
	case strings.TrimSpace(idx.Syntax) == "InetAddressIPv4":
		// InetAddressIPv4 is fixed 4 bytes, identical to IpAddress
		// in dotted-suffix encoding — emit IpAddress so the modal
		// renders a friendly dotted-quad input rather than a
		// 4-byte hex input. The wire encoding is byte-for-byte
		// identical (`.{a}.{b}.{c}.{d}`), so there's no
		// correctness cost.
		return web.TrapIndexColumn{
			Name:   columnName,
			Syntax: "IpAddress",
		}, true
	case isOctetStringSyntax(idx.Syntax):
		lo, hi, sizeOk := extractSizeConstraint(idx.Syntax)
		fixed := sizeOk && lo == hi && lo > 0
		if fixed {
			return web.TrapIndexColumn{
				Name:      columnName,
				Syntax:    "OCTET STRING",
				SizeMin:   lo,
				SizeMax:   hi,
				IsImplied: false,
			}, true
		}
		return web.TrapIndexColumn{
			Name:      columnName,
			Syntax:    "OCTET STRING",
			SizeMin:   lo,
			SizeMax:   hi,
			IsImplied: impliedForCol,
		}, true
	case isOIDSyntax(idx.Syntax):
		return web.TrapIndexColumn{
			Name:      columnName,
			Syntax:    "OBJECT IDENTIFIER",
			IsImplied: impliedForCol,
		}, true
	case isBitsSyntax(idx.Syntax):
		if size := bitsBytes(idx.EnumValues); size > 0 {
			return web.TrapIndexColumn{
				Name:      columnName,
				Syntax:    "BITS",
				SizeMin:   size,
				SizeMax:   size,
				IsImplied: false,
			}, true
		}
	}
	return web.TrapIndexColumn{}, false
}

// isIPAddressSyntax reports whether `s` resolves to an SMI
// `IpAddress` base type, ignoring trailing constraints. The
// compile layer expands the IpAddress TC during parse so the
// syntax string is the literal token; a permissive match also
// catches any whitespace / constraint suffix that future
// smidump versions might emit verbatim.
func isIPAddressSyntax(s string) bool {
	return baseSyntaxToken(s) == "IpAddress"
}

// isIntegerSyntax reports whether `s` resolves to an INTEGER /
// Integer32 base type, ignoring inline enum bodies and range
// constraints. Delegates to `web.TrapTypeLetter`'s integer
// classification — one list, no drift — plus `Unsigned32`, which the
// trap letter maps to "u" but which is a single-integer INDEX type
// for decode purposes (RFC 2578 §7.1.4; `alarmActiveIndex`,
// `vacmContextIndex`, …). Gauge32 also letters as "u" but is NOT an
// index-integer, which is why the extra check matches the base token
// rather than the letter.
func isIntegerSyntax(s string) bool {
	return web.TrapTypeLetter(s) == "i" || baseSyntaxToken(s) == "Unsigned32"
}

// buildTableColumns returns the column rows for a SMIv2 table's
// symbol page. Columns are the children of the entry row, ordered by
// OID. Index columns get the IsIndex flag set.
func (s *Server) buildTableColumns(ctx context.Context, table *model.Symbol) []web.TableColumn {
	if table.Kind != model.KindTable {
		return nil
	}
	rows, err := s.store.ListChildren(ctx, table.OID)
	if err != nil {
		return nil
	}
	var entry *model.Symbol
	for i := range rows {
		if rows[i].Kind == model.KindTableEntry {
			entry = &rows[i]
			break
		}
	}
	if entry == nil {
		return nil
	}
	indexSet := make(map[string]bool, len(entry.IndexColumns))
	for _, n := range entry.IndexColumns {
		indexSet[n] = true
	}
	cols, err := s.store.ListChildren(ctx, entry.OID)
	if err != nil {
		return nil
	}
	out := make([]web.TableColumn, 0, len(cols))
	for _, c := range cols {
		out = append(out, web.TableColumn{
			Position: lastOIDSegment(c.OID),
			Module:   c.ModuleName,
			Name:     c.Name,
			Syntax:   c.Syntax,
			Access:   string(c.Access),
			Status:   string(c.Status),
			Units:    c.Units,
			IsIndex:  indexSet[c.Name],
		})
	}
	return out
}

func (s *Server) handleOID(w http.ResponseWriter, r *http.Request) {
	oid := strings.TrimPrefix(r.URL.Path, "/o/")
	if oid == "" {
		s.notFound(w, r)
		return
	}
	sym, err := s.store.GetSymbolByOID(r.Context(), oid)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Redirect to the workspace selection rather than the canonical
	// /s/... page so the user lands in the navigation context that
	// owns the OID. The /s/... page remains for direct deep links.
	// #nosec G710 -- target is an internal /m/... URL built from DB-owned identifiers; no user-supplied scheme/host.
	http.Redirect(w, r, "/m/"+sym.ModuleName+"/"+sym.OID, http.StatusFound)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		render(w, r, http.StatusOK, web.SearchEmpty())
		return
	}
	ctx := r.Context()
	hits, err := s.searchWithExactMatch(ctx, q, 50)
	switch classifySearchErr(err) {
	case searchErrTooShort:
		// Too broad to search, not "no results" — show the same page as
		// an empty query rather than claiming nothing matched.
		render(w, r, http.StatusOK, web.SearchEmpty())
		return
	case searchErrTimeout:
		render(w, r, http.StatusGatewayTimeout,
			web.InternalError("search timed out — try a longer or more specific query"))
		return
	case searchErrCanceled:
		// The client went away (typeahead abort, navigation) — there is
		// nobody left to render for, and it isn't a server fault worth
		// an error log.
		return
	case searchErrInternal:
		s.internalError(w, r, err)
		return
	}
	if len(hits) == 0 {
		// Fall through to "did you mean": Levenshtein-against-name
		// candidates. Errors here are non-fatal — the no-results
		// page is still useful without suggestions.
		suggestions, err := s.store.DidYouMean(ctx, q, 5)
		if err != nil {
			slog.Warn("did-you-mean failed", "q", q, "err", err)
		}
		render(w, r, http.StatusOK, web.SearchNoResults(q, toWebHits(suggestions)))
		return
	}
	render(w, r, http.StatusOK, web.SearchResults(q, toWebHits(hits)))
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mods, err := s.store.ListModules(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var groups []web.ModuleDiagnostics
	for _, m := range mods {
		if m.ParseStatus == model.ParseStatusClean {
			continue
		}
		diags, err := s.store.ListDiagnosticsByModule(ctx, m.Name)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		groups = append(groups, web.ModuleDiagnostics{Module: m, Diagnostics: diags})
	}
	render(w, r, http.StatusOK, web.Diagnostics(groups))
}

// --- tree page -------------------------------------------------------

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/tree")
	rest = strings.TrimPrefix(rest, "/")
	render(w, r, http.StatusOK, web.TreePage(rest))
}

// --- JSON API --------------------------------------------------------

func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"hits": []any{}})
		return
	}
	hits, err := s.searchWithExactMatch(r.Context(), q, 25)
	switch classifySearchErr(err) {
	case searchErrTooShort:
		// The typeahead fires per keystroke; a declined-as-too-broad
		// query is an empty result, not an error, from its point of view.
		writeJSON(w, http.StatusOK, map[string]any{"hits": []any{}})
		return
	case searchErrTimeout:
		s.apiError(w, r, http.StatusGatewayTimeout, "search timed out", nil)
		return
	case searchErrCanceled:
		// Expected in steady state: the palette aborts the in-flight
		// request on every superseded keystroke. Not an error, and the
		// aborted client cannot read a response anyway.
		return
	case searchErrInternal:
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

// apiTreeMaxLimit caps the page size a caller can request; apiTreeDefaultLimit
// is used when none is given. Bounds keep a single wide node (e.g.
// `enterprises`, ~3k children) from serialising thousands of rows at once.
const (
	apiTreeDefaultLimit = 200
	apiTreeMaxLimit     = 500
)

// treeListParams parses the tree query params shared by the level and spine
// endpoints: the page `limit` (defaulted and clamped to the bounded range),
// `branchesOnly` (the workspace "container map", which hides leaf objects),
// and the kind-chip `family` (honoured only with branchesOnly; any unknown
// value is ignored, degrading to the full map).
func treeListParams(r *http.Request) (limit int, branchesOnly bool, family string) {
	limit = apiTreeDefaultLimit
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > apiTreeMaxLimit {
		limit = apiTreeMaxLimit
	}
	branchesOnly = r.URL.Query().Get("branches") == "1"
	if branchesOnly {
		switch r.URL.Query().Get("family") {
		case "scalar":
			family = "scalar"
		case "table":
			family = "table"
		case "notif":
			family = "notif"
		}
	}
	return limit, branchesOnly, family
}

// segDisplayName resolves one folded-chain segment's display name: the
// stored symbol name when it is symbol-backed, else the IANA canonical
// name for its OID, else the bare numeric segment. Mirrors the per-row
// fallback the tree uses for synthetic bridge nodes, applied per segment
// so a compressed name-path (e.g. "iso.org.dod") names every hop.
func segDisplayName(seg store.FoldSeg) string {
	if seg.HasSymbol && seg.Name != "" {
		return seg.Name
	}
	if canon, ok := iana.LookupCanonical(seg.OID); ok {
		return canon
	}
	return seg.Label
}

// treeItem is one folded child row of the OID trie projected for the tree
// JSON APIs (`/api/v1/tree` and `/api/v1/tree/spine`). A run of single-child
// nodes is path-compressed to its deepest (anchor) node: Position/NamePath
// render the dotted seg/name path, while OID/Name/expand address the anchor.
// DirectOID is the keyset cursor key (the direct child under the parent),
// kept distinct from the anchor so its last segment stays a valid sibling
// key. HasChildren is the EXPANDABLE signal (distinct from ChildCount): in
// branches mode a table-entry has ChildCount>0 but HasChildren=false (its
// only children are hidden leaf columns), so the chevron comes from
// HasChildren while ChildCount drives the badge.
type treeItem struct {
	OID         string `json:"oid"`         // anchor OID — expand + data-oid
	DirectOID   string `json:"directOID"`   // direct child OID — cursor + fold coverage
	Name        string `json:"name"`        // anchor name — /s/ link
	NamePath    string `json:"namePath"`    // dotted resolved names — display
	Module      string `json:"module"`      // anchor module
	Kind        string `json:"kind"`        // anchor kind
	HasSymbol   bool   `json:"hasSymbol"`   // anchor is symbol-backed
	HasChildren bool   `json:"hasChildren"` // anchor is expandable (has a rendered child)
	ChildCount  int64  `json:"childCount"`  // anchor child count — badge
	Position    string `json:"position"`    // dotted seg-path under parent
}

// projectFoldedRows maps a level's folded children into JSON items,
// resolving each chain segment's display name (stored symbol name, else
// IANA canonical, else the bare numeric segment). Shared by the level and
// spine endpoints so both emit identical item shapes.
func projectFoldedRows(children []store.FoldedNodeRow) []treeItem {
	out := make([]treeItem, 0, len(children))
	for _, c := range children {
		segPath := make([]string, len(c.Chain))
		namePath := make([]string, len(c.Chain))
		for i, seg := range c.Chain {
			segPath[i] = seg.Label
			namePath[i] = segDisplayName(seg)
		}
		anchor := c.Anchor()
		out = append(out, treeItem{
			OID:         anchor.OID,
			DirectOID:   c.DirectOID(),
			Name:        anchor.Name,
			NamePath:    strings.Join(namePath, "."),
			Module:      c.ModuleName,
			Kind:        string(c.Kind),
			HasSymbol:   anchor.HasSymbol,
			HasChildren: c.HasChildren(),
			ChildCount:  c.ChildCount,
			Position:    strings.Join(segPath, "."),
		})
	}
	return out
}

// handleAPITree serves the children of an OID from the materialised
// oid_node trie as JSON, paginated by a keyset cursor.
//
//	GET /api/v1/tree?parent={oid}&after={oid}&limit={n}
//
// An empty `parent` is the OID apex (children = the top arcs, normally
// just iso(1); the null-sentinel 0 arc is omitted — see RebuildOIDTree).
// `after` is the last OID from
// the previous page; the server derives its segment for the keyset bound
// (an invalid value degrades to the first page). Each child reports
// `hasChildren` from the stored child_count (no per-child probe) and
// `hasSymbol` — false for synthetic bridge nodes, whose display name
// falls back to the IANA canonical registry and which carry no /s/ link.
// `nextAfter` is the cursor for the next page, or null when exhausted.
func (s *Server) handleAPITree(w http.ResponseWriter, r *http.Request) {
	// Empty parent = the OID apex; ListNodeChildren("") returns the top
	// arcs (normally iso(1); the 0 null-sentinel arc is omitted). A
	// non-empty parent must look like an OID.
	parent := strings.TrimSpace(r.URL.Query().Get("parent"))
	if parent != "" && !web.SelectorLooksLikeOID(parent) {
		s.apiError(w, r, http.StatusBadRequest, "parent must be an OID", nil)
		return
	}
	// Validated request → answer a matching conditional with a fresh 304.
	// Kept after validation so a bad request 400s instead of 304ing; the
	// validators themselves are stamped only on the success path below.
	etag := s.treeETag(r)
	if treeNotModified(w, r, etag) {
		return
	}

	// Forward cursor `after` (last OID of the prior page) or backward
	// cursor `before` (first OID of the current window, for "show
	// earlier"). Invalid values degrade to the first page. `before` wins
	// if both are given.
	afterSeg := int64(-1)
	if after := strings.TrimSpace(r.URL.Query().Get("after")); after != "" && web.SelectorLooksLikeOID(after) {
		if seg, err := strconv.ParseInt(lastOIDSegment(after), 10, 64); err == nil {
			afterSeg = seg
		}
	}
	before := strings.TrimSpace(r.URL.Query().Get("before"))
	backward := before != "" && web.SelectorLooksLikeOID(before)

	limit, branchesOnly, family := treeListParams(r)

	ctx := r.Context()
	var (
		children []store.FoldedNodeRow
		err      error
	)
	if backward {
		beforeSeg := int64(0)
		if seg, perr := strconv.ParseInt(lastOIDSegment(before), 10, 64); perr == nil {
			beforeSeg = seg
		}
		children, err = s.store.ListNodeChildrenFoldedBefore(ctx, parent, beforeSeg, limit, branchesOnly, family)
	} else {
		children, err = s.store.ListNodeChildrenFolded(ctx, parent, afterSeg, limit, branchesOnly, family)
	}
	if err != nil {
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}

	// Each row is one direct child of `parent`, path-compressed: a run of
	// single-child nodes folded to its deepest (anchor) node (see treeItem).
	out := projectFoldedRows(children)

	// Cursors: a full page implies more in that direction. The cursor is
	// the boundary row's DIRECT child OID (not its anchor), so its last
	// segment is a valid sibling key under `parent`. A short page is that
	// direction's end.
	var nextAfter, prevBefore any
	if len(children) == limit {
		if backward {
			prevBefore = children[0].DirectOID()
		} else {
			nextAfter = children[len(children)-1].DirectOID()
		}
	}

	// Stamp validators only now — the read succeeded — and only if the trie
	// generation didn't move mid-request (a rebuild can commit between the
	// validator read and the page read on this single-connection pool; a
	// page from the new generation must not be stored under the old tag —
	// serve it fresh and let the next request revalidate against the new
	// generation). A response WITHOUT a validator (zero generation, read
	// error, or a mid-request rebuild) gets an explicit no-store: a bare
	// 200 with no directives is heuristically cacheable (RFC 9111 §4.2.2),
	// and these bodies must never be stored.
	if etag != "" && etag == s.treeETag(r) {
		treeValidatorHeaders(w, etag)
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parent":     parent,
		"children":   out,
		"nextAfter":  nextAfter,
		"prevBefore": prevBefore,
	})
}

// treeETag computes the strong cache validator for the tree APIs: the trie
// generation — advanced by every RebuildOIDTree and wall-clock-anchored at
// nanosecond resolution, so it tracks the trie's CONTENT (including rebuilds
// after an import or hot reload, where the schema version constant does NOT
// change) and differs across database files (a wiped/recreated DB cannot
// re-issue a previous epoch's tags) — plus the build version, which covers
// the only other input to a rendered row: the compiled-in IANA
// canonical-name table for synthetic segments. One validator serves every
// tree URL: HTTP caches key entries by URL and a conditional request only
// replays the validator stored for that same URL, so per-resource
// uniqueness (RFC 9110) needs no per-URI component. Returns "" — serve
// fresh with no caching headers — when the generation cannot be read OR is
// zero: gen 0 means no rebuild has ever stamped a token (a brand-new DB
// before its first build), and the zero is SHARED across every such
// database, so minting "oidtree-g0-…" would let a cache revalidate one
// DB's response against another's (a false 304 across a DB swap). No
// token, no caching.
func (s *Server) treeETag(r *http.Request) string {
	gen, err := s.store.OIDTreeGeneration(r.Context())
	if err != nil || gen == 0 {
		return ""
	}
	return fmt.Sprintf(`"oidtree-g%d-%s"`, gen, s.version)
}

// treeValidatorHeaders stamps the cache validators on a response. Called
// only on the two cacheable outcomes — the 304 itself and a SUCCESSFUL 200
// body — never before the store read, so a 4xx/5xx error response can
// never carry success validators (a conforming cache could otherwise store
// the error and have a later 304 revalidate it as fresh). Cache-Control is
// deliberately conservative (must-revalidate, no stored max-age): every
// reuse revalidates, but a match returns 304 before any DB work or body
// render. A longer max-age is a safe follow-up once a shared cache tier is
// in play.
func treeValidatorHeaders(w http.ResponseWriter, etag string) {
	if etag == "" {
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
}

// treeNotModified answers a conditional request: when the client's
// If-None-Match already names the current validator, it writes the 304
// (re-stamping the validators, per RFC 9110 §15.4.5) and reports true — the
// caller returns without a body. Call only AFTER request validation, so an
// invalid request still gets its 4xx instead of a 304.
func treeNotModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	if etag == "" {
		return false
	}
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		treeValidatorHeaders(w, etag)
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// etagMatches reports whether the current ETag is listed in an
// If-None-Match header value (a comma-separated list, possibly `*` or with
// a weak `W/` prefix). The tree ETags are strong, so a weak comparison is
// still safe here — a `W/`-prefixed candidate is accepted on its opaque tag.
func etagMatches(header, etag string) bool {
	for _, cand := range strings.Split(header, ",") {
		cand = strings.TrimSpace(cand)
		if cand == "*" || cand == etag {
			return true
		}
		if strings.TrimPrefix(cand, "W/") == etag {
			return true
		}
	}
	return false
}

// handleAPITreeSpine returns every level from the apex down to a focused
// OID in one response — the whole set of pages the client would otherwise
// fetch one round trip per level while expanding the spine.
//
//	GET /api/v1/tree/spine?focus={oid}&branches={0|1}&family={scalar|table|notif}
//
// Each level carries the same child items and next cursor `/api/v1/tree`
// returns for that parent, plus an `anchored` flag when the level was opened
// mid-way through a wide arc (the client renders a "show earlier"
// affordance). Levels are apex-first; the client renders them and computes
// the highlight target itself (rowFor∥deepestAncestorRow), keeping selection
// a single source of truth. branches/family are parsed exactly as in
// handleAPITree.
func (s *Server) handleAPITreeSpine(w http.ResponseWriter, r *http.Request) {
	focus := strings.TrimSpace(r.URL.Query().Get("focus"))
	if focus == "" || !web.SelectorLooksLikeOID(focus) {
		s.apiError(w, r, http.StatusBadRequest, "focus must be an OID", nil)
		return
	}
	// Validated request → answer a matching conditional with a fresh 304.
	// Kept after validation so a bad request 400s instead of 304ing; the
	// validators themselves are stamped only on the success path below.
	etag := s.treeETag(r)
	if treeNotModified(w, r, etag) {
		return
	}

	limit, branchesOnly, family := treeListParams(r)

	spine, err := s.store.SpinePages(r.Context(), focus, branchesOnly, family, limit)
	if err != nil {
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}

	type level struct {
		Parent    string     `json:"parent"`
		Children  []treeItem `json:"children"`
		NextAfter any        `json:"nextAfter"`
		Anchored  bool       `json:"anchored"`
	}
	levels := make([]level, 0, len(spine))
	for _, lv := range spine {
		var nextAfter any
		// A full page implies more siblings below the window; the cursor is
		// the boundary row's DIRECT child OID (a valid sibling key), matching
		// handleAPITree's forward cursor.
		if len(lv.Rows) == limit {
			nextAfter = lv.Rows[len(lv.Rows)-1].DirectOID()
		}
		levels = append(levels, level{
			Parent:    lv.Parent,
			Children:  projectFoldedRows(lv.Rows),
			NextAfter: nextAfter,
			Anchored:  lv.Anchored,
		})
	}

	// Stamp validators only now — the walk succeeded — and only if the trie
	// generation didn't move mid-walk (SpinePages runs one query per level
	// on a single-connection pool, so a rebuild can commit between levels;
	// a spine mixing two generations must not be stored under either tag —
	// serve it fresh and let the next request revalidate). A response
	// WITHOUT a validator gets an explicit no-store — a bare 200 is
	// heuristically cacheable (RFC 9111 §4.2.2) and must never be stored.
	if etag != "" && etag == s.treeETag(r) {
		treeValidatorHeaders(w, etag)
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"focus":  focus,
		"levels": levels,
	})
}

func (s *Server) handleAPISymbol(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/symbol/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		s.apiError(w, r, http.StatusBadRequest, "expected /api/v1/symbol/{module}/{name}", nil)
		return
	}
	sym, err := s.store.GetSymbol(r.Context(), parts[0], parts[1])
	if errors.Is(err, store.ErrNotFound) {
		s.apiError(w, r, http.StatusNotFound, "symbol not found", nil)
		return
	}
	if err != nil {
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	writeJSON(w, http.StatusOK, sym)
}

// searchWithExactMatch first tries to interpret the query as a
// qualified Module::Name lookup; if it hits, the exact match is
// prepended to the FTS5 results so the user always sees their typed
// symbol on top. FTS5 BM25 alone doesn't guarantee exact-match-first
// ranking — see spec R5 scenario "Search by exact symbol".
//
// Queries that look like an OID prefix (digits and dots, optionally
// led by a single dot) bypass FTS5 entirely — FTS5's tokenizer
// strips dots, so an OID-shaped query against the inverted index
// would either match nothing or wildly over-match. The store's
// SearchByOIDPrefix uses LIKE on the indexed `oid` column instead.
//
// A store.ErrQueryTooShort or timeout from the FTS path is returned
// to the caller so it can render an honest state instead of a false
// "no results" — unless the cheap qualified exact-match lookup still
// answers (e.g. "IF-MIB::i"), in which case the error is cleared.
//
// The caller classifies the returned error with classifySearchErr.

// searchErrKind classifies an error from searchWithExactMatch so the
// HTML (handleSearch) and JSON (handleAPISearch) paths respond
// consistently — the only difference between them is how each renders.
type searchErrKind int

const (
	searchErrNone     searchErrKind = iota // no error — render results
	searchErrTooShort                      // too broad to search — empty page/hits
	searchErrTimeout                       // FTS bound exceeded — 504
	searchErrCanceled                      // client went away — nothing to render
	searchErrInternal                      // anything else — 500
)

func classifySearchErr(err error) searchErrKind {
	switch {
	case err == nil:
		return searchErrNone
	case errors.Is(err, store.ErrQueryTooShort):
		return searchErrTooShort
	case errors.Is(err, context.DeadlineExceeded):
		return searchErrTimeout
	case errors.Is(err, context.Canceled):
		return searchErrCanceled
	default:
		return searchErrInternal
	}
}

func (s *Server) searchWithExactMatch(ctx context.Context, q string, limit int) ([]store.SearchHit, error) {
	if prefix, ok := oidPrefixQuery(q); ok {
		return s.store.SearchByOIDPrefix(ctx, prefix, limit)
	}

	hits, err := s.store.Search(ctx, q, limit)
	if err != nil &&
		!errors.Is(err, store.ErrQueryTooShort) &&
		!errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}

	if module, name, ok := splitQualified(q); ok {
		if sym, lookupErr := s.store.GetSymbol(ctx, module, name); lookupErr == nil {
			exact := store.SearchHit{
				SymbolID: sym.ID,
				Module:   sym.ModuleName,
				Name:     sym.Name,
				OID:      sym.OID,
				Kind:     string(sym.Kind),
			}
			for i, h := range hits {
				if h.SymbolID == sym.ID {
					hits = append(hits[:i], hits[i+1:]...)
					break
				}
			}
			hits = append([]store.SearchHit{exact}, hits...)
			err = nil
		}
	}
	return hits, err
}

// --- error pages -----------------------------------------------------

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusNotFound, web.NotFound())
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("handler failed", "path", r.URL.Path, "err", err)
	render(w, r, http.StatusInternalServerError, web.InternalError(err.Error()))
}

// --- helpers ---------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// apiError writes a sanitised JSON error body. The public message is
// what the API client sees; if err is non-nil it goes to slog only —
// preventing internal paths, identifiers, or query fragments from
// leaking through `/api/v1/*`.
func (s *Server) apiError(w http.ResponseWriter, r *http.Request, status int, public string, err error) {
	if err != nil {
		slog.Error("api error",
			"path", r.URL.Path,
			"status", status,
			"err", err,
		)
	}
	writeJSON(w, status, map[string]any{"error": public})
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		slog.Error("render failed", "path", r.URL.Path, "err", err)
	}
}

func toWebHits(hits []store.SearchHit) []web.SearchHit {
	out := make([]web.SearchHit, len(hits))
	for i, h := range hits {
		out[i] = web.SearchHit{
			Module: h.Module,
			Name:   h.Name,
			OID:    h.OID,
			Kind:   h.Kind,
			// Sanitise the FTS5 snippet — preserves <mark>...</mark>
			// markers, escapes everything else. Rendered via
			// templ.Raw in SearchResults, so without this the
			// description text would XSS.
			Snippet: web.SanitizeSnippet(h.Snippet),
		}
	}
	return out
}

// splitQualified parses "Module::Name" into its parts. If only a bare
// name is provided (no "::"), returns ok=false and the caller should
// fall back to a search-by-name strategy.
func splitQualified(s string) (module, name string, ok bool) {
	i := strings.Index(s, "::")
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+2:], true
}

// oidPrefixQuery returns (prefix, true) when q looks like a numeric
// OID — bare digits like "1.3.6.1" or with a leading dot like ".1".
// The store's SearchByOIDPrefix expects the leading dot stripped.
//
// Returning false here means the query goes through FTS5; an empty
// or single-dot input is rejected so we don't widen the search to
// every symbol in the database.
func oidPrefixQuery(q string) (string, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", false
	}
	q = strings.TrimPrefix(q, ".")
	if q == "" {
		return "", false
	}
	for _, r := range q {
		if (r < '0' || r > '9') && r != '.' {
			return "", false
		}
	}
	if strings.HasPrefix(q, ".") || strings.HasSuffix(q, ".") || strings.Contains(q, "..") {
		return "", false
	}
	return q, true
}

// lastOIDSegment returns the trailing dot-separated component of an
// OID — e.g. "10" for "1.3.6.1.2.1.2.2.1.10". Used as the column
// position number on the table-of-tables rendering.
func lastOIDSegment(oid string) string {
	if i := strings.LastIndex(oid, "."); i >= 0 {
		return oid[i+1:]
	}
	return oid
}
