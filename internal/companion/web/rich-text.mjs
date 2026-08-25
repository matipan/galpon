export function parseRichText(value) {
  const lines = String(value || "").replaceAll("\r\n", "\n").replaceAll("\r", "\n").split("\n");
  const blocks = [];
  let index = 0;

  while (index < lines.length) {
    if (!lines[index].trim()) {
      index += 1;
      continue;
    }

    const fence = lines[index].match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
    if (fence) {
      const marker = fence[1][0];
      const length = fence[1].length;
      const language = fence[2].trim().split(/\s+/, 1)[0].slice(0, 32);
      const codeLines = [];
      index += 1;
      while (index < lines.length && !new RegExp(`^ {0,3}${escapeRegExp(marker)}{${length},}\\s*$`).test(lines[index])) {
        codeLines.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) index += 1;
      blocks.push({ kind: "code", language, text: codeLines.join("\n") });
      continue;
    }

    const table = parseTable(lines, index);
    if (table) {
      blocks.push(table.block);
      index = table.next;
      continue;
    }

    const heading = lines[index].match(/^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$/);
    if (heading) {
      blocks.push({ kind: "heading", level: heading[1].length, content: parseInline(heading[2]) });
      index += 1;
      continue;
    }

    if (/^ {0,3}(?:\*\s*){3,}$/.test(lines[index]) || /^ {0,3}(?:-\s*){3,}$/.test(lines[index]) || /^ {0,3}(?:_\s*){3,}$/.test(lines[index])) {
      blocks.push({ kind: "rule" });
      index += 1;
      continue;
    }

    if (/^ {0,3}>/.test(lines[index])) {
      const quoteLines = [];
      while (index < lines.length) {
        const quote = lines[index].match(/^ {0,3}> ?(.*)$/);
        if (!quote) break;
        quoteLines.push(quote[1]);
        index += 1;
      }
      blocks.push({ kind: "quote", blocks: parseRichText(quoteLines.join("\n")) });
      continue;
    }

    const firstListItem = listItem(lines[index]);
    if (firstListItem) {
      const items = [];
      const ordered = firstListItem.ordered;
      const start = firstListItem.start;
      while (index < lines.length) {
        const item = listItem(lines[index]);
        if (!item || item.ordered !== ordered) break;
        const parts = [item.text];
        index += 1;
        while (index < lines.length && lines[index].trim() && !listItem(lines[index]) && !startsBlock(lines, index)) {
          parts.push(lines[index].trim());
          index += 1;
        }
        const task = parts[0].match(/^\[([ xX])\]\s+(.+)$/);
        if (task) {
          parts[0] = task[2];
          items.push({ content: parseInline(parts.join("\n")), checked: task[1].toLowerCase() === "x" });
        } else {
          items.push({ content: parseInline(parts.join("\n")), checked: null });
        }
        if (!lines[index]?.trim()) break;
      }
      blocks.push({ kind: "list", ordered, start, items });
      continue;
    }

    if (/^(?: {4}|\t)/.test(lines[index])) {
      const codeLines = [];
      while (index < lines.length && (/^(?: {4}|\t)/.test(lines[index]) || !lines[index].trim())) {
        codeLines.push(lines[index].replace(/^(?: {4}|\t)/, ""));
        index += 1;
      }
      while (codeLines.at(-1) === "") codeLines.pop();
      blocks.push({ kind: "code", language: "", text: codeLines.join("\n") });
      continue;
    }

    const paragraph = [lines[index]];
    index += 1;
    if (index < lines.length) {
      const setext = lines[index].match(/^ {0,3}(=+|-+)\s*$/);
      if (setext) {
        blocks.push({ kind: "heading", level: setext[1][0] === "=" ? 1 : 2, content: parseInline(paragraph[0].trim()) });
        index += 1;
        continue;
      }
    }
    while (index < lines.length && lines[index].trim() && !startsBlock(lines, index)) {
      paragraph.push(lines[index]);
      index += 1;
    }
    blocks.push({ kind: "paragraph", content: parseInline(paragraph.join("\n").trim()) });
  }

  return blocks;
}

function startsBlock(lines, index) {
  const line = lines[index] || "";
  return /^ {0,3}(?:#{1,6}\s|>|`{3,}|~{3,})/.test(line)
    || /^(?: {4}|\t)/.test(line)
    || Boolean(listItem(line))
    || /^ {0,3}(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$/.test(line)
    || Boolean(parseTable(lines, index));
}

function listItem(line) {
  const match = String(line || "").match(/^\s{0,3}([-+*]|\d+[.)])\s+(.+)$/);
  if (!match) return null;
  const ordered = /^\d/.test(match[1]);
  return {
    ordered,
    start: ordered ? Number.parseInt(match[1], 10) : 1,
    text: match[2],
  };
}

function parseTable(lines, index) {
  if (index + 1 >= lines.length || !lines[index].includes("|")) return null;
  const headers = splitTableRow(lines[index]);
  const delimiters = splitTableRow(lines[index + 1]);
  if (headers.length < 2 || delimiters.length !== headers.length) return null;
  const alignments = delimiters.map((cell) => {
    const value = cell.trim();
    if (!/^:?-{3,}:?$/.test(value)) return null;
    if (value.startsWith(":") && value.endsWith(":")) return "center";
    if (value.endsWith(":")) return "right";
    return "left";
  });
  if (alignments.some((alignment) => alignment === null)) return null;

  const rows = [];
  let next = index + 2;
  while (next < lines.length && lines[next].trim() && lines[next].includes("|")) {
    const cells = splitTableRow(lines[next]);
    while (cells.length < headers.length) cells.push("");
    rows.push(cells.slice(0, headers.length).map(parseInline));
    next += 1;
  }
  return {
    block: {
      kind: "table",
      headers: headers.map(parseInline),
      alignments,
      rows,
    },
    next,
  };
}

function splitTableRow(line) {
  let value = String(line || "").trim();
  if (value.startsWith("|")) value = value.slice(1);
  if (value.endsWith("|") && !value.endsWith("\\|")) value = value.slice(0, -1);
  const cells = [];
  let cell = "";
  let escaped = false;
  let codeFence = 0;
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index];
    if (escaped) {
      cell += character;
      escaped = false;
      continue;
    }
    if (character === "\\") {
      escaped = true;
      cell += character;
      continue;
    }
    if (character === "`") {
      let count = 1;
      while (value[index + count] === "`") count += 1;
      if (codeFence === 0) codeFence = count;
      else if (codeFence === count) codeFence = 0;
      cell += "`".repeat(count);
      index += count - 1;
      continue;
    }
    if (character === "|" && codeFence === 0) {
      cells.push(cell.trim());
      cell = "";
      continue;
    }
    cell += character;
  }
  if (escaped) cell += "\\";
  cells.push(cell.trim());
  return cells;
}

const inlineRules = [
  { kind: "image", pattern: /!\[([^\]\n]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g },
  { kind: "link", pattern: /\[([^\]\n]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g },
  { kind: "code", pattern: /(`+)([\s\S]*?)\1/g },
  { kind: "strong", pattern: /\*\*([^*\n]+)\*\*|__([^_\n]+)__/g },
  { kind: "strike", pattern: /~~([^~\n]+)~~/g },
  { kind: "emphasis", pattern: /\*([^*\n]+)\*|_([^_\n]+)_/g },
  { kind: "autolink", pattern: /<(https?:\/\/[^\s<>]+)>/g },
  { kind: "escape", pattern: /\\([\\`*_[\]{}()#+\-.!|>~])/g },
];

export function parseInline(value) {
  const text = String(value || "");
  const tokens = [];
  let offset = 0;
  while (offset < text.length) {
    const candidate = nextInlineCandidate(text, offset);
    if (!candidate) {
      pushText(tokens, text.slice(offset));
      break;
    }
    if (candidate.index > offset) pushText(tokens, text.slice(offset, candidate.index));
    const { kind, match } = candidate;
    if (kind === "image") {
      tokens.push({ kind: "image", text: match[1], reference: match[2], raw: match[0] });
    } else if (kind === "link") {
      const url = safeWebURL(match[2]);
      if (url) tokens.push({ kind: "link", content: parseInline(match[1]), url });
      else pushText(tokens, match[0]);
    } else if (kind === "code") {
      tokens.push({ kind: "code", text: match[2].replace(/^ | $/g, "") });
    } else if (kind === "strong") {
      tokens.push({ kind: "strong", content: parseInline(match[1] ?? match[2]) });
    } else if (kind === "strike") {
      tokens.push({ kind: "strike", content: parseInline(match[1]) });
    } else if (kind === "emphasis") {
      tokens.push({ kind: "emphasis", content: parseInline(match[1] ?? match[2]) });
    } else if (kind === "autolink") {
      const url = safeWebURL(match[1]);
      if (url) tokens.push({ kind: "link", content: [{ kind: "text", text: match[1] }], url });
      else pushText(tokens, match[0]);
    } else {
      pushText(tokens, match[1]);
    }
    offset = candidate.index + match[0].length;
  }
  return tokens;
}

function nextInlineCandidate(text, offset) {
  let best = null;
  for (let priority = 0; priority < inlineRules.length; priority += 1) {
    const rule = inlineRules[priority];
    rule.pattern.lastIndex = offset;
    const match = rule.pattern.exec(text);
    if (!match) continue;
    if (!best || match.index < best.index || match.index === best.index && priority < best.priority) {
      best = { kind: rule.kind, match, index: match.index, priority };
    }
  }
  return best;
}

function pushText(tokens, value) {
  const parts = String(value || "").split(/( {2,}\n|\\\n|\n)/);
  for (const part of parts) {
    if (!part) continue;
    if (part === "\n") {
      appendText(tokens, " ");
    } else if (part.endsWith("\n")) {
      const previous = tokens.at(-1);
      if (previous?.kind === "text") previous.text = previous.text.trimEnd();
      tokens.push({ kind: "break" });
    } else {
      appendText(tokens, part);
    }
  }
}

function appendText(tokens, text) {
  if (!text) return;
  const previous = tokens.at(-1);
  if (previous?.kind === "text") previous.text += text;
  else tokens.push({ kind: "text", text });
}

export function renderRichText(document, value, options = {}) {
  const container = document.createElement("div");
  container.className = "discussion-text";
  appendBlocks(document, container, parseRichText(value), options);
  return container;
}

function appendBlocks(document, parent, blocks, options) {
  for (const block of blocks) {
    if (block.kind === "code") {
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      if (block.language) code.dataset.language = block.language;
      code.textContent = block.text;
      pre.append(code);
      parent.append(pre);
      configureOverflowRegion(pre, {
        label: block.language ? `Scrollable ${block.language} code block` : "Scrollable code block",
        conditionalFocus: true,
      });
    } else if (block.kind === "heading") {
      const renderedLevel = Math.min(block.level + 1, 6);
      const heading = document.createElement(`h${renderedLevel}`);
      heading.dataset.markdownLevel = String(block.level);
      appendInline(document, heading, block.content, options);
      parent.append(heading);
    } else if (block.kind === "rule") {
      parent.append(document.createElement("hr"));
    } else if (block.kind === "quote") {
      const quote = document.createElement("blockquote");
      appendBlocks(document, quote, block.blocks, options);
      parent.append(quote);
    } else if (block.kind === "table") {
      parent.append(renderTable(document, block, options));
    } else if (block.kind === "list") {
      const list = document.createElement(block.ordered ? "ol" : "ul");
      if (block.ordered && block.start !== 1) list.start = block.start;
      if (block.items.some((item) => item.checked !== null)) list.className = "task-list";
      for (const item of block.items) {
        const listItem = document.createElement("li");
        if (item.checked !== null) {
          listItem.className = "task-list-item";
          const checkbox = document.createElement("input");
          checkbox.type = "checkbox";
          checkbox.checked = item.checked;
          checkbox.disabled = true;
          checkbox.setAttribute("aria-label", item.checked ? "Completed task" : "Open task");
          listItem.append(checkbox);
        }
        appendInline(document, listItem, item.content, options);
        list.append(listItem);
      }
      parent.append(list);
    } else {
      const paragraph = document.createElement("p");
      appendInline(document, paragraph, block.content, options);
      parent.append(paragraph);
    }
  }
}

function renderTable(document, block, options) {
  const frame = document.createElement("div");
  frame.className = "discussion-table-frame";
  const wrapper = document.createElement("div");
  wrapper.className = "discussion-table-wrap";
  wrapper.tabIndex = 0;
  wrapper.setAttribute("role", "region");
  wrapper.setAttribute("aria-label", "Scrollable table");
  const table = document.createElement("table");
  const head = document.createElement("thead");
  const headRow = document.createElement("tr");
  block.headers.forEach((content, index) => {
    const cell = document.createElement("th");
    cell.scope = "col";
    cell.dataset.align = block.alignments[index];
    appendInline(document, cell, content, options);
    headRow.append(cell);
  });
  head.append(headRow);
  table.append(head);
  const body = document.createElement("tbody");
  for (const row of block.rows) {
    const tableRow = document.createElement("tr");
    row.forEach((content, index) => {
      const cell = document.createElement("td");
      cell.dataset.align = block.alignments[index];
      appendInline(document, cell, content, options);
      tableRow.append(cell);
    });
    body.append(tableRow);
  }
  table.append(body);
  wrapper.append(table);
  const hint = document.createElement("p");
  hint.className = "discussion-table-hint";
  hint.textContent = "Swipe or scroll to see all columns";
  frame.append(wrapper, hint);
  configureOverflowRegion(wrapper, { overflowTarget: frame });
  return frame;
}

function configureOverflowRegion(element, { label = "", conditionalFocus = false, overflowTarget = element } = {}) {
  const update = () => {
    const overflows = element.scrollWidth > element.clientWidth + 1;
    overflowTarget.dataset.overflow = overflows ? "true" : "false";
    if (!conditionalFocus) return;
    if (overflows) {
      element.tabIndex = 0;
      element.setAttribute("role", "region");
      element.setAttribute("aria-label", label);
    } else {
      element.removeAttribute("tabindex");
      element.removeAttribute("role");
      element.removeAttribute("aria-label");
    }
  };
  const view = element.ownerDocument?.defaultView;
  view?.requestAnimationFrame?.(update);
  if (view?.ResizeObserver) new view.ResizeObserver(update).observe(element);
}

function appendInline(document, parent, tokens, options) {
  for (const token of tokens) {
    if (token.kind === "code") {
      const code = document.createElement("code");
      code.textContent = token.text;
      parent.append(code);
    } else if (token.kind === "link") {
      const link = document.createElement("a");
      link.href = token.url;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      appendInline(document, link, token.content, options);
      parent.append(link);
    } else if (token.kind === "image") {
      const image = options.resolveImage?.(token);
      if (!image?.source) {
        parent.append(document.createTextNode(token.raw));
        continue;
      }
      const frame = document.createElement("span");
      frame.className = "discussion-image";
      const element = document.createElement("img");
      element.src = image.source;
      element.alt = token.text || image.name || "Discussion image";
      element.loading = "lazy";
      element.decoding = "async";
      if (Number(image.width) > 0 && Number(image.height) > 0) {
        element.width = Number(image.width);
        element.height = Number(image.height);
      }
      frame.append(element);
      parent.append(frame);
    } else if (["strong", "emphasis", "strike"].includes(token.kind)) {
      const element = document.createElement(token.kind === "strong" ? "strong" : token.kind === "emphasis" ? "em" : "del");
      appendInline(document, element, token.content, options);
      parent.append(element);
    } else if (token.kind === "break") {
      parent.append(document.createElement("br"));
    } else {
      parent.append(document.createTextNode(token.text));
    }
  }
}

function safeWebURL(value) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : "";
  } catch {
    return "";
  }
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
