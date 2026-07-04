import globalJsdom from "global-jsdom";

// Side-effect module: install the global DOM. Import this FIRST in every test
// file, before @testing-library/dom and user-event, because those libraries
// bind to globalThis.document as they load. ES module imports evaluate in order,
// so a leading `import "./setup.js"` guarantees jsdom is in place first.
//
// jsdom provides no fetch of its own; the MSW interceptor patches the global
// fetch later (in its before() hook), so we preserve whatever fetch exists
// across the jsdom install. The guard keeps a second import in the same process
// from installing jsdom twice.
if (!globalThis.__JSDOM_INSTALLED__) {
  const savedFetch = globalThis.fetch;
  globalJsdom(undefined, { url: "http://localhost/", pretendToBeVisual: true });
  if (savedFetch && globalThis.fetch !== savedFetch) globalThis.fetch = savedFetch;
  globalThis.__JSDOM_INSTALLED__ = true;
}

// Pin the UI locale to English for every test. The served product defaults to
// Spanish (i18n.js DEFAULT = "es"), but t() is identity under "en", so every
// existing English assertion holds byte-for-byte. Set it unconditionally on each
// import so a test file that ran a switch cannot leak "es" into the next file.
try {
  globalThis.localStorage.setItem("admin-lang", "en");
} catch {
  /* localStorage can be unavailable in hardened environments. */
}
