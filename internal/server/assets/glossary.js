// blittermib glossary popover — vanilla JS island.
//
// Any element with class="glossary" and a data-term attribute opens
// a small popover on click with a definition pulled from a built-in
// dictionary. Once a user has dismissed a term, it stays muted (no
// dotted underline) for that browser via localStorage.
//
// Keeps the glossary terms inline in the JS rather than fetched from
// the server — they're short, finite, and the round-trip is wasted
// for content that never changes.

(function () {
	'use strict';

	const STORAGE_KEY = 'blittermib-glossary-seen';

	const TERMS = {
		'OBJECT-TYPE': 'A SMI macro defining a managed value: its syntax, access permissions, status, and description. Each OBJECT-TYPE has an OID.',
		'TEXTUAL-CONVENTION': 'A named type with semantics layered on top of a base SMI type — e.g. InterfaceIndex on top of Integer32. Lets a MIB document the meaning of a value separately from its raw representation.',
		'NOTIFICATION-TYPE': 'A SMIv2 macro for asynchronous events sent from agents to managers (linkUp, linkDown, etc.). Carries a list of OBJECTS that describe the event context.',
		'OBJECT-IDENTITY': 'A SMIv2 macro that assigns an OID a name and description without making it a queryable value. Used for organizational nodes in the OID tree.',
		'MODULE-IDENTITY': 'The header object of a SMIv2 module: its top-level OID, organization, contact info, description, and revision history.',
		'OBJECT-GROUP': 'A named list of OBJECT-TYPEs that conformant agents must support together. Used in MODULE-COMPLIANCE to define what an implementation must implement.',
		'NOTIFICATION-GROUP': 'A named list of NOTIFICATION-TYPEs an implementation must support together.',
		'MODULE-COMPLIANCE': 'A SMIv2 statement of what an implementation must support to claim conformance with a MIB module — required groups, optional groups, and refinements.',
		'AUGMENTS': 'A SMIv2 clause on a conceptual row indicating it extends another table\'s row 1:1, sharing the same INDEX. Used by MIBs that add columns to an existing table.',
		'INDEX': 'The list of columns that uniquely identify a conceptual row within a SMIv2 table.',
		'MAX-ACCESS': 'A SMIv2 clause restricting how a managed value can be accessed: read-only, read-write, read-create, accessible-for-notify, or not-accessible.',
		'STATUS': 'A SMI clause marking a definition as current, deprecated, or obsolete.',
		Counter32: 'A 32-bit SMIv2 counter that monotonically increases, wrapping back to zero on overflow. Discontinuities reset the counter to zero.',
		Counter64: 'A 64-bit SMIv2 counter (high-capacity counters used in IF-MIB ifXTable). Same semantics as Counter32 but harder to overflow.',
		Gauge32: 'A 32-bit SMIv2 gauge that rises and falls within a range. Latches at MAX_VALUE rather than wrapping.',
		Discontinuities: 'Events that reset or invalidate a counter\'s monotonicity — typically agent restart, interface re-init, or counter type changes. Most counter objects pair with a *DiscontinuityTime sysObject.',
	};

	let seen = null;

	function loadSeen() {
		if (seen !== null) return seen;
		try {
			const raw = localStorage.getItem(STORAGE_KEY);
			seen = new Set(raw ? JSON.parse(raw) : []);
		} catch (e) {
			seen = new Set();
		}
		return seen;
	}

	function markSeen(term) {
		const s = loadSeen();
		if (s.has(term)) return;
		s.add(term);
		try {
			localStorage.setItem(STORAGE_KEY, JSON.stringify(Array.from(s)));
		} catch (e) {
			// fail silently — private mode etc.
		}
	}

	function termFromElement(el) {
		const explicit = el.dataset.term;
		if (explicit) return explicit;
		// fall back to the element's text content trimmed
		const t = (el.textContent || '').trim();
		return t in TERMS ? t : explicit || t;
	}

	function definitionFor(term) {
		if (!term) return null;
		if (TERMS[term]) return TERMS[term];
		// case-insensitive fallback
		const upper = term.toUpperCase();
		for (const key in TERMS) {
			if (key.toUpperCase() === upper) return TERMS[key];
		}
		return null;
	}

	function closeAllPopovers() {
		document.querySelectorAll('.glossary-popover').forEach((el) => el.remove());
	}

	function applySeenStyling() {
		const s = loadSeen();
		document.querySelectorAll('.glossary').forEach((el) => {
			const t = termFromElement(el);
			if (s.has(t)) {
				el.classList.add('glossary-seen');
				el.style.textDecoration = 'none';
			}
		});
	}

	function showPopover(el, term, definition) {
		closeAllPopovers();

		const pop = document.createElement('div');
		pop.className = 'glossary-popover';
		pop.innerHTML =
			'<span class="glossary-term"></span><span class="glossary-def"></span>';
		pop.querySelector('.glossary-term').textContent = term;
		pop.querySelector('.glossary-def').textContent = definition;

		document.body.appendChild(pop);

		const rect = el.getBoundingClientRect();
		const popRect = pop.getBoundingClientRect();
		// Position below the term, clamped to the viewport.
		let top = rect.bottom + window.scrollY + 6;
		let left = rect.left + window.scrollX;
		if (left + popRect.width > window.innerWidth - 16) {
			left = window.innerWidth - popRect.width - 16 + window.scrollX;
		}
		if (left < 8) left = 8;
		pop.style.top = top + 'px';
		pop.style.left = left + 'px';

		markSeen(term);
		applySeenStyling();
	}

	function onClick(e) {
		const el = e.target.closest('.glossary');
		if (!el) {
			closeAllPopovers();
			return;
		}
		e.preventDefault();
		const term = termFromElement(el);
		const def = definitionFor(term);
		if (!def) {
			closeAllPopovers();
			return;
		}
		showPopover(el, term, def);
	}

	function onKey(e) {
		if (e.key === 'Escape') closeAllPopovers();
	}

	function init() {
		applySeenStyling();
		document.addEventListener('click', onClick);
		document.addEventListener('keydown', onKey);
		window.addEventListener('scroll', closeAllPopovers, { passive: true });
	}

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', init);
	} else {
		init();
	}
})();
