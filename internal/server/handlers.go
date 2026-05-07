package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/source"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/web"
)

// pathUnderAny reports whether the absolute form of `p` lives at
// or under the absolute form of any of `roots`. Empty `p` and
// empty roots are rejected; resolving `.` ancestor segments is
// handled by `filepath.Abs` + `filepath.Rel`.
//
// Symlink semantics:
//   - When `p` exists on disk, its symlinks are resolved via
//     `filepath.EvalSymlinks` before the prefix check; the roots
//     are resolved symmetrically so a `/var` → `/private/var`
//     rewrite on macOS doesn't make a real file under
//     `/var/folders/.../mibs` falsely escape.
//   - When `p` doesn't exist, `EvalSymlinks` fails and the raw
//     abs path is checked against raw abs roots. There's no
//     symlink to escape through (the path resolves to nothing),
//     and the lexical-prefix check is sufficient. The caller's
//     follow-up `os.Open` distinguishes "stale recorded path"
//     (410) from "path was unsafe" (404).
//
// Used by the module-download endpoints as a defense-in-depth
// guard against any future writer that might let a module's
// `source_path` be set to an arbitrary file. Today libsmi only
// reports paths under the configured MIB roots, but this guard
// shrinks the blast radius of a regression.
func pathUnderAny(p string, roots []string) bool {
	if p == "" {
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
	for _, r := range roots {
		if r == "" {
			continue
		}
		rabs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved {
			// File symlinks were followed; root must be canonicalised
			// to match. Without this, a real file under a root whose
			// abs path traverses a parent symlink (macOS `/var` →
			// `/private/var`) lexically diverges from the unresolved
			// root and the prefix check would reject it.
			if real, err := filepath.EvalSymlinks(rabs); err == nil {
				rabs = real
			}
		}
		if isUnderRoot(rabs, abs) {
			return true
		}
	}
	return false
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
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
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
	render(w, r, http.StatusOK, web.Landing(modCount, symCount))
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
	// Pre-set the headers — http.ServeFile leaves them alone if
	// they're already populated. .mib files would otherwise default
	// to application/octet-stream which would prompt downloads.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, mod.SourcePath)
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
	if !pathUnderAny(mod.SourcePath, []string{s.mibsDir}) {
		s.notFound(w, r)
		return
	}
	f, err := os.Open(mod.SourcePath)
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
	defer f.Close()
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

	roots := []string{s.mibsDir}

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
	if rootMod.SourcePath != "" && !pathUnderAny(rootMod.SourcePath, roots) {
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

	// Partition into "ship in the ZIP" vs "list in MISSING.txt".
	type shipEntry struct {
		Module     string
		SourcePath string
	}
	type missing struct {
		Module     string
		Reason     string
		ImportedBy string
		Symbols    []string
	}
	var shippable []shipEntry
	var missings []missing
	for _, e := range closure {
		if !e.Loaded {
			missings = append(missings, missing{
				Module:     e.Module,
				Reason:     "not loaded",
				ImportedBy: e.ImportedBy,
				Symbols:    e.Symbols,
			})
			continue
		}
		if e.SourcePath == "" || !pathUnderAny(e.SourcePath, roots) {
			// Spec defines exactly two reason markers (`not loaded`
			// and `source file unreadable`). An empty source path or
			// an out-of-roots path lands here: from the user's
			// perspective the file isn't readable, so it shares the
			// `source file unreadable` marker rather than inventing
			// a third.
			missings = append(missings, missing{
				Module:     e.Module,
				Reason:     "source file unreadable",
				ImportedBy: e.ImportedBy,
				Symbols:    e.Symbols,
			})
			continue
		}
		shippable = append(shippable, shipEntry{
			Module:     e.Module,
			SourcePath: e.SourcePath,
		})
	}

	w.Header().Set("Content-Type", "application/zip")
	setAttachmentDisposition(w, name+"-bundle.zip")

	zw := zip.NewWriter(w)
	defer func() {
		if err := zw.Close(); err != nil {
			slog.Warn("bundle: zip close", "module", name, "err", err)
		}
	}()

	for _, ship := range shippable {
		// Honor request-context cancellation between entries — a
		// disconnected client shouldn't keep us reading and zipping
		// MIBs into a TCP void.
		if err := ctx.Err(); err != nil {
			slog.Warn("bundle: ctx cancelled", "module", name, "err", err)
			return
		}
		f, err := os.Open(ship.SourcePath)
		if err != nil {
			slog.Warn("bundle: open source", "module", ship.Module, "path", ship.SourcePath, "err", err)
			missings = append(missings, missing{
				Module: ship.Module,
				Reason: "source file unreadable",
			})
			continue
		}
		// Stamp each ZIP entry with the source file's mtime so two
		// clients downloading the same bundle one second apart get
		// byte-identical archives. `time.Now()` would forfeit
		// reproducibility and any If-Modified-Since semantics.
		var modTime time.Time
		if info, err := f.Stat(); err == nil {
			modTime = info.ModTime()
		}
		hdr := &zip.FileHeader{
			Name:     ship.Module + ".mib",
			Method:   zip.Deflate,
			Modified: modTime,
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			slog.Warn("bundle: zip header", "module", ship.Module, "err", err)
			_ = f.Close()
			return
		}
		if _, err := io.Copy(fw, f); err != nil {
			slog.Warn("bundle: copy source", "module", ship.Module, "err", err)
			_ = f.Close()
			return
		}
		_ = f.Close()
	}

	// MISSING.txt is always emitted, even when len(missings) == 0,
	// so machine consumers can rely on `unzip -l | grep MISSING.txt`
	// rather than inferring from absence whether the bundle was
	// complete.
	hdr := &zip.FileHeader{
		Name:     "MISSING.txt",
		Method:   zip.Deflate,
		Modified: time.Now(),
	}
	fw, err := zw.CreateHeader(hdr)
	if err != nil {
		slog.Warn("bundle: zip header MISSING.txt", "module", name, "err", err)
		return
	}
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
	if _, err := io.WriteString(fw, buf.String()); err != nil {
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

	// Top-level tree rows are the module's "entry points into the OID
	// space" — symbols whose parent OID is NOT itself a symbol of
	// this module. The simpler `ListChildren(mod.OIDRoot)` strategy
	// fails the common case where MODULE-IDENTITY anchors as a
	// sysObjectID-style sentinel (e.g. ifMIB at 1.3.6.1.2.1.31.1)
	// while the actual symbols hang off mib-2 children (interfaces
	// at 1.3.6.1.2.1.2). Computing the orphan set in Go over the
	// already-loaded `syms` slice keeps this on the per-page hot
	// path without a second SQL round trip.
	moduleOIDs := make(map[string]struct{}, len(syms))
	for i := range syms {
		moduleOIDs[syms[i].OID] = struct{}{}
	}
	var topLevel []model.Symbol
	for i := range syms {
		if _, internal := moduleOIDs[syms[i].ParentOID]; internal {
			continue
		}
		topLevel = append(topLevel, syms[i])
	}

	// Single batched HasChildren query — replaces an N+1 round-trip
	// (with MaxOpenConns=1, those serialize and added up on wide
	// modules).
	parentOIDs := make([]string, 0, len(topLevel))
	for i := range topLevel {
		parentOIDs = append(parentOIDs, topLevel[i].OID)
	}
	hasChildren, err := s.store.HasChildrenBatch(ctx, parentOIDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	treeRows := make([]web.TreeRow, 0, len(topLevel))
	for i := range topLevel {
		treeRows = append(treeRows, web.TreeRow{
			Symbol:      topLevel[i],
			HasChildren: hasChildren[topLevel[i].OID],
		})
	}

	counts, err := s.store.CountByFamily(ctx, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	allModules, err := s.store.ListModules(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
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

	// Pre-compute disk-availability so the module-info bar can hide
	// download affordances when the source has disappeared. A single
	// stat per render is cheap; doing it from inside the templ would
	// pull I/O into the rendering layer. Errors (including ENOENT)
	// flatten to "not downloadable".
	downloadable := false
	if mod.SourcePath != "" &&
		pathUnderAny(mod.SourcePath, []string{s.mibsDir}) {
		if _, err := os.Stat(mod.SourcePath); err == nil {
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
			roots := []string{s.mibsDir}
			for _, e := range closure {
				if e.Loaded && e.SourcePath != "" && pathUnderAny(e.SourcePath, roots) {
					bundleFileCount++
				}
			}
		}
	}

	view := &web.WorkspaceView{
		Module:             mod,
		Counts:             counts,
		TreeRows:           treeRows,
		ListRows:           listRows,
		Modules:            allModules,
		ScopeOID:           oid,
		ModuleDownloadable: downloadable,
		TypeDefs:           web.CollectTypeDefs(syms),
		BundleFileCount:    bundleFileCount,
	}

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
			s.internalError(w, r, lookupErr)
			return
		default:
			selected := &web.SymbolView{Symbol: sym}
			selected.Context = s.buildSymbolContext(ctx, sym)
			if sym.Kind == model.KindTable {
				selected.Columns = s.buildTableColumns(ctx, sym)
			}
			usedBy, err := s.store.ListReferencesTo(ctx, sym.ModuleName, sym.Name)
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			selected.UsedBy = usedBy
			// NOTIFICATION-TYPE / TRAP-TYPE OBJECTS clause —
			// outbound references of kind RefNotificationObject.
			// Surfaced in the workspace right pane as clickable
			// links so a reader can jump from "what does linkDown
			// carry?" straight to ifAdminStatus's detail.
			if sym.Kind == model.KindNotificationType || sym.Kind == model.KindTrapType {
				outRefs, err := s.store.ListReferencesFrom(ctx, sym.ModuleName, sym.Name)
				if err != nil {
					s.internalError(w, r, err)
					return
				}
				selected.NotifyObjects, selected.TrapIndex = s.buildNotifyVarbinds(ctx, outRefs)
			}
			if symMod, err := s.store.GetModule(ctx, sym.ModuleName); err == nil && symMod.SourcePath != "" && sym.SourceLine > 0 {
				if slice, err := source.Slice(symMod.SourcePath, sym.SourceLine, source.DefaultWindow); err == nil && slice != "" {
					selected.SourceText = slice
					selected.SourcePath = symMod.SourcePath
				}
			}
			view.Selected = selected
			// view.OIDPath is still decoded (the scope breadcrumb
			// derives from it via `web.ScopeBreadcrumb`); the
			// right-pane no longer renders an "OID decode"
			// section, but the breadcrumb still needs the chain.
			if sym.OID != "" {
				path, err := s.store.OIDPath(ctx, sym.OID)
				if err != nil {
					s.internalError(w, r, err)
					return
				}
				view.OIDPath = path
			}
		}
	}

	// Auto-expand the tree spine from the module's top-level rows
	// down to the current selection / scope. The user expects the
	// tree to keep its navigation context across full-page
	// navigations — clicking a column shouldn't collapse the
	// entire tree.
	//
	// `expandSet` is the set of OIDs we want pre-expanded (named
	// ancestors of the selection that have children); `selectionOID`
	// is the row that should pick up the `selected` highlight.
	expandSet := make(map[string]struct{})
	for _, st := range view.OIDPath {
		if st.Canonical || st.Name == "" {
			continue
		}
		// Don't expand the selection itself when it's a leaf —
		// there's nothing to drop into.
		if st.Prefix == selectionOID && !web.KindHasChildren(st.Kind) {
			continue
		}
		expandSet[st.Prefix] = struct{}{}
	}
	if len(expandSet) > 0 {
		for i := range view.TreeRows {
			s.expandTreeRow(ctx, &view.TreeRows[i], expandSet, selectionOID)
		}
	}

	render(w, r, http.StatusOK, web.Workspace(view))
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

	view := &web.SymbolView{Symbol: sym}
	view.Context = s.buildSymbolContext(ctx, sym)
	if sym.Kind == model.KindTable {
		view.Columns = s.buildTableColumns(ctx, sym)
	}
	usedBy, err := s.store.ListReferencesTo(ctx, module, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	view.UsedBy = usedBy

	// Source slice for the "Show full SMI source" disclosure.
	if mod, err := s.store.GetModule(ctx, module); err == nil && mod.SourcePath != "" && sym.SourceLine > 0 {
		if slice, err := source.Slice(mod.SourcePath, sym.SourceLine, source.DefaultWindow); err == nil && slice != "" {
			view.SourceText = slice
			view.SourcePath = mod.SourcePath
		}
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
		http.Redirect(w, r, string(web.WorkspaceSymbolURL(matches[0].ModuleName, matches[0].Name, matches[0].OID)), http.StatusFound)
	default:
		render(w, r, http.StatusOK, web.SymbolDisambiguation(name, matches))
	}
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
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t == "IpAddress"
}

// isIntegerSyntax reports whether `s` resolves to an INTEGER /
// Integer32 base type, ignoring inline enum bodies and range
// constraints. Mirrors the integer-side of `web.TrapTypeLetter`
// but stays in the server package to avoid an unnecessary export.
//
// Recognises common integer-subtype Textual Conventions
// (`InterfaceIndex`, etc.) verbatim — the compile layer emits
// the TC name as the symbol's syntax rather than chasing through
// to the underlying base type, so the helper has to know the
// well-known names. Unknown integer TCs fall through to false
// and the trap-simulator modal degrades to its raw-suffix mode
// for that table.
func isIntegerSyntax(s string) bool {
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '{'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch t {
	case "INTEGER", "Integer32",
		// `Unsigned32` is an SMI base type (RFC 2578 §7.1.4) and
		// the second-most-common single-column INDEX syntax in
		// real-world MIB corpora — `alarmActiveIndex`,
		// `vacmContextIndex`, etc. all use it. Smidump emits the
		// literal token verbatim.
		"Unsigned32",
		"Enumeration",
		"InterfaceIndex",
		"InterfaceIndexOrZero",
		"InetPortNumber",
		"InetVersion",
		"IANAifType",
		// TruthValue / RowStatus mirror `web.TrapTypeLetter`.
		"TruthValue",
		"RowStatus":
		return true
	}
	return false
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
	http.Redirect(w, r, "/m/"+sym.ModuleName+"/"+sym.OID, http.StatusFound)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		render(w, r, http.StatusOK, web.SearchEmpty())
		return
	}
	ctx := r.Context()
	hits := s.searchWithExactMatch(ctx, q, 50)
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
	hits := s.searchWithExactMatch(r.Context(), q, 25)
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

// expandTreeRow recursively pre-loads and pre-expands a tree row's
// children when its OID is in expandSet, so the workspace tree
// renders with the path-to-selection already open. Walks the OID
// tree top-down, so a node's children are populated only if the
// node itself is on the auto-expand path.
//
// Selection highlighting is applied in the same pass: if the
// row's OID matches selectionOID, the row gets `Selected = true`
// (the templ adds the `selected` class for the accent stripe).
//
// The dedup logic mirrors handleAPITreeFragment — `ListChildren`
// returns one row per defining module for shared anchors like
// `mgmt` and `system`, so we collapse to one row per OID before
// rendering.
func (s *Server) expandTreeRow(ctx context.Context, row *web.TreeRow, expandSet map[string]struct{}, selectionOID string) {
	if row.Symbol.OID == selectionOID {
		row.Selected = true
	}
	if _, want := expandSet[row.Symbol.OID]; !want {
		return
	}
	row.Expanded = true

	children, err := s.store.ListChildren(ctx, row.Symbol.OID)
	if err != nil {
		// Degrade gracefully: leave the row collapsed so the
		// chevron's HTMX click can retry. Setting Expanded=false
		// also forces TreeRowAlpineState to bake `loaded:false`
		// into the row's x-data, which is what makes the retry
		// path fire — leaving Expanded=true after a failure
		// would brick the row (loaded:true skips the fetch).
		slog.Warn("auto-expand: list children failed", "oid", row.Symbol.OID, "err", err)
		row.Expanded = false
		return
	}
	if len(children) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(children))
	deduped := children[:0]
	for i := range children {
		if _, ok := seen[children[i].OID]; ok {
			continue
		}
		seen[children[i].OID] = struct{}{}
		deduped = append(deduped, children[i])
	}
	children = deduped

	parentOIDs := make([]string, 0, len(children))
	for i := range children {
		parentOIDs = append(parentOIDs, children[i].OID)
	}
	hasChildren, err := s.store.HasChildrenBatch(ctx, parentOIDs)
	if err != nil {
		slog.Warn("auto-expand: has-children batch failed", "oid", row.Symbol.OID, "err", err)
		row.Expanded = false
		return
	}

	row.PreloadedKids = make([]web.TreeRow, 0, len(children))
	for i := range children {
		kid := web.TreeRow{
			Symbol:      children[i],
			HasChildren: hasChildren[children[i].OID],
		}
		s.expandTreeRow(ctx, &kid, expandSet, selectionOID)
		row.PreloadedKids = append(row.PreloadedKids, kid)
	}
}

// handleAPITreeFragment returns the immediate children of an OID
// as an HTML <ul> fragment, suitable for HTMX `beforeend` swap into
// the workspace tree row that triggered the expansion. The
// JSON-returning sibling `handleAPITree` is preserved for the
// standalone tree page.
//
// `ListChildren` returns one row per module defining the OID, so
// shared anchors like `mgmt` / `system` / `interfaces` (defined in
// RFC1155 + RFC1156 + RFC1213 etc.) come back duplicated. The
// workspace tree is a navigation surface keyed by OID, not by
// (module, name), so we dedupe to one row per OID before render.
// Order is preserved from the SQL `ORDER BY oid, name` so the
// retained row is the alphabetically-first module's definition —
// stable across requests and across reloads.
//
// The `?module=…&scope=…` query params let the templ rebuild
// `WorkspaceRowURL` for each child so leaf clicks inside a
// fragment preserve the URL scope, matching the list-row workflow
// (clicking a leaf updates only `?sel=…`, never narrows the list
// to a single OID).
func (s *Server) handleAPITreeFragment(w http.ResponseWriter, r *http.Request) {
	parent := strings.TrimSpace(r.URL.Query().Get("parent"))
	if parent == "" {
		s.notFound(w, r)
		return
	}
	// `module` / `scope` are echoed back into the rendered fragment's
	// URLs via WorkspaceRowURL. Validate against the SMI grammars
	// (RFC 1212 §4.1.6 / RFC 2578 §3.1 for module names; digits +
	// dots for OIDs) before threading through — otherwise an
	// attacker-controlled query value flows into href / data-*
	// attributes. Invalid values degrade silently to empty (the
	// fragment still renders, just without the leaf-vs-container
	// scope-preserving URLs).
	module := strings.TrimSpace(r.URL.Query().Get("module"))
	if !validModuleName(module) {
		module = ""
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if !web.SelectorLooksLikeOID(scope) {
		scope = ""
	}
	ctx := r.Context()
	children, err := s.store.ListChildren(ctx, parent)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	seen := make(map[string]struct{}, len(children))
	deduped := children[:0]
	for i := range children {
		if _, ok := seen[children[i].OID]; ok {
			continue
		}
		seen[children[i].OID] = struct{}{}
		deduped = append(deduped, children[i])
	}
	children = deduped
	parentOIDs := make([]string, 0, len(children))
	for i := range children {
		parentOIDs = append(parentOIDs, children[i].OID)
	}
	hasChildren, err := s.store.HasChildrenBatch(ctx, parentOIDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows := make([]web.TreeRow, 0, len(children))
	for i := range children {
		rows = append(rows, web.TreeRow{
			Symbol:      children[i],
			HasChildren: hasChildren[children[i].OID],
		})
	}
	// Synthetic view threads the URL scope through the templ so
	// `WorkspaceRowURL` builds the same leaf-vs-container URLs the
	// main render uses. Module is required for the URL builder;
	// scope is optional (empty when the caller didn't pass it,
	// which falls back to scope-change on leaf click).
	view := &web.WorkspaceView{
		Module:   &model.Module{Name: module},
		ScopeOID: scope,
	}
	render(w, r, http.StatusOK, web.WorkspaceTreeFragment(view, rows))
}

// handleAPITree returns the immediate children of an OID as JSON,
// suitable for lazy-load expansion in the tree.js island.
//
// The default parent is "1" (the root of the OID space). For each
// child we report whether it has further descendants so the client
// can decide whether to render an expand chevron.
func (s *Server) handleAPITree(w http.ResponseWriter, r *http.Request) {
	parent := strings.TrimSpace(r.URL.Query().Get("parent"))
	if parent == "" {
		parent = "1"
	}
	ctx := r.Context()

	children, err := s.store.ListChildren(ctx, parent)
	if err != nil {
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}

	type item struct {
		OID         string `json:"oid"`
		Name        string `json:"name"`
		Module      string `json:"module"`
		Kind        string `json:"kind"`
		HasChildren bool   `json:"hasChildren"`
		Position    string `json:"position"`
	}
	out := make([]item, 0, len(children))
	for _, c := range children {
		hc, _ := s.store.HasChildren(ctx, c.OID)
		out = append(out, item{
			OID:         c.OID,
			Name:        c.Name,
			Module:      c.ModuleName,
			Kind:        string(c.Kind),
			HasChildren: hc,
			Position:    lastOIDSegment(c.OID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parent":   parent,
		"children": out,
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
func (s *Server) searchWithExactMatch(ctx context.Context, q string, limit int) []store.SearchHit {
	if prefix, ok := oidPrefixQuery(q); ok {
		hits, err := s.store.SearchByOIDPrefix(ctx, prefix, limit)
		if err != nil {
			slog.Warn("oid prefix search failed", "q", q, "err", err)
			return nil
		}
		return hits
	}

	hits, err := s.store.Search(ctx, q, limit)
	if err != nil {
		slog.Warn("search failed", "q", q, "err", err)
	}

	if module, name, ok := splitQualified(q); ok {
		if sym, err := s.store.GetSymbol(ctx, module, name); err == nil {
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
		}
	}
	return hits
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
		if !(r >= '0' && r <= '9') && r != '.' {
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
