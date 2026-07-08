import "./setup.js";
import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { http, HttpResponse } from "msw";
import { screen, within, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { installServer, ORIGIN } from "./msw.js";
import { installDom } from "./dom.js";
import { PersonaList } from "../static/components/personaList.js";
import { PersonaForm } from "../static/components/personaForm.js";
import { applyCatalog } from "../static/components/i18n.js";
import { api } from "../static/api.js";

installDom();
const server = installServer();

// The section catalog is shared module state; reset it after any test that injects one.
afterEach(() => applyCatalog({}));

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

function sourcesHandler(sources = []) {
  return http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources }));
}

test("lists authors from the backend /authors, with the beat badge showing the shared section label", async () => {
  // With the shared section catalog injected, the roster beat badge renders the label
  // (the same source of record the beat pickers use), not the raw slug.
  applyCatalog({ sections: { tech: "Technology" } });
  server.use(
    sourcesHandler(),
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
  const row = heading.closest(".persona-row");
  assert.ok(row);
  // The badge shows the injected label, not the raw slug (which the dataset keeps).
  assert.ok(within(row).getByText("Technology"), "beat badge shows the section label");
  assert.equal(within(row).queryByText("tech"), null, "beat badge does not show the raw slug");
  assert.equal(row.dataset.section, "tech", "the behavior-bearing slug stays on the dataset");
  assert.ok(within(row).getByText("Computing pioneer"));
  assert.equal(within(row).queryByRole("button", { name: /^edit$/i }), null);
});

test("renders a clean zero-state for an empty author registry", async () => {
  server.use(sourcesHandler(), http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })));
  const { list } = mount();
  await list.reload();

  await screen.findByText(/no authors yet/i);
});

test("create upserts a backend author: derived handle + metadata beat/who-i-am/language + gender", async () => {
  let posted = null;
  let created = false;
  server.use(
    sourcesHandler([{ slug: "example-com", domain: "example.com", description: "Example", lean: "neutral", enabled: true, feed_urls: [], metadata: {} }]),
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: created
          ? [{ handle: "ada-reporter", name: "Ada Reporter", avatar: "/media/ada.png", style: "", about: "", gender: "female", topics: [], sources: ["example-com"], metadata: { beat: "world", who_i_am: "world desk" }, deleted: false }]
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
  await user.click(await screen.findByLabelText("example.com"));
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
  assert.deepEqual(posted.sources, ["example-com"]);
  // The roster reloads and shows the new author.
  await screen.findByText("Ada Reporter");
  assert.equal(screen.queryByText(/no authors yet/i), null);
});

test("a blank beat keeps the form in its invalid state with no POST", async () => {
  let posted = false;
  server.use(
    sourcesHandler(),
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

test("editing an author with a legacy off-list beat preserves that beat", async () => {
  let posted = null;
  server.use(
    sourcesHandler(),
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [{ handle: "leg", name: "Legacy Writer", avatar: "/media/legacy.png", style: "old style", about: "old about", gender: "female", topics: [], sources: [], metadata: { beat: "removed-section", who_i_am: "old hand", language: "es" }, deleted: false }],
      })),
    http.get(`${ORIGIN}/sources`, () => HttpResponse.json({ sources: [] })),
    http.get(`${ORIGIN}/authors/leg/sources`, () => HttpResponse.json({ handle: "leg", sources: [] })),
    http.post(`${ORIGIN}/authors`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json(posted);
    }),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();

  const row = (await screen.findByText("Legacy Writer")).closest(".persona-row");
  await user.click(row);
  const dialog = await screen.findByRole("dialog", { name: "Author editor" });
  assert.equal(within(dialog).queryByRole("tablist"), null);
  assert.equal(within(dialog).queryByRole("heading", { name: /legacy writer/i }), null);
  assert.ok(within(dialog).getByRole("heading", { name: /prompt instructions/i }));
  const layout = dialog.querySelector(".persona-edit-layout");
  const identity = layout.querySelector(".persona-identity-column");
  assert.ok(identity, "identity column is present");
  assert.ok(layout.querySelector(".persona-writing-column"), "writing column is present");
  assert.ok(layout.querySelector(".persona-sources--side"), "sources column is present");
  // About moved into the identity column, positioned AFTER Language.
  assert.ok(identity.querySelector("#pe-leg-about"), "About is in the identity column");
  const identityFieldIds = [...identity.querySelectorAll("input, select, textarea")].map((n) => n.id);
  assert.ok(
    identityFieldIds.indexOf("pe-leg-language") < identityFieldIds.indexOf("pe-leg-about"),
    "About comes after Language",
  );
  // The beat picker only offers canonical sections, but an author sitting on a removed or
  // renamed section keeps their value: the select shows it, so saving another field cannot
  // silently rewrite the beat to the first canonical option.
  assert.equal(within(dialog).getByLabelText(/^beat$/i).value, "removed-section");
  const save = within(dialog).getByRole("button", { name: "Save" });
  assert.equal(save.disabled, true, "clean edit form cannot save");
  await user.type(within(dialog).getByLabelText(/^about$/i), " updated");
  assert.equal(save.disabled, false);
  await user.click(save);
  assert.equal(posted, null, "first save click only arms confirmation");
  await user.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await waitFor(() => assert.ok(posted, "author update was posted"));
  assert.equal(posted.metadata.beat, "removed-section");
});

test("the linked-sources filter narrows by active-on-author and orientation", async () => {
  server.use(
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [{ handle: "ada", name: "Ada", avatar: "", style: "s", about: "a", gender: "female", topics: [], sources: [], metadata: { beat: "tech", who_i_am: "w", language: "es" }, deleted: false }],
      })),
    http.get(`${ORIGIN}/sources`, () =>
      HttpResponse.json({
        sources: [
          { slug: "left-co", domain: "left.co", description: "", lean: "left", enabled: true, feed_urls: [], metadata: {} },
          { slug: "right-co", domain: "right.co", description: "", lean: "right", enabled: true, feed_urls: [], metadata: {} },
          { slug: "neutral-co", domain: "neutral.co", description: "", lean: "neutral", enabled: true, feed_urls: [], metadata: {} },
        ],
      })),
    // Only left-co is linked (active) on this author.
    http.get(`${ORIGIN}/authors/ada/sources`, () => HttpResponse.json({ handle: "ada", sources: ["left-co"] })),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();
  await user.click((await screen.findByText("Ada")).closest(".persona-row"));
  const dialog = await screen.findByRole("dialog", { name: "Author editor" });

  const leftRow = (await within(dialog).findByLabelText("left.co")).closest(".link-source-item");
  const rightRow = within(dialog).getByLabelText("right.co").closest(".link-source-item");
  const neutralRow = within(dialog).getByLabelText("neutral.co").closest(".link-source-item");

  // "Active on this author" = only the linked source.
  await user.selectOptions(within(dialog).getByLabelText(/active on this author/i), "active");
  assert.equal(leftRow.hidden, false);
  assert.equal(rightRow.hidden, true);
  assert.equal(neutralRow.hidden, true);

  // Orientation = right (after clearing the active filter).
  await user.selectOptions(within(dialog).getByLabelText(/active on this author/i), "");
  await user.selectOptions(within(dialog).getByLabelText(/orientation/i), "right");
  assert.equal(rightRow.hidden, false);
  assert.equal(leftRow.hidden, true);
  assert.equal(neutralRow.hidden, true);

  // Clear filters restores every row.
  await user.click(within(dialog).getByRole("button", { name: /clear filters/i }));
  assert.equal(leftRow.hidden, false);
  assert.equal(rightRow.hidden, false);
  assert.equal(neutralRow.hidden, false);
});

test("single author edit saves checked source slugs via the fullscreen editor", async () => {
  let putBody = null;
  let posted = null;
  server.use(
    http.get(`${ORIGIN}/authors`, () =>
      HttpResponse.json({
        authors: [{ handle: "ada", name: "Ada Lovelace", avatar: "/media/ada.png", style: "precise", about: "analyst", gender: "female", topics: [], sources: [], metadata: { beat: "tech", who_i_am: "pioneer", language: "en" }, deleted: false }],
      })),
    sourcesHandler([{ slug: "example-com", domain: "example.com", description: "Example", lean: "neutral", enabled: true, feed_urls: [], metadata: {} }]),
    http.get(`${ORIGIN}/authors/ada/sources`, () => HttpResponse.json({ handle: "ada", sources: [] })),
    http.post(`${ORIGIN}/authors`, async ({ request }) => {
      posted = await request.json();
      return HttpResponse.json(posted);
    }),
    http.put(`${ORIGIN}/authors/ada/sources`, async ({ request }) => {
      putBody = await request.json();
      return HttpResponse.json({ handle: "ada", sources: putBody.sources });
    }),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();

  const row = (await screen.findByText("Ada Lovelace")).closest(".persona-row");
  await user.click(row);
  const dialog = await screen.findByRole("dialog", { name: "Author editor" });

  const box = await within(dialog).findByLabelText("example.com");
  await user.click(box);
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  await user.click(within(dialog).getByRole("button", { name: "Confirm" }));

  await waitFor(() => assert.ok(posted, "profile is upserted before source links"));
  await waitFor(() => assert.ok(putBody, "a PUT should have been sent"));
  assert.deepEqual(putBody.sources, ["example-com"]);
  await within(dialog).findByText(/saved/i);
});

test("the gender select shows English labels while each option value stays the enum", async () => {
  server.use(sourcesHandler(), http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })));
  const { list } = mount();
  await list.reload();

  const gender = screen.getByLabelText(/^gender$/i);
  const options = [...gender.querySelectorAll("option")];
  // The behavior-bearing enum stays the option value (posted to the server); only the
  // visible label is the English word, no longer the raw lowercase enum.
  assert.deepEqual(options.map((o) => o.value), ["", "female", "male", "nonbinary"]);
  assert.deepEqual(options.map((o) => o.textContent), ["Unspecified", "Female", "Male", "Nonbinary"]);
});

test("a display name with no letters or digits is rejected with no POST", async () => {
  let posted = false;
  server.use(
    sourcesHandler(),
    http.get(`${ORIGIN}/authors`, () => HttpResponse.json({ authors: [] })),
    http.post(`${ORIGIN}/authors`, () => {
      posted = true;
      return HttpResponse.json({ handle: "x" });
    }),
  );
  const user = userEvent.setup();
  const { list } = mount();
  await list.reload();

  // Every required field is filled (so submit enables), but the display name has no
  // alphanumerics, so its derived handle is empty and the client rejects it.
  await user.type(screen.getByLabelText(/display name/i), "!!!");
  await user.selectOptions(screen.getByLabelText(/^gender$/i), "female");
  await user.selectOptions(screen.getByLabelText(/^beat$/i), "world");
  await user.type(screen.getByLabelText(/^language$/i), "es");
  await user.type(screen.getByLabelText(/avatar path/i), "/media/x.png");
  await user.type(screen.getByLabelText(/who i am/i), "someone");
  await user.type(screen.getByLabelText(/^style$/i), "a style");
  await user.type(screen.getByLabelText(/^about$/i), "about");
  await user.click(screen.getByRole("button", { name: /create author/i }));

  await screen.findByText("Display name must contain letters or digits.");
  assert.equal(posted, false, "no POST for a name that slugifies to empty");
});
