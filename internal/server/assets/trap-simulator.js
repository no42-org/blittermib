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

	// readIndexColumns parses the notify-objects list's
	// `data-trap-index-columns` attribute (a JSON array of
	// {name, syntax, sizeMin, sizeMax, isImplied} entries
	// emitted by the Go template) and initialises a per-column
	// `value` field with a sensible default — empty string for
	// IpAddress and OCTET STRING (so the placeholder shows),
	// zero for INTEGER (so the generated command is valid
	// before the user types anything). Returns [] when the
	// attribute is missing or malformed; the modal then falls
	// back to its scalar-only / raw-suffix UI based on
	// indexMode.
	function readIndexColumns(ul) {
		var raw = ul.dataset.trapIndexColumns;
		if (!raw) return [];
		var parsed;
		try {
			parsed = JSON.parse(raw);
		} catch (_) {
			return [];
		}
		if (!Array.isArray(parsed)) return [];
		var out = [];
		for (var i = 0; i < parsed.length; i++) {
			var col = parsed[i];
			if (!col || typeof col !== 'object') continue;
			var syntax = String(col.syntax || '');
			var defaultValue;
			if (syntax === 'IpAddress' || syntax === 'OCTET STRING' ||
				syntax === 'OBJECT IDENTIFIER' || syntax === 'BITS') {
				// Text-shaped columns start blank so the user
				// sees the placeholder hint rather than a
				// numeric `0` they have to clear before typing.
				defaultValue = '';
			} else if (syntax === 'InetAddressType') {
				// RFC 4001 InetAddressType — default to ipv4(1)
				// since it's the most common variant in real
				// deployments. The user can change to ipv6(2),
				// dns(16), etc. via the select.
				defaultValue = 1;
			} else {
				defaultValue = 0;
			}
			out.push({
				name: String(col.name || ''),
				syntax: syntax,
				sizeMin: Number(col.sizeMin) || 0,
				sizeMax: Number(col.sizeMax) || 0,
				isImplied: Boolean(col.isImplied),
				value: defaultValue,
			});
		}
		return out;
	}

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
			// Per-index-column descriptors: { name, syntax, value }.
			// Populated by open() from the notify-objects list's
			// data-trap-index-columns JSON; the modal walks this
			// to render one type-specific input per column when
			// indexMode === 'indexed'.
			indexColumns: [],
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

			// Row identity (raw-suffix mode only — indexed mode
			// stores per-column values inside indexColumns[].value).
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
				this.indexColumns = readIndexColumns(ul);
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
				this.indexColumns = [];
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
					if (this.indexMode === 'indexed') {
						var parts = '';
						for (var i = 0; i < this.indexColumns.length; i++) {
							parts += this.composeColumn(this.indexColumns[i]);
						}
						return parts;
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

			// composeColumn dispatches per-column suffix composition
			// by `col.syntax`. The classifier ladder by tier:
			//
			//   Tier 1            INTEGER / Integer32-like, IpAddress
			//   Tier 2 commit 2   OCTET STRING (fixed-size)
			//   Tier 2 commit 3   OCTET STRING (variable),
			//                     OBJECT IDENTIFIER
			//
			// For OCTET STRING columns the fixed-vs-variable split
			// is detected from the descriptor: `sizeMin === sizeMax > 0`
			// means a fixed-length field that uses the
			// hex-bytes-with-byte-count composer; everything else
			// (variable range, unbounded) routes to the IMPLIED-
			// aware variable composer that consumes `col.isImplied`
			// to choose length-prefix vs bare-bytes encoding.
			//
			// Unknown syntaxes are composed as a numeric integer —
			// a safe fallback for integer-shaped TCs that the
			// server-side classifier resolved as `indexed` even
			// though the JS doesn't recognise the literal syntax
			// string.
			composeColumn: function (col) {
				if (col.syntax === 'IpAddress') {
					return this.composeIpAddress(col.value);
				}
				if (col.syntax === 'OCTET STRING') {
					if (col.sizeMin > 0 && col.sizeMin === col.sizeMax) {
						return this.composeOctetStringFixed(col);
					}
					return this.composeOctetStringVariable(col);
				}
				if (col.syntax === 'OBJECT IDENTIFIER') {
					return this.composeOID(col);
				}
				if (col.syntax === 'BITS') {
					// BITS encodes as a fixed-length OCTET STRING
					// whose byte count comes from the bits-list's
					// highest-numbered named bit (the server
					// classifier writes it into sizeMin === sizeMax).
					// Same parsing + validation as fixed OCTET STRING
					// — the only delta is the source of the size.
					return this.composeOctetStringFixed(col);
				}
				if (col.syntax === 'InetAddressType') {
					// RFC 4001 InetAddressType — encodes on the wire
					// as a plain integer (the enum value). Reuses
					// composeInteger; the modal renders a `<select>`
					// of the standard enum options so the user picks
					// by name.
					return this.composeInteger(col.value);
				}
				return this.composeInteger(col.value);
			},

			// composeInteger emits ".n" for an INTEGER index column.
			// Empty / non-numeric input yields ".1" so the generated
			// command stays runnable while the user is mid-typing —
			// matches the v1.0 single-int behavior.
			composeInteger: function (value) {
				var n = Number(value);
				if (!isFinite(n)) n = 1;
				return '.' + n;
			},

			// composeIpAddress validates a dotted-quad string and
			// emits ".a.b.c.d". Returns ".<ERROR>" on malformed
			// input — the four octets must each be a non-empty
			// decimal in [0..255], with exactly three dots.
			composeIpAddress: function (value) {
				var s = String(value == null ? '' : value).trim();
				if (s === '') return '.<ERROR>';
				var parts = s.split('.');
				if (parts.length !== 4) return '.<ERROR>';
				var out = '';
				for (var i = 0; i < 4; i++) {
					var octet = parts[i];
					if (octet === '' || /[^0-9]/.test(octet)) return '.<ERROR>';
					var n = Number(octet);
					if (!isFinite(n) || n < 0 || n > 255) return '.<ERROR>';
					out += '.' + n;
				}
				return out;
			},

			// composeOctetStringFixed validates a hex-bytes string
			// against the column's expected fixed length and emits
			// ".N0.N1.…" — N decimal segments, one per byte. The
			// caller's classifier guarantees `col.sizeMin === col.sizeMax`
			// for fixed-size columns; variable-length OCTET STRING
			// indexes use a different code path (raw-suffix today,
			// IMPLIED-aware composer in the next tier).
			//
			// Accepted input shapes (canonicalised before parse):
			//
			//   00:11:22:33:44:55     colon-separated   (preferred)
			//   00 11 22 33 44 55     space-separated
			//   00-11-22-33-44-55     dash-separated
			//   001122334455          no separators
			//
			// All separators are stripped together, so mixed forms
			// like `00:11 22-33:44 55` parse the same. Anything else
			// — wrong byte count, non-hex characters, odd hex length
			// — yields ".<ERROR>" rather than a malformed OID, so
			// the generated command surfaces the problem instead of
			// silently composing garbage.
			composeOctetStringFixed: function (col) {
				var raw = String(col.value == null ? '' : col.value).trim();
				if (raw === '') return '.<ERROR>';
				var hex = raw.replace(/[\s:\-]/g, '');
				if (hex.length === 0 || hex.length % 2 !== 0) return '.<ERROR>';
				if (!/^[0-9a-fA-F]+$/.test(hex)) return '.<ERROR>';
				var bytes = hex.length / 2;
				var want = Number(col.sizeMin) || 0;
				if (want > 0 && bytes !== want) return '.<ERROR>';
				var out = '';
				for (var i = 0; i < hex.length; i += 2) {
					out += '.' + parseInt(hex.substring(i, i + 2), 16);
				}
				return out;
			},

			// composeOctetStringVariable handles the variable-length
			// OCTET STRING path — both unbounded (no SIZE clause) and
			// ranged (`OCTET STRING (SIZE(0..255))`). The encoding
			// depends on `col.isImplied`:
			//
			//   not IMPLIED  →  ".{len}.{b0}.{b1}…"   (length-prefixed)
			//   IMPLIED      →  ".{b0}.{b1}…"         (bare bytes)
			//
			// `col.sizeMax > 0` enforces an upper-bound check (the
			// SIZE constraint's max). Anything outside that range,
			// or non-hex, or odd-length hex yields ".<ERROR>" so the
			// command surfaces the problem rather than emitting a
			// malformed OID.
			composeOctetStringVariable: function (col) {
				var raw = String(col.value == null ? '' : col.value).trim();
				if (raw === '') return '.<ERROR>';
				var hex = raw.replace(/[\s:\-]/g, '');
				if (hex.length === 0 || hex.length % 2 !== 0) return '.<ERROR>';
				if (!/^[0-9a-fA-F]+$/.test(hex)) return '.<ERROR>';
				var bytes = hex.length / 2;
				var maxBytes = Number(col.sizeMax) || 0;
				if (maxBytes > 0 && bytes > maxBytes) return '.<ERROR>';
				var minBytes = Number(col.sizeMin) || 0;
				if (minBytes > 0 && bytes < minBytes) return '.<ERROR>';
				var out = '';
				if (!col.isImplied) {
					out = '.' + bytes;
				}
				for (var i = 0; i < hex.length; i += 2) {
					out += '.' + parseInt(hex.substring(i, i + 2), 16);
				}
				return out;
			},

			// composeOID handles an OBJECT IDENTIFIER index column.
			// The user types a dotted-OID (`.1.3.6.1.4.1.9` or
			// `1.3.6.1.4.1.9` — leading dot is optional and stripped
			// before parsing). Encoding depends on `col.isImplied`:
			//
			//   not IMPLIED  →  ".{len}.{seg0}.{seg1}…"
			//   IMPLIED      →  ".{seg0}.{seg1}…"
			//
			// Each segment must be a non-negative decimal integer.
			// Empty segments (back-to-back dots, leading-trailing
			// stray dot) and non-numeric content yield ".<ERROR>".
			composeOID: function (col) {
				var raw = String(col.value == null ? '' : col.value).trim();
				if (raw === '') return '.<ERROR>';
				if (raw.charAt(0) === '.') raw = raw.substring(1);
				if (raw === '') return '.<ERROR>';
				var parts = raw.split('.');
				var segs = [];
				for (var i = 0; i < parts.length; i++) {
					var p = parts[i];
					if (p === '' || /[^0-9]/.test(p)) return '.<ERROR>';
					var n = Number(p);
					if (!isFinite(n) || n < 0) return '.<ERROR>';
					segs.push(n);
				}
				var out = '';
				if (!col.isImplied) {
					out = '.' + segs.length;
				}
				for (var i = 0; i < segs.length; i++) {
					out += '.' + segs[i];
				}
				return out;
			},

			// octetPlaceholder builds a colon-separated hex hint for
			// OCTET STRING inputs. Fixed-size columns (`sizeMin ===
			// sizeMax > 0`) get an exact-N-pair hint
			// (`00:11:22:33:44:55` for sizeMin=6) so the user
			// immediately sees the required byte count. Variable
			// columns get a generic four-pair example regardless of
			// any lower-bound constraint — the user can type any
			// length within the SIZE range, and the placeholder is
			// just a formatting illustration. The bytes step by
			// 0x11 each so the hint is visually distinct from real-
			// looking addresses (avoids the trap of users thinking
			// the placeholder is a default value to keep).
			octetPlaceholder: function (col) {
				var lo = Number(col && col.sizeMin) || 0;
				var hi = Number(col && col.sizeMax) || 0;
				var n;
				if (lo > 0 && lo === hi) {
					n = lo;
				} else {
					n = 4;
				}
				var parts = [];
				for (var i = 0; i < n; i++) {
					var v = (i * 0x11) % 256;
					var hex = v.toString(16);
					if (hex.length === 1) hex = '0' + hex;
					parts.push(hex);
				}
				return parts.join(':');
			},

			// validateColumn returns a human-readable error string
			// for a per-column input, or '' when the input is valid.
			// Bound to `x-on:input` on each column's `<input>` so the
			// error surface updates live as the user types — matches
			// design.md Decision 6's "live validation" contract.
			//
			// The validator mirrors the composer's parse rules but
			// returns a sentence the user can act on (`Expected 6
			// bytes, got 4.`) rather than the `.<ERROR>` sentinel
			// the composer emits into the OID. The two stay in
			// lock-step: any input where `validateColumn` returns
			// '' must produce a non-`<ERROR>` suffix from
			// `composeColumn`, and vice versa.
			validateColumn: function (col) {
				if (col.syntax === 'IpAddress') {
					var v = String(col.value == null ? '' : col.value).trim();
					if (v === '') return 'Enter a dotted-quad IP (e.g. 10.0.0.1).';
					if (this.composeIpAddress(v) === '.<ERROR>') {
						return 'IP must be four octets in 0..255 (a.b.c.d).';
					}
					return '';
				}
				if (col.syntax === 'OCTET STRING' || col.syntax === 'BITS') {
					var raw = String(col.value == null ? '' : col.value).trim();
					if (raw === '') return 'Enter hex bytes (e.g. 00:11:22:33).';
					var hex = raw.replace(/[\s:\-]/g, '');
					if (hex.length === 0) return 'Empty value.';
					if (!/^[0-9a-fA-F]+$/.test(hex)) {
						return 'Only hex digits 0-9, a-f permitted.';
					}
					if (hex.length % 2 !== 0) {
						return 'Hex byte count must be even (two chars per byte).';
					}
					var bytes = hex.length / 2;
					var lo = Number(col.sizeMin) || 0;
					var hi = Number(col.sizeMax) || 0;
					if (lo > 0 && lo === hi) {
						if (bytes !== lo) {
							return 'Expected ' + lo + ' bytes, got ' + bytes + '.';
						}
					} else {
						if (lo > 0 && bytes < lo) {
							return 'At least ' + lo + ' bytes required.';
						}
						if (hi > 0 && bytes > hi) {
							return 'At most ' + hi + ' bytes permitted.';
						}
					}
					return '';
				}
				if (col.syntax === 'OBJECT IDENTIFIER') {
					var v = String(col.value == null ? '' : col.value).trim();
					if (v === '') return 'Enter a dotted OID (e.g. .1.3.6.1.4.1.9).';
					if (this.composeOID(col) === '.<ERROR>') {
						return 'OID must be a dotted sequence of non-negative integers.';
					}
					return '';
				}
				// INTEGER / fallback — accept anything that parses
				// finite. Empty stays valid (defaults to 0/1 in
				// composeInteger), matching the v1.0 single-int UX.
				return '';
			},

			// composedSuffixPreview walks indexColumns and concatenates
			// each per-column composer's output, giving the user an
			// at-a-glance view of the dotted suffix that will be
			// appended to every column varbind's OID. Empty when the
			// modal isn't in indexed mode (scalar-only and raw-suffix
			// have no per-column row identity to preview).
			composedSuffixPreview: function () {
				if (this.indexMode !== 'indexed') return '';
				var parts = '';
				for (var i = 0; i < this.indexColumns.length; i++) {
					parts += this.composeColumn(this.indexColumns[i]);
				}
				return parts;
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
