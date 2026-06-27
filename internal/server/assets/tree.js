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
	const ROOT_OID = '1';

	function escape(s) {
		const d = document.createElement('div');
		d.textContent = s == null ? '' : String(s);
		return d.innerHTML;
	}

	// fetchPage returns one keyset page of children plus the cursor for
	// the next page (null when the level is exhausted). `after` is the
	// last OID of the previous page.
	async function fetchPage(parent, after) {
		let url = TREE_API + '?parent=' + encodeURIComponent(parent || ROOT_OID);
		if (after) url += '&after=' + encodeURIComponent(after);
		const res = await fetch(url);
		if (!res.ok) throw new Error('tree fetch ' + res.status);
		const data = await res.json();
		return { items: data.children || [], nextAfter: data.nextAfter || null };
	}

	function makeNode(item, level) {
		const li = document.createElement('li');
		li.className = 'tree-node';
		li.dataset.oid = item.oid;
		li.dataset.expanded = 'false';
		li.dataset.hasChildren = item.hasChildren ? 'true' : 'false';
		// ARIA tree pattern (WCAG 4.1.2): the <li> is the focusable
		// treeitem. Roving tabindex — every node starts at -1; exactly
		// one node carries tabindex=0 (set by setRovingTo) so the tree is
		// a single Tab stop and arrows move between nodes.
		li.setAttribute('role', 'treeitem');
		li.setAttribute('aria-level', String(level));
		li.tabIndex = -1;
		if (item.hasChildren) li.setAttribute('aria-expanded', 'false');

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
		btn.textContent = item.hasChildren ? '▸' : ' ';
		if (!item.hasChildren) btn.disabled = true;
		row.appendChild(btn);

		const num = document.createElement('span');
		num.className = 'tree-num';
		num.textContent = '.' + (item.position || '');
		row.appendChild(num);

		// Symbol-backed nodes link to /s/{module}::{name}. Synthetic
		// bridge nodes (item.hasSymbol === false) have no symbol page —
		// render their name (IANA canonical when known, else the numeric
		// segment) as plain text so the subtree is still navigable.
		let nameEl;
		if (item.hasSymbol) {
			nameEl = document.createElement('a');
			nameEl.href = '/s/' + encodeURIComponent(item.module + '::' + item.name);
			nameEl.textContent = item.name;
		} else {
			nameEl = document.createElement('span');
			nameEl.textContent = item.name || ('.' + (item.position || ''));
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

	// childLevel reads a node's aria-level to assign its children the
	// next level down (defaults to 1 so a missing attribute is safe).
	function childLevel(node) {
		const l = parseInt(node.getAttribute('aria-level') || '1', 10);
		return (isNaN(l) ? 1 : l) + 1;
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
			loadMore(more);
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

	async function buildInitial(container) {
		const focus = container.dataset.treeFocus || '';
		const parent = focus || ROOT_OID;
		container.innerHTML = '';

		const ul = document.createElement('ul');
		ul.className = 'tree-children tree-root-list';
		ul.setAttribute('role', 'tree');
		ul.setAttribute('aria-label', 'OID tree');
		container.appendChild(ul);

		try {
			const page = await fetchPage(parent);
			if (page.items.length === 0) {
				ul.innerHTML = '<li class="tree-empty">No OIDs under <code>' + escape(parent) + '</code>.</li>';
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
		}
	}

	let listenersBound = false;

	function bindGlobals() {
		if (listenersBound) return;
		document.addEventListener('click', onClick);
		document.addEventListener('keydown', onKey);
		document.addEventListener('focusin', onFocusIn);
		document.body.addEventListener('htmx:afterSwap', attach);
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
