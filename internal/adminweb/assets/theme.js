(function () {
  const storageKey = "admin-theme";
  const root = document.documentElement;
  const controls = Array.from(document.querySelectorAll("[data-theme-control]"));

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
    controls.forEach((control) => {
      control.value = theme;
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
  controls.forEach((control) => {
    control.addEventListener("change", () => saveTheme(control.value));
  });
  markActiveNav();
})();
