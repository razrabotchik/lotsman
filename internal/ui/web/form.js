import { h, clear } from "./dom.js";
import { api } from "./api.js";

// The smart form: one operation's correction, built as the whole desired
// operationOverrides entry. Every value it shows comes from the draft
// snapshot (store.detail.after); nothing in store.detail is edited in place.

const HINTS = {
  unsupported_media_type: "Pick the media type lotsman should send. Only JSON bodies can be sent.",
  unsupported_parameter_style: "Change the style to one lotsman serializes (form in query, simple in path and header) — only if the API really accepts it.",
  unsupported_parameter_schema: "Replace the parameter's schema with a scalar or an array of scalars.",
  ambiguous_security: "Pick which configured credential this operation uses.",
  policy_unknown_effect_blocked: "Lotsman could not tell what this does. State its effect; anything but read still needs mutations enabled.",
  policy_mutation_blocked: "Mutations are off (execution.allowMutations, restart needed).",
  policy_denied_by_rule: "Blocked by execution rules in lotsman.yaml.",
  policy_not_allowed_by_rule: "Blocked by execution rules in lotsman.yaml.",
  disabled_by_override: "Hidden by a correction — use Publication below.",
  excluded_by_selection: "Excluded by catalog.includeTags in lotsman.yaml.",
  authentication_not_implemented: "Lotsman has no credential for this operation: add an authProfiles entry to lotsman.yaml (restart/reload).",
};
const CANNOT = "This needs new code in lotsman or a fixed document; the workbench cannot lift it.";
const STYLES = {
  query: ["form", "spaceDelimited", "pipeDelimited", "deepObject"], path: ["simple", "label", "matrix"],
  header: ["simple"], cookie: ["form"],
};
const TYPES = ["string", "integer", "number", "boolean", "array of string", "array of integer", "array of number"];
const EFFECTS = ["read", "write", "destructive", "unknown"];

const sendable = (mt) => mt === "application/json" || mt.endsWith("+json");

function typeSchema(choice) {
  if (choice.startsWith("array of ")) return { type: "array", items: { type: choice.slice(9) } };
  return { type: choice };
}

function describe(schema) {
  if (!schema || typeof schema !== "object") return "any";
  if (schema.type === "array") return "array of " + describe(schema.items);
  return Array.isArray(schema.type) ? schema.type.join("|") : (schema.type || "any");
}

// canon is JSON with sorted keys, so two entries compare by meaning.
function canon(v) {
  if (Array.isArray(v)) return "[" + v.map(canon).join(",") + "]";
  if (v && typeof v === "object") {
    return "{" + Object.keys(v).filter((k) => v[k] !== undefined).sort()
      .map((k) => JSON.stringify(k) + ":" + canon(v[k])).join(",") + "}";
  }
  return JSON.stringify(v);
}

function warningsFor(store, key) {
  const change = ((store.state.comparison || {}).changes || []).find((c) => c.key === key);
  return (change && change.warnings) || [];
}

function warnings(list, field) {
  return list.filter((w) => w.field === field).map((w) => h("div", { class: "warning w-" + w.level }, w.message));
}

function usage(store, component) {
  return (store.state.draft.operations || []).filter((o) => o.bodyComponent === component).length;
}

function componentOverride(store, name) {
  const c = (store.state.draft.components || []).find((x) => x.match && x.match.schema === name);
  return c ? structuredClone(c) : null;
}

const pretty = (v) => (v && Object.keys(v).length ? JSON.stringify(v, null, 2) : "");

export function renderForm(el, store) {
  clear(el);
  const d = store.detail;
  if (!d || !d.after || d.after.key !== store.selected) { el.append(h("p", { class: "muted" }, "Loading…")); return; }
  const op = d.after;
  const ov = structuredClone(op.override || {});
  const ws = warningsFor(store, op.key);
  const match = ov.match || (op.operationId ? { operationId: op.operationId } : { method: op.method, path: op.path });
  const entryId = "operation:" + (match.operationId || match.method + " " + match.path);
  const pending = (store.state.entries || []).some((e) => e.id === entryId);

  el.append(h("h2", {}, op.method, " ", op.path, " ", h("span", { class: "pill p-" + op.status }, op.status)));
  el.append(h("p", { class: "muted" }, op.operationId || "", op.summary ? " — " + op.summary : "",
    d.before && d.before.status !== op.status ? " (was " + d.before.status + ")" : ""));
  if (store.error) el.append(h("div", { class: "banner error" }, "The change was not taken: ", String(store.error).replace(/^the change was not taken: /i, "")));
  // Refusals and blockers, coloured by the operation's status: red only for
  // what the workbench cannot lift; "needs new code" only there too.
  const said = new Set();
  for (const r of [...(op.reasons || []), ...(op.blockers || []).map((code) => ({ code }))]) {
    if (said.has(r.code)) continue;
    said.add(r.code);
    // A media type is a decision only when one of the offered types can be sent.
    const liftable = HINTS[r.code] && !(r.code === "unsupported_media_type" && !(op.mediaTypes || []).some(sendable));
    const hint = liftable ? HINTS[r.code] : (op.status === "unsupported" ? CANNOT : HINTS[r.code] || "");
    const level = op.status === "unsupported" ? (liftable ? "warn" : "error") : op.status === "decision" ? "warn" : "info";
    el.append(h("div", { class: "banner " + level },
      h("b", {}, r.code), r.detail ? " — " + r.detail : "", hint ? h("br") : null, hint));
  }
  if ((op.corrections || []).length) {
    el.append(h("div", { class: "banner info" }, "Corrected by configuration: ",
      op.corrections.map((c) => c.target + (c.note ? " (" + c.note + ")" : "")).join("; ")));
  }
  el.append(...warnings(ws, "operation"));

  const tabs = h("div", { class: "tabs" });
  const body = h("div");
  const show = (which) => {
    store.formTab = which;
    for (const b of tabs.children) b.classList.toggle("active", b.dataset.tab === which);
    clear(body).append(which === "dry" ? dryRun(store, op) : editor(store, op, ov, ws, match, entryId, pending));
  };
  for (const [id, label] of [["edit", "Correct"], ["dry", "Dry run"]]) {
    const b = h("button", { type: "button", onclick: () => show(id) }, label);
    b.dataset.tab = id;
    tabs.append(b);
  }
  el.append(tabs, body);
  show(store.formTab === "dry" ? "dry" : "edit");
}

function editor(store, op, ov, ws, match, entryId, pending) {
  const form = h("div");
  const controls = {};

  // Media type.
  if ((op.mediaTypes || []).length) {
    const sec = h("section", { class: "field" }, h("h3", {}, "Media type"));
    const current = ov.mediaType || (op.body && op.body.mediaType) || "";
    for (const mt of op.mediaTypes) {
      const radio = h("input", { type: "radio", name: "mediaType", value: mt, checked: mt === current, disabled: !sendable(mt), "data-f": "mt" });
      sec.append(h("label", { class: "row" }, radio, " ", mt, sendable(mt) ? "" : " — lotsman cannot send this"), h("br"));
    }
    sec.append(...warnings(ws, "mediaType"));
    controls.mediaType = () => { const r = sec.querySelector("input[name=mediaType]:checked"); return r ? r.value : ""; };
    form.append(sec);
  }

  // Parameters. The row shows the parameter as the draft declares it, so an
  // unchanged row reproduces the existing correction and nothing more.
  if ((op.parameters || []).length) {
    const sec = h("section", { class: "field" }, h("h3", {}, "Parameters"));
    const prior = new Map((ov.parameters || []).map((p) => [p.in + "/" + p.name, p]));
    const table = h("table", { class: "params" },
      h("tr", {}, ["name", "style", "explode", "required", "type", ""].map((t) => h("th", {}, t))));
    controls.params = [];
    for (const p of op.parameters) {
      const id = p.in + "/" + p.name;
      const styles = STYLES[p.in] || [];
      const style = h("select", { "data-f": "style " + id },
        (styles.includes(p.style) ? styles : [p.style, ...styles]).map((s) => h("option", { value: s, selected: s === p.style }, s)));
      const explode = h("input", { type: "checkbox", checked: p.explode, "data-f": "explode " + id });
      const required = h("input", { type: "checkbox", checked: p.required || p.in === "path", disabled: p.in === "path", "data-f": "required " + id });
      const was = prior.get(id) && prior.get(id).schema;
      const type = h("select", { "data-f": "type " + id },
        h("option", { value: "keep" }, was ? "keep (corrected: " + describe(was) + ")" : "keep (" + describe(p.schema) + ")"),
        TYPES.map((t) => h("option", { value: t }, t)));
      table.append(h("tr", {},
        h("td", {}, id, p.reason ? h("div", { class: "warning w-error" }, p.reason) : null),
        h("td", {}, style), h("td", {}, explode), h("td", {}, required),
        h("td", {}, type, p.schema ? h("details", {}, h("summary", {}, "current schema"), h("pre", {}, JSON.stringify(p.schema, null, 2))) : null),
        h("td", {}, ...warnings(ws, "parameter " + id))));
      controls.params.push({ p, style, explode, required, type });
    }
    sec.append(table);
    form.append(sec);
  }

  // Body patch, for this operation or for the shared component.
  if (op.body || op.bodyComponent || ov.requestBody) {
    const sec = h("section", { class: "field" }, h("h3", {}, "Request body"));
    if (op.body) {
      sec.append(h("details", {}, h("summary", {}, "current schema (" + op.body.mediaType + ")"),
        h("pre", {}, JSON.stringify(op.body.schema, null, 2))));
    }
    const comp = op.bodyComponent ? componentOverride(store, op.bodyComponent) : null;
    const texts = { operation: pretty(ov.requestBody && ov.requestBody.patch), component: pretty(comp && comp.patch) };
    const initial = { ...texts };
    const patch = h("textarea", { "data-f": "patch", placeholder: '{"required": ["name"]}  — JSON Merge Patch over JSON Schema 2020-12' });
    patch.value = texts.operation;
    let scope = null;
    if (op.bodyComponent) {
      const n = usage(store, op.bodyComponent);
      scope = h("select", { "data-f": "scope" }, h("option", { value: "operation" }, "this operation only"),
        h("option", { value: "component" }, `everywhere — component ${op.bodyComponent} (used by ${n} operation${n === 1 ? "" : "s"})`));
      let shown = "operation";
      scope.addEventListener("change", () => { texts[shown] = patch.value; shown = scope.value; patch.value = texts[shown]; });
      controls.syncBody = () => { texts[shown] = patch.value; };
      sec.append(h("label", {}, "Apply to: ", scope), h("br"));
    } else {
      controls.syncBody = () => { texts.operation = patch.value; };
    }
    const invalid = h("div");
    sec.append(patch, invalid, ...warnings(ws, "requestBody"));
    controls.body = { texts, initial, comp, invalid };
    form.append(sec);
  }

  // Effect, publication, credential, note.
  const effect = h("select", { "data-f": "effect" },
    h("option", { value: "" }, `as inferred (${op.effectSource === "local_override" && !ov.effect ? "overridden" : op.effect})`),
    EFFECTS.map((e) => h("option", { value: e, selected: ov.effect === e }, e)));
  const enabled = h("input", { type: "checkbox", checked: !op.excluded, "data-f": "enabled" });
  const profiles = store.state.draft.profiles || [];
  const inUse = (op.authProfiles || []).join(", ");
  const auth = h("select", { "data-f": "auth" }, h("option", { value: "" }, "automatic" + (inUse && !ov.authProfile ? " (" + inUse + ")" : "")),
    profiles.map((p) => h("option", { value: p.name, selected: ov.authProfile === p.name }, p.name + " (" + p.scheme + ")")));
  const note = h("input", { type: "text", value: ov.note || "", placeholder: "why — kept in lotsman.yaml and shown in reports", "data-f": "note" });
  form.append(
    h("section", { class: "field" }, h("h3", {}, "Effect"), effect, ...warnings(ws, "effect")),
    h("section", { class: "field" }, h("h3", {}, "Publication"), h("label", {}, enabled, " published to agents"), ...warnings(ws, "enabled")),
    ...(profiles.length ? [h("section", { class: "field" }, h("h3", {}, "Credential"), auth, ...warnings(ws, "authProfile"))] : []),
    h("section", { class: "field" }, h("h3", {}, "Note"), note));

  restore(store, op.key, form);

  const apply = async () => {
    const entries = buildEntries(op, ov, match, controls, { effect: effect.value, enabled: enabled.checked, auth: auth.value, note: note.value.trim() });
    if (!entries) return;
    if (!entries.length) { store.error = null; store.formMemory = null; store.rerender(); return; }
    store.formMemory = { key: op.key, values: remember(form) };
    for (const e of entries) if (!(await store.act(e))) return;
    store.formMemory = null;
  };
  const revert = async () => {
    try {
      store.error = null;
      await store.apply(await api.revert(entryId));
    } catch (e) {
      store.error = e.message;
    }
    store.formMemory = null;
    store.rerender();
  };
  form.append(h("div", { class: "actions" },
    h("button", { class: "primary", type: "button", onclick: apply }, "Apply"),
    pending ? h("button", { type: "button", onclick: revert }, "Revert this operation") : null,
    op.override ? h("button", { type: "button", onclick: () => store.act({ kind: "operation", operationKey: op.key, match, remove: true }) },
      "Remove the correction") : null));
  return form;
}

// remember/restore keep what the operator typed across a refused change:
// the draft did not move, so the form would otherwise snap back.
function remember(form) {
  const values = {};
  for (const c of form.querySelectorAll("[data-f]")) {
    const id = c.dataset.f + (c.type === "radio" ? " " + c.value : "");
    values[id] = c.type === "checkbox" || c.type === "radio" ? c.checked : c.value;
  }
  return values;
}

function restore(store, key, form) {
  const mem = store.formMemory;
  if (!mem || mem.key !== key || !store.error) return;
  for (const c of form.querySelectorAll("[data-f]")) {
    const id = c.dataset.f + (c.type === "radio" ? " " + c.value : "");
    if (!(id in mem.values)) continue;
    if (c.type === "checkbox" || c.type === "radio") c.checked = mem.values[id];
    else { c.value = mem.values[id]; if (c.dataset.f === "scope") c.dispatchEvent(new Event("change")); }
  }
  // The scope switch above swapped the text; put the typed patch back.
  const patch = form.querySelector("[data-f=patch]");
  if (patch && "patch" in mem.values) patch.value = mem.values.patch;
}

// buildEntries turns the form into the whole desired operationOverrides
// entry, plus a componentOverrides entry when the shared body patch changed.
// An empty list means nothing changed; null means the input is not valid.
function buildEntries(op, ov, match, controls, rest) {
  const next = { match: structuredClone(match) };
  const out = [];
  if (controls.mediaType) {
    const mt = controls.mediaType();
    const automatic = op.body && op.body.mediaType === mt && !ov.mediaType;
    if (mt && !automatic) next.mediaType = mt;
  }
  if (controls.params) {
    const prior = new Map((ov.parameters || []).map((p) => [p.in + "/" + p.name, structuredClone(p)]));
    for (const c of controls.params) {
      const id = c.p.in + "/" + c.p.name;
      const pc = Object.assign({ name: c.p.name, in: c.p.in }, prior.get(id) || {});
      if (c.style.value !== c.p.style) pc.style = c.style.value;
      if (c.explode.checked !== c.p.explode) pc.explode = c.explode.checked;
      if (c.p.in !== "path" && c.required.checked !== c.p.required) pc.required = c.required.checked;
      if (c.type.value !== "keep") pc.schema = typeSchema(c.type.value);
      if (Object.keys(pc).length > 2) prior.set(id, pc);
    }
    if (prior.size) next.parameters = [...prior.values()];
  }
  if (controls.body) {
    controls.syncBody();
    const { texts, initial, comp, invalid } = controls.body;
    clear(invalid);
    const parse = (text, what) => {
      if (!text.trim()) return null;
      try {
        const v = JSON.parse(text);
        if (v && typeof v === "object" && !Array.isArray(v)) return v;
        throw new Error("a patch is a JSON object");
      } catch (e) {
        invalid.append(h("div", { class: "warning w-error" }, "The " + what + " patch is not valid: " + e.message));
        return undefined;
      }
    };
    const opPatch = parse(texts.operation, "body");
    const compPatch = texts.component !== initial.component ? parse(texts.component, "component") : null;
    if (opPatch === undefined || compPatch === undefined) return null;
    if (opPatch) next.requestBody = { patch: opPatch };
    if (texts.component !== initial.component) {
      out.push(compPatch
        ? { kind: "component", schema: op.bodyComponent,
          component: { match: { schema: op.bodyComponent }, patch: compPatch, note: (comp && comp.note) || undefined } }
        : { kind: "component", schema: op.bodyComponent, remove: true });
    }
  }
  if (rest.effect) next.effect = rest.effect;
  // Only a changed checkbox states publication; an operation hidden by the
  // catalog is not turned into an explicit enabled: false by Apply.
  const enabled = rest.enabled !== !op.excluded ? rest.enabled : ov.enabled;
  if (enabled !== undefined && enabled !== null) next.enabled = enabled;
  if (rest.auth) next.authProfile = rest.auth;
  if (rest.note) next.note = rest.note;

  const states = Object.keys(next).filter((k) => k !== "match" && k !== "note");
  const had = Object.keys(ov).length > 0;
  if (states.length) {
    if (!had || canon(next) !== canon(ov)) out.unshift({ kind: "operation", operationKey: op.key, match: next.match, operation: next });
  } else if (had) {
    out.unshift({ kind: "operation", operationKey: op.key, match: next.match, remove: true });
  }
  return out;
}

function dryRun(store, op) {
  const box = h("div");
  const args = h("textarea", {}, store.dryArgs && store.dryArgs.key === op.key ? store.dryArgs.text : "{}");
  const out = h("pre", {}, "Arguments are grouped: {\"path\": {...}, \"query\": {...}, \"headers\": {...}, \"body\": ...}");
  const run = async () => {
    store.dryArgs = { key: op.key, text: args.value };
    let parsed;
    try { parsed = JSON.parse(args.value || "{}"); } catch (e) { out.textContent = "Not JSON: " + e.message; return; }
    try { out.textContent = (await api.explain(op.key, parsed)).text; } catch (e) { out.textContent = e.message; }
  };
  box.append(h("section", { class: "field" }, h("h3", {}, "Arguments (JSON)"), args,
    h("div", { class: "actions" }, h("button", { class: "primary", type: "button", onclick: run }, "Explain the call"))),
  out, h("p", { class: "muted" }, "No network request is made. Credentials are shown as references."));
  return box;
}
