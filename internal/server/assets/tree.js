// blittermib OID tree island — vanilla JS.
//
// Lazy-loads the OID hierarchy via /api/v1/tree?parent={oid}. Each
// node is a <li> with an expand/collapse button and a link to its
// /s/{module}::{name} page. Children are fetched on first expand
// and cached in the DOM.
//
// HTMX integration: re-runs init() on htmx:afterSwap so the tree
// reattaches to the [data-tree] container after a body swap.

(function () {
	'use strict';

	const TREE_API = '/api/v1/tree';
	const ROOT_OID = '1';

	function escape(s) {
		const d = document.createElement('div');
		d.textContent = s == null ? '' : String(s);
		return d.innerHTML;
	}

	async function fetchChildren(parent) {
		const url = TREE_API + '?parent=' + encodeURIComponent(parent || ROOT_OID);
		const res = await fetch(url);
		if (!res.ok) throw new Error('tree fetch ' + res.status);
		const data = await res.json();
		return data.children || [];
	}

	function makeNode(item) {
		const li = document.createElement('li');
		li.className = 'tree-node';
		li.dataset.oid = item.oid;
		li.dataset.expanded = 'false';
		li.dataset.hasChildren = item.hasChildren ? 'true' : 'false';

		const row = document.createElement('div');
		row.className = 'tree-row';

		const btn = document.createElement('button');
		btn.type = 'button';
		btn.className = 'tree-expand';
		btn.setAttribute('aria-label', 'expand');
		btn.setAttribute('aria-expanded', 'false');
		btn.dataset.action = 'expand';
		btn.textContent = item.hasChildren ? '▸' : ' ';
		if (!item.hasChildren) btn.disabled = true;
		row.appendChild(btn);

		const num = document.createElement('span');
		num.className = 'tree-num';
		num.textContent = '.' + (item.position || '');
		row.appendChild(num);

		const link = document.createElement('a');
		link.className = 'tree-name';
		link.href = '/s/' + encodeURIComponent(item.module + '::' + item.name);
		link.textContent = item.name;
		row.appendChild(link);

		const meta = document.createElement('span');
		meta.className = 'tree-meta';
		meta.textContent = item.module + ' · ' + item.kind;
		row.appendChild(meta);

		li.appendChild(row);
		return li;
	}

	async function expand(node) {
		if (node.dataset.expanded === 'true') return;
		if (node.dataset.hasChildren !== 'true') return;

		node.dataset.expanded = 'true';
		const btn = node.querySelector('.tree-expand');
		if (btn) {
			btn.textContent = '▾';
			btn.setAttribute('aria-expanded', 'true');
		}

		let children = node.querySelector(':scope > .tree-children');
		if (children) {
			children.hidden = false;
			return; // already populated
		}

		children = document.createElement('ul');
		children.className = 'tree-children';
		const placeholder = document.createElement('li');
		placeholder.className = 'tree-loading';
		placeholder.textContent = 'Loading…';
		children.appendChild(placeholder);
		node.appendChild(children);

		try {
			const items = await fetchChildren(node.dataset.oid);
			children.removeChild(placeholder);
			if (items.length === 0) {
				node.dataset.hasChildren = 'false';
				if (btn) btn.disabled = true;
				return;
			}
			items.forEach((item) => children.appendChild(makeNode(item)));
		} catch (err) {
			placeholder.textContent = 'Failed to load.';
			placeholder.classList.add('tree-error');
			console.warn('tree expand failed', err);
		}
	}

	function collapse(node) {
		if (node.dataset.expanded !== 'true') return;
		node.dataset.expanded = 'false';
		const btn = node.querySelector('.tree-expand');
		if (btn) {
			btn.textContent = '▸';
			btn.setAttribute('aria-expanded', 'false');
		}
		const children = node.querySelector(':scope > .tree-children');
		if (children) children.hidden = true;
	}

	function onClick(e) {
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

	function onKey(e) {
		if (!e.target.closest('.tree-expand')) return;
		// ArrowRight expands, ArrowLeft collapses.
		const node = e.target.closest('.tree-node');
		if (!node) return;
		if (e.key === 'ArrowRight') {
			e.preventDefault();
			expand(node);
		} else if (e.key === 'ArrowLeft') {
			e.preventDefault();
			collapse(node);
		}
	}

	async function buildInitial(container) {
		const focus = container.dataset.treeFocus || '';
		const parent = focus || ROOT_OID;
		container.innerHTML = '';

		const ul = document.createElement('ul');
		ul.className = 'tree-children tree-root-list';
		container.appendChild(ul);

		try {
			const items = await fetchChildren(parent);
			if (items.length === 0) {
				ul.innerHTML = '<li class="tree-empty">No OIDs under <code>' + escape(parent) + '</code>.</li>';
				return;
			}
			items.forEach((item) => ul.appendChild(makeNode(item)));
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
		document.body.addEventListener('htmx:afterSwap', attach);
		document.documentElement.addEventListener('htmx:afterSwap', attach);
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
