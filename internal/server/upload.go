package server

import "net/http"

// Upload + delete + management surface — gated by EnableUploads
// (which only fires when BLITTERMIB_UPLOAD_ENABLED parses truthy).
// The detail bodies of these handlers land in §2 / §3 / §5 of
// openspec/changes/web-upload/tasks.md.

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	// §2.1–§2.4: multipart parse, atomic-write, sync compile,
	// per-file outcomes JSON.
	http.Error(w, "upload handler not yet implemented", http.StatusNotImplemented)
}

func (s *Server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	// §3.1: validate name, traversal-guard, os.Remove, 204.
	http.Error(w, "upload-delete handler not yet implemented", http.StatusNotImplemented)
}

func (s *Server) handleUploadIndex(w http.ResponseWriter, r *http.Request) {
	// §5.1–§5.2: list mibs/upload/, render templ with drop zone +
	// per-file rows + delete buttons.
	http.Error(w, "upload-index handler not yet implemented", http.StatusNotImplemented)
}
