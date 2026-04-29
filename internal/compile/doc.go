// Package compile parses a directory of MIB files by subprocessing
// libsmi's smidump and smilint, then transforms the structured output
// into the normalized model used by the rest of the application.
//
// A second pass over all parsed modules computes the cross-reference
// index that powers "Used by", "Augmented by", and "Indexed by"
// navigation in the UI.
package compile
