(function () {
  // The active UI language is server-resolved from the admin_lang cookie, so the
  // switch sets that cookie (scoped to /admin) and reloads. Path /admin matches
  // the cookie the Go handler reads via r.Cookie("admin_lang").
  document.querySelectorAll("[data-lang-choice]").forEach(function (button) {
    button.addEventListener("click", function () {
      document.cookie =
        "admin_lang=" + button.dataset.langChoice + ";path=/admin;max-age=31536000;samesite=Lax";
      window.location.reload();
    });
  });
})();
