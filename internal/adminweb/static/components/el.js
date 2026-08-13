// A tiny DOM builder so the components stay buildless and dependency-free.
// el("div", { class: "x", onClick: fn }, [child, "text"]) -> HTMLElement.

export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);

  for (const [key, value] of Object.entries(attrs)) {
    if (value == null || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key === "html") node.innerHTML = value;
    else if (key.startsWith("on") && typeof value === "function") {
      node.addEventListener(key.slice(2).toLowerCase(), value);
    } else if (value === true) node.setAttribute(key, "");
    else node.setAttribute(key, value);
  }

  for (const child of [].concat(children)) {
    if (child == null || child === false) continue;
    node.append(child.nodeType ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

export function subTabs(items, { className = "subtabs", label = "Sections" } = {}) {
  const buttons = [];
  const panels = [];
  const tablist = el("div", { class: `${className}-list`, role: "tablist", "aria-label": label });

  function select(i) {
    buttons.forEach((button, j) => {
      const on = i === j;
      button.setAttribute("aria-selected", on ? "true" : "false");
      button.tabIndex = on ? 0 : -1;
    });
    panels.forEach((panel, j) => {
      panel.hidden = i !== j;
    });
  }

  function onKeydown(event, i) {
    const last = items.length - 1;
    let next = null;
    if (event.key === "ArrowRight") next = i === last ? 0 : i + 1;
    else if (event.key === "ArrowLeft") next = i === 0 ? last : i - 1;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = last;
    if (next == null) return;
    event.preventDefault();
    select(next);
    buttons[next].focus();
  }

  items.forEach((item, i) => {
    const tabId = `${className}-${item.id}-tab`;
    const panelId = `${className}-${item.id}-panel`;
    const button = el(
      "button",
      {
        type: "button",
        class: `${className}-tab`,
        role: "tab",
        id: tabId,
        "aria-controls": panelId,
        "aria-selected": i === 0 ? "true" : "false",
        tabindex: i === 0 ? "0" : "-1",
        onClick: () => select(i),
        onKeydown: (event) => onKeydown(event, i),
      },
      item.label,
    );
    const panel = el(
      "section",
      {
        class: `${className}-panel`,
        role: "tabpanel",
        id: panelId,
        "aria-labelledby": tabId,
      },
      item.content,
    );
    panel.hidden = i !== 0;
    buttons.push(button);
    panels.push(panel);
    tablist.append(button);
  });

  return { element: el("div", { class: `${className}-shell` }, [tablist, ...panels]), buttons, panels, select };
}

// A labeled control: an explicit <label for=id> next to the control, so the
// accessible name is set and tests can query by role/label.
export function field(labelText, control, id) {
  return el("div", { class: "field" }, [el("label", { for: id }, labelText), control]);
}

let helpSeq = 0;

// A small inline (?) marker that carries an explanation for the control next to
// it. Clicking toggles a viewport-centered popup (.help-popover) instead of the
// browser title tooltip, so the explanation works with mouse, touch, and
// keyboard and no container can crop it.
export function help(text) {
  const id = `help-popover-${++helpSeq}`;
  const popover = el("span", { class: "help-popover", id, hidden: true }, text);
  const button = el(
    "button",
    {
      class: "help",
      type: "button",
      "aria-label": text,
      "aria-expanded": "false",
      "aria-describedby": id,
      title: text,
    },
    "?",
  );
  function setOpen(open) {
    popover.hidden = !open;
    button.setAttribute("aria-expanded", open ? "true" : "false");
  }
  button.addEventListener("click", (event) => {
    event.stopPropagation();
    setOpen(popover.hidden);
  });
  button.addEventListener("keydown", (event) => {
    if (event.key === "Escape") setOpen(false);
  });
  document.addEventListener("click", () => setOpen(false));
  return el("span", { class: "help-wrap" }, [button, popover]);
}

// Only render an image src we trust: a same-origin path or an explicit http(s)
// or data:image URL. Anything else (a javascript: or other odd scheme) is
// rejected so the caller can fall back. Shared by the persona avatar and the
// run hero-image preview.
export function isSafeImageSrc(value) {
  if (!value || typeof value !== "string") return false;
  return value.startsWith("/") || /^https?:\/\//i.test(value) || /^data:image\//i.test(value);
}
