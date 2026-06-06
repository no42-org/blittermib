/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateLegacyUpload: when mibs/upload/ exists and mibs/import/
// does not, the legacy folder is renamed to import/ (its files flow
// through the new pipeline on the same boot).
func TestMigrateLegacyUpload(t *testing.T) {
	mibs := t.TempDir()
	upload := filepath.Join(mibs, "upload")
	if err := os.MkdirAll(upload, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upload, "PENDING-MIB"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	migrateLegacyUpload(mibs)

	importDir := filepath.Join(mibs, "import")
	if _, err := os.Stat(importDir); err != nil {
		t.Fatalf("import/ not created by migration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(importDir, "PENDING-MIB")); err != nil {
		t.Errorf("pending file not carried into import/: %v", err)
	}
	if _, err := os.Stat(upload); !os.IsNotExist(err) {
		t.Errorf("legacy upload/ still present after migration (err=%v)", err)
	}
}

// TestMigrateLegacyUploadBothPresent: when both upload/ and import/
// exist, no migration happens and upload/ is left untouched (the
// operator must merge manually).
func TestMigrateLegacyUploadBothPresent(t *testing.T) {
	mibs := t.TempDir()
	upload := filepath.Join(mibs, "upload")
	importDir := filepath.Join(mibs, "import")
	for _, d := range []string{upload, importDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(upload, "STILL-HERE")
	if err := os.WriteFile(marker, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	migrateLegacyUpload(mibs)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("upload/ file removed despite both dirs present: %v", err)
	}
	if _, err := os.Stat(upload); err != nil {
		t.Errorf("upload/ dir removed despite both dirs present: %v", err)
	}
}

// TestMigrateLegacyUploadNoLegacy: a fresh corpus with no upload/ is a
// silent no-op.
func TestMigrateLegacyUploadNoLegacy(t *testing.T) {
	mibs := t.TempDir()
	migrateLegacyUpload(mibs) // must not panic or create anything
	if _, err := os.Stat(filepath.Join(mibs, "import")); !os.IsNotExist(err) {
		t.Errorf("import/ created without a legacy upload/ (err=%v)", err)
	}
}
