import { before, after, afterEach } from "node:test";
import { setupServer } from "msw/node";

export const ORIGIN = "http://localhost";

// Stand up an MSW server for a test file and wire its lifecycle into node:test.
// onUnhandledRequest "error" makes any call the test did not stub a hard failure,
// so a forgotten handler surfaces instead of hanging.
export function installServer() {
  const server = setupServer();
  before(() => server.listen({ onUnhandledRequest: "error" }));
  afterEach(() => server.resetHandlers());
  after(() => server.close());
  return server;
}
