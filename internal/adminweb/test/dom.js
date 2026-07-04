import { beforeEach } from "node:test";

// jsdom is installed by test/setup.js (imported first in each test file), so the
// global document is ready before @testing-library loads. Here we only reset
// document.body between tests so each starts from a clean DOM.
export function installDom() {
  beforeEach(() => {
    document.body.innerHTML = "";
  });
}
