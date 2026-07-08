const THEME_KEY = "panel-theme";
const themeChoices = document.querySelectorAll("[data-theme-choice]");

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

applyTheme(readTheme());

themeChoices.forEach((button) => {
  button.addEventListener("click", () => saveTheme(button.dataset.themeChoice));
});
