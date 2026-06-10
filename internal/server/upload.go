package server

import (
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
	"github.com/no42-org/blittermib/internal/mibimport"
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
	errCodeDuplicate   = "duplicate"
)

// uploadOutcome is the per-file response shape. Uploads traverse the
// import pipeline (mib-import-pipeline), so the outcome carries the
// pipeline status: imported (with module detail + curated dest),
// failed (reason; file in import/failed/), or duplicate (existing
// corpus path; file in import/duplicate/). OK stays as the legacy
// boolean (true only for imported).
//
// httpStatus is internal — it lets the handler pick a meaningful
// HTTP status code on a single-file batch so the spec's "the
// response is 413" scenarios hold. Multi-file batches always return
// 200 with per-file detail.
type uploadOutcome struct {
	Name        string             `json:"name"`
	OK          bool               `json:"ok"`
	Status      string             `json:"status,omitempty"` // imported | failed | duplicate
	Module      string             `json:"module,omitempty"`
	Symbols     int                `json:"symbols,omitempty"`
	OID         string             `json:"oid,omitempty"`
	Diagnostics []model.Diagnostic `json:"diagnostics,omitempty"`
	Dest        string             `json:"dest,omitempty"`     // curated path (imported)
	Existing    string             `json:"existing,omitempty"` // corpus path (duplicate)
	Replaced    bool               `json:"replaced,omitempty"`
	Error       string             `json:"error,omitempty"`
	ErrorCode   string             `json:"errorCode,omitempty"`

	httpStatus int // not serialised
}

type uploadResponse struct {
	Uploaded []uploadOutcome `json:"uploaded"`
}

// handleUpload accepts a multipart batch, atomically stages each
// accepted part into import/ (the single intake path), then runs the
// import pipeline once for the whole batch so intra-batch IMPORTS
// resolve. The response reports the pipeline outcome per file.
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

	if err := s.importer.EnsureDirs(); err != nil {
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
		oc := s.processUploadPart(part, replace)
		_ = part.Close()
		outcomes = append(outcomes, oc)
		if oc.OK {
			acceptedPaths = append(acceptedPaths, filepath.Join(s.importer.Dir(), oc.Name))
		}
	}

	// Stage all parts first, run the pipeline ONCE so smidump's
	// IMPORTS resolver sees prerequisites on disk regardless of part
	// arrival order in the multipart body.
	if len(acceptedPaths) > 0 {
		var results []mibimport.Outcome
		if replace {
			results = s.importer.ImportReplace(r.Context(), acceptedPaths)
		} else {
			results = s.importer.Import(r.Context(), acceptedPaths)
		}
		byPath := make(map[string]mibimport.Outcome, len(results))
		for _, res := range results {
			byPath[res.Path] = res
		}
		for i := range outcomes {
			if !outcomes[i].OK {
				continue
			}
			res, ok := byPath[filepath.Join(s.importer.Dir(), outcomes[i].Name)]
			if !ok {
				// Pipeline deferred the file (settle window / budget);
				// the rescan will pick it up. NOT a success — the
				// import has not happened yet.
				outcomes[i].OK = false
				outcomes[i].Status = "pending"
				outcomes[i].Error = "import deferred; the file is queued and will be processed shortly"
				outcomes[i].ErrorCode = "pending"
				continue
			}
			outcomes[i].Status = string(res.Status)
			switch res.Status {
			case mibimport.StatusImported:
				if res.Module != nil {
					outcomes[i].Module = res.Module.Name
					outcomes[i].OID = res.Module.OIDRoot
				}
				outcomes[i].Symbols = res.SymbolCount
				outcomes[i].Diagnostics = res.Diagnostics
				outcomes[i].Dest = res.Dest
			case mibimport.StatusDuplicate:
				outcomes[i].OK = false
				outcomes[i].Existing = res.Existing
				outcomes[i].Error = res.Reason
				outcomes[i].ErrorCode = errCodeDuplicate
				outcomes[i].httpStatus = http.StatusConflict
			default: // failed
				outcomes[i].OK = false
				outcomes[i].Error = res.Reason
				outcomes[i].ErrorCode = errCodeCompile
				outcomes[i].httpStatus = http.StatusUnprocessableEntity
			}
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

// processUploadPart stages a single multipart part into import/:
// validate, write to import/.tmp, sniff, link/rename into place.
// Returns an outcome with Name + OK + Error + httpStatus filled in;
// the pipeline pass fills the rest for accepted entries.
func (s *Server) processUploadPart(part interface {
	FileName() string
	io.Reader
}, replace bool) uploadOutcome {
	raw := part.FileName()
	// Reject path separators and traversal segments BEFORE
	// filepath.Base — otherwise `../../../etc/passwd` collapses to
	// `passwd` and silently writes into the intake dir.
	if raw == "" || raw == "." || raw == ".." ||
		strings.ContainsAny(raw, "/\\\x00") {
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

	target := filepath.Join(s.importer.Dir(), name)
	existed := false
	if _, err := os.Lstat(target); err == nil {
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "stat target: " + err.Error()
		oc.ErrorCode = errCodeIO
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	if existed && !replace {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "a file with this name is already pending import"
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
		oc.ErrorCode = errCodeIO
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	tmpPath := filepath.Join(s.importer.TmpDir(), name+"."+suffix+".upload")
	// #nosec G304 -- tmpPath is filepath.Join(TmpDir, validatedName + crypto-random suffix); rooted under import/.tmp.
	f, err := os.Create(tmpPath)
	if err != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(part, maxUploadFileSize+1))
		oc.Error = "create tmp: " + err.Error()
		oc.ErrorCode = errCodeIO
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
		oc.ErrorCode = errCodeIO
		oc.httpStatus = http.StatusInternalServerError
		return oc
	}
	if syncErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		oc.Error = "fsync/close failed"
		oc.ErrorCode = errCodeIO
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
		// The synchronous front rejects non-MIBs at the door without
		// touching import/ (folder drops of non-MIBs quarantine
		// instead — they have no requester to answer). If the rejected
		// upload looks like an snmpwalk capture, point at /walk rather
		// than the generic "no MIB marker".
		oc.Error = "no MIB marker"
		if contentLooksLikeWalk(sniffHead(tmpPath, 256<<10)) {
			oc.Error = "this looks like an snmpwalk capture, not a MIB — decode it at /walk instead"
		}
		_ = os.Remove(tmpPath)
		oc.ErrorCode = errCodeNoMarker
		oc.httpStatus = http.StatusUnprocessableEntity
		return oc
	}

	// Two paths:
	//   replace=true  → os.Rename (clobbers a same-name pending file).
	//   replace=false → os.Link (atomic claim; EEXIST eliminates the
	//                   Lstat→Rename TOCTOU against a concurrent
	//                   uploader).
	if replace {
		if err := os.Rename(tmpPath, target); err != nil {
			_ = os.Remove(tmpPath)
			oc.Error = "rename: " + err.Error()
			oc.ErrorCode = errCodeIO
			oc.httpStatus = http.StatusInternalServerError
			return oc
		}
	} else {
		if err := os.Link(tmpPath, target); err != nil {
			_ = os.Remove(tmpPath)
			if errors.Is(err, os.ErrExist) {
				oc.Error = "a file with this name is already pending import"
				oc.ErrorCode = errCodeExists
				oc.httpStatus = http.StatusConflict
				return oc
			}
			oc.Error = "link: " + err.Error()
			oc.ErrorCode = errCodeIO
			oc.httpStatus = http.StatusInternalServerError
			return oc
		}
		if err := os.Remove(tmpPath); err != nil {
			slog.Warn("upload tmp cleanup failed", "path", tmpPath, "err", err)
		}
	}

	oc.OK = true
	oc.Replaced = existed
	return oc
}

// sniffHead reads up to n bytes from the start of a file for content
// detection (e.g. distinguishing a MIB from an snmpwalk capture).
// Returns "" on any error.
func sniffHead(path string, n int) string {
	// #nosec G304 -- path is the import-pipeline temp file (filepath.Join(TmpDir, validatedName + crypto-random suffix), rooted under import/.tmp), not raw user input.
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(io.LimitReader(f, int64(n)))
	return string(b)
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

// handleUploadDelete serves /api/v1/upload/{name}:
//
//	DELETE ?from=pending|failed|duplicate  → remove the file (and its
//	        sidecar for quarantined entries). Default: pending.
//	POST   ?action=replace&from=duplicate|failed → re-run the file
//	        through the pipeline with replacement allowed (the
//	        sanctioned overwrite path for updating a module, D11).
func (s *Server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(uploadCSRFHeader) == "" {
		http.Error(w, "missing CSRF header", http.StatusForbidden)
		return
	}
	const prefix = "/api/v1/upload/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, prefix))
	if name == "" || name == "." || name == ".." ||
		!mibcorpus.ValidModuleName.MatchString(name) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}

	dir := s.importer.Dir()
	switch r.URL.Query().Get("from") {
	case "failed":
		dir = s.importer.FailedDir()
	case "duplicate":
		dir = s.importer.DuplicateDir()
	case "", "pending":
	default:
		http.Error(w, "invalid from", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if err := mibimport.RemoveQuarantined(dir, name); err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			s.internalError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPost:
		if r.URL.Query().Get("action") != "replace" {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		// The sanctioned overwrite path applies to QUARANTINED files
		// only (design D11) — pending intake files go through the
		// normal pipeline, which quarantines duplicates for an
		// explicit decision.
		if dir == s.importer.Dir() {
			http.Error(w, "replace applies to quarantined files (from=failed|duplicate)", http.StatusBadRequest)
			return
		}
		res := s.importer.ImportReplacing(r.Context(), filepath.Join(dir, name))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if res.Status != mibimport.StatusImported {
			w.WriteHeader(http.StatusUnprocessableEntity)
		}
		_ = json.NewEncoder(w).Encode(res)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleImportIndex renders the /import management page: drop zone,
// pending intake files, the failed/ and duplicate/ quarantines with
// their sidecar reasons, and recent pipeline outcomes from the store.
func (s *Server) handleImportIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	view := web.ImportView{}

	if pending, err := s.importer.Pending(); err == nil {
		for _, p := range pending {
			info, err := os.Stat(p)
			if err != nil {
				continue
			}
			view.Pending = append(view.Pending, web.ImportPendingRow{
				Name: filepath.Base(p), Size: info.Size(),
			})
		}
	}
	if failed, err := mibimport.ListQuarantine(s.importer.FailedDir()); err == nil {
		for _, q := range failed {
			view.Failed = append(view.Failed, quarantineRow(q))
		}
	}
	if dups, err := mibimport.ListQuarantine(s.importer.DuplicateDir()); err == nil {
		for _, q := range dups {
			view.Duplicate = append(view.Duplicate, quarantineRow(q))
		}
	}
	if recent, err := s.store.ListImportOutcomes(r.Context(), 50); err == nil {
		for _, o := range recent {
			view.Recent = append(view.Recent, web.ImportRecentRow{
				Name: o.Name, Status: o.Status, Module: o.ModuleName,
				Detail: o.Detail, OccurredAt: o.OccurredAt,
			})
		}
	}
	render(w, r, http.StatusOK, web.ImportIndex(view))
}

func quarantineRow(q mibimport.QuarantineEntry) web.ImportQuarantineRow {
	return web.ImportQuarantineRow{
		Name:       q.Name,
		Size:       q.Size,
		Reason:     q.Sidecar.Reason,
		Existing:   q.Sidecar.Existing,
		OccurredAt: q.Sidecar.OccurredAt,
	}
}
