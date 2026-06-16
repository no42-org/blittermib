/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

import (
	"sort"
	"strings"

	"github.com/no42-org/blittermib/internal/model"
)

// direction classifies a name token as raise-leaning (fault onset),
// clear-leaning (recovery), or neither.
type direction int

const (
	dirNone direction = iota
	dirRaise
	dirClear
)

// raiseTokens / clearTokens are the opposing directional vocabulary the
// name-token signal matches. A pair is a candidate when two
// notifications share the same root (the name with its directional
// token stripped) and carry opposing tokens. The raise-leaning member
// is the problem; the clear-leaning member resolves it.
var raiseTokens = map[string]string{
	"down": "up", "fail": "ok", "failed": "ok", "failure": "ok",
	"lost": "restored", "loss": "restored", "off": "on", "error": "normal",
	"inactive": "active", "disabled": "enabled", "alarm": "clear", "alert": "clear",
	"abnormal": "normal",
}

var clearTokens = map[string]bool{
	"up": true, "ok": true, "restored": true, "restore": true, "cleared": true,
	"clear": true, "normal": true, "on": true, "active": true, "enabled": true,
	"recovered": true, "recover": true,
}

// tokenize splits a symbol name into lowercase tokens on camelCase
// boundaries and the separators `-`, `_`, ` `, `.`. e.g. "linkDown" ->
// ["link","down"]; "bgpBackwardTrans" -> ["bgp","backward","trans"].
func tokenize(name string) []string {
	var toks []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			toks = append(toks, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	prevLower := false
	for _, r := range name {
		switch {
		case r == '-' || r == '_' || r == ' ' || r == '.':
			flush()
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower {
				flush()
			}
			cur = append(cur, r)
			prevLower = false
		default:
			cur = append(cur, r)
			prevLower = r >= 'a' && r <= 'z'
		}
	}
	flush()
	return toks
}

// splitDirection finds the directional token in a tokenized name and
// returns the remaining root (tokens rejoined), the direction, and the
// matched token. The LAST directional token wins (most specific). When
// no directional token is present, dir is dirNone.
func splitDirection(toks []string) (root string, dir direction, token string) {
	idx, d, tok := -1, dirNone, ""
	for i, t := range toks {
		switch {
		case raiseTokens[t] != "":
			idx, d, tok = i, dirRaise, t
		case clearTokens[t]:
			idx, d, tok = i, dirClear, t
		}
	}
	if idx < 0 {
		return strings.Join(toks, "-"), dirNone, ""
	}
	rest := make([]string, 0, len(toks)-1)
	rest = append(rest, toks[:idx]...)
	rest = append(rest, toks[idx+1:]...)
	return strings.Join(rest, "-"), d, tok
}

// varbindSets maps each notification's name to the set of its varbind
// keys ("module::name"), drawn from the notification-object references.
func varbindSets(refs []model.Reference) map[string]map[string]bool {
	out := make(map[string]map[string]bool)
	for _, r := range refs {
		if r.Kind != model.RefNotificationObject {
			continue
		}
		set := out[r.SourceName]
		if set == nil {
			set = make(map[string]bool)
			out[r.SourceName] = set
		}
		set[r.TargetModule+"::"+r.TargetName] = true
	}
	return out
}

// sharedVarbind returns the lexically-first varbind key present in both
// sets, or "" if they share none. Deterministic by construction.
func sharedVarbind(a, b map[string]bool) string {
	if len(a) == 0 || len(b) == 0 {
		return ""
	}
	var shared []string
	for k := range a {
		if b[k] {
			shared = append(shared, k)
		}
	}
	if len(shared) == 0 {
		return ""
	}
	sort.Strings(shared)
	return shared[0]
}

// shortVarbind strips the module prefix from a "module::name" key for
// human-readable evidence ("IF-MIB::ifIndex" -> "ifIndex").
func shortVarbind(key string) string {
	if i := strings.LastIndex(key, "::"); i >= 0 {
		return key[i+2:]
	}
	return key
}
