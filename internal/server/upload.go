package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/web"
)

// maxUploadFileSize is the per-file cap (D7). 10 MB covers all
// real-world standard and major-vendor MIBs with headroom; anything
// larger is rejected as "definitely not a MIB."
const maxUploadFileSize = 10 << 20

// uploadCSRFHeader is a same-origin sentinel: any browser fetch from
// our own pages adds it; cross-origin browser fetches setting custom
// headers trigger a CORS preflight that our server doesn't honour, so
// the actual POST/DELETE never fires. Belt-and-braces against the
// "operator left the tab open while browsing evil.com" failure mode.
const uploadCSRFHeader = "X-Blittermib-Upload"

// Stable error codes carried alongside the human-readable Error
// string so client code can branch reliably without sniffing English.
const (
	errCodeInvalidName = "invalid-name"
	errCodeExists      = "exists"
	errCodeNoMarker    = "no-marker"
	errCodeTooLarge    = "too-large"
	errCodeIO          = "io"
	errCodeCompile     = "compile-failed"
)

// uploadOutcome is the per-file response shape. JSON tags align with
// the spec scenarios in
// openspec/changes/web-upload/specs/web-upload/spec.md.
//
// httpStatus is internal — it lets the handler pick a meaningful
// HTTP status code on a single-file batch (200/400/409/413/422) so
// the spec's "the response is 413" scenarios hold without making the
// caller scrape an HTTP body to discover what went wrong. For
// multi-file batches, we always return 200 and the per-file outcomes
// carry the detail.
type uploadOutcome struct {
	Name        string             `json:"name"`
	OK          bool               `json:"ok"`
	Module      string             `json:"module,omitempty"`
	Symbols     int                `json:"symbols,omitempty"`
	OID         string             `json:"oid,omitempty"`
	Diagnostics []model.Diagnostic `json:"diagnostics,omitempty"`
	Replaced    bool               `json:"replaced,omitempty"`
	Error       string             `json:"error,omitempty"`
	ErrorCode   string             `json:"errorCode,omitempty"`

	httpStatus int // not serialised
}

type uploadResponse struct {
	Uploaded []uploadOutcome `json:"uploaded"`
}

// handleUpload implements the multi-file batched upload pipeline
// per design.md D4 + D5 + D6a:
//
//  1. validate each multipart part's filename (ValidModuleName)
//  2. enforce 10 MB cap per file via a LimitReader
//  3. atomic-write the bytes to mibs/upload/.tmp/<name>.upload
//  4. sniff the temp file with HasMIBOpener
//  5. on collision, refuse unless ?replace=true
//  6. rename(2) into mibs/upload/<name>
//  7. once ALL accepted parts are written, fire one loadFiles call
//     so smidump's IMPORTS resolver sees the whole batch on disk
//  8. emit per-file outcomes as JSON
//
// All step-level failures attach to the per-file outcome and the
// handler keeps processing remaining parts. The watcher will fire
// after the renames and recompile redundantly (D6b — accepted).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(uploadCSRFHeader) == "" {
		http.Error(w, "missing CSRF header", http.StatusForbidden)
		return
	}

	replaceQ := r.URL.Query().Get("replace")
	replace, _ := strconv.ParseBool(replaceQ)

	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected multipart/form-data", http.StatusBadRequest)
		return
	}

	tmpDir := filepath.Join(s.uploadDir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		s.internalError(w, r, err)
		return
	}

	var outcomes []uploadOutcome
	var acceptedPaths []string

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, "malformed multipart body", http.StatusBadRequest)
			return
		}
		oc := s.processUploadPart(part, replace, tmpDir)
		_ = part.Close()
		outcomes = append(outcomes, oc)
		if oc.OK {
			acceptedPaths = append(acceptedPaths, filepath.Join(s.uploadDir, oc.Name))
		}
	}

	// D14 — write all parts first, fire loadFiles ONCE so smidump's
	// IMPORTS resolver sees prerequisites on disk regardless of part
	// arrival order in the multipart body.
	if len(acceptedPaths) > 0 && s.loadFiles != nil {
		results := s.loadFiles(r.Context(), acceptedPaths)
		byPath := make(map[string]LoadOutcome, len(results))
		for _, r := range results {
			byPath[r.Path] = r
		}
		for i := range outcomes {
			if !outcomes[i].OK {
				continue
			}
			path := filepath.Join(s.uploadDir, outcomes[i].Name)
			res, ok := byPath[path]
			if !ok {
				continue
			}
			if res.Err != nil {
				outcomes[i].OK = false
				outcomes[i].Error = "compile failed: " + res.Err.Error()
				outcomes[i].ErrorCode = errCodeCompile
				continue
			}
			if res.Module != nil {
				outcomes[i].Module = res.Module.Name
				outcomes[i].OID = res.Module.OIDRoot
			}
			outcomes[i].Symbols = res.SymbolCount
			outcomes[i].Diagnostics = res.Diagnostics
		}
	}

	if len(outcomes) == 0 {
		http.Error(w, "no parts in multipart body", http.StatusBadRequest)
		return
	}

	status := http.StatusOK
	if len(outcomes) == 1 && !outcomes[0].OK && outcomes[0].httpStatus != 0 {
		status = outcomes[0].httpStatus
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(uploadResponse{Uploaded: outcomes}); err != nil {
		slog.Warn("upload response encode failed", "err", err)
	}
}

// processUploadPart handles a single multipart part end-to-end:
// validate, write to tmp, sniff, rename. Returns an outcome that
// already has Name + OK + Error + httpStatus filled in; the
// post-batch compile pass adds Module/OID/Symbols/Diagnostics for
// successful entries.
func (s *Server) processUploadPart(part interface {
	FileName() string
	io.Reader
}, replace bool, tmpDir string) uploadOutcome {
	raw := part.FileName()
	// Reject path separators and traversal segments BEFORE
	// filepath.Base — otherwise `../../../etc/passwd` collapses to
	// `passwd` and silently writes to mibs/upload/passwd.
	if raw == "" || raw == "." || raw == ".." ||
		strings.ContainsAny(raw, "/\\\x00") ||
		strings.Contains(raw, "..") {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		return uploadOutcome{
			Name:       filepath.Base(raw),
			Error:      "invalid filename",
			ErrorCode:  errCodeInvalidName,
			httpStatus: http.StatusBadRequest,
		}
	}
	name := filepath.Base(raw)
	oc := uploadOutcome{Name: name}

	if !mibcorpus.ValidModuleName.MatchString(name) {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "invalid filename"
		oc.ErrorCode = errCodeInvalidName
		oc.httpStatus = http.StatusBadRequest
		return oc
	}

	target := filepath.Join(s.uploadDir, name)
	existed := false
	if _, err := os.Lstat(target); err == nil {
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "stat target: " + err.Error()
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	if existed && !replace {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "destination already exists"
		oc.ErrorCode = errCodeExists
		oc.httpStatus = http.StatusConflict
		return oc
	}

	// Random suffix in tmp filename so two concurrent uploads of the
	// same name don't truncate each other mid-write.
	suffix, err := randHex(8)
	if err != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "rand: " + err.Error()
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	tmpPath := filepath.Join(tmpDir, name+"."+suffix+".upload")
	f, err := os.Create(tmpPath)
	if err != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "create tmp: " + err.Error()
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	// LimitReader reads at most maxUploadFileSize+1 bytes; if we
	// actually pulled that many, the source exceeded the cap.
	n, copyErr := io.Copy(f, io.LimitReader(part, maxUploadFileSize+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		oc.Error = "io: " + copyErr.Error()
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	if syncErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		oc.Error = "fsync/close failed"
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	if n > maxUploadFileSize {
		_ = os.Remove(tmpPath)
		oc.Error = "file exceeds 10 MB limit"
		oc.ErrorCode = errCodeTooLarge
		oc.httpStatus = http.StatusRequestEntityTooLarge
		return oc
	}

	ok, err := mibcorpus.HasMIBOpener(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		oc.Error = "sniff failed: " + err.Error()
		oc.ErrorCode = errCodeIO
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	if !ok {
		_ = os.Remove(tmpPath)
		oc.Error = "no MIB marker"
		oc.ErrorCode = errCodeNoMarker
		oc.httpStatus = http.StatusUnprocessableEntity
		return oc
	}

	// Atomic claim: Link returns EEXIST if the target already exists.
	// On the no-replace path that's the collision signal — but we
	// already checked above, so a Link EEXIST here means a concurrent
	// upload won the race. Surface it as a refusal too. On the replace
	// path, Rename clobbers existing.
	if replace && existed {
		if err := os.Rename(tmpPath, target); err != nil {
			_ = os.Remove(tmpPath)
			oc.Error = "rename: " + err.Error()
			oc.httpStatus = http.StatusInternalServerError
			return oc
		}
	} else {
		if err := os.Link(tmpPath, target); err != nil {
			_ = os.Remove(tmpPath)
			if errors.Is(err, os.ErrExist) {
				oc.Error = "destination already exists"
				oc.ErrorCode = errCodeExists
				oc.httpStatus = http.StatusConflict
				return oc
			}
			oc.Error = "link: " + err.Error()
			oc.httpStatus = http.StatusInternalServerError
			return oc
		}
		// Link succeeded; remove the now-redundant tmp file.
		_ = os.Remove(tmpPath)
	}

	oc.OK = true
	oc.Replaced = existed
	return oc
}

// randHex returns 2*n hex characters. Used for tmp-file suffixes so
// concurrent uploads of the same filename don't truncate each
// other's in-flight writes.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// handleUploadDelete removes a single file from mibs/upload/. The
// only accepted path shape is /api/v1/upload/<name> where <name>
// passes ValidModuleName; anything escaping mibs/upload/ via
// ../-style traversal or absolute paths is refused.
//
// On success, the watcher's debounced reload drops the corresponding
// module from the store within ~250 ms (per the existing fsnotify
// pipeline); we don't need to wire a synchronous unload here.
func (s *Server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(uploadCSRFHeader) == "" {
		http.Error(w, "missing CSRF header", http.StatusForbidden)
		return
	}
	const prefix = "/api/v1/upload/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	rawName := strings.TrimPrefix(r.URL.Path, prefix)
	// URL-decoded by net/http during routing; defend against any
	// surviving traversal characters by going through filepath.Base
	// + ValidModuleName.
	name := filepath.Base(rawName)
	if name == "" || name == "." || name == ".." {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	if !mibcorpus.ValidModuleName.MatchString(name) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	target := filepath.Join(s.uploadDir, name)
	// Defence-in-depth: even after ValidModuleName, refuse anything
	// that doesn't resolve to a child of s.uploadDir.
	rel, err := filepath.Rel(s.uploadDir, target)
	if err != nil || !filepath.IsLocal(rel) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadIndex renders the /upload management page: the same
// drop zone the landing page hosts, plus a list of every entry
// currently in mibs/upload/ regardless of load state. Each row
// shows the filename, size, load state (loaded / parse error /
// non-MIB), and a delete affordance. Files whose module name also
// resolves to a corpus path outside upload/ get a `shadows: <path>`
// annotation per design.md D10.
func (s *Server) handleUploadIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows, err := s.uploadRows(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, web.UploadIndex(rows))
}

// uploadRows lists mibs/upload/ entries (skipping .gitkeep, hidden
// dirs like .tmp/) and decorates each with load state + shadow
// annotation.
//
// Load-state lookup keys by SourcePath, NOT by filename. A MIB's
// module name (the identifier inside the file) is often unrelated to
// its filename — `cisco-bgp4.mib` may declare module
// `CISCO-BGP4-MIB`. Asking the store via GetModule(filename) would
// silently misclassify those as "parse error".
//
// Shadow detection: if the basename appears anywhere else under
// s.mibsDir (outside upload/), the row carries the corpus-relative
// path of that file. Walked once per request — for a 322-file
// corpus this is <50 ms; not worth caching for v1.
func (s *Server) uploadRows(ctx context.Context) ([]web.UploadRow, error) {
	entries, err := os.ReadDir(s.uploadDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	shadows, err := s.shadowMap()
	if err != nil {
		// Log and continue — shadow info is decorative, not load-
		// bearing.
		slog.Warn("shadow map walk failed; rows will lack shadow annotations", "err", err)
		shadows = nil
	}
	// Build a path-keyed index of loaded modules so per-row lookup
	// is O(1) on a path basis (vs the broken filename-based key).
	bySrc, _ := s.modulesBySourcePath(ctx)

	var rows []web.UploadRow
	for _, e := range entries {
		name := e.Name()
		// Skip exactly the staging-only files: .gitkeep and the
		// .tmp/ directory. Other dotfiles (real ones the operator
		// dropped) still surface so they can be deleted via the UI.
		if name == ".gitkeep" || (e.IsDir() && name == ".tmp") {
			continue
		}
		if e.IsDir() {
			continue
		}
		path := filepath.Join(s.uploadDir, name)
		info, err := e.Info()
		if err != nil {
			slog.Warn("upload row stat failed; skipping", "name", name, "err", err)
			continue
		}
		row := web.UploadRow{
			Name: name,
			Size: info.Size(),
		}
		if mod, ok := bySrc[path]; ok {
			row.State = "loaded"
			row.Module = mod.Name
			if diags, dErr := s.store.ListDiagnosticsByModule(ctx, mod.Name); dErr == nil {
				row.DiagCount = len(diags)
			}
		} else {
			// Either nothing loaded for this file, or the loaded
			// module under this name has a SourcePath elsewhere.
			// Sniff the marker to decide between parse-error and
			// non-MIB.
			ok, sErr := mibcorpus.HasMIBOpener(path)
			switch {
			case sErr != nil:
				slog.Warn("upload row sniff failed", "name", name, "err", sErr)
				row.State = "parse error"
			case ok:
				row.State = "parse error"
			default:
				row.State = "non-MIB skipped"
			}
		}
		// Shadow annotation: this upload masks a corpus file with
		// the same basename.
		if rel, ok := shadows[name]; ok {
			row.Shadows = rel
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// modulesBySourcePath returns a map of canonical source path →
// module pointer for every loaded module. Used by uploadRows to
// resolve "is this file loaded?" correctly even when filename and
// module name diverge.
func (s *Server) modulesBySourcePath(ctx context.Context) (map[string]*model.Module, error) {
	mods, err := s.store.ListModules(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*model.Module, len(mods))
	for i := range mods {
		if mods[i].SourcePath == "" {
			continue
		}
		out[mods[i].SourcePath] = &mods[i]
	}
	return out, nil
}

// shadowMap builds a basename → corpus-relative path index of every
// MIB-shaped file UNDER s.mibsDir but NOT under s.uploadDir. The
// /upload page uses this to flag which uploads mask a curated
// corpus file (the operator deserves to know: deleting the upload
// entry will unload the module entirely, not "fall back" to the
// corpus version automatically).
func (s *Server) shadowMap() (map[string]string, error) {
	out := make(map[string]string)
	err := filepath.WalkDir(s.mibsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != s.mibsDir && (strings.HasPrefix(name, ".") || name == "LICENSES" || name == "upload") {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, ".") {
			return nil
		}
		// No extension filter — the upload sniffer accepts anything
		// that passes HasMIBOpener regardless of extension, so
		// shadow detection must too.
		rel, err := filepath.Rel(s.mibsDir, path)
		if err != nil {
			return nil
		}
		out[base] = rel
		return nil
	})
	return out, err
}
