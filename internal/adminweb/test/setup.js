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

// The panel is single-language English: t() returns the English key unless a test
// installs a catalog via applyCatalog(), so every English assertion holds with no
// locale to pin. (A component test never injects a catalog, so it sees pure English.)
