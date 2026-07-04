import { el, clear, field, isSafeImageSrc, subTabs } from "./el.js";
import { t } from "./i18n.js";
import { SECTIONS } from "./personaForm.js";

export function ArticlesPanel({ api } = {}) {
  const liveList = el("div", { class: "article-card-list scroll-pane" });
  const deletedList = el("div", { class: "article-card-list scroll-pane" });

  const tabs = subTabs(
    [
      {
        id: "published",
        label: t("Articles"),
        content: el("section", { class: "panel panel-fill" }, [
          el("div", { class: "panel-head" }, [el("h2", {}, t("Articles"))]),
          liveList,
        ]),
      },
      {
        id: "deleted",
        label: t("Deleted articles"),
        content: el("section", { class: "panel panel-fill" }, [
          el("div", { class: "panel-head" }, [el("h2", {}, t("Deleted articles"))]),
          deletedList,
        ]),
      },
    ],
    { className: "content-tabs", label: t("Article sections") },
  );

  const element = el("div", { class: "articles workspace-section" }, tabs.element);

  async function reload() {
    renderLoading(liveList);
    renderLoading(deletedList);
    try {
      const data = await api.listArticles({ order: "newest", limit: 500, include_deleted: true });
      const articles = (data && data.articles) || [];
      const live = articles.filter((article) => !article.deleted);
      const deleted = articles.filter((article) => article.deleted);
      renderList(liveList, live, t("No articles yet."));
      renderList(deletedList, deleted, t("No deleted articles."));
    } catch (err) {
      renderError(liveList, t("Could not load articles: {msg}", { msg: err.message }));
      renderError(deletedList, t("Could not load articles: {msg}", { msg: err.message }));
    }
  }

  function renderLoading(node) {
    clear(node);
    node.append(el("p", { class: "muted" }, t("Loading articles...")));
  }

  function renderError(node, message) {
    clear(node);
    node.append(el("p", { class: "error", role: "alert" }, message));
  }

  function renderList(node, articles, emptyText) {
    clear(node);
    if (!articles.length) {
      node.append(el("p", { class: "muted empty-state" }, emptyText));
      return;
    }
    for (const article of articles) node.append(card(article));
  }

  function card(article) {
    const cardStatus = el("p", { class: "form-status", role: "status", "aria-live": "polite" });
    const openBtn = el("button", { type: "button", class: "secondary" }, t("Open"));
    openBtn.setAttribute("aria-expanded", "false");

    let mutateBtn;
    if (article.deleted) {
      mutateBtn = el("button", { type: "button", class: "secondary" }, t("Restore"));
      mutateBtn.addEventListener("click", () => act(mutateBtn, () => api.restoreArticle(article.slug), cardStatus));
    } else {
      let armed = false;
      mutateBtn = el("button", { type: "button", class: "secondary" }, t("Delete"));
      mutateBtn.addEventListener("click", () => {
        if (!armed) {
          armed = true;
          mutateBtn.textContent = t("Confirm");
          mutateBtn.dataset.confirm = "true";
          return;
        }
        act(mutateBtn, () => api.deleteArticle(article.slug), cardStatus);
      });
    }

    const row = el(
      "article",
      {
        class: "article-row article-card",
        dataset: { slug: article.slug, section: article.section || "", deleted: article.deleted ? "true" : "false" },
      },
      [
        preview(article),
        el("div", { class: "article-actions" }, [openBtn, mutateBtn]),
        cardStatus,
      ],
    );

    let dialog = null;
    async function openDetail() {
      if (dialog) {
        dialog.showModal();
        return;
      }
      cardStatus.textContent = t("Loading article...");
      try {
        const full = await api.getArticle(article.slug);
        cardStatus.textContent = "";
        dialog = buildDetailDialog(full, cardStatus);
        document.body.append(dialog);
        dialog.showModal();
      } catch (err) {
        cardStatus.textContent = t("Could not load article: {msg}", { msg: err.message });
      }
    }

    openBtn.addEventListener("click", () => {
      openBtn.setAttribute("aria-expanded", "true");
      openDetail().finally(() => openBtn.setAttribute("aria-expanded", "false"));
    });

    return row;
  }

  function preview(article) {
    const metadata = article.metadata || {};
    const image = metadata.image;
    const subtitle = metadata.subtitle || metadata.description || "";
    const date = String(article.published_at || "").slice(0, 10);
    const topics = (article.topics || []).slice(0, 4);
    const hasImage = isSafeImageSrc(image);
    return el("div", { class: "site-preview", dataset: { media: hasImage ? "true" : "false" } }, [
      hasImage ? el("figure", { class: "site-preview-media" }, el("img", { src: image, alt: article.title || "" })) : null,
      el("div", { class: "site-preview-body" }, [
        el("div", { class: "site-preview-kicker" }, [
          el("span", { class: "section-link" }, sectionLabel(article.section)),
          date ? el("time", { datetime: article.published_at || "" }, date) : null,
          article.deleted ? el("span", { class: "badge" }, t("deleted")) : null,
        ]),
        el("h3", { class: "site-preview-title" }, article.title || ""),
        subtitle ? el("p", { class: "site-preview-subtitle" }, subtitle) : null,
        topics.length ? el("div", { class: "topic-chips" }, topics.map((topic) => el("span", { class: "topic-chip" }, topic))) : null,
        el("div", { class: "site-preview-meta" }, [
          el("span", { class: "byline" }, article.author || t("unknown")),
          el("span", { class: "published" }, article.published_at || ""),
        ]),
      ]),
    ]);
  }

  function buildDetailDialog(full, cardStatus) {
    const close = el("button", { type: "button", class: "secondary article-dialog-close", "aria-label": t("Close") }, "×");
    const dialog = el("dialog", { class: "article-dialog" });
    close.addEventListener("click", () => dialog.close());
    dialog.addEventListener("cancel", () => dialog.close());
    dialog.append(
      el("div", { class: "article-dialog-shell" }, [
        el("div", { class: "article-dialog-head" }, [
          el("div", {}, [
            el("span", { class: "section-link" }, sectionLabel(full.section)),
            el("h2", {}, full.title || ""),
          ]),
          close,
        ]),
        buildDetail(full, cardStatus),
      ]),
    );
    return dialog;
  }

  function buildDetail(full, cardStatus) {
    const wrap = el("div", { class: "article-detail-wrap" });

    const slug = full.slug;
    const title = el("input", { type: "text", id: `ae-${slug}-title`, value: full.title || "" });
    const author = el("input", { type: "text", id: `ae-${slug}-author`, value: full.author || "" });
    const section = el("select", { id: `ae-${slug}-section` }, SECTIONS.map((s) => el("option", { value: s }, s)));
    section.value = full.section || SECTIONS[0];
    const topics = el("input", { type: "text", id: `ae-${slug}-topics`, value: (full.topics || []).join(", ") });
    const body = el("textarea", { id: `ae-${slug}-body`, rows: "8" }, full.body || "");
    const save = el("button", { type: "submit" }, t("Save article"));

    const editForm = el("form", { class: "article-edit" }, [
      field(t("Title"), title, `ae-${slug}-title`),
      field(t("Author"), author, `ae-${slug}-author`),
      field(t("Section"), section, `ae-${slug}-section`),
      field(t("Topics (comma separated)"), topics, `ae-${slug}-topics`),
      field(t("Body"), body, `ae-${slug}-body`),
      el("div", { class: "article-actions" }, [save]),
    ]);
    editForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const payload = {
        title: title.value.trim(),
        body: body.value,
        author: author.value.trim(),
        section: section.value,
        topics: topics.value.split(/[,\n]/).map((s) => s.trim()).filter(Boolean),
      };
      if (full.metadata && Object.keys(full.metadata).length) payload.metadata = full.metadata;
      save.disabled = true;
      setStatus(cardStatus, "pending", t("Publishing article..."));
      try {
        await api.updateArticle(slug, payload);
        setStatus(cardStatus, "done", t("Article saved."));
        await reload();
      } catch (err) {
        save.disabled = false;
        setStatus(cardStatus, "error", t("Could not save article ({code}): {msg}", { code: err.code, msg: err.message }));
      }
    });

    const previewPane = el("div", { class: "article-preview-pane" }, [
      preview(full),
      el("pre", { class: "article-body" }, full.body || ""),
    ]);
    const detailTabs = subTabs(
      [
        { id: "edit", label: t("Edit"), content: editForm },
        { id: "preview", label: t("Preview"), content: previewPane },
      ],
      { className: "editor-tabs", label: t("Article editor") },
    );
    wrap.append(detailTabs.element);
    return wrap;
  }

  async function act(button, run, statusNode) {
    button.disabled = true;
    try {
      await run();
      await reload();
    } catch (err) {
      button.disabled = false;
      setStatus(statusNode, "error", t("Could not save article ({code}): {msg}", { code: err.code, msg: err.message }));
    }
  }

  return { element, reload };
}

function sectionLabel(section) {
  const labels = { politics: "Política", economics: "Misterio y conspiración", tech: "Tecnología", world: "Mundo" };
  return labels[section] || section || "CN";
}

function setBusy(button, formEl, busy) {
  button.disabled = busy;
  formEl.setAttribute("aria-busy", busy ? "true" : "false");
}

function setStatus(node, state, text) {
  node.dataset.state = state;
  node.textContent = text;
  const assertive = state === "error";
  node.setAttribute("role", assertive ? "alert" : "status");
  node.setAttribute("aria-live", assertive ? "assertive" : "polite");
}
