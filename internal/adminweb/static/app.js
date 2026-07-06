import { api as defaultApi } from "./api.js";
import { el, help, subTabs } from "./components/el.js";
import { t, currentLocale, setLocale, LANGS, LANG_LABEL } from "./components/i18n.js";
import { Health } from "./components/health.js";
import { ArticlesPanel } from "./components/articlesPanel.js";
import { PortadaPanel } from "./components/portadaPanel.js";
import { PersonaList } from "./components/personaList.js";
import { PersonaForm } from "./components/personaForm.js";
import { TemasPanel } from "./components/temasPanel.js";
import { SourcesPanel } from "./components/sourcesPanel.js";

// The byline-freeze note for the Autores tab (section 1.6): editing a persona does
// not rewrite the author name/bio/avatar already stamped on published articles.
const BYLINE_FREEZE_NOTE =
  "Editing a persona does not rewrite already-published bylines: a published article's author name, bio, and " +
  "avatar are frozen from the earliest article that carried them.";

const THEME_KEY = "panel-theme";

function readTheme() {
  try {
    return localStorage.getItem(THEME_KEY) || "system";
  } catch {
    return "system";
  }
}

function applyTheme(value) {
  const theme = value === "light" || value === "dark" ? value : "system";
  document.documentElement.dataset.theme = theme;
  return theme;
}

function saveTheme(value) {
  const theme = applyTheme(value);
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    /* localStorage can be unavailable in hardened browsers. */
  }
  return theme;
}

// The visible theme label is translated; the dataset.theme value (system/light/
// dark) is the behavior-bearing token and stays untranslated.
const THEME_LABEL = { system: "System", light: "Light", dark: "Dark" };

function ThemeControl() {
  const buttons = ["system", "light", "dark"].map((theme) =>
    el(
      "button",
      {
        type: "button",
        dataset: { theme },
        "aria-pressed": "false",
        onClick: () => setTheme(theme),
      },
      t(THEME_LABEL[theme]),
    ),
  );

  function setTheme(theme) {
    const active = saveTheme(theme);
    buttons.forEach((button) => {
      button.setAttribute("aria-pressed", button.dataset.theme === active ? "true" : "false");
    });
  }

  const initial = applyTheme(readTheme());
  buttons.forEach((button) => {
    button.setAttribute("aria-pressed", button.dataset.theme === initial ? "true" : "false");
  });
  return el("div", { class: "admin-theme-switch", role: "group", "aria-label": t("Theme") }, buttons);
}

function BrandWordmark(className) {
  return el("div", { class: className, "aria-label": "El Censurado Web" }, [
    el("span", { class: "brand-word brand-word-el" }, [
      el("span", { class: "brand-el-e" }, "E"),
      el("span", { class: "brand-el-l" }, "L"),
    ]),
    el("span", { class: "brand-word brand-word-censurado" }, "censurado"),
    el("span", { class: "brand-word brand-word-web" }, ["W", el("span", { class: "brand-e" }, "e"), "b"]),
  ]);
}

// Mount the panel into `root`. `deps.api` is injectable so a test can mount the
// whole app against MSW or a stub; production passes nothing and the real /api
// client is used. Returns the live component handles for tests.
//
// The panel is a SIX-tab ARIA tablist: Articles, Portada, Autores, Temas, Sources,
// Status. It talks to the backend only (same-origin); there is no brain proxy and no
// prompt/workflow editing (those live in the brain's prompt files, edited in git).
// Switching tabs only toggles `hidden` (hide, not unmount), so controls in inactive
// tabs stay in the DOM and remain findable by tests and assistive tech. Every tab is
// a real panel; none are placeholders.
export function mountApp(root, deps = {}) {
  const api = deps.api || defaultApi;

  const health = Health({ api });
  const articles = ArticlesPanel({ api });
  const portada = PortadaPanel({ api });
  const list = PersonaList({ api });
  const form = PersonaForm({ api, onCreated: () => list.reload() });
  const temas = TemasPanel({ api });
  const sources = SourcesPanel({ api });

  const newPersonaPanel = el("section", { class: "panel" }, [
    el("div", { class: "panel-head" }, [
      el("h2", {}, t("New persona")),
      help(t("Create an author persona from explicit fields. The new author joins the roster below.")),
    ]),
    el("p", { class: "muted" }, t(BYLINE_FREEZE_NOTE)),
    form.element,
  ]);
  const authorsSurface = subTabs(
    [
      { id: "new-author", label: t("New persona"), content: newPersonaPanel },
      { id: "authors-list", label: t("Authors"), content: list.element },
    ],
    { className: "content-tabs", label: t("Author sections") },
  );

  const healthPanel = el("section", { class: "panel" }, [
    el("div", { class: "panel-head" }, [
      el("h2", {}, t("Health")),
      help(t("Live ping of the backend that serves this panel. Green means it answered.")),
    ]),
    health.element,
  ]);

  const tabs = [
    { id: "articles", label: t("Articles"), content: [articles.element] },
    { id: "portada", label: t("Portada"), content: [portada.element] },
    { id: "authors", label: t("Authors"), content: [authorsSurface.element] },
    { id: "temas", label: t("Topics"), content: [temas.element] },
    { id: "sources", label: t("Sources"), content: [sources.element] },
    { id: "status", label: t("Status"), content: [healthPanel] },
  ];

  const tabButtons = [];
  const panels = [];
  const tablist = el("div", { class: "tablist", role: "tablist", "aria-label": t("Console sections") });
  const pageTitle = el("h1", { class: "page-title" }, tabs[0].label);
  let active = 0;

  function select(i) {
    active = i;
    pageTitle.textContent = tabs[i].label;
    tabButtons.forEach((btn, j) => {
      const on = j === i;
      btn.setAttribute("aria-selected", on ? "true" : "false");
      btn.toggleAttribute("aria-current", on);
      btn.tabIndex = on ? 0 : -1;
    });
    panels.forEach((p, j) => {
      p.hidden = j !== i;
    });
    tabButtons[i].focus();
  }

  function onTabKey(event, i) {
    const last = tabs.length - 1;
    let next = null;
    if (event.key === "ArrowRight") next = i === last ? 0 : i + 1;
    else if (event.key === "ArrowLeft") next = i === 0 ? last : i - 1;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = last;
    if (next != null) {
      event.preventDefault();
      select(next);
    }
  }

  tabs.forEach((tab, i) => {
    const tabId = `tab-${tab.id}`;
    const panelId = `panel-${tab.id}`;
    const button = el(
      "button",
      {
        class: "tab",
        role: "tab",
        type: "button",
        id: tabId,
        "aria-controls": panelId,
        "aria-selected": i === 0 ? "true" : "false",
        tabindex: i === 0 ? "0" : "-1",
        onClick: () => select(i),
        onKeydown: (event) => onTabKey(event, i),
      },
      tab.label,
    );
    const panel = el(
      "div",
      { class: "tab-panel", role: "tabpanel", id: panelId, "aria-labelledby": tabId, tabindex: "0" },
      el("div", { class: "tab-stack" }, tab.content),
    );
    panel.hidden = i !== 0;
    tabButtons.push(button);
    panels.push(panel);
    tablist.append(button);
  });

  // The language switch mirrors ThemeControl: two buttons (EN/ES), aria-pressed by
  // the active locale. Switching persists the choice and RE-MOUNTS the whole app
  // through mountApp(root, deps), so every string rebuilds in the new locale.
  function LangControl() {
    const activeLocale = currentLocale();
    const buttons = LANGS.map((lang) =>
      el(
        "button",
        {
          type: "button",
          dataset: { lang },
          "aria-pressed": lang === activeLocale ? "true" : "false",
          onClick: () => {
            setLocale(lang);
            mountApp(root, deps);
          },
        },
        LANG_LABEL[lang],
      ),
    );
    return el("div", { class: "admin-lang-switch", role: "group", "aria-label": t("Language") }, buttons);
  }

  const sidebar = el("aside", { class: "app-sidebar", "aria-label": t("Primary") }, tablist);

  const topbar = el("header", { class: "app-topbar" }, [
    el("div", { class: "app-header-brand" }, [BrandWordmark("admin-wordmark admin-wordmark-shell")]),
    el("div", { class: "app-header-divider", "aria-hidden": "true" }),
    el("div", { class: "app-titleblock" }, [el("p", { class: "kicker" }, t("Control panel")), pageTitle]),
    el("div", { class: "admin-controls" }, [LangControl(), ThemeControl()]),
  ]);

  root.replaceChildren(
    el("div", { class: "app-shell" }, [
      topbar,
      sidebar,
      el("div", { class: "app-workspace" }, [el("main", { class: "app-main" }, panels)]),
    ]),
  );

  // Keep the document chrome in step with the active locale.
  document.documentElement.lang = currentLocale();
  document.title = t("Admin Panel");

  health.check();
  articles.reload();
  portada.reload();
  list.reload();
  temas.reload();
  sources.reload();

  return { health, articles, portada, list, form, temas, sources, tabs: tabButtons, panels, select };
}

// Browser entry point. index.html loads this module under a strict CSP (no inline
// script), so the boot runs here rather than from an inline <script>. Guarded on
// the #app element so importing mountApp in a test (where no #app exists at import
// time) has no side effect.
const root = typeof document !== "undefined" ? document.getElementById("app") : null;
if (root) mountApp(root);
