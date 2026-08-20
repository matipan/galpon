export function parseRichText(value) {
  const lines = String(value || "").replaceAll("\r\n", "\n").split("\n");
  const blocks = [];
  let paragraph = [];
  let list = [];
  let code = null;

  const flushParagraph = () => {
    const text = paragraph.join("\n").trim();
    if (text) blocks.push({ kind: "paragraph", content: parseInline(text) });
    paragraph = [];
  };
  const flushList = () => {
    if (list.length) blocks.push({ kind: "list", items: list.map(parseInline) });
    list = [];
  };

  for (const line of lines) {
    const fence = line.match(/^\s*```([^`]*)$/);
    if (code) {
      if (fence) {
        blocks.push({ kind: "code", language: code.language, text: code.lines.join("\n") });
        code = null;
      } else {
        code.lines.push(line);
      }
      continue;
    }
    if (fence) {
      flushParagraph();
      flushList();
      code = { language: fence[1].trim().slice(0, 32), lines: [] };
      continue;
    }
    const item = line.match(/^\s*[-*]\s+(.+)$/);
    if (item) {
      flushParagraph();
      list.push(item[1]);
      continue;
    }
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    flushList();
    paragraph.push(line);
  }
  if (code) {
    blocks.push({ kind: "code", language: code.language, text: code.lines.join("\n") });
  }
  flushParagraph();
  flushList();
  return blocks;
}

export function parseInline(value) {
  const text = String(value || "");
  const tokens = [];
  const pattern = /!\[([^\]\n]*)\]\(([^)\s]+)\)|`([^`\n]+)`|\[([^\]\n]+)\]\(([^)\s]+)\)/g;
  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > offset) tokens.push({ kind: "text", text: text.slice(offset, match.index) });
    if (match[1] !== undefined) {
      tokens.push({ kind: "image", text: match[1], reference: match[2], raw: match[0] });
    } else if (match[3] !== undefined) {
      tokens.push({ kind: "code", text: match[3] });
    } else {
      const url = safeWebURL(match[5]);
      if (url) tokens.push({ kind: "link", text: match[4], url });
      else tokens.push({ kind: "text", text: match[0] });
    }
    offset = match.index + match[0].length;
  }
  if (offset < text.length) tokens.push({ kind: "text", text: text.slice(offset) });
  return tokens;
}

export function renderRichText(document, value, options = {}) {
  const container = document.createElement("div");
  container.className = "discussion-text";
  for (const block of parseRichText(value)) {
    if (block.kind === "code") {
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      if (block.language) code.dataset.language = block.language;
      code.textContent = block.text;
      pre.append(code);
      container.append(pre);
      continue;
    }
    const element = document.createElement(block.kind === "list" ? "ul" : "p");
    const items = block.kind === "list" ? block.items : [block.content];
    for (const item of items) {
      const target = block.kind === "list" ? document.createElement("li") : element;
      appendInline(document, target, item, options);
      if (block.kind === "list") element.append(target);
    }
    container.append(element);
  }
  return container;
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
      link.textContent = token.text;
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
