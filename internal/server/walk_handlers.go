package server

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/no42-org/blittermib/internal/walk"
	"github.com/no42-org/blittermib/internal/web"
)

// walkMaxBytes caps the POST body for /walk/decode and /walk/bundle.
// At ~100–150 bytes per -On line that's roughly 70–100k OIDs — covers a
// full device walk comfortably; larger captures are refused with a 413
// and guidance to filter or split.
const walkMaxBytes = 10 << 20 // 10 MB

// contentLooksLikeMIB reports whether text is an SMI MIB module rather
// than an snmpwalk capture — used to redirect a MIB pasted into the
// walk box over to /import. The markers are SMI keywords that never
// appear in `-On` walk output.
func contentLooksLikeMIB(text string) bool {
	for _, m := range []string{"DEFINITIONS ::=", "::= BEGIN", "OBJECT-TYPE", "OBJECT IDENTIFIER ::="} {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// contentLooksLikeWalk reports whether text is an snmpwalk capture
// rather than a MIB — used to redirect a walk uploaded to /import over
// to /walk. A MIB parses to zero walk entries; a walk parses to many.
func contentLooksLikeWalk(text string) bool {
	w := walk.Parse(text)
	return len(w.Entries) > 0 && len(w.Entries) >= w.SkippedLines
}

// handleWalkUpload serves the GET /walk capture intake page.
func (s *Server) handleWalkUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Mark the context so the shared layout drops the redundant topbar
	// decode control + modal on this page (the intake form is here).
	r = r.WithContext(web.WithWalkPage(r.Context()))
	render(w, r, http.StatusOK, web.WalkUpload(web.WalkUploadView{}))
}

// renderWalkUploadError re-renders the intake page with an error
// message. Like handleWalkUpload it marks the context as the walk page
// so the shared layout suppresses the redundant topbar decode control
// and modal — a rejected submit is the page the user is most likely to
// retry from.
func renderWalkUploadError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	r = r.WithContext(web.WithWalkPage(r.Context()))
	render(w, r, status, web.WalkUpload(web.WalkUploadView{Error: msg}))
}

// handleWalkDecode parses a posted walk, resolves it against the store,
// and renders the grouped results. The walk content is held only for
// the duration of the request: no DB row, no file, no slog of values.
func (s *Server) handleWalkDecode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	walkText, status, msg := s.readWalk(w, r)
	if status != http.StatusOK {
		renderWalkUploadError(w, r, status, msg)
		return
	}

	parsed := walk.Parse(walkText)
	if len(parsed.Entries) == 0 && contentLooksLikeMIB(walkText) {
		// A MIB module pasted into the walk box — nothing resolves and
		// every line is "skipped". Redirect rather than dead-end.
		renderWalkUploadError(w, r, http.StatusUnprocessableEntity,
			"That looks like a MIB module, not an snmpwalk capture. Upload MIB files at /import instead.")
		return
	}

	resolved, err := walk.Resolve(ctx, parsed, s.store)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	notifs, err := walk.NotificationSummary(ctx, s.store, resolved.Modules)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	render(w, r, http.StatusOK, web.WalkResults(buildWalkResults(walkText, resolved, notifs)))
}

// readWalk pulls the walk text from the request, enforcing the 10 MB
// cap before any parsing. Returns (text, 200, "") on success, or an
// empty text with an HTTP status + user message on rejection.
func (s *Server) readWalk(w http.ResponseWriter, r *http.Request) (string, int, string) {
	r.Body = http.MaxBytesReader(w, r.Body, walkMaxBytes)

	var text string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// #nosec G120 -- r.Body is wrapped in http.MaxBytesReader(walkMaxBytes) above, so the multipart parse is bounded.
		if err := r.ParseMultipartForm(walkMaxBytes); err != nil {
			return walkReadError(err)
		}
		// ParseMultipartForm spills parts above its in-memory threshold
		// to temp files; remove them when we're done. Belt-and-braces
		// for the "the walk is never written to disk" posture, and a
		// plain temp-file-leak guard.
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		switch f, _, err := r.FormFile("walkfile"); {
		case err == nil:
			defer func() { _ = f.Close() }()
			b, err := io.ReadAll(f)
			if err != nil {
				return walkReadError(err)
			}
			text = string(b)
		case !errors.Is(err, http.ErrMissingFile):
			// A genuine upload error — not simply "no file submitted",
			// which is the normal paste-only case and falls through to
			// the textarea field below.
			return walkReadError(err)
		}
		if strings.TrimSpace(text) == "" {
			text = r.FormValue("walk")
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return walkReadError(err)
		}
		text = r.FormValue("walk")
	}

	if strings.TrimSpace(text) == "" {
		return "", http.StatusBadRequest, "Paste or upload a non-empty snmpwalk capture."
	}
	return text, http.StatusOK, ""
}

func walkReadError(err error) (string, int, string) {
	// The decode form posts multipart/form-data, so the common oversize
	// path runs through ParseMultipartForm. That can surface the cap as
	// a *http.MaxBytesError or, depending on where the limit trips, as a
	// wrapped multipart error whose message ends "request body too
	// large" — treat both as 413.
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) ||
		(err != nil && strings.Contains(err.Error(), "request body too large")) {
		return "", http.StatusRequestEntityTooLarge,
			"Walk too large — the decoder accepts up to 10 MB (roughly 70–100k OIDs). " +
				"Filter the walk (e.g. grep a single subtree) or split it, then try again."
	}
	return "", http.StatusBadRequest, "Could not read the uploaded walk."
}

// buildWalkResults maps a resolved walk + notification summary into the
// logic-free view model. Display strings are computed here.
func buildWalkResults(walkText string, rw walk.ResolvedWalk, notifs []walk.NotifModule) web.WalkResultsView {
	// Aggregate every resolved entry per module, in the resolver's
	// sorted module order — name-prefixed records (the default snmpwalk
	// output form) count the same as -On numeric ones, and a module
	// whose objects only reported "no such instance" still appears
	// (with a zero value count) so the summary, the bundle, and the
	// notification section all describe the same module set.
	// ObjectCount is distinct resolved symbols; ValueCount is
	// value-bearing instances. (The workspace chip counts matched list
	// rows instead, which includes ancestor table/entry rows — related
	// figures, not the same number.)
	type modAgg struct {
		objects map[string]struct{}
		values  int
	}
	agg := make(map[string]*modAgg)
	resolvedCount := 0
	for _, re := range rw.Entries {
		if !re.Resolved {
			continue
		}
		resolvedCount++
		a := agg[re.Module]
		if a == nil {
			a = &modAgg{objects: make(map[string]struct{})}
			agg[re.Module] = a
		}
		a.objects[re.Symbol] = struct{}{}
		if !re.Entry.NotPresent {
			a.values++
		}
	}
	var modules []web.WalkModuleSummary
	for _, m := range rw.Modules {
		a := agg[m]
		if a == nil {
			continue
		}
		modules = append(modules, web.WalkModuleSummary{
			Module:      m,
			ObjectCount: len(a.objects),
			ValueCount:  a.values,
			Counts:      moduleCountsLabel(len(a.objects), a.values),
		})
	}

	unresolved := aggregateUnresolved(rw)

	var notifView []web.WalkNotifModule
	for _, n := range notifs {
		notifView = append(notifView, web.WalkNotifModule{
			Module: n.Module,
			Count:  notifCountLabel(n.Count),
		})
	}

	view := web.WalkResultsView{
		Summary: fmt.Sprintf("%d entries · %d resolved · %d module(s)",
			len(rw.Entries), resolvedCount, len(modules)),
		SkippedLines:  rw.SkippedLines,
		ParserNotes:   rw.ParserNotes,
		Modules:       modules,
		Unresolved:    unresolved,
		Notifications: notifView,
		WalkText:      walkText,
		WalkDataJSON:  walkDataJSON(rw),
		HasResults:    len(modules) > 0 || len(unresolved) > 0,
	}
	return view
}

// walkDataJSON marshals the walk's resolved instance OIDs to their
// values as {"oids":{oid:value}} for the workspace overlay to persist
// in sessionStorage. Name-prefixed records are normalised back to the
// numeric instance OID (symbol OID + suffix) so they decorate the
// workspace like -On records do. Not-present entries carry no value,
// and unresolved entries can't match any loaded module's rows — both
// are skipped, which also keeps the payload (and the browser-storage
// quota it competes for) proportional to what can actually decorate.
func walkDataJSON(rw walk.ResolvedWalk) string {
	oids := make(map[string]string)
	for _, re := range rw.Entries {
		if !re.Resolved || re.Entry.NotPresent {
			continue
		}
		ident := re.Entry.Ident
		if !re.Entry.Numeric() {
			ident = re.SymbolOID
			if re.Suffix != "" {
				ident += "." + re.Suffix
			}
		}
		oids[ident] = re.Entry.Value
	}
	b, err := json.Marshal(map[string]any{"oids": oids})
	if err != nil {
		return ""
	}
	return string(b)
}

// moduleCountsLabel renders the per-module summary counts, e.g.
// "4 objects · 10 values".
func moduleCountsLabel(objects, values int) string {
	o, v := "objects", "values"
	if objects == 1 {
		o = "object"
	}
	if values == 1 {
		v = "value"
	}
	return fmt.Sprintf("%d %s · %d %s", objects, o, values, v)
}

func notifCountLabel(n int) string {
	if n == 1 {
		return "1 notification/trap definition"
	}
	return fmt.Sprintf("%d notification/trap definitions", n)
}

// aggregateUnresolved collapses per-entry Unresolved guidance into one
// row per enterprise/prefix, ordered by descending occurrence count
// then OID for stable output.
func aggregateUnresolved(rw walk.ResolvedWalk) []web.WalkUnresolvedRow {
	type acc struct {
		count int
		hint  string
		oid   string
	}
	order := []string{}
	byKey := map[string]*acc{}
	for _, re := range rw.Entries {
		u := re.Unresolved
		if u == nil {
			continue
		}
		key, displayOID := unresolvedKey(u)
		a, ok := byKey[key]
		if !ok {
			a = &acc{hint: unresolvedHint(u), oid: displayOID}
			byKey[key] = a
			order = append(order, key)
		}
		a.count++
	}

	out := make([]web.WalkUnresolvedRow, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		out = append(out, web.WalkUnresolvedRow{OID: a.oid, Count: a.count, Hint: a.hint})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].OID < out[j].OID
	})
	return out
}

func unresolvedKey(u *walk.Unresolved) (key, displayOID string) {
	if u.EnterpriseID != 0 {
		prefix := fmt.Sprintf("1.3.6.1.4.1.%d", u.EnterpriseID)
		return prefix, "." + prefix
	}
	return u.Prefix, "." + u.Prefix
}

func unresolvedHint(u *walk.Unresolved) string {
	switch {
	case u.EnterpriseName != "":
		return fmt.Sprintf("PEN %d (%s) — load a vendor MIB to decode further", u.EnterpriseID, u.EnterpriseName)
	case u.EnterpriseID != 0:
		return fmt.Sprintf("PEN %d (unknown vendor) — load the vendor's MIB to decode further", u.EnterpriseID)
	case u.MatchedModuleRoot != "":
		return fmt.Sprintf("nearest loaded module: %s, but no symbol covers this prefix", u.MatchedModuleRoot)
	case u.CanonicalName != "":
		return fmt.Sprintf("under %s; no loaded module covers this prefix", u.CanonicalName)
	default:
		return "no loaded module covers this prefix"
	}
}

// handleWalkBundle streams a ZIP for a posted walk: the walk verbatim,
// a README, every loaded module the walk touched plus the union of
// their import closures, and a MISSING.txt manifest extended with the
// unresolved-OID section. Mirrors handleModuleBundle's streaming and
// path-traversal guard, generalised to multiple roots.
func (s *Server) handleWalkBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	walkText, status, msg := s.readWalk(w, r)
	if status != http.StatusOK {
		renderWalkUploadError(w, r, status, msg)
		return
	}

	rw, err := walk.Resolve(ctx, walk.Parse(walkText), s.store)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	roots := []string{s.mibsDir}
	closure, err := s.store.ListImportClosureUnion(ctx, rw.Modules)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	shippable, missings := partitionClosure(closure, roots)

	date := time.Now().UTC().Format("2006-01-02")
	w.Header().Set("Content-Type", "application/zip")
	setAttachmentDisposition(w, "walk-bundle-"+date+".zip")

	zw := zip.NewWriter(w)
	defer func() {
		if err := zw.Close(); err != nil {
			slog.Warn("walk bundle: zip close", "err", err)
		}
	}()

	// The walk verbatim, byte-for-byte as posted.
	if err := writeZipString(zw, "walk.txt", walkText); err != nil {
		slog.Warn("walk bundle: walk.txt", "err", err)
		return
	}
	if err := writeZipString(zw, "README.txt", walkBundleReadme(len(rw.Modules), len(shippable))); err != nil {
		slog.Warn("walk bundle: README.txt", "err", err)
		return
	}

	extra, ok := copyMIBsToZip(ctx, zw, shippable, "walk bundle")
	missings = append(missings, extra...)
	if !ok {
		return
	}

	// MISSING.txt: unshippable modules + unresolved OIDs.
	var buf strings.Builder
	fmt.Fprintln(&buf, "# Modules referenced but not currently loaded")
	if len(missings) == 0 {
		fmt.Fprintln(&buf, "# (none — every closure module was shipped)")
	}
	for _, m := range missings {
		fmt.Fprintf(&buf, "%s\n", m.Module)
		if m.ImportedBy != "" {
			fmt.Fprintf(&buf, "  imported by: %s\n", m.ImportedBy)
		}
		fmt.Fprintf(&buf, "  reason:      %s\n\n", m.Reason)
	}

	fmt.Fprintln(&buf, "# OIDs in this walk with no covering module")
	unresolved := aggregateUnresolved(rw)
	if len(unresolved) == 0 {
		fmt.Fprintln(&buf, "# (none — every OID resolved to a loaded module)")
	}
	for _, u := range unresolved {
		fmt.Fprintf(&buf, "%s\n", u.OID)
		fmt.Fprintf(&buf, "  count: %d\n", u.Count)
		fmt.Fprintf(&buf, "  hint:  %s\n\n", u.Hint)
	}

	if err := writeZipString(zw, "MISSING.txt", buf.String()); err != nil {
		slog.Warn("walk bundle: MISSING.txt", "err", err)
		return
	}
}

func walkBundleReadme(modules, shipped int) string {
	return fmt.Sprintf(`This bundle was exported from blittermib for an snmpwalk capture
that touched %d module(s); %d of them are included here along with
their transitively-imported MIBs.

To decode the walk locally with Net-SNMP:

  unzip walk-bundle-*.zip -d walk-bundle
  cd walk-bundle
  snmptranslate -m ALL -M . -On <oid>
  snmpwalk -m ALL -M . ... <host>

The original walk capture is in walk.txt.
MISSING.txt lists modules referenced but not loaded into blittermib at
export time, and OIDs in the walk that no loaded module covered.
`, modules, shipped)
}
