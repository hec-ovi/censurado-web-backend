(function () {
  const storageKey = "admin-theme";
  const root = document.documentElement;
  const choices = Array.from(document.querySelectorAll("[data-theme-choice]"));

  function preferredTheme() {
    try {
      return localStorage.getItem(storageKey) || "system";
    } catch {
      return "system";
    }
  }

  function applyTheme(value) {
    const theme = value === "light" || value === "dark" ? value : "system";
    root.dataset.theme = theme;
    choices.forEach((button) => {
      button.setAttribute("aria-pressed", button.dataset.themeChoice === theme ? "true" : "false");
    });
  }

  function saveTheme(value) {
    try {
      localStorage.setItem(storageKey, value);
    } catch {
      /* localStorage can be unavailable in hardened browsers. */
    }
    applyTheme(value);
  }

  function markActiveNav() {
    const path = window.location.pathname.replace(/\/$/, "");
    document.querySelectorAll(".admin-nav-link").forEach((link) => {
      const href = new URL(link.getAttribute("href"), window.location.origin).pathname.replace(/\/$/, "");
      const active = href === "/admin/articles" ? path === "/admin" || path.startsWith("/admin/articles") : path.startsWith(href);
      link.toggleAttribute("aria-current", active);
    });
  }

  applyTheme(preferredTheme());
  choices.forEach((button) => {
    button.addEventListener("click", () => saveTheme(button.dataset.themeChoice));
  });
  markActiveNav();
})();
