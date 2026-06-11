// walk-overlay.js — client-only walk decoration for the workspace.
//
// Two jobs, both keyed off a single sessionStorage entry
// (`blittermib-walk`, a JSON `{oids:{instanceOID:value}}` map).
// sessionStorage (not localStorage) on purpose: the walk is ephemeral,
// and localStorage is shared with htmx's `htmx-history-cache` (full
// page-HTML snapshots) — a multi-MB walk competing with that cache hit
// the per-origin quota and failed to persist, leaving the workspace
// with nothing to decorate. sessionStorage has its own quota.
//
//   1. Writer: on the /walk results page a hidden
//      `#blittermib-walk-data` element carries the decoded walk as a
//      data attribute. We persist it to sessionStorage so the workspace
//      can read it without another server round-trip.
//   2. Reader: on a module workspace page (`#workspace-list` present),
//      decorate each list row whose OID appears in the walk with a
//      value badge, expose an "in walk" filter chip, and show a
//      status-bar indicator with a clear button.
//
// It also wires the client-side sort of the "MIBs in this walk" list on
// the results page (by name or by value count) — no server round-trip,
// reordering the rows already in the DOM.
//
// The server stays walk-unaware on the workspace surface (design
// Decision 5). The page renders identically without a walk; all of
// this is additive and purely client-side. Filtering is done with a
// CSS class that ANDs with Alpine's x-show inline display, so it does
// not fight the existing kind-chip / text filters in workspace.js.
(function () {
	'use strict';

	var KEY = 'blittermib-walk';
	var FILTER_KEY = 'blittermib-walk-filter';

	// store wraps sessionStorage in try/catch: every call site must
	// tolerate storage being disabled (some private-browsing modes) or
	// the quota being exceeded. get returns null on failure, set
	// returns false instead of throwing, del swallows errors.
	var store = {
		get: function (k) {
			try { return sessionStorage.getItem(k); } catch (e) { return null; }
		},
		set: function (k, v) {
			try { sessionStorage.setItem(k, v); return true; } catch (e) { return false; }
		},
		del: function (k) {
			try { sessionStorage.removeItem(k); } catch (e) { /* ignore */ }
		},
	};

	// walkFilterActive is the persistent state of the "in walk" filter.
	// The closure variable survives htmx partial swaps (the list pane is
	// replaced on scope changes, which would otherwise reset the filter
	// and drop the `#in-walk` hash); sessionStorage survives full reloads
	// of the pushed URL. Re-applied after every swap so the filter holds
	// across in-workspace navigation.
	var walkFilterActive = false;

	// indexCache memoizes the parsed walk + its prefix index per raw
	// payload, so the O(keys × OID-depth) index build runs once per
	// stored walk instead of on every htmx settle.
	var indexCache = { raw: null, walk: null };

	function loadWalk() {
		var raw = store.get(KEY);
		if (!raw) return null;
		if (indexCache.raw === raw) return indexCache.walk;
		var obj;
		try {
			obj = JSON.parse(raw);
		} catch (e) {
			return null;
		}
		if (!obj || typeof obj.oids !== 'object' || obj.oids === null) return null;
		obj.index = buildWalkIndex(obj.oids);
		indexCache.raw = raw;
		indexCache.walk = obj;
		return obj;
	}

	// buildWalkIndex maps every dotted prefix of every instance OID to
	// {count, keys}: count is the number of instances at or under that
	// prefix (the badge figure — never truncated), keys a ≤8-instance
	// sample for the tooltip. Rows then match in O(1) instead of
	// scanning the full key set per row — with a 40k-OID walk over a
	// 2k-row module the old scan was ~10^8 prefix compares per settle.
	function buildWalkIndex(oids) {
		var index = Object.create(null);
		for (var k in oids) {
			var dot = -1;
			for (;;) {
				dot = k.indexOf('.', dot + 1);
				var prefix = dot === -1 ? k : k.slice(0, dot);
				var b = index[prefix] || (index[prefix] = { count: 0, keys: [] });
				b.count++;
				if (b.keys.length < 8) b.keys.push(k);
				if (dot === -1) break;
			}
		}
		return index;
	}

	// persistFromResultsPage runs on the /walk results page only. The
	// workspace overlay reads the walk from sessionStorage, so this is
	// the only bridge from the results page to the per-module value
	// badges.
	function persistFromResultsPage() {
		var el = document.getElementById('blittermib-walk-data');
		if (!el) return;
		var raw = el.getAttribute('data-walk');
		if (!raw) return;
		try {
			JSON.parse(raw); // validate
		} catch (e) {
			return; // malformed payload — leave any prior walk untouched
		}
		if (!store.set(KEY, raw)) {
			// Storage full (large walks can exceed the per-origin quota)
			// or disabled. Drop any stale walk so the workspace doesn't
			// decorate the wrong data, and surface why — otherwise the
			// values/chip silently never appear in the workspace.
			store.del(KEY);
			warnPersistFailed();
		}
	}

	// warnPersistFailed shows, on the results page, why the walk won't
	// decorate the workspace, with the actionable next step. setItem can
	// fail for two reasons we can't tell apart: the quota (large walks)
	// or storage being disabled entirely (e.g. some private-browsing
	// modes) — the message covers both rather than misdiagnosing one as
	// the other.
	function warnPersistFailed() {
		try {
			console.warn(
				'blittermib: the decoded walk could not be kept in browser ' +
				'storage (quota exceeded, or storage unavailable), so its values ' +
				'cannot be shown in the module workspace.'
			);
		} catch (e) {
			/* ignore */
		}
		var head = document.querySelector('.walk-results-head');
		if (!head || head.querySelector('.walk-store-warning')) return;
		var note = document.createElement('p');
		note.className = 'walk-note walk-store-warning';
		note.textContent =
			'Your browser could not keep this walk in storage (it may exceed ' +
			'the storage quota, or storage may be unavailable), so decoded ' +
			'values will not appear in the module workspace. For large walks, ' +
			'filter to a smaller subtree (e.g. one OID branch) and decode again.';
		head.appendChild(note);
	}

	// addValueBadge renders the single value (one instance) or the full
	// instance count — the visible figure is never truncated. The
	// tooltip samples up to 8 instances, each labelled by its suffix
	// relative to the row (".1: 4444"), with an ellipsis when capped.
	function addValueBadge(row, bucket, rowOID, oids) {
		if (row.querySelector('.walk-val')) return; // already decorated
		var cell = row.querySelector('.list-cell-oid');
		if (!cell) return;
		var span = document.createElement('span');
		span.className = 'walk-val';
		span.textContent = bucket.count === 1
			? oids[bucket.keys[0]]
			: bucket.count + ' values';
		var parts = bucket.keys.map(function (k) {
			var sfx = k === rowOID ? '' : k.slice(rowOID.length);
			return (sfx ? sfx + ': ' : '') + oids[k];
		});
		span.title = parts.join('  ·  ') +
			(bucket.count > bucket.keys.length ? '  ·  …' : '');
		cell.appendChild(span);
	}

	// decorate marks matching rows, badges them, and returns the match
	// count. A row matches when its OID is an instance OID or any
	// ancestor prefix of one — an O(1) index lookup. Idempotent — safe
	// to re-run after an htmx list swap.
	function decorate(walk, list) {
		var index = walk.index;
		var rows = list.querySelectorAll('.list-row');
		var matched = 0;
		rows.forEach(function (row) {
			var rowOID = row.getAttribute('data-oid') || '';
			var bucket = rowOID ? index[rowOID] : null;
			if (!bucket) {
				row.removeAttribute('data-in-walk');
				return;
			}
			matched++;
			row.setAttribute('data-in-walk', 'true');
			addValueBadge(row, bucket, rowOID, walk.oids);
		});
		return matched;
	}

	function rowCount(list) {
		return list.querySelectorAll('.list-row').length;
	}

	// injectChip adds an "in walk" toggle alongside the kind chips. It is
	// plain JS (no Alpine bindings), so Alpine ignores it. Clicking it
	// flips the persistent walkFilterActive state; the `walk-only` class
	// on the list pane is what CSS uses to hide non-walk rows. The chip is
	// re-injected after each list swap (the swap replaces the chip row),
	// so its state is reflected from walkFilterActive, not the DOM.
	function injectChip(list, matched) {
		var chips = document.querySelector('.kind-chips');
		if (!chips || chips.querySelector('.kind-walk')) return;
		var chip = document.createElement('button');
		chip.type = 'button';
		chip.className = 'kind-chip kind-walk';
		chip.setAttribute('role', 'tab');
		chip.setAttribute('data-walk-active', 'false');
		chip.title = 'Show only the OIDs present in the loaded walk';
		chip.textContent = 'in walk (' + matched + ')';
		chip.addEventListener('click', function () {
			walkFilterActive = !walkFilterActive;
			persistFilter(walkFilterActive);
			applyFilterState(list);
		});
		chips.appendChild(chip);
	}

	// updateIndicator creates the status-bar indicator on first need and
	// refreshes its text on every pass. The status bar is page chrome
	// that survives htmx scope swaps, so a write-once count would go
	// stale as the list narrows — including down to "0 of N" when a
	// scoped subtree has no walk data. Only the very first paint with
	// zero matches leaves the page untouched (no-walk pages render
	// identically, per the design).
	function updateIndicator(matched, total) {
		var bar = document.querySelector('.status-bar');
		if (!bar) return;
		var wrap = bar.querySelector('.walk-indicator');
		if (!wrap) {
			if (matched === 0) return; // walk touches nothing here — stay invisible
			wrap = document.createElement('span');
			wrap.className = 'walk-indicator';

			// Plain informational text — not a link. Navigating back to
			// the upload page from a status-bar count made no sense.
			var label = document.createElement('span');
			label.className = 'walk-indicator-label';
			wrap.appendChild(label);

			var clear = document.createElement('button');
			clear.type = 'button';
			clear.className = 'walk-clear';
			clear.textContent = 'clear';
			clear.title = 'Forget the loaded walk';
			clear.addEventListener('click', function () {
				store.del(KEY);
				store.del(FILTER_KEY);
				location.reload();
			});
			wrap.appendChild(clear);

			bar.appendChild(wrap);
		}
		wrap.querySelector('.walk-indicator-label').textContent =
			matched + ' of ' + total + ' in walk';
	}

	function persistFilter(on) {
		// Storage unavailable is fine — the closure variable still works.
		if (on) store.set(FILTER_KEY, '1');
		else store.del(FILTER_KEY);
	}

	// initFilterState seeds walkFilterActive once per page load: ON if the
	// page was reached via a `#in-walk` launcher link, or if a prior
	// toggle left it on (sessionStorage). Arriving via the link also
	// persists the flag, so it survives the first scope-changing
	// navigation — which drops the hash from the pushed URL.
	function initFilterState() {
		var stored = store.get(FILTER_KEY) === '1';
		var fromHash = location.hash === '#in-walk';
		walkFilterActive = fromHash || stored;
		if (fromHash) {
			persistFilter(true);
			// Consume the hash one-shot. Left in the URL it would
			// re-assert the filter on every reload, overriding a user who
			// explicitly toggled the chip off; the persisted flag is the
			// durable state from here on.
			try {
				history.replaceState(null, '', location.pathname + location.search);
			} catch (e) {
				/* ignore */
			}
		}
	}

	// applyFilterState reflects walkFilterActive onto the list pane and the
	// chip. Called on first paint and after every list swap, so the filter
	// is preserved across in-workspace navigation instead of resetting.
	function applyFilterState(list) {
		list.classList.toggle('walk-only', walkFilterActive);
		var chip = document.querySelector('.kind-walk');
		if (chip) chip.setAttribute('data-walk-active', walkFilterActive ? 'true' : 'false');
	}

	function applyWorkspace() {
		var list = document.getElementById('workspace-list');
		if (!list) return; // not a workspace page
		var walk = loadWalk();
		if (!walk) return; // no walk loaded — page stays identical
		var matched = decorate(walk, list);
		updateIndicator(matched, rowCount(list));
		if (matched === 0) return; // nothing to filter in this view — no chip
		injectChip(list, matched);
		applyFilterState(list);
	}

	// sortModuleRows reorders the "MIBs in this walk" rows in place by
	// name (localeCompare) or value count (numeric). dir is +1 asc / -1
	// desc; value ties break by name ascending for a stable order.
	function sortModuleRows(list, key, dir) {
		var rows = Array.prototype.slice.call(list.querySelectorAll('.walk-module-row'));
		rows.sort(function (a, b) {
			var an = a.getAttribute('data-module') || '';
			var bn = b.getAttribute('data-module') || '';
			if (key === 'values') {
				var av = parseInt(a.getAttribute('data-values'), 10) || 0;
				var bv = parseInt(b.getAttribute('data-values'), 10) || 0;
				if (av !== bv) return (av - bv) * dir;
				return an.localeCompare(bn);
			}
			return an.localeCompare(bn) * dir;
		});
		rows.forEach(function (r) { list.appendChild(r); });
	}

	// applyResultsSort wires the results-page sort controls. No-op on any
	// page without them (workspace, etc.).
	function applyResultsSort() {
		var controls = document.querySelector('[data-walk-sort-controls]');
		var list = document.querySelector('[data-walk-sortable]');
		if (!controls || !list || controls.dataset.walkBound) return;
		controls.dataset.walkBound = '1';

		var key = null;
		var dir = 1;
		var btns = controls.querySelectorAll('[data-walk-sort]');
		btns.forEach(function (btn) {
			btn.addEventListener('click', function () {
				var k = btn.getAttribute('data-walk-sort');
				if (k === key) {
					dir = -dir; // re-click toggles direction
				} else {
					key = k;
					dir = k === 'values' ? -1 : 1; // values default high→low, name A→Z
				}
				sortModuleRows(list, key, dir);
				btns.forEach(function (b) {
					b.removeAttribute('data-active');
					b.removeAttribute('data-dir');
				});
				btn.setAttribute('data-active', 'true');
				btn.setAttribute('data-dir', dir > 0 ? 'asc' : 'desc');
			});
		});
	}

	function init() {
		persistFromResultsPage();
		initFilterState();
		applyWorkspace();
		applyResultsSort();
	}

	// `defer` guarantees the DOM is parsed; run now, then re-decorate
	// after the workspace's partial (htmx) navigation. A scope change
	// replaces #workspace-list as an out-of-band swap that fires twice in
	// quick succession (htmx:oobAfterSwap) — reacting to that mid-swap
	// raced against a half-replaced DOM. Instead re-run on
	// htmx:afterSettle, which fires once the swap (main + OOB) has settled,
	// and re-query the live list inside applyWorkspace. Without this the
	// filter, badges, and chip vanish on a column click with no way back.
	init();
	document.body.addEventListener('htmx:afterSettle', function () {
		if (document.getElementById('workspace-list')) applyWorkspace();
	});
})();
