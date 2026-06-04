// workspace.js — Alpine x-data factory for the 3-pane workspace.
//
// Loaded via <script src="/static/workspace.js" defer> from the
// Base template. The factory must be installed on `window` BEFORE
// alpine.min.js runs `Alpine.start()`; with `defer` ordering the
// browser executes us first because we appear earlier in <head>.
//
// State held here is the *interactive* layer:
//   - filter:     transient text-search query for the list pane
//   - kindFilter: which kind-chip is active (all / scalar / table
//                 / notif). Persisted in sessionStorage so the
//                 chip stays selected as the user clicks through
//                 rows. In-workspace clicks now swap panes partially
//                 (the grid's Alpine scope survives — see the
//                 partial-navigation sync below), but full
//                 navigations (module switch, deep link, reload)
//                 still rebuild the shell and reset x-data.
//
// Selection / scope live in the URL (/m/{name}/{scope}?sel=…).
// Tree-expanded state is server-driven (auto-expand pass).
var KIND_FILTER_KEY = 'blittermib-kind-filter';
var KIND_FILTER_VALUES = { all: 1, scalar: 1, table: 1, notif: 1 };

function loadKindFilter() {
	try {
		var v = sessionStorage.getItem(KIND_FILTER_KEY);
		return v && KIND_FILTER_VALUES[v] ? v : 'all';
	} catch (e) {
		return 'all';
	}
}

function saveKindFilter(v) {
	try {
		sessionStorage.setItem(KIND_FILTER_KEY, v);
	} catch (e) {
		// SessionStorage can throw in private-mode Safari and quota
		// edge cases; the chip still works in-memory, persistence
		// just degrades to per-page.
	}
}

window.workspace = function () {
	return {
		filter: '',
		kindFilter: loadKindFilter(),

		init() {
			this.$watch('kindFilter', (v) => saveKindFilter(v));
			// Scroll the server-marked selected row into view. On
			// long modules the highlighted row often lands below
			// the fold and the user has to hunt for it; this makes
			// the selection self-revealing on page load. Runs on
			// the next frame so Alpine's x-show pass has finished
			// hiding rows that don't match the current filter —
			// scrolling before that lands at the wrong vertical
			// offset.
			requestAnimationFrame(() => {
				var row = document.querySelector('.list-row.selected');
				if (row) {
					row.scrollIntoView({ block: 'center', behavior: 'auto' });
				}
			});
		},

		// matchesKind reads `data-kind` from the row and answers
		// "is this row visible under the current kind chip?" Family
		// groupings mirror the handoff `helpers.js#typeFamily`
		// structural buckets: scalar+column under "scalar",
		// table+table-entry under "table", notification-type under
		// "notif". Other kinds (TC, group, compliance) appear only
		// under "all".
		matchesKind(el) {
			const k = el.dataset.kind || '';
			switch (this.kindFilter) {
				case 'all':
					return true;
				case 'scalar':
					return k === 'scalar' || k === 'column';
				case 'table':
					return k === 'table' || k === 'table-entry';
				case 'notif':
					return k === 'notification-type';
			}
			return true;
		},

		// matchesRow is the AND of the kind-chip filter and the
		// text-input filter. Server-side scope filtering already
		// narrowed the row set when the URL has a selection; this
		// is the additional client-side narrowing.
		matchesRow(el) {
			if (!this.matchesKind(el)) return false;
			const q = (this.filter || '').toLowerCase();
			if (!q) return true;
			const name = (el.dataset.name || '').toLowerCase();
			const oid = el.dataset.oid || '';
			return name.includes(q) || oid.includes(q);
		},
	};
};

// Alpine 3's MutationObserver auto-initializes any x-data scopes
// inserted into the DOM, so HTMX `beforeend` swaps (the chevron's
// children-fragment fetch is the only htmx flow on this page after
// hx-boost was removed) light up without further help.
//
// An earlier version called `Alpine.initTree(document.body)` from
// htmx:afterSwap as a "defensive re-init" — but that re-evaluated
// the parent row's `x-data="{ expanded: false, ... }"` initializer
// after each fragment swap, resetting `expanded` to false and
// hiding the just-appended children. Removed.

// --- partial-navigation sync (workspace-partial-nav) ----------------
//
// In-workspace clicks swap only the detail pane (plus, on scope
// changes, the list pane out-of-band) — the A/B contract in the
// change's design doc. The tree pane is intentionally NEVER swapped,
// so its DOM and scroll position persist; selection highlight,
// expansion along the new OID path, and the selected-row reveal are
// synchronized here after each swap. (The grid's own Alpine scope is
// never swapped either, so `filter` / `kindFilter` survive without
// help.)
(function () {
	'use strict';

	// navTarget derives {scope, sel} from the swapped request's URL.
	// Prefer htmx's pathInfo over `location` — the history push may
	// not have landed when afterSwap fires.
	function navTarget(evt) {
		var p = evt.detail && evt.detail.pathInfo;
		var raw =
			(p && (p.finalRequestPath || p.requestPath)) ||
			location.pathname + location.search;
		var u;
		try {
			u = new URL(raw, location.origin);
		} catch (e) {
			return null;
		}
		if (u.pathname.indexOf('/m/') !== 0) return null;
		var rest = u.pathname.slice(3);
		var i = rest.indexOf('/');
		return {
			scope: i >= 0 ? decodeURIComponent(rest.slice(i + 1)) : '',
			sel: u.searchParams.get('sel') || '',
		};
	}

	// moveSelected clears the pane's `.selected` rows and marks the row
	// whose data attribute matches. Returns the marked row (or null).
	function moveSelected(paneSel, attr, value) {
		document
			.querySelectorAll(paneSel + ' .selected')
			.forEach(function (el) {
				el.classList.remove('selected');
			});
		if (!value) return null;
		var row = document.querySelector(
			paneSel + ' [' + attr + '="' + CSS.escape(value) + '"]'
		);
		if (row) row.classList.add('selected');
		return row;
	}

	function treeNode(oid) {
		return document.querySelector(
			'#workspace-tree li.tree-row[data-oid="' + CSS.escape(oid) + '"]'
		);
	}

	function isExpanded(node) {
		try {
			if (window.Alpine && typeof Alpine.$data === 'function') {
				var st = Alpine.$data(node);
				if (st && typeof st.expanded === 'boolean') return st.expanded;
			}
		} catch (e) {
			/* fall through to the DOM attribute */
		}
		var btn = node.querySelector(':scope > .tree-row-head .tree-chevron');
		return !!btn && btn.getAttribute('aria-expanded') === 'true';
	}

	function waitFor(fn, timeoutMs) {
		return new Promise(function (resolve) {
			var t0 = Date.now();
			(function poll() {
				var v = fn();
				if (v) return resolve(v);
				if (Date.now() - t0 > timeoutMs) return resolve(null);
				setTimeout(poll, 50);
			})();
		});
	}

	// expandTreeTo expands the workspace tree along `oid`'s prefix path
	// by driving each collapsed container's own chevron handler — the
	// inline Alpine expression owns the fragment fetch and state, so
	// programmatic clicks reuse it exactly (no duplicated fetch logic,
	// no ancestor re-initialization). Prefixes above the rendered root
	// are skipped; if a fetched level doesn't surface the next node
	// within the timeout the walk stops — the tree simply doesn't
	// highlight deeper than it can show.
	async function expandTreeTo(oid) {
		if (!oid) return;
		var parts = oid.split('.');
		for (var depth = 1; depth < parts.length; depth++) {
			var prefix = parts.slice(0, depth).join('.');
			var node = treeNode(prefix);
			if (!node) continue; // above the rendered root slice
			if (!node.querySelector(':scope > .tree-children-container')) {
				continue; // leaf row — nothing to expand
			}
			if (!isExpanded(node)) {
				var btn = node.querySelector(
					':scope > .tree-row-head .tree-chevron'
				);
				if (!btn) continue;
				btn.click();
			}
			var nextPrefix = parts.slice(0, depth + 1).join('.');
			var next = await waitFor(function () {
				return treeNode(nextPrefix);
			}, 1500);
			if (!next) return;
		}
	}

	// revealListSelection scrolls the selected list row into view only
	// when it sits outside the list pane's scrollport — a case-A click
	// happened on a visible row and must not yank the scroll position.
	function revealListSelection(row) {
		if (!row) return;
		var pane = document.getElementById('workspace-list');
		if (!pane) return;
		var pr = pane.getBoundingClientRect();
		var rr = row.getBoundingClientRect();
		if (rr.top < pr.top || rr.bottom > pr.bottom) {
			row.scrollIntoView({ block: 'center', behavior: 'auto' });
		}
	}

	// navGen guards against overlapping syncs: two rapid navigations
	// each start an async expandTreeTo walk, and the slower (stale)
	// walk's trailing highlight must not overwrite the newer one.
	var navGen = 0;

	document.body.addEventListener('htmx:afterSwap', function (evt) {
		if (
			!evt.detail ||
			!evt.detail.target ||
			evt.detail.target.id !== 'workspace-detail'
		) {
			return;
		}
		var nav = navTarget(evt);
		if (!nav) return;
		var gen = ++navGen;
		var sel = nav.sel;
		// ?sel= carries an OID or a name. SMI identifiers must start
		// with a letter (RFC 2578 §3.1), so a leading digit is a
		// reliable discriminator.
		var selIsOID = /^\d/.test(sel);
		// List pane: case B arrives server-marked (re-marking is a
		// no-op); case A needs the client-side move.
		var listRow = sel
			? moveSelected(
					'#workspace-list',
					selIsOID ? 'data-oid' : 'data-name',
					sel
			  )
			: moveSelected('#workspace-list', 'data-oid', nav.scope);
		// Reveal on the next frame so Alpine's x-show pass has hidden
		// filter-excluded rows first — mirrors the full-page init()
		// reveal; measuring earlier lands at the wrong offset.
		requestAnimationFrame(function () {
			revealListSelection(listRow);
		});
		// Tree pane: never swapped — highlight + expand along the path.
		var treeOID = selIsOID && sel ? sel : nav.scope;
		moveSelected('#workspace-tree', 'data-oid', treeOID);
		expandTreeTo(treeOID).then(function () {
			if (gen !== navGen) return; // superseded by a newer navigation
			moveSelected('#workspace-tree', 'data-oid', treeOID);
		});
	});
})();
