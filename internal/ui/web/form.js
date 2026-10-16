import { h, clear } from "./dom.js";

export function renderForm(el, store) { clear(el).append(h("h2", {}, store.selected || "")); }
