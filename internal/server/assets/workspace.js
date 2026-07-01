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
// The OID tree is the client-side tree.js island; it syncs its own
// selection/expansion (see syncSelection in tree.js).
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
			this.$watch('kindFilter', (v) => {
				saveKindFilter(v);
				this.applyFilter();
				// The OID tree is a separate vanilla island (tree.js); tell
				// it to re-filter its container map to the chosen family.
				window.dispatchEvent(new CustomEvent('blittermib:kindfilter', { detail: v }));
			});
			this.$watch('filter', () => this.applyFilter());
			// Apply the persisted kind filter to the server-rendered
			// rows before the selection reveal measures geometry.
			this.applyFilter();
			// Scroll the server-marked selected row into view. On
			// long modules the highlighted row often lands below
			// the fold and the user has to hunt for it; this makes
			// the selection self-revealing on page load.
			requestAnimationFrame(() => {
				var row = document.querySelector('.list-row.selected');
				if (row) {
					row.scrollIntoView({ block: 'center', behavior: 'auto' });
				}
			});
		},

		// applyFilter hides non-matching list rows in one batched pass
		// by toggling the `hidden` attribute. An earlier version put
		// x-show="matchesRow($el)" on every row, which created one
		// Alpine reactive effect per row — 7,000+ effects on large
		// modules, re-evaluated on every keystroke and rebuilt on every
		// list swap. One plain loop over the rows is microseconds of
		// JS, and the rows' content-visibility containment keeps the
		// resulting relayout proportional to the visible viewport.
		applyFilter() {
			var rows = document.querySelectorAll('#workspace-list .list-row');
			var kindCount = 0;
			for (var i = 0; i < rows.length; i++) {
				if (this.matchesKind(rows[i])) kindCount++;
				rows[i].hidden = !this.matchesRow(rows[i]);
			}
			this.updateScopeCount(kindCount);
		},

		// updateScopeCount makes the list's "N objects" scope count reflect
		// the active KIND chip, so it agrees with the family-filtered tree
		// (e.g. a notif-scoped trap group reads "12 objects", not the 14
		// that also counts the structural object-identity nodes). The text
		// grep is a transient refinement and does NOT change the count. On
		// the 'all' chip kindCount is every row — the server's original
		// value. No-op when unscoped (the count span isn't rendered).
		updateScopeCount(n) {
			var el = document.querySelector('#workspace-list .list-scope-count');
			if (el) el.textContent = n + (n === 1 ? ' object' : ' objects');
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

// Case-B partial navigations replace the list pane via an out-of-band
// swap; the fresh rows arrive unfiltered and need the active filter
// re-applied. Registered once at module scope — NOT inside the Alpine
// component's init() — so a re-initialized component (however the
// document gets there) can never stack a second listener pinning a
// dead scope; the live component is resolved at event time instead.
document.body.addEventListener('htmx:oobAfterSwap', function (evt) {
	if (
		!evt.detail ||
		!evt.detail.target ||
		evt.detail.target.id !== 'workspace-list'
	) {
		return;
	}
	var grid = document.querySelector('.workspace-grid');
	if (!grid || !window.Alpine || typeof Alpine.$data !== 'function') return;
	var ws = Alpine.$data(grid);
	if (ws && typeof ws.applyFilter === 'function') ws.applyFilter();
});

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

// --- delegated workspace interactions --------------------------------
//
// Copy buttons and in-workspace navigation used to carry per-element
// Alpine and htmx attributes (x-data/x-on:click on every copy button,
// hx-get/hx-trigger on every row and link). On a 7,000-symbol module
// that repeated attribute text added megabytes of HTML, so the
// behavior lives here once, document-delegated — which also survives
// htmx pane swaps without rebinding.
//
// Navigation elements opt in via markup:
//   - `data-nav` on an <a>: plain click issues a partial-navigation
//     GET through htmx; modifier-clicks fall through to the browser's
//     native link handling (new tab / new window).
//   - `data-href` on a tr.list-row: the whole row is a click target
//     for the same navigation; clicks on inner links/buttons belong
//     to those elements.
//
// htmx.ajax gets the clicked element as `source`, so the request
// inherits the workspace grid's hx-target / hx-swap / hx-push-url —
// the same inheritance the per-element hx-get attributes used.
(function () {
	'use strict';

	document.addEventListener('click', function (e) {
		var btn = e.target.closest('.copy-btn[data-clipboard-text]');
		if (btn) {
			if (navigator.clipboard) {
				navigator.clipboard.writeText(btn.dataset.clipboardText).then(
					function () {
						btn.classList.add('copied');
						// Re-clicks within the flash window restart it
						// instead of letting the first timer strip the
						// class mid-flash.
						clearTimeout(btn._copiedTimer);
						btn._copiedTimer = setTimeout(function () {
							btn.classList.remove('copied');
						}, 1500);
					},
					function () {
						// Write denied (unfocused document, permissions
						// policy) — no false "copied" flash, no unhandled
						// rejection.
					}
				);
			}
			return;
		}

		if (!e.target.closest('.workspace-grid')) return;

		// If htmx never loaded (asset failure, CSP), keep the browser's
		// native href navigation instead of preventDefault-ing links
		// into dead clicks.
		var hasHtmx = typeof htmx !== 'undefined';

		var a = e.target.closest('a[data-nav]');
		if (a) {
			if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
			if (!hasHtmx) return;
			e.preventDefault();
			htmx.ajax('GET', a.getAttribute('href'), { source: a });
			return;
		}

		var row = e.target.closest('tr.list-row[data-href]');
		if (!row || e.target.closest('a, button')) return;
		if (e.metaKey || e.ctrlKey) {
			window.open(row.dataset.href, '_blank');
			return;
		}
		if (e.shiftKey) {
			window.open(row.dataset.href);
			return;
		}
		if (e.altKey || !hasHtmx) return;
		htmx.ajax('GET', row.dataset.href, { source: row });
	});
})();

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
		// Reveal on the next frame: the workspace component's
		// htmx:oobAfterSwap hook has already re-applied the filter
		// synchronously during swap processing, so the deferred
		// measurement sees the final row visibility.
		requestAnimationFrame(function () {
			revealListSelection(listRow);
		});
		// The tree pane (the tree.js island) syncs its own selection on
		// htmx:afterSwap — see syncSelection in tree.js.
	});
})();
