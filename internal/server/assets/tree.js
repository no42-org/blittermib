// blittermib OID tree island — vanilla JS.
//
// Lazy-loads the OID hierarchy via /api/v1/tree?parent={oid}. Each
// node is a <li> with an expand/collapse button and a link to its
// /s/{module}::{name} page. Children are fetched on first expand
// and cached in the DOM.
//
// Re-attaches to the [data-tree] container after htmx partial
// swaps bring one in.

(function () {
	'use strict';

	const TREE_API = '/api/v1/tree';
	// Empty parent = the OID apex; its children are the top arcs (normally
	// just iso(1); the 0 null-sentinel arc is omitted server-side).
	const ROOT_OID = '';

	// cfg is per-page tree configuration, read from the [data-tree]
	// container in buildInitial. `mode` is 'standalone' (the /tree page —
	// roots at focus, names link to /s/) or 'workspace' (the /m/ left
	// pane — roots at the apex, expands the spine down to the focus, and
	// names drive in-page detail-pane navigation).
	let cfg = { mode: 'standalone', focus: '', module: '', scope: '' };

	// Container kinds drill in (scope change); everything else is a leaf
	// that just swaps the detail pane. Mirrors web.KindHasChildren /
	// WorkspaceRowURL (internal/web/helpers.go) — keep in sync.
	const CONTAINER_KINDS = new Set([
		'table', 'table-entry', 'object-identity', 'module-identity',
	]);

	function escape(s) {
		const d = document.createElement('div');
		d.textContent = s == null ? '' : String(s);
		return d.innerHTML;
	}

	function parentOf(oid) {
		const i = oid.lastIndexOf('.');
		return i > 0 ? oid.slice(0, i) : '';
	}

	function lastSeg(oid) {
		const i = oid.lastIndexOf('.');
		const s = parseInt(i >= 0 ? oid.slice(i + 1) : oid, 10);
		return isNaN(s) ? 0 : s;
	}

	function oidUnderPrefix(oid, prefix) {
		return oid === prefix || oid.indexOf(prefix + '.') === 0;
	}

	// cssEsc escapes an OID for use inside a CSS attribute selector value
	// (CSS.escape where available — OIDs are digits and dots, so a missing
	// CSS.escape is harmless).
	function cssEsc(s) {
		return window.CSS && CSS.escape ? CSS.escape(s) : s;
	}

	// A folded row stands in for a run of OIDs from its DIRECT child
	// (data-direct) down to its ANCHOR (data-oid). rowCovers tests whether
	// `oid` lies on that run — used to resolve a folded-away ancestor to
	// the single row that represents it.
	function rowCovers(node, oid) {
		const direct = node.dataset.direct || node.dataset.oid;
		return oidUnderPrefix(oid, direct) && oidUnderPrefix(node.dataset.oid, oid);
	}

	// rowFor finds the rendered row representing `oid`: an exact anchor
	// match, or the folded row whose run covers it (its ancestors may have
	// been compressed away, so no node carries `oid` as its own data-oid).
	//
	// The covering row's data-direct is one of `oid`'s ancestor prefixes
	// and its anchor (data-oid) is a descendant-or-equal of `oid`. So probe
	// `oid`'s prefixes by attribute index (≤ depth lookups) rather than
	// scanning every rendered node.
	function rowFor(root, oid) {
		const exact = treeNode(root, oid);
		if (exact) return exact;
		for (let cur = oid; cur; cur = parentOf(cur)) {
			const n = root.querySelector('.tree-node[data-direct="' + cssEsc(cur) + '"]');
			if (n && oidUnderPrefix(n.dataset.oid, oid)) return n;
		}
		return null;
	}

	// deepestAncestorRow returns the rendered row whose anchor is the
	// longest strict prefix of `focus` — the deepest point the spine has
	// reached so far, from which to expand one level further toward focus.
	// An ancestor row's anchor (data-oid) is by definition an exact prefix
	// of focus, so walk focus's prefixes longest-first and return the first
	// anchored row (≤ depth lookups), not a full-tree scan.
	function deepestAncestorRow(root, focus) {
		for (let cur = parentOf(focus); cur; cur = parentOf(cur)) {
			const n = treeNode(root, cur);
			if (n) return n;
		}
		return null;
	}

	// childTowards returns the direct child of `anchor` that lies on the
	// path to `focus` (anchor + the next focus segment), or '' if focus is
	// not below anchor.
	function childTowards(anchor, focus) {
		if (anchor === focus || !oidUnderPrefix(focus, anchor)) return '';
		const seg = focus.slice(anchor.length + 1).split('.')[0];
		return seg ? anchor + '.' + seg : '';
	}

	// workspaceRowURL mirrors web.WorkspaceRowURL for a symbol-backed
	// node. It ALWAYS targets the node's OWN module (item.module) — in a
	// global tree most nodes belong to other modules, and a leaf must
	// route to /m/{its module}, not the page's current module. Scope is
	// preserved only for a same-module leaf that lives under the current
	// scope; otherwise it scopes to the leaf's parent. Containers drill.
	//
	// Selection is by NAME (unique per module), like web.WorkspaceRowURL —
	// so a click resolves to THIS symbol even on a buggy MIB that shares
	// its OID with another (a bare-OID sel would hit the tie-break winner).
	function workspaceRowURL(item) {
		const m = encodeURIComponent(item.module);
		const sel = '?sel=' + encodeURIComponent(item.name);
		if (!CONTAINER_KINDS.has(item.kind)) {
			if (item.module === cfg.module && cfg.scope && oidUnderPrefix(item.oid, cfg.scope)) {
				return '/m/' + m + '/' + encodeURIComponent(cfg.scope) + sel;
			}
			const p = parentOf(item.oid);
			if (p) return '/m/' + m + '/' + encodeURIComponent(p) + sel;
			return '/m/' + m + sel;
		}
		return '/m/' + m + '/' + encodeURIComponent(item.oid) + sel;
	}

	// currentFamily is the kind-chip family applied to the workspace tree
	// ('' = all). Initialised from the persisted chip so a reload respects
	// the active filter; updated by the blittermib:kindfilter event.
	let currentFamily = readKindFamily();

	// normFamily maps a kind-chip value to the tree's family param, '' for
	// 'all' or anything unrecognised.
	function normFamily(v) {
		return v === 'scalar' || v === 'table' || v === 'notif' ? v : '';
	}

	function readKindFamily() {
		try {
			return normFamily(sessionStorage.getItem('blittermib-kind-filter'));
		} catch (e) {
			return '';
		}
	}

	// branchesParam asks the server to hide leaf objects (the workspace
	// "container map" — leaves are listed in the center pane) and, when a
	// kind chip is active, to prune to that family's branches. The
	// standalone /tree browser omits it and shows every OID.
	function branchesParam() {
		if (cfg.mode !== 'workspace') return '';
		return currentFamily ? '&branches=1&family=' + currentFamily : '&branches=1';
	}

	// fetchPage returns one keyset page of children plus the cursor for
	// the next page (null when the level is exhausted). `after` is the
	// last OID of the previous page (or an anchor OID for spine expansion).
	async function fetchPage(parent, after) {
		let url = TREE_API + '?parent=' + encodeURIComponent(parent || ROOT_OID) + branchesParam();
		if (after) url += '&after=' + encodeURIComponent(after);
		const res = await fetch(url);
		if (!res.ok) throw new Error('tree fetch ' + res.status);
		const data = await res.json();
		return { items: data.children || [], nextAfter: data.nextAfter || null };
	}

	// fetchPageBefore returns the page of children immediately PRECEDING
	// `before` (ascending), plus prevBefore (the cursor for the page
	// before that, or null at the level's start). Powers "show earlier".
	async function fetchPageBefore(parent, before) {
		const url = TREE_API + '?parent=' + encodeURIComponent(parent || ROOT_OID) + branchesParam() +
			'&before=' + encodeURIComponent(before);
		const res = await fetch(url);
		if (!res.ok) throw new Error('tree fetch ' + res.status);
		const data = await res.json();
		return { items: data.children || [], prevBefore: data.prevBefore || null };
	}

	function makeNode(item, level) {
		const li = document.createElement('li');
		li.className = 'tree-node';
		// data-oid is the ANCHOR (deepest node of a folded run) — what
		// expand/select address; data-direct is the run's direct child
		// under the parent — the keyset cursor key and the basis for
		// rowCovers. They coincide on an unfolded row.
		li.dataset.oid = item.oid;
		li.dataset.direct = item.directOID || item.oid;
		li.dataset.expanded = 'false';
		// hasChildren is the EXPANDABLE signal from the server — in the
		// workspace container map it is "has a child the tree will render",
		// so a table-entry whose only children are leaf columns is a
		// non-expandable tree leaf even though childCount > 0 (the badge).
		const hasChildren = !!item.hasChildren;
		li.dataset.hasChildren = hasChildren ? 'true' : 'false';
		// ARIA tree pattern (WCAG 4.1.2): the <li> is the focusable
		// treeitem. Roving tabindex — every node starts at -1; exactly
		// one node carries tabindex=0 (set by setRovingTo) so the tree is
		// a single Tab stop and arrows move between nodes.
		li.setAttribute('role', 'treeitem');
		li.setAttribute('aria-level', String(level));
		li.tabIndex = -1;
		if (hasChildren) li.setAttribute('aria-expanded', 'false');
		// Workspace mode highlights the focused (searched/selected) node.
		// The focus may be folded INTO this row (an ancestor compressed
		// away), so test run-coverage, not just anchor equality.
		if (cfg.mode === 'workspace' && cfg.focus && rowCovers(li, cfg.focus)) {
			li.classList.add('tree-selected');
			li.setAttribute('aria-selected', 'true');
		}

		const row = document.createElement('div');
		row.className = 'tree-row';

		const btn = document.createElement('button');
		btn.type = 'button';
		btn.className = 'tree-expand';
		// The treeitem itself conveys expand state and is keyboard-driven
		// (←/→), so the button is a mouse affordance only: hide it from
		// AT and take it out of the tab order to avoid a double-announce.
		btn.setAttribute('aria-hidden', 'true');
		btn.tabIndex = -1;
		btn.dataset.action = 'expand';
		btn.textContent = hasChildren ? '▸' : ' ';
		if (!hasChildren) btn.disabled = true;
		row.appendChild(btn);

		const num = document.createElement('span');
		num.className = 'tree-num';
		num.textContent = '.' + (item.position || '');
		row.appendChild(num);

		// A folded row shows the dotted name-path of its run
		// ("iso.org.dod"); the row links/expands as a whole to the anchor
		// (its deepest node). An unfolded row's namePath is just its name.
		const np = item.namePath || item.name || ('.' + (item.position || ''));

		// Symbol-backed anchors link to /s/{module}::{name}. Synthetic
		// bridge anchors (item.hasSymbol === false) have no symbol page —
		// render the name-path (IANA canonical when known, else the numeric
		// segment) as plain text so the subtree is still navigable.
		let nameEl;
		if (item.hasSymbol) {
			nameEl = document.createElement('a');
			if (cfg.mode === 'workspace') {
				nameEl.href = workspaceRowURL(item);
				// Same-module clicks swap the detail pane in place
				// (data-nav → htmx partial). Cross-module clicks navigate
				// NATIVELY to the other module's workspace: a data-nav
				// partial would hit the server's cross-module guard, which
				// HX-Refreshes the current (wrong) URL — so the target
				// module would be unreachable by click.
				if (item.module === cfg.module) {
					nameEl.setAttribute('data-nav', '');
				}
			} else {
				nameEl.href = '/s/' + encodeURIComponent(item.module + '::' + item.name);
			}
			nameEl.textContent = np;
		} else if (cfg.mode === 'workspace' && cfg.module) {
			// Synthetic bridge (no /s/ page) — but in the workspace it can
			// still SCOPE the list to its OID. Its descendants belong to the
			// module being viewed (e.g. the `{trapGroup 0}` wrapper that
			// holds a module's notifications), so scope to the OID within the
			// current module rather than leaving it a dead, unclickable span.
			nameEl = document.createElement('a');
			nameEl.href = '/m/' + encodeURIComponent(cfg.module) + '/' + encodeURIComponent(item.oid);
			nameEl.setAttribute('data-nav', '');
			nameEl.textContent = np;
			nameEl.classList.add('tree-name-synthetic');
		} else {
			nameEl = document.createElement('span');
			nameEl.textContent = np;
			nameEl.classList.add('tree-name-synthetic');
		}
		nameEl.classList.add('tree-name');
		// Not a separate tab stop; Enter on the treeitem follows it.
		nameEl.tabIndex = -1;
		row.appendChild(nameEl);

		const meta = document.createElement('span');
		meta.className = 'tree-meta';
		// Synthetic nodes carry no module/kind; leave the meta empty.
		meta.textContent = item.hasSymbol ? (item.module + ' · ' + item.kind) : '';
		row.appendChild(meta);

		// Child-count badge — the figure that makes a folded row legible
		// (".1.3.6 iso.org.dod" still has 1 child below). Anchor's count.
		if (item.childCount > 0) {
			const badge = document.createElement('span');
			badge.className = 'tree-badge';
			badge.textContent = String(item.childCount);
			row.appendChild(badge);
		}

		li.appendChild(row);
		return li;
	}

	// makeMore builds the "show more" sentinel appended after a partial
	// level. Clicking it fetches the next keyset page (carried in its
	// data-* attributes) and inserts those nodes ahead of itself.
	function makeMore(parent, after, level) {
		const li = document.createElement('li');
		li.className = 'tree-more';
		const btn = document.createElement('button');
		btn.type = 'button';
		btn.className = 'tree-more-btn';
		btn.dataset.parent = parent;
		btn.dataset.after = after;
		btn.dataset.level = String(level);
		btn.textContent = 'Show more…';
		li.appendChild(btn);
		return li;
	}

	// appendPage appends a page of children to `ul`, then a load-more
	// sentinel when the server reported a next cursor.
	function appendPage(ul, parent, page, level) {
		page.items.forEach((item) => ul.appendChild(makeNode(item, level)));
		if (page.nextAfter) ul.appendChild(makeMore(parent, page.nextAfter, level));
	}

	async function loadMore(btn) {
		const ul = btn.closest('ul');
		const li = btn.closest('.tree-more');
		if (!ul || !li) return;
		const parent = btn.dataset.parent;
		const level = parseInt(btn.dataset.level || '1', 10);
		btn.disabled = true;
		btn.textContent = 'Loading…';
		try {
			const page = await fetchPage(parent, btn.dataset.after);
			page.items.forEach((item) => ul.insertBefore(makeNode(item, level), li));
			if (page.nextAfter) {
				btn.dataset.after = page.nextAfter;
				btn.disabled = false;
				btn.textContent = 'Show more…';
			} else {
				ul.removeChild(li);
			}
		} catch (err) {
			btn.disabled = false;
			btn.textContent = 'Show more (retry)…';
			console.warn('tree load-more failed', err);
		}
	}

	// makeEarlier builds a "show earlier" sentinel placed at the TOP of a
	// level the spine opened anchored mid-way (through a wide node).
	// Clicking it PREPENDS the preceding page (backward keyset) without
	// disturbing the already-rendered window or its expanded descendants.
	function makeEarlier(parent, level) {
		const li = document.createElement('li');
		li.className = 'tree-more tree-earlier';
		const btn = document.createElement('button');
		btn.type = 'button';
		btn.className = 'tree-more-btn';
		btn.dataset.earlier = '1';
		btn.dataset.parent = parent;
		btn.dataset.level = String(level);
		btn.textContent = '⤒ Show earlier…';
		li.appendChild(btn);
		return li;
	}

	async function loadEarlier(btn) {
		const li = btn.closest('.tree-more');
		const ul = btn.closest('ul');
		if (!ul || !li) return;
		const parent = btn.dataset.parent;
		const level = parseInt(btn.dataset.level || '1', 10);
		// The 'before' bound is the level's current first real node — so
		// we fetch the page just below it and prepend, never touching the
		// existing (possibly expanded) window. The keyset is on the DIRECT
		// child's segment, so bound by data-direct (the anchor in data-oid
		// may sit several folded segments deeper, giving the wrong seg).
		const firstNode = ul.querySelector(':scope > .tree-node');
		const firstDirect = firstNode && (firstNode.dataset.direct || firstNode.dataset.oid);
		if (!firstDirect) {
			ul.removeChild(li);
			return;
		}
		btn.disabled = true;
		btn.textContent = 'Loading…';
		try {
			const page = await fetchPageBefore(parent, firstDirect);
			const frag = document.createDocumentFragment();
			page.items.forEach((item) => frag.appendChild(makeNode(item, level)));
			ul.insertBefore(frag, firstNode);
			if (page.prevBefore) {
				btn.disabled = false;
				btn.textContent = '⤒ Show earlier…';
			} else {
				ul.removeChild(li); // reached the level's first child
			}
		} catch (err) {
			btn.disabled = false;
			btn.textContent = '⤒ Show earlier (retry)…';
			console.warn('tree load-earlier failed', err);
		}
	}

	// childLevel reads a node's aria-level to assign its children the
	// next level down (defaults to 1 so a missing attribute is safe).
	function childLevel(node) {
		const l = parseInt(node.getAttribute('aria-level') || '1', 10);
		return (isNaN(l) ? 1 : l) + 1;
	}

	// treeNode finds a rendered node by OID within a tree root.
	function treeNode(root, oid) {
		return root.querySelector('.tree-node[data-oid="' + cssEsc(oid) + '"]');
	}

	async function expand(node) {
		if (node.dataset.expanded === 'true') return;
		if (node.dataset.hasChildren !== 'true') return;

		node.dataset.expanded = 'true';
		node.setAttribute('aria-expanded', 'true');
		const btn = node.querySelector('.tree-expand');
		if (btn) btn.textContent = '▾';

		let children = node.querySelector(':scope > .tree-children');
		if (children) {
			children.hidden = false;
			return; // already populated
		}

		children = document.createElement('ul');
		children.className = 'tree-children';
		children.setAttribute('role', 'group');
		const placeholder = document.createElement('li');
		placeholder.className = 'tree-loading';
		placeholder.textContent = 'Loading…';
		children.appendChild(placeholder);
		node.appendChild(children);

		try {
			const page = await fetchPage(node.dataset.oid);
			children.removeChild(placeholder);
			if (page.items.length === 0) {
				// The node turned out to be a leaf. Reset the full
				// expanded/hasChildren/aria-expanded triad so it stays
				// consistent — otherwise dataset.expanded lingers at
				// 'true' and a later collapse()/ArrowLeft re-adds
				// aria-expanded to a childless node (invalid ARIA).
				node.dataset.expanded = 'false';
				node.dataset.hasChildren = 'false';
				node.removeAttribute('aria-expanded');
				children.hidden = true;
				if (btn) btn.disabled = true;
				return;
			}
			appendPage(children, node.dataset.oid, page, childLevel(node));
		} catch (err) {
			placeholder.textContent = 'Failed to load.';
			placeholder.classList.add('tree-error');
			console.warn('tree expand failed', err);
		}
	}

	// loadAnchored expands `node` to reveal a specific child `childOID`.
	// It first tries the level's NORMAL first page — the common case
	// (narrow levels like iso → org → dod) renders fully with no extra
	// affordance. Only when the spine child is BEYOND the first page (a
	// genuinely wide arc, e.g. enterprises with a high-PEN vendor) does it
	// re-fetch the keyset page anchored at the child and add a "show
	// earlier" affordance — so "show earlier" appears only where siblings
	// were actually skipped. No-op if the child is already rendered.
	async function loadAnchored(node, childOID) {
		if (node.dataset.hasChildren !== 'true') return;
		const root = node.closest('[data-tree]');
		if (root && rowFor(root, childOID)) {
			// Already present (level previously loaded) — the child may be
			// folded into a deeper row, so test coverage, not exact OID.
			if (node.dataset.expanded !== 'true') expand(node);
			return;
		}
		let children = node.querySelector(':scope > .tree-children');
		if (children) {
			// Level was loaded but this window doesn't include the spine
			// child (the user paged past it via "Show more"). Reload so the
			// spine can continue — dropping the prior window for THIS level.
			node.removeChild(children);
		}
		node.dataset.expanded = 'true';
		node.setAttribute('aria-expanded', 'true');
		const chevron = node.querySelector(':scope > .tree-row > .tree-expand');
		if (chevron) chevron.textContent = '▾';
		children = document.createElement('ul');
		children.className = 'tree-children';
		children.setAttribute('role', 'group');
		node.appendChild(children);

		const parentOID = node.dataset.oid;
		let page = await fetchPage(parentOID, '');
		let anchored = false;
		// Each item is keyed by its DIRECT child under parentOID; childOID
		// IS that direct child, so match on directOID (the anchor may sit
		// deeper after folding).
		if (!page.items.some((it) => (it.directOID || it.oid) === childOID)) {
			// The child isn't on the first page → this is a wide level;
			// jump to the page that starts at the child, and offer a way
			// back to the earlier siblings we skipped.
			const s = lastSeg(childOID);
			if (s > 0) {
				page = await fetchPage(parentOID, parentOID + '.' + (s - 1));
				anchored = true;
			}
		}
		if (anchored) children.appendChild(makeEarlier(parentOID, childLevel(node)));
		appendPage(children, parentOID, page, childLevel(node));
	}

	// expandSpineTo expands the tree from the apex down to `focusOID`,
	// anchoring each wide level at the next spine node, then selects and
	// scrolls the focus into view.
	//
	// Path compression means the focus OID's numeric ancestors are NOT all
	// rendered rows — a single-child run (e.g. iso→org→dod) is one row
	// whose anchor sits several segments below the parent. So the walk is
	// driven by the rendered rows: from the deepest row that is an ancestor
	// of focus, expand toward focus by its anchor's next child, and repeat
	// until a row covers focus (or the spine genuinely breaks).
	async function expandSpineTo(root, focusOID) {
		// Bounded by OID depth; the guard only backstops a data anomaly.
		let prevAnchor = '';
		for (let guard = 0; guard < 64; guard++) {
			if (rowFor(root, focusOID)) break; // focus rendered/covered — done
			const ancestor = deepestAncestorRow(root, focusOID);
			if (!ancestor) break; // spine broke (apex page didn't include it)
			// Deepest ancestor is a tree-leaf container (e.g. focus is a leaf
			// object hidden from the container map) — stop and highlight it.
			if (ancestor.dataset.hasChildren !== 'true') break;
			// No progress since the last expand (e.g. a stale focus whose
			// child doesn't exist) — stop rather than spin.
			if (ancestor.dataset.oid === prevAnchor) break;
			prevAnchor = ancestor.dataset.oid;
			const child = childTowards(ancestor.dataset.oid, focusOID);
			if (!child) break;
			await loadAnchored(ancestor, child);
		}
		// The focus may itself be hidden (a leaf object in the container
		// map); fall back to the deepest container that holds it so the tree
		// still shows "which scope am I in". Return it for the caller to
		// highlight (callers own selection state / generation guarding).
		const target = rowFor(root, focusOID) || deepestAncestorRow(root, focusOID);
		if (target) {
			// Make it the single tab stop and scroll into view, but DON'T
			// steal focus on load (that would yank the caret into the tree
			// the moment the page renders).
			root.querySelectorAll('.tree-node').forEach((n) => { n.tabIndex = -1; });
			target.tabIndex = 0;
			if (target.scrollIntoView) target.scrollIntoView({ block: 'center' });
		}
		return target;
	}

	function collapse(node) {
		if (node.dataset.expanded !== 'true') return;
		node.dataset.expanded = 'false';
		node.setAttribute('aria-expanded', 'false');
		const btn = node.querySelector('.tree-expand');
		if (btn) btn.textContent = '▸';
		const children = node.querySelector(':scope > .tree-children');
		if (children) children.hidden = true;
	}

	function onClick(e) {
		const more = e.target.closest('.tree-more-btn');
		if (more) {
			e.preventDefault();
			if (more.dataset.earlier) {
				loadEarlier(more);
			} else {
				loadMore(more);
			}
			return;
		}
		const btn = e.target.closest('.tree-expand');
		if (!btn) return;
		const node = btn.closest('.tree-node');
		if (!node) return;
		e.preventDefault();
		if (node.dataset.expanded === 'true') {
			collapse(node);
		} else {
			expand(node);
		}
	}

	// visibleNodes returns the treeitems currently on screen (collapsed
	// branches are hidden, so their descendants drop out), in DOM/visual
	// order — the sequence ↑/↓ walks.
	function visibleNodes(root) {
		return Array.prototype.filter.call(
			root.querySelectorAll('.tree-node'),
			(n) => n.offsetParent !== null);
	}

	// setRovingTo makes `node` the single tabbable treeitem and focuses
	// it — the roving-tabindex half of the ARIA tree keyboard contract.
	function setRovingTo(node) {
		const root = node.closest('[data-tree]');
		if (root) {
			root.querySelectorAll('.tree-node').forEach((n) => { n.tabIndex = -1; });
		}
		node.tabIndex = 0;
		node.focus();
	}

	// Full ARIA tree keyboard model (WCAG 2.1.1), active while focus is
	// on a treeitem: ↑/↓ move between visible nodes, → expands then
	// descends, ← collapses then ascends, Enter follows the link, Space
	// toggles, Home/End jump to the ends.
	function onKey(e) {
		const node = e.target.closest('.tree-node');
		if (!node) return;
		const root = e.target.closest('[data-tree]');
		if (!root) return;

		switch (e.key) {
			case 'ArrowDown':
			case 'ArrowUp': {
				e.preventDefault();
				const items = visibleNodes(root);
				const i = items.indexOf(node);
				if (i < 0) return;
				const next = e.key === 'ArrowDown' ? items[i + 1] : items[i - 1];
				if (next) setRovingTo(next);
				break;
			}
			case 'ArrowRight':
				e.preventDefault();
				if (node.dataset.hasChildren === 'true' && node.dataset.expanded !== 'true') {
					expand(node);
				} else if (node.dataset.expanded === 'true') {
					const child = node.querySelector(':scope > .tree-children > .tree-node');
					if (child) setRovingTo(child);
				}
				break;
			case 'ArrowLeft': {
				e.preventDefault();
				if (node.dataset.expanded === 'true') {
					collapse(node);
				} else {
					const parent = node.parentElement && node.parentElement.closest('.tree-node');
					if (parent) setRovingTo(parent);
				}
				break;
			}
			case 'Enter': {
				const link = node.querySelector(':scope > .tree-row > .tree-name');
				if (link) { e.preventDefault(); link.click(); }
				break;
			}
			case ' ':
			case 'Spacebar':
				// Always swallow Space on a focused treeitem so it never
				// page-scrolls; toggle expand/collapse when the node has
				// children, otherwise it's a no-op.
				e.preventDefault();
				if (node.dataset.hasChildren === 'true') {
					node.dataset.expanded === 'true' ? collapse(node) : expand(node);
				}
				break;
			case 'Home': {
				e.preventDefault();
				const items = visibleNodes(root);
				if (items[0]) setRovingTo(items[0]);
				break;
			}
			case 'End': {
				e.preventDefault();
				const items = visibleNodes(root);
				if (items.length) setRovingTo(items[items.length - 1]);
				break;
			}
		}
	}

	// Keep the roving tabindex in sync when a node is focused by mouse,
	// so a later Tab returns to where the user last was.
	function onFocusIn(e) {
		const node = e.target.closest && e.target.closest('.tree-node');
		if (!node) return;
		const root = node.closest('[data-tree]');
		if (!root) return;
		root.querySelectorAll('.tree-node').forEach((n) => {
			if (n !== node) n.tabIndex = -1;
		});
		node.tabIndex = 0;
	}

	// buildGen serialises rebuilds: a chip toggle (or reload) can start a
	// new buildInitial while an earlier one is mid-await. Each run stamps a
	// generation and bails after every await once a newer run has started,
	// so overlapping rebuilds never append into each other's (detached) DOM
	// or run two spine walks on the same container.
	let buildGen = 0;

	async function buildInitial(container) {
		const gen = ++buildGen;
		cfg = {
			mode: container.dataset.treeMode === 'workspace' ? 'workspace' : 'standalone',
			focus: container.dataset.treeFocus || '',
			module: container.dataset.treeModule || '',
			scope: container.dataset.treeScope || '',
		};
		// Standalone roots AT the focus (or the apex); workspace roots at
		// the apex and expands the spine down TO the focus.
		const parent = cfg.mode === 'workspace' ? ROOT_OID : (cfg.focus || ROOT_OID);
		container.innerHTML = '';

		const ul = document.createElement('ul');
		ul.className = 'tree-children tree-root-list';
		ul.setAttribute('role', 'tree');
		ul.setAttribute('aria-label', 'OID tree');
		container.appendChild(ul);

		try {
			const page = await fetchPage(parent);
			if (gen !== buildGen) return; // superseded by a newer rebuild
			if (page.items.length === 0) {
				ul.innerHTML = '<li class="tree-empty">No OIDs under <code>' + escape(parent || 'the root') + '</code>.</li>';
				return;
			}
			appendPage(ul, parent, page, 1);
			// Seed the roving tabindex on the first node so the tree is
			// reachable with a single Tab.
			const first = ul.querySelector(':scope > .tree-node');
			if (first) first.tabIndex = 0;
		} catch (err) {
			container.innerHTML = '<div class="tree-error">Failed to load tree.</div>';
			console.warn('tree init failed', err);
			return;
		}
		// Spine expansion is best-effort and SEPARATELY guarded: a
		// deep-level fetch failure must not discard the apex/upper levels
		// already rendered above.
		if (cfg.mode === 'workspace' && cfg.focus) {
			try {
				const target = await expandSpineTo(container, cfg.focus);
				if (gen !== buildGen) return; // superseded mid-spine-walk
				// Highlight the resolved row. makeNode already selects a row
				// whose run COVERS the focus; this also covers the case where
				// the focus is a hidden leaf and `target` is its container.
				if (target) markSelected(container, target);
			} catch (err) {
				console.warn('tree spine expand failed', err);
			}
		}
	}

	// currentWorkspaceSelection reads the selection from a workspace URL
	// (/m/{module}/{scope}?sel={oid|name}). The focus OID is the `sel`
	// when it looks like an OID, else the scope. SMI names start with a
	// letter, so a leading digit reliably marks an OID.
	function currentWorkspaceSelection() {
		const m = location.pathname.match(/^\/m\/([^/]+)(?:\/([^/?]+))?/);
		if (!m) return null;
		const scope = m[2] ? decodeURIComponent(m[2]) : '';
		const sel = new URLSearchParams(location.search).get('sel') || '';
		const oidLike = /^[0-9]+(\.[0-9]+)*$/;
		const focus = oidLike.test(sel) ? sel : scope;
		return { module: decodeURIComponent(m[1]), scope: scope, focus: focus };
	}

	function clearSelection(container) {
		container.querySelectorAll('.tree-selected').forEach((n) => {
			n.classList.remove('tree-selected');
			n.removeAttribute('aria-selected');
		});
	}

	// markSelected highlights `node` and moves the tree's single tab stop
	// onto it, so the roving tabindex stays in lockstep with the visible
	// selection (without stealing focus).
	function markSelected(container, node) {
		node.classList.add('tree-selected');
		node.setAttribute('aria-selected', 'true');
		container.querySelectorAll('.tree-node').forEach((n) => { n.tabIndex = -1; });
		node.tabIndex = 0;
	}

	// selectGen serializes overlapping syncs: a slower (stale) spine walk
	// must not re-apply its highlight/scroll over a newer navigation.
	let selectGen = 0;

	// syncSelection keeps the workspace tree's highlight + expansion in
	// step with in-page (htmx) navigation: after the detail pane swaps,
	// the URL names a new selection, so move the highlight and, if the
	// node isn't rendered yet, expand the spine down to it.
	async function syncSelection() {
		if (cfg.mode !== 'workspace') return;
		const cur = currentWorkspaceSelection();
		if (!cur) return;
		cfg.module = cur.module;
		cfg.scope = cur.scope;
		if (cur.focus === cfg.focus) return; // selection unchanged
		cfg.focus = cur.focus;
		const container = document.querySelector('[data-tree]');
		if (!container) return;
		const gen = ++selectGen;
		// Clear unconditionally — including when navigating to a name /
		// no-OID selection (a TC), which has nothing to highlight.
		clearSelection(container);
		if (!cur.focus) return;
		// rowFor (not treeNode): the selection's ancestors may be folded,
		// so it can be represented by a covering row rather than its own.
		let node = rowFor(container, cur.focus);
		if (!node) {
			try {
				await expandSpineTo(container, cur.focus);
			} catch (err) {
				console.warn('tree sync expand failed', err);
				return;
			}
			if (gen !== selectGen) return; // superseded by a newer nav
			// rowFor first; for a hidden leaf selection, fall back to the
			// container row that holds it.
			node = rowFor(container, cur.focus) || deepestAncestorRow(container, cur.focus);
		}
		if (node) markSelected(container, node);
	}

	let listenersBound = false;

	function bindGlobals() {
		if (listenersBound) return;
		document.addEventListener('click', onClick);
		document.addEventListener('keydown', onKey);
		document.addEventListener('focusin', onFocusIn);
		document.body.addEventListener('htmx:afterSwap', function () {
			attach();
			syncSelection();
		});
		// The kind chips (Alpine, in workspace.js) dispatch this when the
		// active family changes; re-filter the tree's container map to it
		// and re-reveal the current selection.
		window.addEventListener('blittermib:kindfilter', function (e) {
			const fam = normFamily(e && e.detail);
			if (fam === currentFamily) return;
			currentFamily = fam;
			const container = document.querySelector('[data-tree]');
			if (!container || container.dataset.treeBuilt !== 'true') return;
			// data-tree-focus is frozen at page load (the tree pane never
			// htmx-swaps), but buildInitial re-reads it — so sync it to the
			// LIVE selection (cfg.focus, tracked by syncSelection) first, or
			// the rebuild would revert the highlight to the page-load node.
			if (cfg.mode === 'workspace') container.dataset.treeFocus = cfg.focus || '';
			// buildInitial re-fetches every level with the new family and
			// re-reveals the current selection.
			buildInitial(container);
		});
		listenersBound = true;
	}

	function attach() {
		const container = document.querySelector('[data-tree]');
		if (!container) return;
		if (container.dataset.treeBuilt === 'true') return;
		container.dataset.treeBuilt = 'true';
		buildInitial(container);
	}

	function init() {
		attach();
		bindGlobals();
	}

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', init);
	} else {
		init();
	}
})();
