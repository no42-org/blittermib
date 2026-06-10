// walk-overlay.js — client-only walk decoration for the workspace.
//
// Two jobs, both keyed off a single localStorage entry
// (`blittermib-walk`, a JSON `{oids:{instanceOID:value}}` map):
//
//   1. Writer: on the /walk results page a hidden
//      `#blittermib-walk-data` element carries the decoded walk as a
//      data attribute. We persist it to localStorage so the workspace
//      can read it without another server round-trip.
//   2. Reader: on a module workspace page (`#workspace-list` present),
//      decorate each list row whose OID appears in the walk with a
//      value badge, expose an "in walk" filter chip, and show a
//      status-bar indicator with a clear button.
//
// The server stays walk-unaware on the workspace surface (design
// Decision 5). The page renders identically without a walk; all of
// this is additive and purely client-side. Filtering is done with a
// CSS class that ANDs with Alpine's x-show inline display, so it does
// not fight the existing kind-chip / text filters in workspace.js.
(function () {
	'use strict';

	var KEY = 'blittermib-walk';

	function loadWalk() {
		try {
			var raw = localStorage.getItem(KEY);
			if (!raw) return null;
			var obj = JSON.parse(raw);
			if (!obj || typeof obj.oids !== 'object' || obj.oids === null) return null;
			return obj;
		} catch (e) {
			return null;
		}
	}

	// persistFromResultsPage runs on the /walk results page only.
	function persistFromResultsPage() {
		var el = document.getElementById('blittermib-walk-data');
		if (!el) return;
		var raw = el.getAttribute('data-walk');
		if (!raw) return;
		try {
			JSON.parse(raw); // validate before storing
			localStorage.setItem(KEY, raw);
		} catch (e) {
			/* malformed payload — leave any prior walk untouched */
		}
	}

	// valuesUnder collects the walk values whose OID is the row's OID
	// or an instance/column beneath it (rowOID + "."). Caps the list so
	// a column with thousands of instances doesn't build a huge title.
	function valuesUnder(oids, keys, rowOID) {
		var out = [];
		var prefix = rowOID + '.';
		for (var i = 0; i < keys.length; i++) {
			var k = keys[i];
			if (k === rowOID || k.lastIndexOf(prefix, 0) === 0) {
				out.push(oids[k]);
				if (out.length >= 8) break;
			}
		}
		return out;
	}

	function addValueBadge(row, vals) {
		if (row.querySelector('.walk-val')) return; // already decorated
		var cell = row.querySelector('.list-cell-oid');
		if (!cell) return;
		var span = document.createElement('span');
		span.className = 'walk-val';
		span.textContent = vals.length === 1 ? vals[0] : vals.length + ' values';
		span.title = vals.join('  ·  ');
		cell.appendChild(span);
	}

	// decorate marks matching rows, badges them, and returns the match
	// count. Idempotent — safe to re-run after an htmx list swap.
	function decorate(walk, list) {
		var oids = walk.oids;
		var keys = Object.keys(oids);
		if (!keys.length) return 0;
		var rows = list.querySelectorAll('.list-row');
		var matched = 0;
		rows.forEach(function (row) {
			var rowOID = row.getAttribute('data-oid') || '';
			if (!rowOID) return;
			var vals = valuesUnder(oids, keys, rowOID);
			if (!vals.length) {
				row.removeAttribute('data-in-walk');
				return;
			}
			matched++;
			row.setAttribute('data-in-walk', 'true');
			addValueBadge(row, vals);
		});
		return matched;
	}

	function rowCount(list) {
		return list.querySelectorAll('.list-row').length;
	}

	// injectChip adds an "in walk" toggle alongside the kind chips. It
	// is plain JS (no Alpine bindings), so Alpine ignores it; toggling
	// flips a `walk-only` class on the list pane that CSS uses to hide
	// non-walk rows.
	function injectChip(list, matched) {
		var chips = document.querySelector('.kind-chips');
		if (!chips || chips.querySelector('.kind-walk')) return;
		var chip = document.createElement('button');
		chip.type = 'button';
		chip.className = 'kind-chip kind-walk';
		chip.setAttribute('data-walk-active', 'false');
		chip.textContent = 'in walk (' + matched + ')';
		chip.addEventListener('click', function () {
			var on = list.classList.toggle('walk-only');
			chip.setAttribute('data-walk-active', on ? 'true' : 'false');
		});
		chips.appendChild(chip);
	}

	function injectIndicator(matched, total) {
		var bar = document.querySelector('.status-bar');
		if (!bar || bar.querySelector('.walk-indicator')) return;
		var wrap = document.createElement('span');
		wrap.className = 'walk-indicator';

		var label = document.createElement('a');
		label.href = '/walk';
		label.className = 'walk-indicator-label';
		label.textContent = matched + ' of ' + total + ' in walk';
		wrap.appendChild(label);

		var clear = document.createElement('button');
		clear.type = 'button';
		clear.className = 'walk-clear';
		clear.textContent = 'clear';
		clear.title = 'Forget the loaded walk';
		clear.addEventListener('click', function () {
			try {
				localStorage.removeItem(KEY);
			} catch (e) {
				/* ignore */
			}
			location.reload();
		});
		wrap.appendChild(clear);

		bar.appendChild(wrap);
	}

	function applyWorkspace() {
		var list = document.getElementById('workspace-list');
		if (!list) return; // not a workspace page
		var walk = loadWalk();
		if (!walk) return; // no walk loaded — page stays identical
		var matched = decorate(walk, list);
		if (matched === 0) return; // walk touches nothing in this module
		injectChip(list, matched);
		injectIndicator(matched, rowCount(list));
	}

	function init() {
		persistFromResultsPage();
		applyWorkspace();
	}

	// `defer` guarantees the DOM is parsed; run now, then re-decorate
	// after the workspace's partial (htmx) list-pane swaps, which
	// replace the rows we annotated.
	init();
	document.body.addEventListener('htmx:afterSwap', function (evt) {
		var t = evt.detail && evt.detail.target;
		if (!t) return;
		if (t.id === 'workspace-list' || (t.querySelector && t.querySelector('#workspace-list'))) {
			applyWorkspace();
		}
	});
})();
