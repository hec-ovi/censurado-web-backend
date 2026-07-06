import "./setup.js";
import { test } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { PersonaList } from "../static/components/personaList.js";
import { PersonaForm } from "../static/components/personaForm.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

// Mount the form + list wired together exactly as the app does (the create form
// reloads the roster on success), so the test exercises the real Autores surface
// against the same-origin backend author registry (no brain).
function mount() {
  const list = PersonaList({ api });
  const form = PersonaForm({ api, onCreated: () => list.reload() });
  document.body.appendChild(form.element);
  document.body.appendChild(list.element);
  return { list, form };
}

test("lists authors from the backend /authors (beat lives in metadata)", async () => {
  server.use(
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [
          { handle: "ada", name: "Ada Lovelace", avatar: "", style: "", about: "", gender: "", topics: [], sources: [], metadata: { beat: "tech", who_i_am: "Computing pioneer" }, deleted: false },
        ],
      })),
  );
  const { list } = mount();
  await list.reload();

  const heading = await screen.findByText("Ada Lovelace");
  const card = heading.closest(".persona-card");
  assert.ok(card);
  assert.ok(within(card).getByText("tech"));
  assert.ok(within(card).getByText("Computing pioneer"));
});

test("renders a clean zero-state for an empty author registry", async () => {
  server.use(http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })));
  const { list } = mount();
  await list.reload();

  await screen.findByText(/no authors yet/i);
});

test("create upserts a backend author: derived handle + metadata beat/who-i-am/language + gender", async () => {
  let posted = null;
  let created = false;
  server.use(
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: created
          ? [{ handle: "ada-reporter", name: "Ada Reporter", avatar: "", style: "", about: "", gender: "female", topics: ["politics"], sources: [], metadata: { beat: "world", who_i_am: "world desk" }, deleted: false }]
          : [],
      })),
    http.post(`${ORIGIN}/authors`, async ({ request }) => {
      posted = await request.json();
      created = true;
      return HttpResponse.json({ handle: "ada-reporter", name: "Ada Reporter" });
    }),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();
  await screen.findByText(/no authors yet/i);

  // Every author field is required now (topics are curated separately, not on create).
  await user.type(screen.getByLabelText(/display name/i), "Ada Reporter");
  await user.selectOptions(screen.getByLabelText(/^gender$/i), "female");
  await user.selectOptions(screen.getByLabelText(/^beat$/i), "world");
  await user.type(screen.getByLabelText(/^language$/i), "es");
  await user.type(screen.getByLabelText(/avatar path/i), "/media/ada.png");
  await user.type(screen.getByLabelText(/who i am/i), "covers the world desk");
  await user.type(screen.getByLabelText(/^style$/i), "plain and direct");
  await user.type(screen.getByLabelText(/^about$/i), "World desk reporter");
  await user.click(screen.getByRole("button", { name: /create author/i }));

  await waitFor(() => assert.ok(posted, "a POST should have been sent"));
  // Backend author shape: handle derived from the display name; beat/who-i-am/language ride
  // metadata; gender/about/style/avatar are first-class. No topics field on the create form.
  assert.equal(posted.handle, "ada-reporter");
  assert.equal(posted.name, "Ada Reporter");
  assert.equal(posted.style, "plain and direct");
  assert.equal(posted.about, "World desk reporter");
  assert.equal(posted.gender, "female");
  assert.equal(posted.avatar, "/media/ada.png");
  assert.equal(posted.metadata.beat, "world");
  assert.equal(posted.metadata.who_i_am, "covers the world desk");
  assert.equal(posted.metadata.language, "es");
  assert.equal(posted.topics, undefined);
  // The roster reloads and shows the new author.
  await screen.findByText("Ada Reporter");
  assert.equal(screen.queryByText(/no authors yet/i), null);
});

test("a blank beat keeps the form in its invalid state with no POST", async () => {
  let posted = false;
  server.use(
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.post(`${ORIGIN}/authors`, () => {
      posted = true;
      return HttpResponse.json({ handle: "x" });
    }),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();

  await user.type(screen.getByLabelText(/display name/i), "No Beat");
  await user.type(screen.getByLabelText(/who i am/i), "someone");
  await user.type(screen.getByLabelText(/^style$/i), "a style");

  // Beat (among other required fields) left blank: submit stays disabled, so no POST can
  // fire and the empty beat is flagged aria-invalid.
  const createBtn = screen.getByRole("button", { name: /create author/i });
  assert.equal(createBtn.disabled, true);
  await user.click(createBtn);
  assert.equal(posted, false, "no POST for an invalid form");
  assert.equal(screen.getByLabelText(/^beat$/i).getAttribute("aria-invalid"), "true");
});

test("source-link saves the checked source slugs via PUT /authors/{handle}/sources", async () => {
  let putBody = null;
  server.use(
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [{ handle: "ada", name: "Ada Lovelace", avatar: "", style: "", about: "", gender: "", topics: [], sources: [], metadata: { beat: "tech", who_i_am: "pioneer" }, deleted: false }],
      })),
    http.get(`${ORIGIN}/sources`, () =>
      HttpResponse.json({ sources: [{ slug: "example-com", domain: "example.com", description: "Example", lean: "neutral", enabled: true, feed_urls: [], metadata: {} }] })),
    http.get(`${ORIGIN}/authors/ada/sources`, () => HttpResponse.json({ handle: "ada", sources: [] })),
    http.put(`${ORIGIN}/authors/ada/sources`, async ({ request }) => {
      putBody = await request.json();
      return HttpResponse.json({ handle: "ada", sources: putBody.sources });
    }),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();

  const card = (await screen.findByText("Ada Lovelace")).closest(".persona-card");
  await user.click(within(card).getByRole("button", { name: /^edit$/i }));
  const dialog = await screen.findByRole("dialog", { name: /ada lovelace/i });
  await user.click(within(dialog).getByRole("tab", { name: /^sources$/i }));

  const box = await within(dialog).findByLabelText("example.com");
  await user.click(box);
  await user.click(within(dialog).getByRole("button", { name: /save sources/i }));

  await waitFor(() => assert.ok(putBody, "a PUT should have been sent"));
  assert.deepEqual(putBody.sources, ["example-com"]);
  await within(dialog).findByText(/sources updated/i);
});
