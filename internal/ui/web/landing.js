import { h, clear } from "./dom.js";

export function renderLanding(el, store, act) {
  const st = store.state, d = st.draft;
  clear(el);
  el.append(h("h2", {}, "Configuration"), h("p", { class: "muted" }, st.configPath));
  if (d.specError) el.append(h("div", { class: "banner error" }, "The specification could not be read: ", d.specError,
    h("br"), "Fix the document, or correct it with an overlay below."));
  if (d.configError) el.append(h("div", { class: "banner error" }, "The configuration does not validate: ", d.configError));
  const counts = { decision: 0, unsupported: 0, blocked: 0, works: 0 };
  for (const op of d.operations || []) counts[op.status]++;
  if (!d.specError) el.append(h("p", {},
    h("span", { class: "pill p-decision" }, counts.decision + " need a decision"), " ",
    h("span", { class: "pill p-unsupported" }, counts.unsupported + " need new code or a fixed document"), " ",
    h("span", { class: "pill p-blocked" }, counts.blocked + " blocked"), " ",
    h("span", { class: "pill p-works" }, counts.works + " work")));
  for (const p of d.problems || []) el.append(h("div", { class: "banner warn" }, p.section, p.index >= 0 ? "[" + p.index + "]" : "", ": ", p.message));

  // Overlays: the one thing editable even when the document does not parse.
  const list = [...(d.overlays || [])];
  const box = h("section", { class: "field" }, h("h3", {}, "spec.overlays"));
  const ul = h("ul");
  list.forEach((path, i) => ul.append(h("li", {}, path, " ", h("button", { type: "button",
    onclick: () => act({ kind: "overlays", overlays: list.filter((_, j) => j !== i) }) }, "remove"))));
  const input = h("input", { type: "text", placeholder: "./vendor-fixes.overlay.yaml" });
  box.append(ul, input, " ", h("button", { type: "button", onclick: () => {
    if (input.value.trim()) act({ kind: "overlays", overlays: [...list, input.value.trim()] });
  } }, "add overlay"));
  el.append(box, h("p", { class: "muted" }, "Pick an operation on the left. Amber needs a decision you can make here; red needs new code in lotsman or a fixed document."));
}
