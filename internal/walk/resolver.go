package walk

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/no42-org/blittermib/internal/iana"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// ResolvedEntry is a walk Entry decorated with what the store (and,
// failing that, the IANA registries) could tell us about its OID.
type ResolvedEntry struct {
	Entry Entry

	Resolved  bool   // a loaded module owns the OID's column/scalar
	Module    string // owning module name (when Resolved)
	Symbol    string // owning symbol name (when Resolved)
	SymbolOID string // the matched symbol's OID
	Suffix    string // dotted instance/index suffix after the matched symbol

	// Index decode (v1.0: single-INTEGER columns only).
	IndexName   string // e.g. "ifIndex" when decoded
	IndexValue  string // e.g. "1" when decoded
	IndexDecode string // "integer" | "raw-suffix" | "" (scalar instance / none)

	Unresolved *Unresolved // set when no loaded module covers the OID
}

// Unresolved carries the best guidance available for an OID that no
// loaded module covers: the nearest module root, the canonical SMI
// arc, and the vendor PEN.
type Unresolved struct {
	Prefix            string // the OID (or raw identifier) that didn't resolve
	MatchedModuleRoot string // name of the loaded module whose oid_root is the longest prefix, if any
	EnterpriseID      uint32 // vendor PEN when under enterprises
	EnterpriseName    string // organization name for the PEN, if known
	CanonicalName     string // deepest well-known SMI arc (e.g. "enterprises")
}

// ResolvedWalk is the fully-decorated walk.
type ResolvedWalk struct {
	Entries      []ResolvedEntry
	Modules      []string // sorted, unique names of modules the walk resolved into
	SkippedLines int
	ParserNotes  []string
}

// resolver carries the store plus per-Resolve lookup memos. Walks are
// massively repetitive — the same column prefix appears once per
// instance, and unresolved vendor OIDs probe the same shrinking
// prefixes tens of thousands of times — so memoizing both positive and
// negative lookups turns an O(entries × OID-depth) query storm into a
// handful of queries per distinct prefix.
type resolver struct {
	s      *store.Store
	byOID  map[string]*model.Symbol // GetSymbolByOID memo; nil value = not found
	byName map[string]*model.Symbol // GetSymbol memo, keyed module + "\x00" + name; nil = not found
}

// memoLookup wraps a store getter with positive + negative
// memoization: ErrNotFound is cached as a nil entry and returned as
// (nil, nil) so callers treat "not found" as a plain miss.
func memoLookup(memo map[string]*model.Symbol, key string, get func() (*model.Symbol, error)) (*model.Symbol, error) {
	if sym, ok := memo[key]; ok {
		return sym, nil
	}
	sym, err := get()
	switch {
	case err == nil:
		memo[key] = sym
		return sym, nil
	case errors.Is(err, store.ErrNotFound):
		memo[key] = nil
		return nil, nil
	default:
		return nil, err
	}
}

func (r *resolver) symbolByOID(ctx context.Context, oid string) (*model.Symbol, error) {
	return memoLookup(r.byOID, oid, func() (*model.Symbol, error) {
		return r.s.GetSymbolByOID(ctx, oid)
	})
}

func (r *resolver) symbolByName(ctx context.Context, module, name string) (*model.Symbol, error) {
	return memoLookup(r.byName, module+"\x00"+name, func() (*model.Symbol, error) {
		return r.s.GetSymbol(ctx, module, name)
	})
}

// Resolve decorates every entry in the walk against the store. It
// loads the module list once for the unresolved-OID root match and
// makes no writes — resolution is a pure read over the corpus.
func Resolve(ctx context.Context, w Walk, s *store.Store) (ResolvedWalk, error) {
	rw := ResolvedWalk{SkippedLines: w.SkippedLines, ParserNotes: w.ParserNotes}

	mods, err := s.ListModules(ctx)
	if err != nil {
		return rw, err
	}

	r := &resolver{
		s:      s,
		byOID:  make(map[string]*model.Symbol),
		byName: make(map[string]*model.Symbol),
	}

	modSet := make(map[string]struct{})
	lookupErrs := 0
	for _, e := range w.Entries {
		// A 100k-entry walk shouldn't grind on for a caller that hung
		// up — bail as soon as the request context is gone.
		if err := ctx.Err(); err != nil {
			return rw, err
		}
		re, err := r.resolveEntry(ctx, mods, e)
		if err != nil {
			// Degrade gracefully: a store lookup failure on one entry
			// shouldn't discard the rest of the decode (matching the
			// parser's tolerant stance). Mark it unresolved and move
			// on. The error detail is deliberately not surfaced — it
			// could echo walk content into a user-visible note.
			re = ResolvedEntry{Entry: e, Unresolved: &Unresolved{Prefix: e.Ident}}
			lookupErrs++
		}
		if re.Resolved {
			modSet[re.Module] = struct{}{}
		}
		rw.Entries = append(rw.Entries, re)
	}
	if lookupErrs > 0 {
		rw.ParserNotes = append(rw.ParserNotes,
			fmt.Sprintf("%d entries could not be looked up against the store", lookupErrs))
	}
	for m := range modSet {
		rw.Modules = append(rw.Modules, m)
	}
	sort.Strings(rw.Modules)
	return rw, nil
}

func (r *resolver) resolveEntry(ctx context.Context, mods []model.Module, e Entry) (ResolvedEntry, error) {
	re := ResolvedEntry{Entry: e}

	oid, ok := r.numericOID(ctx, e)
	if !ok {
		// A name-prefixed identifier we couldn't normalise to an OID
		// (the module/symbol isn't loaded). Surface it as unresolved
		// keyed by the raw identifier — there's no numeric prefix to
		// match a vendor PEN against.
		re.Unresolved = &Unresolved{Prefix: e.Ident}
		return re, nil
	}

	sym, suffix, found, err := r.findSymbol(ctx, oid)
	if err != nil {
		return re, err
	}
	if found && !isStructuralMatch(sym.OID, oid) {
		re.Resolved = true
		re.Module = sym.ModuleName
		re.Symbol = sym.Name
		re.SymbolOID = sym.OID
		re.Suffix = suffix
		if err := r.decodeIndex(ctx, sym, suffix, &re); err != nil {
			return re, err
		}
		return re, nil
	}

	re.Unresolved = unresolvedFor(mods, oid)
	return re, nil
}

// numericOID returns the numeric OID for an entry. Bare numeric
// identifiers pass through; a name-prefixed identifier
// (MODULE::symbol.suffix) is resolved against the store and the
// instance/index suffix re-appended.
func (r *resolver) numericOID(ctx context.Context, e Entry) (string, bool) {
	if e.Numeric() {
		return e.Ident, true
	}
	id := e.Ident
	var module, rest string
	if i := strings.Index(id, "::"); i >= 0 {
		module, rest = id[:i], id[i+2:]
	} else {
		rest = id
	}
	name, suffix := splitNameSuffix(rest)
	if module == "" || name == "" {
		return "", false
	}
	sym, err := r.symbolByName(ctx, module, name)
	if err != nil || sym == nil || sym.OID == "" {
		return "", false
	}
	oid := sym.OID
	if suffix != "" {
		oid += "." + suffix
	}
	return oid, true
}

// splitNameSuffix splits "sysName.0" into ("sysName", "0"). SMI names
// never contain a dot, so the first dot starts the suffix.
func splitNameSuffix(rest string) (string, string) {
	if i := strings.IndexByte(rest, '.'); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, ""
}

// findSymbol walks the OID up segment by segment, returning the first
// loaded symbol whose OID matches a prefix and the remaining suffix.
func (r *resolver) findSymbol(ctx context.Context, oid string) (*model.Symbol, string, bool, error) {
	segs := strings.Split(oid, ".")
	for n := len(segs); n >= 2; n-- {
		prefix := strings.Join(segs[:n], ".")
		sym, err := r.symbolByOID(ctx, prefix)
		if err != nil {
			return nil, "", false, err
		}
		if sym != nil {
			return sym, strings.Join(segs[n:], "."), true, nil
		}
	}
	return nil, "", false, nil
}

// decodeIndex applies the v1.0 index tier: a column whose entry has
// exactly one INTEGER/Integer32 index decodes its single-integer
// suffix to `name=value`. Everything else with a suffix is marked
// raw-suffix; a scalar instance (the lone `.0`) carries no index.
func (r *resolver) decodeIndex(ctx context.Context, sym *model.Symbol, suffix string, re *ResolvedEntry) error {
	if suffix == "" {
		return nil
	}
	if sym.Kind != model.KindColumn {
		// Scalar instance (.0) or a non-columnar match — the suffix is
		// the instance identifier, not a table index.
		return nil
	}

	entry, err := r.symbolByOID(ctx, sym.ParentOID)
	if err != nil {
		return err
	}
	if entry == nil {
		// No parent entry to read INDEX from — can't decode, but this
		// is an absence, not a failure.
		re.IndexDecode = "raw-suffix"
		return nil
	}

	if len(entry.IndexColumns) == 1 {
		idxSym, err := r.symbolByName(ctx, entry.ModuleName, entry.IndexColumns[0])
		if err != nil {
			return err
		}
		if idxSym != nil && isIntegerSyntax(idxSym.Syntax) && isSingleInteger(suffix) {
			re.IndexName = entry.IndexColumns[0]
			re.IndexValue = suffix
			re.IndexDecode = "integer"
			return nil
		}
	}
	re.IndexDecode = "raw-suffix"
	return nil
}

// unresolvedFor builds the guidance for an OID no loaded module owns:
// the longest module oid_root prefix, the deepest canonical arc, and
// the vendor PEN when the OID lives under enterprises.
func unresolvedFor(mods []model.Module, oid string) *Unresolved {
	u := &Unresolved{Prefix: oid}

	var bestRoot string
	for _, m := range mods {
		if m.OIDRoot == "" {
			continue
		}
		if oidHasPrefix(oid, m.OIDRoot) && len(m.OIDRoot) > len(bestRoot) {
			bestRoot = m.OIDRoot
			u.MatchedModuleRoot = m.Name
		}
	}

	if steps := iana.ResolveCanonical(oid); len(steps) > 0 {
		u.CanonicalName = steps[len(steps)-1].Name
	}

	if pen, ok := enterprisePEN(oid); ok {
		u.EnterpriseID = pen
		if org, ok := iana.LookupPEN(pen); ok {
			u.EnterpriseName = org
		}
	}
	return u
}

func oidHasPrefix(oid, prefix string) bool {
	return oid == prefix || strings.HasPrefix(oid, prefix+".")
}

// enterprisesArcs is the arc count of the enterprises node
// (1.3.6.1.4.1). The loaded SMI modules (RFC1155-SMI, SNMPv2-SMI)
// define enterprises/private/internet/mgmt/mib-2/… as real symbols, so
// a naive shrinking-prefix search "resolves" any OID whose own MIB is
// not loaded to one of these scaffolding nodes — defeating the
// unresolved-OID guidance (PEN hint / canonical arc / module-root
// match).
const enterprisesArcs = 6 // 1 . 3 . 6 . 1 . 4 . 1

// structuralArcMax is the deepest arc count treated as SMI scaffolding
// outside enterprises. Standard walkable objects live at ≥8 arcs
// (1.3.6.1.2.1.<group>.<object> and deeper); the structural arcs —
// internet (4), mgmt/private (5), mib-2/enterprises (6), and group
// anchors like system/interfaces (7) — all sit at ≤7.
const structuralArcMax = 7

// isStructuralMatch reports whether a shrinking-prefix symbol match
// landed on SMI scaffolding rather than the OID's owning subtree. Two
// rules:
//
//   - Under enterprises, any match at or above the enterprises node is
//     scaffolding (the vendor's own MIB isn't loaded) — the PEN hint is
//     the useful answer.
//   - Anywhere, a match at ≤7 arcs that the walked OID extends by two
//     or more arcs is scaffolding (e.g. an OID under an unloaded
//     standard MIB matching `mib-2`). The ≥2 guard keeps legitimate
//     shallow scalars resolvable: a 7-arc object's `.0` instance
//     extends it by exactly one arc.
func isStructuralMatch(symOID, oid string) bool {
	symArcs := strings.Count(symOID, ".") + 1
	if _, ok := enterprisePEN(oid); ok && symArcs <= enterprisesArcs {
		return true
	}
	if symArcs > structuralArcMax {
		return false
	}
	oidArcs := strings.Count(oid, ".") + 1
	return oidArcs-symArcs >= 2
}

// enterprisePEN extracts the PEN segment from an OID under
// .1.3.6.1.4.1.{PEN}.*
func enterprisePEN(oid string) (uint32, bool) {
	const ent = "1.3.6.1.4.1."
	if !strings.HasPrefix(oid, ent) {
		return 0, false
	}
	seg := oid[len(ent):]
	if i := strings.IndexByte(seg, '.'); i >= 0 {
		seg = seg[:i]
	}
	n, err := strconv.ParseUint(seg, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

func isIntegerSyntax(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "INTEGER") || strings.HasPrefix(s, "Integer32")
}

func isSingleInteger(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}
