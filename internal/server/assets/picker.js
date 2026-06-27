// picker.js — Alpine x-data factory for the status-bar module
// picker overlay.
//
// The overlay is body-mounted in module_picker.templ and starts
// hidden (x-cloak + x-show="open"). Open and close transitions
// run via the standard Alpine event hooks bound in the templ:
//   - status-bar module button dispatches `picker:open`
//   - Escape key closes the overlay
//   - Enter key navigates to the active row
//   - Up/Down arrows move the active selection
//
// The full module list is shipped as templ-escaped JSON in the
// `data-modules` attribute of `#module-picker-data` (mirroring how
// walk.templ ships WalkDataJSON) — NOT a <script type="application/json">
// block: templ emits script content as raw text, so a component call
// there ships as literal source instead of JSON.
// Filtering is pure client-side; the full match list renders into
// the card's scrollable list (capped at 70vh by CSS), so every
// module is reachable by scrolling — no typing required.
window.picker = (function () {
	// currentModuleName extracts the open module from a workspace URL
	// so the scoped picker can highlight "where you are".
	function currentModuleName() {
		var m = window.location.pathname.match(/^\/m\/([^/]+)/);
		if (!m) return '';
		try {
			return decodeURIComponent(m[1]);
		} catch (_) {
			return m[1];
		}
	}

	function loadModules() {
		var el = document.getElementById('module-picker-data');
		if (!el) return [];
		try {
			var data = JSON.parse(el.getAttribute('data-modules') || '[]');
			return Array.isArray(data) ? data : [];
		} catch (_) {
			return [];
		}
	}

	return function () {
		return {
			open: false,
			query: '',
			active: 0,
			modules: [],
			// walkScope ([{name, values}, …] or null) arrives as the
			// open event's detail — the walk overlay dispatches it; the
			// picker never reads the walk's sessionStorage itself. Set
			// fresh on EVERY open, so a scope can never leak from the
			// walk trigger into a later module-button open.
			walkScope: null,
			currentModule: '',
			// Accessibility: element to return focus to on close, and the
			// focus-trap teardown handle while open (WCAG 2.4.3).
			returnFocusTo: null,
			trapOff: null,

			init: function () {
				this.modules = loadModules();
				this.$watch('query', () => {
					this.active = 0;
					if (this.$refs.list) this.$refs.list.scrollTop = 0;
				});
				// Trap Tab within the card while open; on close, tear the
				// trap down and return focus to whatever opened the picker.
				this.$watch('open', (isOpen) => {
					if (isOpen) {
						this.$nextTick(() => {
							// Guard the deferred install: if the picker was
							// closed before this tick ran, the else branch
							// already fired with trapOff still null, so
							// installing now would leak a keydown listener on
							// the hidden card that nothing ever tears down.
							if (!this.open) return;
							var card = this.$el.querySelector('.module-picker-card');
							if (card && window.blitterA11y) {
								this.trapOff = window.blitterA11y.focusTrap(card);
							}
						});
					} else {
						if (this.trapOff) { this.trapOff(); this.trapOff = null; }
						var rt = this.returnFocusTo;
						this.returnFocusTo = null;
						if (rt && typeof rt.focus === 'function') {
							try { rt.focus(); } catch (_) { /* node removed */ }
						}
					}
				});
				// Arrow / Enter navigation only fires while the
				// overlay is open and the input has focus.
				this.$el.addEventListener('keydown', (e) => {
					if (!this.open) return;
					if (e.key === 'ArrowDown') {
						e.preventDefault();
						var n = this.filtered.length;
						if (n) this.active = (this.active + 1) % n;
						this.scrollActiveIntoView();
					} else if (e.key === 'ArrowUp') {
						e.preventDefault();
						var n2 = this.filtered.length;
						if (n2) this.active = (this.active - 1 + n2) % n2;
						this.scrollActiveIntoView();
					} else if (e.key === 'Enter') {
						var v = this.filtered[this.active];
						if (v && !v.missing) {
							e.preventDefault();
							window.location.assign('/m/' + encodeURIComponent(v.name));
						}
					}
				});
			},

			// show opens the overlay, scoped when the open event carried
			// a walk module list as detail (the status-bar walk
			// indicator) and unscoped otherwise (the module-name
			// button). The query deliberately persists across opens —
			// pre-existing behaviour for the unscoped path.
			show: function (detail) {
				var ws = detail && Array.isArray(detail.walkModules) && detail.walkModules.length > 0
					? detail.walkModules
					: null;
				this.walkScope = ws;
				this.currentModule = currentModuleName();
				this.active = 0;
				// Remember the trigger (status-bar module button / walk
				// indicator) so close() can return focus to it.
				this.returnFocusTo = document.activeElement;
				this.open = true;
				this.$nextTick(() => this.$refs.input.focus());
			},

			// clearScope widens a walk-scoped picker to the full module
			// list without closing it (the scope chip's ✕).
			clearScope: function () {
				this.walkScope = null;
				this.active = 0;
				this.$refs.input.focus();
			},

			// scrollActiveIntoView keeps the keyboard selection visible
			// inside the scrollable list as the arrows move it.
			scrollActiveIntoView: function () {
				this.$nextTick(() => {
					var el = this.$el.querySelector('.module-picker-item[data-active="true"]');
					if (el) el.scrollIntoView({ block: 'nearest' });
				});
			},

			// scopedRows projects the walk scope onto the loaded-module
			// list: value counts merged in, ordered by value count
			// descending then name, and walk modules that are no longer
			// loaded marked missing (rendered disabled instead of
			// linking to a 404).
			get scopedRows() {
				if (!this.walkScope) return null;
				var byName = {};
				this.modules.forEach(function (m) { byName[m.name] = m; });
				var rows = this.walkScope.map(function (wm) {
					var loaded = byName[wm.name];
					return {
						name: wm.name,
						oid: loaded ? loaded.oid : '',
						walkValues: wm.values,
						missing: !loaded,
					};
				});
				rows.sort(function (a, b) {
					return (b.walkValues - a.walkValues) || a.name.localeCompare(b.name);
				});
				return rows;
			},

			get filtered() {
				var base = this.scopedRows || this.modules;
				var q = (this.query || '').toLowerCase();
				if (!q) return base;
				return base.filter((m) => {
					var n = (m.name || '').toLowerCase();
					var o = (m.oid || '').toLowerCase();
					return n.indexOf(q) >= 0 || o.indexOf(q) >= 0;
				});
			},

		};
	};
})();

// Belt-and-suspenders htmx:afterSwap close hook: ensure any
// transient open state doesn't leak past a partial swap (e.g. a
// swap fires while the picker is open). workspace.js handles
// Alpine.initTree re-init.
if (typeof document !== 'undefined') {
	document.body.addEventListener('htmx:afterSwap', function () {
		document.dispatchEvent(new CustomEvent('picker:close'));
	});
}
