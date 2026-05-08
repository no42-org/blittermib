// upload.js — Alpine x-data factory for the landing-page drop zone.
//
// State machine (the `state` reactive value):
//   idle        — empty drop zone, ready to receive files
//   dragover    — at least one drag is hovering over the zone
//   uploading   — POST /api/v1/upload is in flight
//   error       — the last POST failed at the request level (network,
//                 5xx); per-file errors live in `results`, not here
//   done        — last POST returned; results populated
//
// Per-file outcomes from the response are appended to `results`,
// keyed by name + timestamp so two uploads of the same name don't
// collide as duplicate Alpine x-for keys. The "Replace" button on
// a 409 row re-POSTs that file with ?replace=true.
//
// Drag/drop tracking uses a counter to avoid the well-known issue
// where `dragleave` fires when you cross a child element. Increment
// on enter, decrement on leave; the zone is "dragover" while the
// counter is positive.
window.dropZone = function () {
	return {
		state: 'idle',
		filesInFlight: 0,
		results: [],
		dragCounter: 0,
		// internal cache of pending File objects keyed by name so
		// the Replace button can re-POST the original bytes
		// without re-prompting the user.
		_pending: {},

		onDragEnter: function () {
			this.dragCounter++;
			if (this.state === 'idle' || this.state === 'done') {
				this.state = 'dragover';
			}
		},
		onDragOver: function () {
			// no-op; preventDefault on the host element keeps the
			// drop event reachable.
		},
		onDragLeave: function () {
			this.dragCounter = Math.max(0, this.dragCounter - 1);
			if (this.dragCounter === 0 && this.state === 'dragover') {
				this.state = 'idle';
			}
		},
		onDrop: function (event) {
			this.dragCounter = 0;
			var files = event.dataTransfer && event.dataTransfer.files;
			if (!files || files.length === 0) {
				this.state = 'idle';
				return;
			}
			this.upload(Array.from(files), false);
		},
		onPick: function (event) {
			var files = event.target.files;
			if (!files || files.length === 0) return;
			this.upload(Array.from(files), false);
			// reset so picking the same file twice still fires
			// onChange.
			event.target.value = '';
		},

		upload: function (fileList, replace) {
			var self = this;
			var fd = new FormData();
			fileList.forEach(function (f) {
				fd.append('files', f, f.name);
				self._pending[f.name] = f;
			});
			self.filesInFlight = fileList.length;
			self.state = 'uploading';
			var url = '/api/v1/upload' + (replace ? '?replace=true' : '');
			fetch(url, { method: 'POST', body: fd })
				.then(function (resp) {
					return resp.json().catch(function () { return { uploaded: [] }; });
				})
				.then(function (data) {
					var ts = Date.now();
					(data.uploaded || []).forEach(function (r) {
						self.results.unshift({
							name: r.name,
							ok: !!r.ok,
							module: r.module || '',
							symbols: r.symbols || 0,
							error: r.error || '',
							canReplace: !r.ok && r.error && r.error.indexOf('already exists') >= 0,
							ts: ts,
						});
					});
					self.state = 'done';
					self.filesInFlight = 0;
				})
				.catch(function (err) {
					self.results.unshift({
						name: '(batch)',
						ok: false,
						error: 'request failed: ' + err,
						canReplace: false,
						ts: Date.now(),
					});
					self.state = 'error';
					self.filesInFlight = 0;
				});
		},

		replace: function (row) {
			var f = this._pending[row.name];
			if (!f) return;
			this.upload([f], true);
		},
	};
};
