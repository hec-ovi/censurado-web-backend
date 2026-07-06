import { currentLocale, setLocale, t } from "./components/i18n.js";

const THEME_KEY = "panel-theme";
const themeChoices = document.querySelectorAll("[data-theme-choice]");
const langChoices = document.querySelectorAll("[data-lang-choice]");

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
  themeChoices.forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.themeChoice === theme));
  });
  return theme;
}

function saveTheme(value) {
  const theme = applyTheme(value);
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    /* localStorage can be unavailable in hardened browsers. */
  }
}

function applyLocale(value) {
  const locale = value === "en" || value === "es" ? value : currentLocale();
  document.documentElement.lang = locale;
  langChoices.forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.langChoice === locale));
  });
  document.querySelectorAll("[data-i18n]").forEach((node) => {
    node.textContent = t(node.dataset.i18n);
  });
  document.querySelectorAll("[data-i18n-placeholder]").forEach((node) => {
    node.setAttribute("placeholder", t(node.dataset.i18nPlaceholder));
  });
  document.querySelectorAll("[data-i18n-aria]").forEach((node) => {
    node.setAttribute("aria-label", t(node.dataset.i18nAria));
  });
}

applyTheme(readTheme());
applyLocale(currentLocale());

themeChoices.forEach((button) => {
  button.addEventListener("click", () => saveTheme(button.dataset.themeChoice));
});

langChoices.forEach((button) => {
  button.addEventListener("click", () => {
    applyLocale(setLocale(button.dataset.langChoice));
  });
});
