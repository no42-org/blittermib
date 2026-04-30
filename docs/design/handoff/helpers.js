// MIB Browser — helpers
//
// Kind strings here mirror Go's model.SymbolKind constants exactly so
// this prototype can consume the real backend's JSON without
// translation: "scalar" / "table" / "table-entry" / "column" /
// "object-identity" / "module-identity" / "notification-type" /
// "object-group" / "notification-group" / "module-compliance" /
// "textual-convention".
window.MIB_HELPERS = (() => {
  // Map an SNMP type string to a color family class
  function typeFamily(type, kind, isIndex) {
    if (kind === "notification-type") return "t-notif";
    if (
      kind === "table" || kind === "table-entry" ||
      kind === "object-identity" || kind === "module-identity"
    ) return "t-struct";
    if (isIndex) return "t-index";
    if (!type) return "t-struct";
    const t = type.toLowerCase();
    if (t.startsWith("counter")) return "t-counter";
    if (t.startsWith("gauge") || t.startsWith("unsigned")) return "t-gauge";
    if (t.startsWith("integer")) return "t-int";
    if (t === "displaystring" || t.includes("string") || t.includes("octet")) return "t-text";
    if (t === "timeticks") return "t-time";
    if (t.includes("address") || t === "ipaddress" || t === "macaddress" || t === "physaddress") return "t-addr";
    if (t === "truthvalue" || t === "boolean") return "t-bool";
    if (t.includes("index") || t.includes("rowpointer")) return "t-index";
    return "t-struct";
  }

  function kindGlyph(kind) {
    switch (kind) {
      case "object-identity":    return { glyph: "·", label: "OBJ" };
      case "module-identity":    return { glyph: "·", label: "MOD" };
      case "scalar":             return { glyph: "s",  label: "SCALAR" };
      case "table":              return { glyph: "▤", label: "TBL" };
      case "table-entry":        return { glyph: "▦", label: "ROW" };
      case "column":             return { glyph: "▥", label: "COL" };
      case "notification-type":  return { glyph: "!", label: "NOTIF" };
      default: return { glyph: "·", label: "" };
    }
  }

  // Walk the tree, yielding flat rows w/ depth + parent path
  function flatten(node, depth = 0, path = [], expanded, out = []) {
    const fullPath = [...path, node];
    out.push({ node, depth, path: fullPath });
    const isOpen = expanded.has(node.oid);
    if (isOpen && node.children) {
      for (const c of node.children) flatten(c, depth + 1, fullPath, expanded, out);
    }
    return out;
  }

  // Search across whole tree (visible regardless of expand)
  function searchAll(node, q, out = [], path = []) {
    const fullPath = [...path, node];
    const hay = (node.name + " " + node.oid + " " + (node.desc || "") + " " + (node.type || "")).toLowerCase();
    if (q === "" || hay.includes(q)) {
      out.push({ node, depth: fullPath.length - 1, path: fullPath });
    }
    if (node.children) for (const c of node.children) searchAll(c, q, out, fullPath);
    return out;
  }

  // Auto-expand on search: include all ancestors of any match
  function searchWithAncestors(root, q) {
    const ql = q.toLowerCase();
    const matches = [];
    const ancestors = new Set();

    function walk(node, path) {
      const hay = (node.name + " " + node.oid + " " + (node.desc || "") + " " + (node.type || "")).toLowerCase();
      const matched = hay.includes(ql);
      if (matched) {
        matches.push(node.oid);
        for (const a of path) ancestors.add(a.oid);
      }
      if (node.children) for (const c of node.children) walk(c, [...path, node]);
    }
    walk(root, []);
    return { matches: new Set(matches), ancestors };
  }

  // Highlight match within a string
  function highlight(text, q) {
    if (!q) return [{ t: text }];
    const i = text.toLowerCase().indexOf(q.toLowerCase());
    if (i < 0) return [{ t: text }];
    return [
      { t: text.slice(0, i) },
      { t: text.slice(i, i + q.length), mark: true },
      { t: text.slice(i + q.length) },
    ];
  }

  // Split OID into prefix + tail (last segment)
  function splitOid(oid) {
    const parts = oid.split(".");
    return { prefix: parts.slice(0, -1).join("."), tail: parts.at(-1) };
  }

  // Count of all descendants by type-family
  function countTypes(node, acc = { counter: 0, gauge: 0, int: 0, text: 0, table: 0, scalar: 0, notif: 0, total: 0 }) {
    acc.total++;
    if (node.kind === "table") acc.table++;
    if (node.kind === "scalar" || node.kind === "column") acc.scalar++;
    if (node.kind === "notification-type") acc.notif++;
    const fam = typeFamily(node.type, node.kind);
    if (fam === "t-counter") acc.counter++;
    if (fam === "t-gauge") acc.gauge++;
    if (fam === "t-int") acc.int++;
    if (fam === "t-text") acc.text++;
    if (node.children) node.children.forEach(c => countTypes(c, acc));
    return acc;
  }

  function findByOid(node, oid) {
    if (node.oid === oid) return node;
    if (node.children) {
      for (const c of node.children) {
        const f = findByOid(c, oid);
        if (f) return f;
      }
    }
    return null;
  }

  return { typeFamily, kindGlyph, flatten, searchAll, searchWithAncestors, highlight, splitOid, countTypes, findByOid };
})();
