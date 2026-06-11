package main

import "github.com/no42-org/blittermib/internal/mibcorpus"

// The classification + group-mapping logic lives in
// internal/mibcorpus/ so cmd/mib-migrate and cmd/mib-ingest share
// identical routing rules. This file re-exports the migrate-tool-
// facing names as type aliases + value re-bindings so the rest of
// the migrate package keeps the short identifiers it had before
// the extract refactor.

type Confidence = mibcorpus.Confidence

const (
	ConfidenceHigh   = mibcorpus.ConfidenceHigh
	ConfidenceMedium = mibcorpus.ConfidenceMedium
	ConfidenceLow    = mibcorpus.ConfidenceLow
)

// Classify is re-exported for the migrate-package consumers.
var Classify = mibcorpus.Classify

// LoadGroups is re-exported for the migrate-package consumers.
var LoadGroups = mibcorpus.LoadGroups
