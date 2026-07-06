const SVG_NS = "http://www.w3.org/2000/svg";

function svgIcon(className, children) {
  const node = document.createElementNS(SVG_NS, "svg");
  node.setAttribute("class", className);
  node.setAttribute("viewBox", "0 0 24 24");
  node.setAttribute("fill", "none");
  node.setAttribute("stroke", "currentColor");
  node.setAttribute("stroke-width", "2");
  node.setAttribute("stroke-linecap", "round");
  node.setAttribute("stroke-linejoin", "round");
  node.setAttribute("aria-hidden", "true");
  node.setAttribute("focusable", "false");
  for (const child of children) node.append(child);
  return node;
}

function svgNode(tag, attrs = {}) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
  return node;
}

export function SearchIcon(className = "icon") {
  return svgIcon(className, [
    svgNode("circle", { cx: "11", cy: "11", r: "8" }),
    svgNode("path", { d: "m21 21-4.3-4.3" }),
  ]);
}

export function TrashIcon(className = "icon") {
  return svgIcon(className, [
    svgNode("path", { d: "M3 6h18" }),
    svgNode("path", { d: "M8 6V4c0-1.1.9-2 2-2h4c1.1 0 2 .9 2 2v2" }),
    svgNode("path", { d: "m19 6-1 14c-.1 1.1-1 2-2.1 2H8.1c-1.1 0-2-.9-2.1-2L5 6" }),
    svgNode("path", { d: "M10 11v6" }),
    svgNode("path", { d: "M14 11v6" }),
  ]);
}
