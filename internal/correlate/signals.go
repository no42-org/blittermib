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

// firstShared returns the lexically-first key present in both sets, or
// "" if they share none. Deterministic by construction; used for both
// the varbind-signature and group-membership signals.
func firstShared(a, b map[string]bool) string {
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

// clearPhrases / raisePhrases are the DESCRIPTION-prose vocabulary,
// including the protocol-state idioms that carry direction when the
// name does not (e.g. BGP "enters the established state" /
// "lower numbered state"). Clear phrases are matched FIRST so a
// recovery worded around the fault state ("left the down state") is not
// misread as a raise.
var clearPhrases = []string{
	"left the down state", "out of the down state", "comes up", "has come up",
	"back in service", "returned to service", "returned to normal",
	"is restored", "restored", "cleared", "no longer", "recovered",
	"enters the established state", "established state", "established",
	"normal operation", "transitioned into some other state",
}

var raisePhrases = []string{
	"about to enter the down state", "enter the down state", "down state",
	"has failed", "failure", "failed", "loss of", "is lost", "lost",
	"lower numbered state", "higher numbered state to a lower", "backward",
	"unreachable", "not responding", "abnormal", "degraded",
}

// descriptionDirection scans a notification's DESCRIPTION (and
// REFERENCE) prose for directional intent, returning the direction and
// the phrase that matched (for evidence). It is a corroborating signal:
// callers require an independent grouping signal before pairing on it.
func descriptionDirection(description, reference string) (direction, string) {
	text := strings.ToLower(description + " " + reference)
	for _, p := range clearPhrases {
		if strings.Contains(text, p) {
			return dirClear, p
		}
	}
	for _, p := range raisePhrases {
		if strings.Contains(text, p) {
			return dirRaise, p
		}
	}
	return dirNone, ""
}

// groupSets maps each notification name to the set of group keys
// ("module::group") it belongs to, from NOTIFICATION-GROUP /
// OBJECT-GROUP membership references (source = group, target = member).
func groupSets(refs []model.Reference) map[string]map[string]bool {
	out := make(map[string]map[string]bool)
	for _, r := range refs {
		if r.Kind != model.RefGroupMember {
			continue
		}
		set := out[r.TargetName]
		if set == nil {
			set = make(map[string]bool)
			out[r.TargetName] = set
		}
		set[r.SourceModule+"::"+r.SourceName] = true
	}
	return out
}

// legacyStatus reports whether a STATUS marks a notification as
// deprecated or obsolete — definitions kept for backward compatibility
// rather than current use.
func legacyStatus(s model.Status) bool {
	return s == model.StatusDeprecated || s == model.StatusObsolete
}

// statusCompatible reports whether two notifications may be paired given
// their STATUS. A `current` notification is never paired with a
// `deprecated`/`obsolete` near-duplicate (which would, e.g., cross-pair
// the legacy and current BGP transition/established notifications that
// share identical varbinds): pairing is allowed only when both members
// are legacy or both are active.
func statusCompatible(a, b model.Status) bool {
	return legacyStatus(a) == legacyStatus(b)
}

// shortVarbind strips the module prefix from a "module::name" key for
// human-readable evidence ("IF-MIB::ifIndex" -> "ifIndex").
func shortVarbind(key string) string {
	if i := strings.LastIndex(key, "::"); i >= 0 {
		return key[i+2:]
	}
	return key
}
