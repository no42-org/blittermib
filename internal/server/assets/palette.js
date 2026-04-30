// blittermib command palette — vanilla JS island.
//
// Listens for ⌘K / Ctrl+K and / (when no input is focused), opens a
// search overlay backed by /api/v1/search?q=…, supports keyboard
// navigation, and routes to the matching /s/{Module}::{Name} on Enter.
//
// No external dependencies.

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

	function escape(s) {
		const d = document.createElement('div');
		d.textContent = s == null ? '' : String(s);
		return d.innerHTML;
	}

	function show() {
		overlay.dataset.state = 'visible';
		input.value = '';
		list.innerHTML = '';
		empty.dataset.state = 'hidden';
		hits = [];
		active = -1;
		input.focus();
	}

	function hide() {
		overlay.dataset.state = 'hidden';
	}

	function isVisible() {
		return overlay.dataset.state === 'visible';
	}

	function isInputLike(el) {
		if (!el) return false;
		const tag = el.tagName;
		return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
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
		// Use full navigation so HTMX hx-boost picks up the destination
		// in its standard request lifecycle.
		window.location.href = '/s/' + encodeURIComponent(h.Module + '::' + h.Name);
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
		}
	}

	function onGlobal(e) {
		// ⌘K / Ctrl+K from anywhere
		if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
			e.preventDefault();
			isVisible() ? hide() : show();
			return;
		}
		// "/" opens the palette unless the user is typing in a real input
		if (e.key === '/' && !isInputLike(document.activeElement) && !isVisible()) {
			e.preventDefault();
			show();
		}
		// Esc from anywhere closes
		if (e.key === 'Escape' && isVisible()) {
			e.preventDefault();
			hide();
		}
	}

	function init() {
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

		// Optional: trigger from any element with data-palette-toggle
		document.addEventListener('click', (e) => {
			if (e.target.closest('[data-palette-toggle]')) {
				e.preventDefault();
				isVisible() ? hide() : show();
			}
		});

		document.addEventListener('keydown', onGlobal);
	}

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', init);
	} else {
		init();
	}
})();
