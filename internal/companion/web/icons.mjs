const paths = {
  search: ['M21 21l-4.4-4.4', 'M19 11a8 8 0 1 1-16 0 8 8 0 0 1 16 0'],
  plus: ['M12 5v14M5 12h14'],
  back: ['M15 18l-6-6 6-6'],
  close: ['M18 6 6 18M6 6l12 12'],
  send: ['M12 19V5m-7 7 7-7 7 7'],
  file: ['M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z', 'M14 2v6h6M8 13h8M8 17h6'],
  edit: ['M12 20h9', 'M16.5 3.5a2.1 2.1 0 0 1 3 3L9 17l-4 1 1-4z'],
  terminal: ['m4 5 6 7-6 7M12 19h8'],
  globe: ['M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0M3 12h18', 'M12 3a17 17 0 0 1 0 18 17 17 0 0 1 0-18'],
  list: ['M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01'],
  layers: ['m12 3 10 5-10 5L2 8zM2 12l10 5 10-5M2 16l10 5 10-5'],
  agent: ['M9 3h6M12 3v4M5 7h14v12H5zM9 12h.01M15 12h.01M9 16h6M2 11v4M22 11v4'],
};

export function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "line-icon");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  for (const d of paths[name] || paths.terminal) {
    const path = document.createElementNS(svg.namespaceURI, "path");
    path.setAttribute("d", d);
    svg.append(path);
  }
  return svg;
}
