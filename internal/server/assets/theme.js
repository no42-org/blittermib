// blittermib theme toggle — vanilla JS island.
//
// Pre-paint theme is applied by an inline <script> in base.templ
// (no flash of wrong palette). This deferred script wires up the
// click toggle on every [data-theme-toggle] button.
//
// Persists the user's choice in localStorage under
// 'blittermib-theme'. If never toggled, the page follows the
// system prefers-color-scheme via the CSS @media block.

(function () {
	'use strict';

	const KEY = 'blittermib-theme';

	function currentTheme() {
		return document.documentElement.dataset.theme || '';
	}

	function setTheme(t) {
		if (t === 'light' || t === 'dark') {
			document.documentElement.dataset.theme = t;
			try {
				localStorage.setItem(KEY, t);
			} catch (e) {
				// localStorage unavailable (private mode etc.) —
				// theme still applies for this page load.
			}
		}
	}

	function toggle() {
		// If no override is set, default to flipping AWAY from the
		// effective system theme.
		const cur = currentTheme();
		if (cur === 'light') {
			setTheme('dark');
			return;
		}
		if (cur === 'dark') {
			setTheme('light');
			return;
		}
		const prefersDark =
			window.matchMedia &&
			window.matchMedia('(prefers-color-scheme: dark)').matches;
		setTheme(prefersDark ? 'light' : 'dark');
	}

	function onClick(e) {
		const btn = e.target.closest('[data-theme-toggle]');
		if (!btn) return;
		e.preventDefault();
		toggle();
	}

	// Expose for inline onclick fallbacks (e.g. prototype HTML).
	window.toggleTheme = toggle;

	if (!document.body) {
		document.addEventListener('DOMContentLoaded', () =>
			document.addEventListener('click', onClick),
		);
	} else {
		document.addEventListener('click', onClick);
	}
})();
