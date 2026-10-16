import { h, clear } from "./dom.js";
import { api } from "./api.js";

export function renderTray(el, store) {
  clear(el);
  const st = store.state;
  const entries = st.entries || [];
  const s = st.comparison.summary;
  if (!entries.length) { el.append(h("span", { class: "muted" }, "No pending changes")); return; }
  el.append(h("span", {}, `${entries.length} change${entries.length === 1 ? "" : "s"} · `,
    h("span", { class: "fixed" }, `+${s.fixed} fixed`), " · ",
    h("span", { class: "broken" }, `−${s.broken} broken`), " · ",
    h("span", { class: "altered" }, `${s.altered} altered`)));
  for (const e of entries) {
    const label = e.kind === "component" ? "component " + (e.schema || (e.component && e.component.match.schema))
      : e.kind === "overlays" ? "overlays" : (e.match ? (e.match.operationId || e.match.method + " " + e.match.path) : e.id);
    el.append(h("span", { class: "chip" },
      h("button", { class: "chip", type: "button", onclick: () => e.operationKey && store.selectKey(e.operationKey) }, label + (e.remove ? " (remove)" : "")),
      h("button", { class: "chip", type: "button", title: "revert", onclick: () => revertEntry(store, e.id) }, "×")));
  }
  el.append(h("span", { class: "spacer" }), h("button", { class: "primary", type: "button", onclick: () => review(store) }, "Review and save"));
}

async function revertEntry(store, id) {
  try { await store.apply(await api.revert(id)); } catch (e) { store.toast(e.message, "error"); }
}

let reviewing = false;

async function review(store) {
  if (reviewing) return;
  reviewing = true;
  let dlg = null;
  try {
    await showReview(store, (d) => (dlg = d));
  } catch (e) {
    if (dlg) dlg.remove();
    reviewing = false;
    store.toast(e.message || String(e), "error");
  }
}

async function showReview(store, setDialog) {
  let diff = "", diffError = null;
  try { diff = String((await api.diff()).diff); } catch (e) { diffError = e.message; }
  const dlg = h("dialog");
  setDialog(dlg);
  dlg.addEventListener("close", () => { dlg.remove(); reviewing = false; });
  const close = () => dlg.close();
  const st = store.state;
  const pre = h("pre", { class: "diff" });
  for (const line of diff.split("\n")) {
    const cls = line.startsWith("@@") ? "hunk" : line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : "";
    pre.append(h("span", { class: cls }, line), "\n");
  }
  const widening = st.comparison.widening || [];
  const ticks = widening.map((w) => ({ w, box: h("input", { type: "checkbox" }) }));
  const save = h("button", { class: "primary", type: "button" }, "Save");
  const status = h("div");
  const sync = () => { save.disabled = diffError !== null || ticks.some((t) => !t.box.checked); };
  ticks.forEach((t) => t.box.addEventListener("change", sync));
  sync();
  dlg.append(h("h2", {}, "Review and save"), h("p", { class: "muted" }, st.configPath));
  if (widening.length) {
    const box = h("div", { class: "widening" }, h("b", {}, "These changes let an agent do more without asking a person. Confirm each:"));
    for (const t of ticks) box.append(h("div", {}, h("label", {}, t.box, " ", t.w.message)));
    dlg.append(box);
  }
  for (const p of st.draft.problems || []) dlg.append(h("div", { class: "banner warn" }, "serve will refuse this configuration: ", p.message));
  if (diffError !== null) dlg.append(h("div", { class: "banner error" }, "Could not load the diff: ", diffError));
  else dlg.append(pre);
  dlg.append(status, h("div", { class: "actions" }, save, h("button", { type: "button", onclick: close }, "Cancel")));
  save.addEventListener("click", async () => {
    try {
      await store.apply(await api.save(ticks.filter((t) => t.box.checked).map((t) => t.w.id)));
      close();
      store.toast("Saved. A running `serve --watch` picks it up; stdio clients need a restart.");
    } catch (e) {
      clear(status).append(h("div", { class: "banner error" }, e.message));
      if (e.status === 409) status.append(h("button", { type: "button", onclick: async () => {
        try { await store.apply(await api.reload()); close(); } catch (e2) { clear(status).append(h("div", { class: "banner error" }, e2.message)); }
      } }, "Reload from disk, keep my changes"));
    }
  });
  document.body.append(dlg);
  dlg.showModal();
}
