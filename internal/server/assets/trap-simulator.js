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
			indexMode: 'raw-suffix',
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

			open: function () {
				var ul = document.querySelector('.notify-objects');
				if (!ul) return;
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
				this.isOpen = true;
			},

			close: function () {
				this.isOpen = false;
				// Auto-clear credentials on close (S7 mitigation).
				// Community is treated as a default-able non-secret
				// since it's typically "public" for testing — only
				// the v3 auth/priv passwords get wiped.
				this.v3AuthPass = '';
				this.v3PrivPass = '';
				this.copied = false;
			},

			validateEngineID: function () {
				var v = (this.v3EngineID || '').trim();
				if (v === '' || /^[0-9a-fA-F]+$/.test(v)) {
					this.engineIDError = '';
				} else {
					this.engineIDError = 'Engine ID must be hex (0-9, a-f).';
				}
			},

			suffix: function (vb) {
				if (vb.isColumn) {
					if (this.indexMode === 'single-int') {
						return '.' + this.rowIndex;
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
					return quote(v == null ? '' : String(v));
				}
				// Numeric types: empty → 0
				var s = String(v == null ? '' : v).trim();
				return s === '' ? '0' : s;
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
					this.host,
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
					parts.push('-e', this.v3EngineID);
				}
				parts.push(this.host, "''", this.notif.oid);
				var cmd = parts.join(' ');
				var vbs = this.varbindArgs();
				if (vbs.length === 0) return cmd;
				return cmd + ' \\\n  ' + vbs.join(' \\\n  ');
			},

			generateV1: function () {
				// RFC 2576 SMIv2 -> SMIv1 translation, per §3.1:
				//   - For SMIv2 notifications whose OID ends in `.0.N`,
				//     the enterprise OID is the prefix before `.0`,
				//     and the specific-trap is N.
				//   - Otherwise the prefix-before-last-segment becomes
				//     the enterprise OID and the last segment becomes
				//     the specific-trap.
				//   - snmpTrapOID.0 is prepended to the varbind list
				//     with the original notification OID as its value.
				//
				// When the user picks a non-enterpriseSpecific generic
				// trap (0-5 = coldStart, warmStart, linkDown, linkUp,
				// authenticationFailure, egpNeighborLoss), enterprise
				// is taken from the user's input or defaults to the
				// notification's parent prefix; specific = 0.
				var oid = (this.notif.oid || '').replace(/^\./, '');
				var enterprise = '.' + oid;
				var specific = this.specificTrap;

				if (this.genericTrap === 6) {
					var parts = oid.split('.');
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
				}

				var cmdParts = [
					'snmptrap',
					'-v', '1',
					'-c', quote(this.community),
					this.host,
					enterprise,
					this.agentAddr,
					String(this.genericTrap),
					String(specific),
					String(this.uptime),
				];

				// Prepend snmpTrapOID.0 with the original notification
				// OID as its OID-typed value. Then the original
				// varbinds.
				var vbs = [SNMP_TRAP_OID + ' o ' + this.notif.oid].concat(this.varbindArgs());

				var cmd = cmdParts.join(' ');
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
						setTimeout(function () { self.copied = false; }, 2000);
					}, function () {});
				}
			},
		};
	};
})();
