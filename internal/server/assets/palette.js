// blittermib command palette — vanilla JS island.
//
// Listens for ⌘K / Ctrl+K and / (when no input is focused), opens a
// search overlay backed by /api/v1/search?q=…, supports keyboard
// navigation, and routes to the matching /s/{Module}::{Name} on Enter.
//
// The site uses htmx for partial swaps (workspace pane, tree
// fragments); full pages are normal navigations that re-run this
// script. Init is split into attachOverlay/attachLanding (idempotent,
// re-run after swaps) and attachGlobals (document-level handlers,
// installed once per page load).

(function () {
	'use strict';

	const SEARCH_URL = '/api/v1/search';
	const DEBOUNCE_MS = 80;
	const MAX_RESULTS = 25;

	const TEMPLATE = `
<div class="palette-overlay" data-state="hidden" role="dialog" aria-modal="true" aria-labelledby="palette-input">
	<div class="palette" role="combobox" aria-expanded="false">
		<input
			type="text"
			class="palette-input"
			id="palette-input"
			placeholder="Search symbols, OIDs, modules…"
			autocomplete="off"
			spellcheck="false"
			aria-controls="palette-results"
		/>
		<ul class="palette-results" id="palette-results" role="listbox"></ul>
		<div class="palette-empty" data-state="hidden">No matches.</div>
	</div>
</div>`;

	let overlay, input, list, empty, modalCtl;
	// Focus trap: remember the element that had focus when the palette
	// opened so we can restore it on close. Without this, dismissing
	// the palette leaves keyboard focus orphaned in the DOM.
	let returnFocusTo = null;

	function escape(s) {
		const d = document.createElement('div');
		d.textContent = s == null ? '' : String(s);
		return d.innerHTML;
	}

	// searchController wires the typeahead model shared by both search
	// surfaces (modal palette, landing hero): debounced /api/v1/search
	// fetch with a stale-response guard, hit-list rendering, active-row
	// tracking, and click navigation. Surface-specific chrome and
	// Enter/Escape semantics stay in the callers, injected via opts:
	//   input      — the text input to bind the debounced fetch to
	//   list       — the container the hit <li>s render into
	//   itemClass  — li class ('palette-item' / 'hero-result')
	//   warnLabel  — console.warn prefix on fetch failure
	//   onRender   — called after each re-render with the controller,
	//                to show/hide the surface's empty/dropdown chrome
	//   beforeNavigate — optional, runs before location.href changes
	function searchController(opts) {
		let debounce;
		let lastSeq = 0;

		const ctl = {
			hits: [],
			active: -1,
		};

		function updateActive() {
			const items = opts.list.querySelectorAll('.' + opts.itemClass);
			items.forEach((el, i) => {
				const on = i === ctl.active;
				el.classList.toggle('active', on);
				el.setAttribute('aria-selected', on ? 'true' : 'false');
				if (on) el.scrollIntoView({ block: 'nearest' });
			});
		}

		function render() {
			if (ctl.hits.length === 0) {
				opts.list.innerHTML = '';
				ctl.active = -1;
				opts.onRender(ctl);
				return;
			}
			opts.list.innerHTML = ctl.hits
				.map(
					(h, i) => `
<li class="${opts.itemClass}" data-idx="${i}" role="option" aria-selected="${i === 0}">
	<span class="palette-name">${escape(h.Name)}</span>
	<span class="palette-oid">${escape(h.OID)}</span>
	<span class="palette-meta">${escape(h.Module)} · ${escape(h.Kind)}</span>
</li>`,
				)
				.join('');
			ctl.active = 0;
			opts.onRender(ctl);
			updateActive();
		}

		async function search(q) {
			const seq = ++lastSeq;
			if (!q.trim()) {
				ctl.hits = [];
				render();
				return;
			}
			try {
				const res = await fetch(SEARCH_URL + '?q=' + encodeURIComponent(q));
				if (!res.ok) throw new Error('search ' + res.status);
				const data = await res.json();
				if (seq !== lastSeq) return; // stale response, ignore
				ctl.hits = (data.hits || []).slice(0, MAX_RESULTS);
				render();
			} catch (err) {
				console.warn(opts.warnLabel, err);
				ctl.hits = [];
				render();
			}
		}

		ctl.move = function (delta) {
			if (ctl.hits.length === 0) return;
			ctl.active = (ctl.active + delta + ctl.hits.length) % ctl.hits.length;
			updateActive();
		};

		ctl.navigate = function (i) {
			const h = ctl.hits[i];
			if (!h) return;
			if (opts.beforeNavigate) opts.beforeNavigate();
			// Deliberate full navigation: search jumps can cross modules,
			// which is outside the workspace partial-navigation contract
			// (in-workspace pane swaps only). Hit rows go to the workspace
			// selection so the user lands in the navigation context that
			// owns the OID, matching what the /search results page does.
			window.location.href = '/m/' + encodeURIComponent(h.Module) + '/' + h.OID;
		};

		ctl.navigateActive = function () {
			if (ctl.active >= 0) ctl.navigate(ctl.active);
		};

		ctl.clear = function () {
			ctl.hits = [];
			render();
		};

		// reset empties the model without rendering empty-state chrome —
		// used when (re)opening a surface with a blank query.
		ctl.reset = function () {
			ctl.hits = [];
			ctl.active = -1;
			opts.list.innerHTML = '';
		};

		opts.input.addEventListener('input', () => {
			clearTimeout(debounce);
			const q = opts.input.value;
			debounce = setTimeout(() => search(q), DEBOUNCE_MS);
		});
		opts.list.addEventListener('click', (e) => {
			const li = e.target.closest('.' + opts.itemClass);
			if (!li) return;
			ctl.navigate(parseInt(li.dataset.idx, 10));
		});

		return ctl;
	}

	function show() {
		if (!overlay) attachOverlay();
		overlay.dataset.state = 'visible';
		input.value = '';
		modalCtl.reset();
		empty.dataset.state = 'hidden';
		// Save the previously-focused element so we can return focus
		// to it when the palette closes — better keyboard ergonomics
		// than dropping focus on document.body.
		const ae = document.activeElement;
		if (ae && ae !== document.body) returnFocusTo = ae;
		input.focus();
	}

	function hide() {
		if (!overlay) return;
		overlay.dataset.state = 'hidden';
		if (returnFocusTo && typeof returnFocusTo.focus === 'function') {
			try { returnFocusTo.focus(); } catch (_) { /* node removed */ }
		}
		returnFocusTo = null;
	}

	function isVisible() {
		return overlay && overlay.dataset.state === 'visible';
	}

	function isInputLike(el) {
		if (!el) return false;
		const tag = el.tagName;
		return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
	}

	// Landing-page search input. When present, ⌘K / `/` / the topbar
	// search button focus this field instead of opening the modal —
	// the hero search already is the search affordance on that page,
	// and the modal would just shadow it.
	function landingSearch() {
		return document.querySelector('.hero-search-input');
	}

	function focusLanding(el) {
		try {
			el.focus();
			if (typeof el.select === 'function') el.select();
		} catch (_) { /* node detached */ }
	}

	function onKey(e) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			modalCtl.move(1);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			modalCtl.move(-1);
		} else if (e.key === 'Enter') {
			e.preventDefault();
			modalCtl.navigateActive();
		} else if (e.key === 'Escape') {
			e.preventDefault();
			hide();
		} else if (e.key === 'Tab') {
			// Focus trap: the palette only has the input as a real
			// focus target (results are click/Enter-driven). Stop Tab
			// from leaving the modal so background focus stays parked
			// where it was when the palette opened.
			e.preventDefault();
			input.focus();
		}
	}

	function onGlobal(e) {
		if ((e.metaKey || e.ctrlKey) && !e.shiftKey && (e.key === 'k' || e.key === 'K')) {
			e.preventDefault();
			const ls = landingSearch();
			if (ls) { focusLanding(ls); return; }
			isVisible() ? hide() : show();
			return;
		}
		if (e.key === '/' && !isInputLike(document.activeElement) && !isVisible()) {
			e.preventDefault();
			const ls = landingSearch();
			if (ls) { focusLanding(ls); return; }
			show();
		}
		if (e.key === 'Escape' && isVisible()) {
			e.preventDefault();
			hide();
		}
	}

	// attachOverlay (re)injects the overlay element + its element-scoped
	// listeners. Safe to call multiple times: it returns early if the
	// overlay already exists.
	function attachOverlay() {
		if (document.querySelector('.palette-overlay')) {
			overlay = document.querySelector('.palette-overlay');
			input = overlay.querySelector('.palette-input');
			list = overlay.querySelector('.palette-results');
			empty = overlay.querySelector('.palette-empty');
			return;
		}
		const root = document.createElement('div');
		root.innerHTML = TEMPLATE;
		document.body.appendChild(root.firstElementChild);

		overlay = document.querySelector('.palette-overlay');
		input = overlay.querySelector('.palette-input');
		list = overlay.querySelector('.palette-results');
		empty = overlay.querySelector('.palette-empty');

		input.addEventListener('keydown', onKey);
		overlay.addEventListener('click', (e) => {
			if (e.target === overlay) hide();
		});
		modalCtl = searchController({
			input,
			list,
			itemClass: 'palette-item',
			warnLabel: 'palette search failed',
			beforeNavigate: hide,
			onRender(c) {
				empty.dataset.state =
					c.hits.length === 0 && input.value.trim() ? 'visible' : 'hidden';
			},
		});
	}

	// attachGlobals attaches handlers on document/window — installed
	// once per page load.
	function attachGlobals() {
		document.addEventListener('click', (e) => {
			if (e.target.closest('[data-palette-toggle]')) {
				e.preventDefault();
				const ls = landingSearch();
				if (ls) { focusLanding(ls); return; }
				isVisible() ? hide() : show();
			}
		});
		document.addEventListener('keydown', onGlobal);
		// Re-attach after htmx partial swaps in case a swap brought in
		// (or removed) the landing form or the overlay's anchor markup.
		const reattach = () => { attachOverlay(); attachLanding(); };
		document.body.addEventListener('htmx:afterSwap', reattach);
	}

	// attachLanding wires up typeahead on the landing-page hero search:
	// debounced /api/v1/search calls render hits in a dropdown below
	// the input, with the same item shape and keyboard model as the
	// modal palette. Enter on a highlighted hit navigates to it; Enter
	// with no hit lets the form submit to /search as before.
	function attachLanding() {
		const ls = landingSearch();
		if (!ls || ls.dataset.heroBound === '1') return;
		const dropdown = document.getElementById('hero-results');
		if (!dropdown) return;
		ls.dataset.heroBound = '1';

		const ctl = searchController({
			input: ls,
			list: dropdown,
			itemClass: 'hero-result',
			warnLabel: 'hero search failed',
			onRender(c) {
				dropdown.dataset.state = c.hits.length === 0 ? 'hidden' : 'visible';
			},
		});

		ls.addEventListener('keydown', (e) => {
			if (e.key === 'ArrowDown') {
				if (ctl.hits.length === 0) return;
				e.preventDefault();
				ctl.move(1);
			} else if (e.key === 'ArrowUp') {
				if (ctl.hits.length === 0) return;
				e.preventDefault();
				ctl.move(-1);
			} else if (e.key === 'Enter') {
				// Highlighted hit short-circuits to OID navigation; with no
				// hits we let the form submit to /search.
				if (ctl.active >= 0 && ctl.hits.length > 0) {
					e.preventDefault();
					ctl.navigate(ctl.active);
				}
			} else if (e.key === 'Escape') {
				if (dropdown.dataset.state === 'visible') {
					e.preventDefault();
					ctl.clear();
				}
			}
		});
	}

	function init() {
		attachOverlay();
		attachGlobals();
		attachLanding();
	}

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', init);
	} else {
		init();
	}
})();
