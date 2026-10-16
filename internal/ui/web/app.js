import { api } from "./api.js";
import { h, clear } from "./dom.js";
import * as tree from "./tree.js";
import { renderLanding } from "./landing.js";
import { renderForm } from "./form.js";
import { renderTray } from "./tray.js";

export const store = {
  state: null, selected: null, detail: null, error: null,
  filter: { q: "", onlyProblems: false },
  expanded: new Set(), collapsed: new Set(), order: [],
};

const $ = (id) => document.getElementById(id);

export function toast(message, kind = "") {
  const t = $("toast");
  t.textContent = message;
  t.className = "toast show " + kind;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.className = "toast"), 3500);
}

// refresh installs a new state from the server and redraws everything.
export function refresh(state) {
  store.state = state;
  rerender();
}

function rerender() {
  const st = store.state;
  $("file").textContent = st.configPath;
  const ops = st.draft.operations || [];
  const n = (s) => ops.filter((o) => o.status === s).length;
  $("counts").textContent = `${ops.length} operations · ${n("decision")} decide · ${n("unsupported")} unsupported`;
  tree.render($("tree"), store, select);
  if (store.selected) renderForm($("main"), store); else renderLanding($("main"), store, act);
  renderTray($("tray"), store);
}
store.rerender = rerender;

export async function select(key) {
  store.selected = key;
  store.error = null;
  try {
    store.detail = key ? await api.operation(key) : null;
  } catch (e) {
    store.detail = null;
    toast(e.message, "error");
  }
  rerender();
}

// act sends one entry; a refused change keeps the draft and says why.
export async function act(entry) {
  try {
    refresh(await api.apply(entry));
    store.error = null;
    if (store.selected) store.detail = await api.operation(store.selected);
    rerender();
    return true;
  } catch (e) {
    store.error = e.message;
    // A refusal's body carries the draft as it would have been; the real
    // draft did not move, so show that one.
    try { store.state = await api.state(); } catch { /* keep the last good state */ }
    rerender();
    return false;
  }
}
store.act = act;

store.removeStale = (p) => act(p.match
  ? { kind: "operation", match: p.match, remove: true }
  : { kind: "component", schema: p.schema, remove: true });

store.apply = async (state) => { refresh(state); if (store.selected) await select(store.selected); };
store.selectKey = (key) => select(key);
store.toast = (m, k) => toast(m, k);

function nextProblem() {
  const list = store.order.filter((o) => o.status === "decision" || o.status === "unsupported");
  if (!list.length) return toast("No problems left");
  const at = list.findIndex((o) => o.key === store.selected);
  select(list[(at + 1) % list.length].key);
}

async function start() {
  $("search").addEventListener("input", (e) => { store.filter.q = e.target.value.trim(); rerender(); });
  $("only-problems").addEventListener("change", (e) => { store.filter.onlyProblems = e.target.checked; rerender(); });
  $("next-problem").addEventListener("click", nextProblem);
  try {
    refresh(await api.state());
  } catch (e) {
    clear($("main")).append(h("div", { class: "banner error" }, e.message));
  }
}

start();
