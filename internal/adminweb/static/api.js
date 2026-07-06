// The panel HTTP client. The panel is served by the backend itself now, so every
// call is SAME-ORIGIN to the backend JSON API (no brain, no proxy, no separate base):
//   - reads (GET) need a valid session; writes echo the CSRF token,
//   - the browser session maps to the operator identity server-side, so this code
//     never holds a bearer token.
// Every request rides credentials: same-origin (the session cookie goes along) and
// sets X-Requested-With so an expired session gets a clean 401 the app can act on,
// never an HTML login redirect the fetch would silently follow. Every state-changing
// request echoes the CSRF token (read from the non-HttpOnly cnz_panel_csrf cookie) in
// the X-CSRF-Token header (double-submit).
//
// Errors are normalized to an Error carrying { code, status, body } so the UI can
// show the backend's problem+json code/detail instead of a bare status.

// The backend sets the CSRF token in a readable cookie. Read it live on each write so
// a re-login that rotates it is picked up without a reload.
const CSRF_COOKIE = "cnz_panel_csrf";
function csrfToken() {
  const raw = (globalThis.document && document.cookie) || "";
  const match = raw.match(new RegExp("(?:^|;\\s*)" + CSRF_COOKIE + "=([^;]*)"));
  return match ? decodeURIComponent(match[1]) : "";
}

function toUrl(path) {
  const origin = (globalThis.location && globalThis.location.origin) || "http://localhost";
  return new URL(path, origin);
}

async function request(path, options = {}) {
  const method = (options.method || "GET").toUpperCase();
  const headers = { ...(options.headers || {}) };
  const isForm = typeof FormData !== "undefined" && options.body instanceof FormData;
  if (!isForm && !("content-type" in headers)) headers["content-type"] = "application/json";
  // Mark every request as an XHR so the session gate answers a 401 (not a 303 to the
  // login page) when the session is gone.
  headers["x-requested-with"] = "XMLHttpRequest";
  if (method !== "GET" && method !== "HEAD") {
    const token = csrfToken();
    if (token) headers["x-csrf-token"] = token;
  }

  const res = await fetch(toUrl(path), { credentials: "same-origin", ...options, headers });

  const text = await res.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = { raw: text };
    }
  }

  if (!res.ok) {
    const code = (body && body.code) || `http_${res.status}`;
    const detail = (body && body.detail) || res.statusText || code;
    const err = new Error(detail);
    err.code = code;
    err.status = res.status;
    err.body = body;
    throw err;
  }
  return body;
}

function query(params) {
  const usable = Object.entries(params).filter(([, v]) => v != null && v !== "");
  if (!usable.length) return "";
  return "?" + usable.map(([k, v]) => `${k}=${encodeURIComponent(v)}`).join("&");
}

const enc = encodeURIComponent;

const get = (p) => request(p);
const post = (p, b) => request(p, { method: "POST", body: b == null ? undefined : JSON.stringify(b) });
const put = (p, b) => request(p, { method: "PUT", body: JSON.stringify(b) });
const del = (p) => request(p, { method: "DELETE" });

export const api = {
  // Liveness of the backend that serves this panel (same-origin). Returns { ok } or
  // throws; the shell is already backend-served, so a failure means the process is
  // down, not a cross-service hop.
  health: async () => {
    const res = await fetch(toUrl("/healthz"), {
      credentials: "same-origin",
      headers: { "x-requested-with": "XMLHttpRequest" },
    });
    return { ok: res.ok, status: res.status };
  },

  // --- Authors (the content author registry) ---------------------------
  // Backend authors are keyed by handle. Style is the private voice prompt; it is
  // edited here but never rendered on the public site.
  listAuthors: (includeDeleted) => get("/authors" + query({ include_deleted: includeDeleted ? "true" : "" })),
  // Create-or-update is one full upsert (the backend has no partial PATCH): the caller
  // sends the whole author row. handle is required. sources, when present, replaces the
  // attached-source set in the same request; omit it to leave the set untouched.
  upsertAuthor: (body) => post("/authors", body),
  deleteAuthor: (handle) => del(`/authors/${enc(handle)}`),
  restoreAuthor: (handle) => post(`/authors/${enc(handle)}/restore`),
  getAuthorSources: (handle) => get(`/authors/${enc(handle)}/sources`),
  putAuthorSources: (handle, sources) => put(`/authors/${enc(handle)}/sources`, { sources }),

  // --- Sources (the source registry the newsroom draws from) -----------
  // Backend sources are keyed by slug (derived from the domain). Like authors, edit is
  // a full upsert, so an edit or an enable/disable toggle sends the whole row with the
  // one field changed and an explicit slug + domain so it never forks a new row.
  listSources: (includeDeleted) => get("/sources" + query({ include_deleted: includeDeleted ? "true" : "" })),
  upsertSource: (body) => post("/sources", body),
  deleteSource: (slug) => del(`/sources/${enc(slug)}`),
  restoreSource: (slug) => post(`/sources/${enc(slug)}/restore`),

  // --- Articles --------------------------------------------------------
  listArticleDays: (params = {}) => get("/articles:days" + query(params)),
  listArticleFacets: () => get("/articles:facets"),
  listArticles: (params = {}) => get("/articles" + query(params)),
  getArticle: (slug) => get(`/articles/${enc(slug)}`),
  updateArticle: (slug, body) => put(`/articles/${enc(slug)}`, body),
  deleteArticle: (slug) => del(`/articles/${enc(slug)}`),

  // --- Portadas (the per-day front-page plan) --------------------------
  getPortadas: () => get("/portadas"),
  upsertPortada: (body) => post("/portadas", body),
};
