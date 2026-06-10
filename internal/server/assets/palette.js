// blittermib command palette — vanilla JS island.
//
// Listens for ⌘K / Ctrl+K and / (when no input is focused), opens a
// search overlay backed by /api/v1/search?q=…, supports keyboard
// navigation, and routes to the matching /s/{Module}::{Name} on Enter.
//
// HTMX integration: base.templ uses hx-boost on <body> with
// hx-swap="outerHTML", which means body (and its children) is
// replaced on every internal navigation. Without re-attaching, the
// palette overlay would be torn down by the first nav. We split
// init into attachOverlay (idempotent, runs on every swap) and
// attachGlobals (runs once for document-level handlers).

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

	let overlay, input, list, empty;
	let active = -1;
	let hits = [];
	let debounce;
	let lastReqSeq = 0;
	// Focus trap: remember the element that had focus when the palette
	// opened so we can restore it on close. Without this, dismissing
	// the palette leaves keyboard focus orphaned in the DOM.
	let returnFocusTo = null;

	function escape(s) {
		const d = document.createElement('div');
		d.textContent = s == null ? '' : String(s);
		return d.innerHTML;
	}

	function show() {
		if (!overlay) attachOverlay();
		overlay.dataset.state = 'visible';
		input.value = '';
		list.innerHTML = '';
		empty.dataset.state = 'hidden';
		hits = [];
		active = -1;
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

	async function search(q) {
		const seq = ++lastReqSeq;
		if (!q.trim()) {
			hits = [];
			renderHits();
			return;
		}
		try {
			const res = await fetch(SEARCH_URL + '?q=' + encodeURIComponent(q));
			if (!res.ok) throw new Error('search ' + res.status);
			const data = await res.json();
			if (seq !== lastReqSeq) return; // stale response, ignore
			hits = (data.hits || []).slice(0, MAX_RESULTS);
			renderHits();
		} catch (err) {
			console.warn('palette search failed', err);
			hits = [];
			renderHits();
		}
	}

	function renderHits() {
		if (hits.length === 0) {
			list.innerHTML = '';
			empty.dataset.state = input.value.trim() ? 'visible' : 'hidden';
			active = -1;
			return;
		}
		empty.dataset.state = 'hidden';
		list.innerHTML = hits
			.map(
				(h, i) => `
<li class="palette-item" data-idx="${i}" role="option" aria-selected="${i === 0}">
	<span class="palette-name">${escape(h.Name)}</span>
	<span class="palette-oid">${escape(h.OID)}</span>
	<span class="palette-meta">${escape(h.Module)} · ${escape(h.Kind)}</span>
</li>`,
			)
			.join('');
		active = 0;
		updateActive();
	}

	function updateActive() {
		const items = list.querySelectorAll('.palette-item');
		items.forEach((el, i) => {
			const on = i === active;
			el.classList.toggle('active', on);
			el.setAttribute('aria-selected', on ? 'true' : 'false');
			if (on) el.scrollIntoView({ block: 'nearest' });
		});
	}

	function navigate(i) {
		const h = hits[i];
		if (!h) return;
		hide();
		// Deliberate full navigation: palette jumps can cross modules,
		// which is outside the workspace partial-navigation contract
		// (in-workspace pane swaps only). Hit rows go to the workspace
		// selection so the user lands in the navigation context that
		// owns the OID, matching what the /search results page does.
		window.location.href = '/m/' + encodeURIComponent(h.Module) + '/' + h.OID;
	}

	function onInput() {
		clearTimeout(debounce);
		const q = input.value;
		debounce = setTimeout(() => search(q), DEBOUNCE_MS);
	}

	function onKey(e) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			if (hits.length === 0) return;
			active = (active + 1) % hits.length;
			updateActive();
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			if (hits.length === 0) return;
			active = (active - 1 + hits.length) % hits.length;
			updateActive();
		} else if (e.key === 'Enter') {
			e.preventDefault();
			if (active >= 0) navigate(active);
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

		input.addEventListener('input', onInput);
		input.addEventListener('keydown', onKey);
		overlay.addEventListener('click', (e) => {
			if (e.target === overlay) hide();
		});
		list.addEventListener('click', (e) => {
			const li = e.target.closest('.palette-item');
			if (!li) return;
			navigate(parseInt(li.dataset.idx, 10));
		});
	}

	// attachGlobals attaches handlers on document/window — these survive
	// hx-boost swaps and must only be installed once.
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
		// Re-attach the overlay after every htmx swap — body is the
		// hx-target, so the overlay vanishes with each navigation.
		// attachLanding also runs because the landing form is a body
		// child and gets swapped out / back in by hx-boost.
		const reattach = () => { attachOverlay(); attachLanding(); };
		document.body.addEventListener('htmx:afterSwap', reattach);
		// Some swaps replace body itself; listen on documentElement too.
		document.documentElement.addEventListener('htmx:afterSwap', reattach);
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

		let lhits = [];
		let lactive = -1;
		let ldebounce;
		let llastSeq = 0;

		function lrender() {
			if (lhits.length === 0) {
				dropdown.innerHTML = '';
				dropdown.dataset.state = 'hidden';
				lactive = -1;
				return;
			}
			dropdown.innerHTML = lhits
				.map(
					(h, i) => `
<li class="hero-result" data-idx="${i}" role="option" aria-selected="${i === 0}">
	<span class="palette-name">${escape(h.Name)}</span>
	<span class="palette-oid">${escape(h.OID)}</span>
	<span class="palette-meta">${escape(h.Module)} · ${escape(h.Kind)}</span>
</li>`,
				)
				.join('');
			dropdown.dataset.state = 'visible';
			lactive = 0;
			lupdateActive();
		}

		function lupdateActive() {
			const items = dropdown.querySelectorAll('.hero-result');
			items.forEach((el, i) => {
				const on = i === lactive;
				el.classList.toggle('active', on);
				el.setAttribute('aria-selected', on ? 'true' : 'false');
				if (on) el.scrollIntoView({ block: 'nearest' });
			});
		}

		function lnavigate(i) {
			const h = lhits[i];
			if (!h) return;
			window.location.href = '/m/' + encodeURIComponent(h.Module) + '/' + h.OID;
		}

		async function lsearch(q) {
			const seq = ++llastSeq;
			if (!q.trim()) { lhits = []; lrender(); return; }
			try {
				const res = await fetch(SEARCH_URL + '?q=' + encodeURIComponent(q));
				if (!res.ok) throw new Error('search ' + res.status);
				const data = await res.json();
				if (seq !== llastSeq) return;
				lhits = (data.hits || []).slice(0, MAX_RESULTS);
				lrender();
			} catch (err) {
				console.warn('hero search failed', err);
				lhits = [];
				lrender();
			}
		}

		ls.addEventListener('input', () => {
			clearTimeout(ldebounce);
			const q = ls.value;
			ldebounce = setTimeout(() => lsearch(q), DEBOUNCE_MS);
		});
		ls.addEventListener('keydown', (e) => {
			if (e.key === 'ArrowDown') {
				if (lhits.length === 0) return;
				e.preventDefault();
				lactive = (lactive + 1) % lhits.length;
				lupdateActive();
			} else if (e.key === 'ArrowUp') {
				if (lhits.length === 0) return;
				e.preventDefault();
				lactive = (lactive - 1 + lhits.length) % lhits.length;
				lupdateActive();
			} else if (e.key === 'Enter') {
				// Highlighted hit short-circuits to OID navigation; with no
				// hits we let the form submit to /search.
				if (lactive >= 0 && lhits.length > 0) {
					e.preventDefault();
					lnavigate(lactive);
				}
			} else if (e.key === 'Escape') {
				if (dropdown.dataset.state === 'visible') {
					e.preventDefault();
					lhits = [];
					lrender();
				}
			}
		});
		dropdown.addEventListener('click', (e) => {
			const li = e.target.closest('.hero-result');
			if (!li) return;
			lnavigate(parseInt(li.dataset.idx, 10));
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
