// A small round analog clock, hands set to a given HH:MM, used by the
// Automation list to show fire times at a glance. Pure SVG, no dependencies;
// the accessible name is the time itself.

const SVG_NS = "http://www.w3.org/2000/svg";

function svgEl(tag, attrs = {}) {
  const node = document.createElementNS(SVG_NS, tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
  return node;
}

// One hand from the center, rotated to `deg` (0 = 12 o'clock), `length` long.
function hand(deg, length, cls) {
  const rad = ((deg - 90) * Math.PI) / 180;
  return svgEl("line", {
    class: cls,
    x1: 16, y1: 16,
    x2: (16 + length * Math.cos(rad)).toFixed(2),
    y2: (16 + length * Math.sin(rad)).toFixed(2),
  });
}

export function ClockIcon(hhmm, { size = 18 } = {}) {
  const [h, m] = hhmm.split(":").map(Number);
  const svg = svgEl("svg", {
    viewBox: "0 0 32 32", width: size, height: size,
    class: "clock-icon", role: "img", "aria-label": hhmm,
  });
  svg.append(
    svgEl("circle", { class: "clock-face", cx: 16, cy: 16, r: 14 }),
    hand(((h % 12) + m / 60) * 30, 7, "clock-hand clock-hand-hour"),
    hand(m * 6, 10.5, "clock-hand clock-hand-minute"),
    svgEl("circle", { class: "clock-pin", cx: 16, cy: 16, r: 1.4 }),
  );
  const title = svgEl("title");
  title.textContent = hhmm;
  svg.prepend(title);
  return svg;
}
