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
//                 rows; the workspace shell is rebuilt on every
//                 navigation now that hx-boost is gone, and Alpine
//                 x-data otherwise resets to its default.
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
