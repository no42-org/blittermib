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
// Filtering is pure client-side; the visible window is capped at
// MAX_VISIBLE so a 1k-bundle doesn't push the overlay off-screen,
// and a "+N more" indicator nudges the user to keep typing.
window.picker = (function () {
	var MAX_VISIBLE = 10;

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

			init: function () {
				this.modules = loadModules();
				this.$watch('query', () => { this.active = 0; });
				// Arrow / Enter navigation only fires while the
				// overlay is open and the input has focus.
				this.$el.addEventListener('keydown', (e) => {
					if (!this.open) return;
					if (e.key === 'ArrowDown') {
						e.preventDefault();
						var n = this.visible.length;
						if (n) this.active = (this.active + 1) % n;
					} else if (e.key === 'ArrowUp') {
						e.preventDefault();
						var n2 = this.visible.length;
						if (n2) this.active = (this.active - 1 + n2) % n2;
					} else if (e.key === 'Enter') {
						var v = this.visible[this.active];
						if (v) {
							e.preventDefault();
							window.location.assign('/m/' + encodeURIComponent(v.name));
						}
					}
				});
			},

			get filtered() {
				var q = (this.query || '').toLowerCase();
				if (!q) return this.modules;
				return this.modules.filter((m) => {
					var n = (m.name || '').toLowerCase();
					var o = (m.oid || '').toLowerCase();
					return n.indexOf(q) >= 0 || o.indexOf(q) >= 0;
				});
			},

			get visible() {
				return this.filtered.slice(0, MAX_VISIBLE);
			},

			get hiddenCount() {
				return Math.max(0, this.filtered.length - MAX_VISIBLE);
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
