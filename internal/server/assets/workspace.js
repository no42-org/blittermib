// workspace.js — Alpine x-data factory for the 3-pane workspace.
//
// Loaded via <script src="/static/workspace.js" defer> from the
// Base template. The factory must be installed on `window` BEFORE
// alpine.min.js runs `Alpine.start()`; with `defer` ordering the
// browser executes us first because we appear earlier in <head>.
//
// State held here is the *interactive* layer:
//   - filter: string for the center-pane symbol-list filter input
//
// Selection state lives in the URL (/m/{name}/{oid}) — that's
// authoritative, deep-linkable, and survives reload without any
// JS. Tree-expanded state is also intentionally ephemeral; the
// HTMX tree-fragment endpoint streams children when the user
// expands a chevron and the in-page DOM holds them until the next
// navigation.
window.workspace = function () {
	return {
		filter: '',
		kindFilter: 'all',

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

// Alpine 3 already attaches a MutationObserver that initializes
// new x-data roots inserted via hx-boost body swaps; the explicit
// htmx:afterSwap hook below is a defensive belt-and-suspenders
// re-init in case Alpine's observer ever misses a swap (state on
// the previous workspace instance is intentionally lost on
// navigation; selection lives in the URL).
if (typeof document !== 'undefined') {
	document.body.addEventListener('htmx:afterSwap', function () {
		if (window.Alpine && typeof window.Alpine.initTree === 'function') {
			window.Alpine.initTree(document.body);
		}
	});
}
