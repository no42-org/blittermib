// trap-simulator.js — Alpine x-data factory for the trap-simulator
// modal overlay.
//
// The modal is mounted at the workspace shell level (sibling of
// modulePicker) and starts hidden (x-cloak + x-show="isOpen").
// The right-pane "Simulate" button dispatches `simulate:open`
// on the window; this controller listens for that, reads the
// notification's varbind metadata from data-* attributes on the
// rendered `<ul class="notify-objects">` list, builds an in-state
// model of the form, and regenerates the snmptrap command live as
// the user types.
//
// Privacy posture (per spec):
//   - Form values stay client-side. No HTTP requests are issued
//     carrying form data.
//   - Password fields use type="password"; auto-clear on close.
//   - No localStorage of credentials.
//   - The user copies the generated command and runs it
//     themselves. The simulator never fires a real trap.
window.trapSimulator = (function () {
	// snmpTrapOID per RFC 1907 §6 — used by the v1 form to
	// prepend the original SMIv2 notification OID as the first
	// varbind during the SMIv2-to-SMIv1 translation per RFC 2576.
	var SNMP_TRAP_OID = '.1.3.6.1.6.3.1.1.4.1.0';

	function readVarbinds(ul) {
		var rows = ul.querySelectorAll('.notify-object-row');
		var out = [];
		for (var i = 0; i < rows.length; i++) {
			var li = rows[i];
			var enumValues = [];
			if (li.dataset.enumValues) {
				try {
					enumValues = JSON.parse(li.dataset.enumValues);
				} catch (_) {
					enumValues = [];
				}
			}
			var typeLetter = li.dataset.trapTypeLetter || 's';
			var defaultValue;
			if (enumValues.length > 0) {
				defaultValue = enumValues[0].number;
			} else if (typeLetter === 'i' || typeLetter === 'u' ||
				typeLetter === 'c' || typeLetter === 'C' ||
				typeLetter === 't') {
				defaultValue = 0;
			} else {
				defaultValue = '';
			}
			out.push({
				oid: li.dataset.oid || '',
				name: li.dataset.name || '',
				module: li.dataset.module || '',
				syntax: li.dataset.syntax || '',
				typeLetter: typeLetter,
				isColumn: li.dataset.isColumn === 'true',
				enumValues: enumValues,
				value: defaultValue,
			});
		}
		return out;
	}

	function quote(s) {
		// Bash-quote a string value: wrap in single quotes, escaping
		// any single quotes inside as '\''. Safe for snmptrap
		// invocation.
		return "'" + String(s).replace(/'/g, "'\\''") + "'";
	}

	return function trapSimulator() {
		return {
			isOpen: false,
			notif: { name: '', oid: '', module: '' },
			// Default to scalar-only so a stale state can't bleed
			// the raw-suffix UI into a fresh open() before the
			// metadata reads. open() always sets the real value
			// from the rendered DOM.
			indexMode: 'scalar-only',
			indexLabel: '',
			varbinds: [],

			// Target group
			version: 'v2c',
			host: 'localhost:162',
			community: 'public',

			// v1-specific
			agentAddr: '0.0.0.0',
			genericTrap: 6,
			specificTrap: 0,
			uptime: 0,

			// v3-specific
			v3User: '',
			v3Level: 'noAuthNoPriv',
			v3AuthProto: 'SHA',
			v3AuthPass: '',
			v3PrivProto: 'AES',
			v3PrivPass: '',
			v3EngineID: '',

			// Row identity
			rowIndex: 1,
			rawSuffix: '',

			// UI state
			copied: false,
			engineIDError: '',
			openError: '',
			copyError: '',

			open: function () {
				var ul = document.querySelector('.notify-objects');
				if (!ul) {
					// Surface to the user — without this signal the
					// modal silently stays closed and the click
					// looks like dead UI.
					this.openError = 'Could not load notification metadata for the simulator.';
					this.isOpen = true;
					return;
				}
				this.notif = {
					oid: ul.dataset.notificationOid || '',
					name: ul.dataset.notificationName || '',
					module: ul.dataset.notificationModule || '',
				};
				this.indexMode = ul.dataset.trapIndexMode || 'raw-suffix';
				this.indexLabel = ul.dataset.trapIndexLabel || '';
				this.varbinds = readVarbinds(ul);
				this.copied = false;
				this.engineIDError = '';
				this.openError = '';
				this.copyError = '';
				this.isOpen = true;
			},

			close: function () {
				this.isOpen = false;
				// Auto-clear credentials on close (S7 mitigation).
				// All credential-shaped fields go back to empty so
				// the next open() requires deliberate re-entry, and
				// transient identifiers (engine ID, v3 user, agent
				// address, per-varbind values) reset to their
				// initial defaults so the modal doesn't carry
				// state from a previous notification.
				this.v3AuthPass = '';
				this.v3PrivPass = '';
				this.v3User = '';
				this.v3EngineID = '';
				this.agentAddr = '0.0.0.0';
				this.rowIndex = 1;
				this.rawSuffix = '';
				this.specificTrap = 0;
				this.uptime = 0;
				// Per-varbind values reset to their first-enum /
				// type-default; community + host are deliberately
				// preserved (they're not secrets and re-typing
				// them every time is friction).
				this.varbinds.forEach(function (vb) {
					if (vb.enumValues && vb.enumValues.length > 0) {
						vb.value = vb.enumValues[0].number;
					} else if (vb.typeLetter === 'i' || vb.typeLetter === 'u' ||
						vb.typeLetter === 'c' || vb.typeLetter === 'C' ||
						vb.typeLetter === 't') {
						vb.value = 0;
					} else {
						vb.value = '';
					}
				});
				this.copied = false;
				this.engineIDError = '';
				this.openError = '';
				this.copyError = '';
				// Reset indexMode so a previous notification's
				// raw-suffix mode doesn't bleed into the next open.
				this.indexMode = 'scalar-only';
				this.indexLabel = '';
			},

			validateEngineID: function () {
				var v = (this.v3EngineID || '').trim();
				if (v === '') {
					this.engineIDError = '';
					return;
				}
				// RFC 3411 §5: snmpEngineID is 5–32 octets.
				// In hex form that's 10–64 chars and the length
				// must be even. The regex enforces both.
				if (/^([0-9a-fA-F]{2}){5,32}$/.test(v)) {
					this.engineIDError = '';
				} else {
					this.engineIDError = 'Engine ID must be 10–64 hex chars (5–32 octets, even length).';
				}
			},

			suffix: function (vb) {
				if (vb.isColumn) {
					if (this.indexMode === 'single-int') {
						// x-model.number on an empty input yields
						// NaN; fall back to 1 so the generated
						// command stays valid until the user fixes
						// the input.
						var n = Number(this.rowIndex);
						if (!isFinite(n)) n = 1;
						return '.' + n;
					}
					if (this.indexMode === 'raw-suffix') {
						// rawSuffix may or may not include a leading dot
						var s = (this.rawSuffix || '').trim();
						if (s === '') return '';
						return s.charAt(0) === '.' ? s : '.' + s;
					}
				}
				// Scalars (or columns in scalar-only mode) use .0
				return '.0';
			},

			formatValue: function (vb) {
				var v = vb.value;
				if (vb.typeLetter === 's' || vb.typeLetter === 'a' ||
					vb.typeLetter === 'o' || vb.typeLetter === 'x' ||
					vb.typeLetter === 'b') {
					// String-shaped types: shell-quote so embedded
					// spaces / special chars survive copy-paste.
					// For OID / BITS / hex, an empty value would
					// produce `''` in the command — not a runnable
					// varbind. Surface a placeholder so the user
					// notices instead of pasting a broken command.
					var sv = (v == null ? '' : String(v)).trim();
					if (sv === '' && (vb.typeLetter === 'o' ||
						vb.typeLetter === 'b' ||
						vb.typeLetter === 'x')) {
						return '<EDIT>';
					}
					return quote(v == null ? '' : String(v));
				}
				// Numeric types — guard against NaN from
				// x-model.number on a cleared input.
				var n = Number(v);
				if (!isFinite(n)) {
					// Fall back to 0 only when the user truly
					// blanked the field; if the input held a
					// non-numeric, surface a placeholder.
					var raw = String(v == null ? '' : v).trim();
					if (raw === '') return '0';
					return '<EDIT>';
				}
				return String(n);
			},

			varbindArgs: function () {
				var self = this;
				return this.varbinds.map(function (vb) {
					var oid = (vb.oid || '') + self.suffix(vb);
					return oid + ' ' + vb.typeLetter + ' ' + self.formatValue(vb);
				});
			},

			generateV2c: function () {
				var parts = [
					'snmptrap',
					'-v', '2c',
					'-c', quote(this.community),
					quote(this.host),
					"''",
					this.notif.oid,
				];
				var cmd = parts.join(' ');
				var vbs = this.varbindArgs();
				if (vbs.length === 0) return cmd;
				return cmd + ' \\\n  ' + vbs.join(' \\\n  ');
			},

			generateV3: function () {
				var parts = [
					'snmptrap',
					'-v', '3',
					'-u', quote(this.v3User || '<USER>'),
					'-l', this.v3Level,
				];
				if (this.v3Level !== 'noAuthNoPriv') {
					parts.push('-a', this.v3AuthProto, '-A', quote(this.v3AuthPass));
				}
				if (this.v3Level === 'authPriv') {
					parts.push('-x', this.v3PrivProto, '-X', quote(this.v3PrivPass));
				}
				if (this.v3EngineID) {
					parts.push('-e', quote(this.v3EngineID));
				}
				parts.push(quote(this.host), "''", this.notif.oid);
				var cmd = parts.join(' ');
				var vbs = this.varbindArgs();
				if (vbs.length === 0) return cmd;
				return cmd + ' \\\n  ' + vbs.join(' \\\n  ');
			},

			generateV1: function () {
				// RFC 2576 SMIv2 -> SMIv1 translation:
				//
				// - For an enterpriseSpecific trap (genericTrap = 6),
				//   the SMIv2 notification's OID splits into an
				//   enterprise + specific-trap pair: the last segment
				//   becomes specific-trap, the prefix becomes the
				//   enterprise OID. Per the SMIv2 notification
				//   convention, a trailing `.0` between the assignment
				//   arc and the specific-trap segment is dropped from
				//   the enterprise. snmpTrapOID.0 is prepended to the
				//   varbind list with the original notification OID
				//   as its OID-typed value.
				//
				// - For a generic trap (0-5: coldStart, warmStart,
				//   linkDown, linkUp, authenticationFailure,
				//   egpNeighborLoss), enterprise is the notification's
				//   parent prefix (drop the last segment) per
				//   RFC 2576 §3.2, specific-trap is forced to 0, and
				//   snmpTrapOID.0 is NOT prepended (the receiver
				//   derives the trap OID from the generic-trap field
				//   itself).
				var oid = (this.notif.oid || '').replace(/^\./, '');
				var parts = oid ? oid.split('.') : [];
				var enterprise = '.' + oid;
				var specific = this.specificTrap;
				var prependTrapOID = false;

				if (this.genericTrap === 6) {
					if (parts.length >= 2) {
						specific = Number(parts[parts.length - 1]);
						var prefix = parts.slice(0, -1);
						// Drop a trailing ".0" between the assignment
						// arc and the specific-trap segment per the
						// SMIv2-style notification convention.
						if (prefix.length > 1 && prefix[prefix.length - 1] === '0') {
							prefix = prefix.slice(0, -1);
						}
						enterprise = '.' + prefix.join('.');
					}
					prependTrapOID = true;
				} else {
					// Generic 0-5: enterprise is the notification's
					// parent prefix (drop last segment); specific = 0.
					if (parts.length >= 2) {
						enterprise = '.' + parts.slice(0, -1).join('.');
					}
					specific = 0;
				}

				var cmdParts = [
					'snmptrap',
					'-v', '1',
					'-c', quote(this.community),
					quote(this.host),
					enterprise,
					quote(this.agentAddr),
					String(this.genericTrap),
					String(specific),
					String(this.uptime),
				];

				var vbs = this.varbindArgs();
				if (prependTrapOID) {
					vbs = [SNMP_TRAP_OID + ' o ' + this.notif.oid].concat(vbs);
				}

				var cmd = cmdParts.join(' ');
				if (vbs.length === 0) return cmd;
				return cmd + ' \\\n  ' + vbs.join(' \\\n  ');
			},

			generated: function () {
				switch (this.version) {
					case 'v2c': return this.generateV2c();
					case 'v3':  return this.generateV3();
					case 'v1':  return this.generateV1();
				}
				return '';
			},

			copyToClipboard: function () {
				var txt = this.generated();
				var self = this;
				if (navigator.clipboard) {
					navigator.clipboard.writeText(txt).then(function () {
						self.copied = true;
						self.copyError = '';
						setTimeout(function () { self.copied = false; }, 2000);
					}, function () {
						self.copyError = 'Copy failed — select the command above and use Cmd-C / Ctrl-C.';
					});
					return;
				}
				// Fallback for non-HTTPS / older browsers without
				// `navigator.clipboard`. Use a transient textarea
				// + execCommand('copy'), and surface a hint if
				// even that fails.
				try {
					var ta = document.createElement('textarea');
					ta.value = txt;
					ta.style.position = 'fixed';
					ta.style.opacity = '0';
					document.body.appendChild(ta);
					ta.select();
					var ok = document.execCommand('copy');
					document.body.removeChild(ta);
					if (ok) {
						self.copied = true;
						self.copyError = '';
						setTimeout(function () { self.copied = false; }, 2000);
					} else {
						self.copyError = 'Copy failed — select the command above and use Cmd-C / Ctrl-C.';
					}
				} catch (_) {
					self.copyError = 'Copy failed — select the command above and use Cmd-C / Ctrl-C.';
				}
			},
		};
	};
})();
