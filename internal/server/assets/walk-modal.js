// walk-modal.js — the "Decode SNMP Walk" topbar control.
//
// Two jobs, both progressive enhancement over the shared
// walkIntakeForm (see internal/web/walk.templ):
//
//   1. Modal: upgrade the topbar control ([data-walk-modal-open],
//      a plain link to /walk without JS) into an opener for the
//      body-mounted [data-walk-modal] overlay. Close on the ✕,
//      Escape, or a click on the backdrop; return focus to the
//      control that opened it.
//   2. Drop zone: let the user drop a capture file onto the
//      textarea zone ([data-walk-dropzone]) instead of using the
//      file picker — the dropped FileList is assigned to the form's
//      <input type="file"> so submission stays a plain multipart
//      POST. No fetch: the site has no hx-boost, so the form
//      navigates natively to the /walk results page.
//
// The drop zone is wired on every page that carries the form (the
// modal on all pages, plus the /walk page's own copy), so it also
// enhances the standalone page.
(function () {
	'use strict';

	var modal = null;
	var returnFocusTo = null;

	function open(trigger) {
		if (!modal) return;
		returnFocusTo = trigger || null;
		if (returnFocusTo) returnFocusTo.setAttribute('aria-expanded', 'true');
		modal.dataset.state = 'visible';
		var ta = modal.querySelector('textarea[name="walk"]');
		if (ta) ta.focus();
	}

	function close() {
		if (!modal || modal.dataset.state !== 'visible') return;
		modal.dataset.state = 'hidden';
		if (returnFocusTo) {
			returnFocusTo.setAttribute('aria-expanded', 'false');
			if (typeof returnFocusTo.focus === 'function') returnFocusTo.focus();
		}
		returnFocusTo = null;
	}

	// wireDropzone makes `zone` accept a dropped file, assigning it to
	// the file input in the same form and reflecting the name in the
	// "Or upload a file:" label.
	function wireDropzone(zone) {
		var form = zone.closest('form');
		var input = form ? form.querySelector('[data-walk-file-input]') : null;
		var label = form ? form.querySelector('[data-walk-file-label]') : null;
		var idleLabel = label ? label.textContent : '';
		// Counter guards the well-known dragleave-on-child-cross issue.
		var depth = 0;

		zone.addEventListener('dragenter', function (e) {
			e.preventDefault();
			depth++;
			zone.dataset.drag = 'over';
		});
		zone.addEventListener('dragover', function (e) {
			// preventDefault keeps the drop event reachable.
			e.preventDefault();
		});
		zone.addEventListener('dragleave', function () {
			depth = Math.max(0, depth - 1);
			if (depth === 0) delete zone.dataset.drag;
		});
		zone.addEventListener('drop', function (e) {
			e.preventDefault();
			depth = 0;
			delete zone.dataset.drag;
			var files = e.dataTransfer && e.dataTransfer.files;
			if (!files || !files.length || !input) return;
			// Assigning a drop's FileList to a file input keeps the form a
			// plain POST. The input is single-file, so reduce a multi-file
			// or folder drop to the first file. The setter is read-only in
			// some older engines — guard so a throw leaves the picker as
			// the fallback rather than aborting silently.
			try {
				if (files.length > 1 && typeof DataTransfer === 'function') {
					var dt = new DataTransfer();
					dt.items.add(files[0]);
					input.files = dt.files;
				} else {
					input.files = files;
				}
				if (label) label.textContent = files[0].name;
			} catch (err) {
				/* read-only input.files — leave the file picker as fallback */
			}
		});

		if (input && label) {
			input.addEventListener('change', function () {
				label.textContent = input.files && input.files.length
					? input.files[0].name
					: idleLabel;
			});
		}
	}

	function bindOnce(el, key) {
		if (!el || el.dataset[key]) return false;
		el.dataset[key] = '1';
		return true;
	}

	function init() {
		modal = document.querySelector('[data-walk-modal]');

		Array.prototype.forEach.call(
			document.querySelectorAll('[data-walk-modal-open]'),
			function (btn) {
				if (!bindOnce(btn, 'walkBound')) return;
				btn.addEventListener('click', function (e) {
					// No modal markup → let the link fall through to /walk.
					if (!modal) return;
					e.preventDefault();
					open(btn);
				});
			}
		);

		if (modal && bindOnce(modal, 'walkBound')) {
			// Swallow a file dropped anywhere in the modal but outside the
			// dropzone — without this the browser navigates to the file,
			// destroying the page. The dropzone's own handler still runs
			// for drops that land on it.
			modal.addEventListener('dragover', function (e) { e.preventDefault(); });
			modal.addEventListener('drop', function (e) { e.preventDefault(); });
			// Close on a backdrop click — but only when the press STARTED
			// on the backdrop, so a text selection dragged out of the
			// textarea and released on the backdrop does not dismiss it.
			var downOnBackdrop = false;
			modal.addEventListener('mousedown', function (e) {
				downOnBackdrop = (e.target === modal);
			});
			modal.addEventListener('click', function (e) {
				if (e.target === modal && downOnBackdrop) close();
			});
			var x = modal.querySelector('[data-walk-modal-close]');
			if (x) x.addEventListener('click', close);
		}

		Array.prototype.forEach.call(
			document.querySelectorAll('[data-walk-dropzone]'),
			function (zone) {
				if (!bindOnce(zone, 'walkBound')) return;
				wireDropzone(zone);
			}
		);

		// Decoding a large walk is a multi-second full-page POST that
		// leaves the form looking frozen. On submit, flip the button to a
		// "Decoding…" state and reveal an indeterminate progress bar (via
		// the form's `walk-decoding` class). The form still submits and
		// navigates natively — we don't preventDefault.
		Array.prototype.forEach.call(
			document.querySelectorAll('form.walk-intake'),
			function (form) {
				if (!bindOnce(form, 'walkSubmitBound')) return;
				form.addEventListener('submit', function () {
					form.classList.add('walk-decoding');
					form.setAttribute('aria-busy', 'true');
					var btn = form.querySelector('button[type="submit"]');
					if (btn) btn.textContent = 'Decoding…';
				});
				var btn = form.querySelector('button[type="submit"]');
				if (btn) {
					// Swallow the second click of a double-click: the first
					// click submits and swaps the label to "Decoding…"; the
					// second would word-select the new label in browsers
					// that ignore unprefixed user-select (Safari < 18.2),
					// painting a dark ::selection box behind the text.
					btn.addEventListener('mousedown', function (e) {
						if (e.detail > 1) e.preventDefault();
					});
				}
			}
		);

		if (bindOnce(document.documentElement, 'walkModalGlobal')) {
			// bfcache restore (Safari/Firefox Back) revives the mutated DOM
			// without re-running scripts — without this reset the form
			// comes back stuck on "Decoding…" with an animating bar.
			window.addEventListener('pageshow', function (e) {
				if (!e.persisted) return;
				Array.prototype.forEach.call(
					document.querySelectorAll('form.walk-intake.walk-decoding'),
					function (form) {
						form.classList.remove('walk-decoding');
						form.removeAttribute('aria-busy');
						var btn = form.querySelector('button[type="submit"]');
						if (btn) btn.textContent = 'Decode';
					}
				);
			});
			document.addEventListener('keydown', function (e) {
				if (e.key === 'Escape') {
					close();
					return;
				}
				// ⌘⇧K / Ctrl+Shift+K — open the decode modal (parallel to
				// ⌘K search). On the /walk page the modal is suppressed, so
				// focus its inline capture textarea instead.
				if ((e.metaKey || e.ctrlKey) && e.shiftKey && (e.key === 'k' || e.key === 'K')) {
					e.preventDefault();
					if (modal) {
						open(document.querySelector('[data-walk-modal-open]'));
					} else {
						var ta = document.querySelector('form.walk-intake textarea[name="walk"]');
						if (ta) ta.focus();
					}
				}
			});
		}
	}

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', init);
	} else {
		init();
	}
})();
