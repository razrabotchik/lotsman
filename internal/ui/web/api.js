// call resolves to the JSON body. Guard refusals (403, 404, 400) are plain
// text, so a body that is not JSON becomes the error message.
async function call(method, path, body) {
  const init = { method, credentials: "same-origin", headers: {} };
  if (body !== undefined) { init.headers["Content-Type"] = "application/json"; init.body = JSON.stringify(body); }
  const res = await fetch(path, init);
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text.trim() }; }
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status; err.data = data;
    throw err;
  }
  return data;
}

const q = encodeURIComponent;
export const api = {
  state: () => call("GET", "/api/state"),
  operation: (key) => call("GET", "/api/operation?key=" + q(key)),
  apply: (entry) => call("POST", "/api/draft", entry),
  revert: (id) => call("DELETE", "/api/draft?id=" + q(id)),
  diff: () => call("GET", "/api/diff"),
  save: (acknowledged) => call("POST", "/api/save", { acknowledged }),
  reload: () => call("POST", "/api/reload"),
  explain: (key, args) => call("POST", "/api/explain", { key, arguments: args }),
};
