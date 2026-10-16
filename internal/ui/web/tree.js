import { h, clear } from "./dom.js";

const PROBLEM = new Set(["decision", "unsupported"]);

// pendingKeys is the set of operation keys the draft holds an entry for.
export function pendingKeys(state) {
  const keys = new Set();
  for (const e of state.entries || []) if (e.operationKey) keys.add(e.operationKey);
  return keys;
}

function visible(op, filter, pending) {
  if (filter.onlyProblems && !PROBLEM.has(op.status) && !pending.has(op.key)) return false;
  if (!filter.q) return true;
  const q = filter.q.toLowerCase();
  return [op.path, op.operationId || "", op.summary || ""].some((s) => s.toLowerCase().includes(q));
}

// build groups operations by first tag, then by path segment, collapsing
// chains of single-child segments the way an IDE compacts folders.
export function build(ops, filter, pending) {
  const root = { name: "", children: new Map(), ops: [] };
  for (const op of ops) {
    if (!visible(op, filter, pending)) continue;
    const tag = (op.tags && op.tags[0]) || "untagged";
    let node = child(root, tag);
    for (const seg of op.path.split("/").filter(Boolean)) node = child(node, seg);
    node.ops.push(op);
  }
  return root;
}

function child(node, name) {
  if (!node.children.has(name)) node.children.set(name, { name, children: new Map(), ops: [] });
  return node.children.get(name);
}

function problems(node) {
  let n = node.ops.filter((o) => PROBLEM.has(o.status)).length;
  for (const c of node.children.values()) n += problems(c);
  return n;
}

// order lists the visible operations in tree order, for "next problem".
export function order(node, out = []) {
  for (const op of node.ops) out.push(op);
  for (const c of node.children.values()) order(c, out);
  return out;
}

export function render(el, store, onSelect) {
  const pending = pendingKeys(store.state);
  const root = build(store.state.draft.operations || [], store.filter, pending);
  clear(el);
  const stale = staleGroup(store);
  if (stale) el.append(stale);
  const ul = h("ul");
  for (const c of root.children.values()) ul.append(branch(c, "", store, pending, onSelect, 0));
  el.append(ul);
  store.order = order(root);
}

function branch(node, prefix, store, pending, onSelect, depth) {
  // Compact single-child chains.
  let name = node.name;
  while (node.ops.length === 0 && node.children.size === 1 && depth > 0) {
    node = [...node.children.values()][0];
    name += "/" + node.name;
  }
  const id = prefix + "/" + name;
  const count = problems(node);
  const open = store.filter.q || store.expanded.has(id) || (count > 0 && !store.collapsed.has(id));
  const li = h("li");
  li.append(h("button", { class: "branch", type: "button", onclick: () => {
      if (open) { store.collapsed.add(id); store.expanded.delete(id); } else { store.expanded.add(id); store.collapsed.delete(id); }
      store.rerender();
    } }, open ? "▾ " : "▸ ", name, count ? h("span", { class: "count" }, count) : null));
  if (open) {
    const ul = h("ul");
    for (const op of node.ops) ul.append(leaf(op, store, pending, onSelect));
    for (const c of node.children.values()) ul.append(branch(c, id, store, pending, onSelect, depth + 1));
    li.append(ul);
  }
  return li;
}

function leaf(op, store, pending, onSelect) {
  return h("li", {}, h("button", {
      class: "leaf" + (store.selected === op.key ? " selected" : ""), type: "button",
      title: op.summary || op.path, onclick: () => onSelect(op.key) },
    h("span", { class: "dot s-" + op.status }),
    h("span", { class: "method" }, op.method),
    op.operationId || op.path,
    pending.has(op.key) ? h("span", { class: "pending", title: "pending change" }) : null));
}

// staleGroup lists corrections that match nothing, with a remove action each.
function staleGroup(store) {
  const stale = (store.state.draft.problems || []).filter((p) => p.match || p.schema);
  if (!stale.length) return null;
  const box = h("div", {}, h("div", { class: "group" }, "Stale corrections · " + stale.length));
  const ul = h("ul");
  for (const p of stale) {
    const label = p.match ? (p.match.operationId || p.match.method + " " + p.match.path) : "component " + p.schema;
    ul.append(h("li", {}, h("button", { class: "branch", type: "button", title: p.message,
      onclick: () => store.removeStale(p) }, h("span", { class: "dot s-unsupported" }), label, h("span", { class: "count" }, "remove"))));
  }
  box.append(ul);
  return box;
}
