// Display-only link formatting. The source conversation and the target of
// each link stay unchanged. Code, named links, images and reference
// definitions pass through without URL substitutions.

const escapeHTML = (text) => String(text).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

export function linkLabel(raw) {
  let url;
  try { url = new URL(raw); } catch { return null; }
  if (!["https:", "http:"].includes(url.protocol) || !url.hostname || url.username || url.password) return null;
  let label = raw.replace(/^https:\/\//i, "");
  if (url.protocol === "https:" && url.hostname === "github.com" && !url.port) {
    const match = /^\/([\w.-]+)\/([\w.-]+)(?:\/(pull|issues)\/(\d+))?\/?$/.exec(url.pathname);
    if (match && !url.search && !url.hash) {
      label = `${match[1]}/${match[2]}${match[4] ? ` #${match[4]}` : ""}`;
    }
  }
  return label;
}

// balancedEnd skips a Markdown label or destination, including escaped
// delimiters and nested brackets. An unfinished label stays literal while
// the message streams.
function balancedEnd(text, start, open, close) {
  let depth = 0;
  for (let i = start; i < text.length; i++) {
    if (text[i] === "\\") { i++; continue; }
    if (text[i] === open) depth++;
    else if (text[i] === close && --depth === 0) return i + 1;
  }
  return text.length;
}

function urlEnd(text, start) {
  let end = start;
  let parentheses = 0;
  while (end < text.length) {
    const c = text[end];
    if (/[\s<>"'`\[\]\\|]/.test(c)) break;
    if (c === "(") parentheses++;
    if (c === ")" && --parentheses < 0) break;
    end++;
  }
  while (end > start && /[.,;:!?*_~]/.test(text[end - 1])) end--;
  // A trailing HTML entity belongs to the surrounding prose in GFM.
  const entity = /&(?:[a-zA-Z]+|#\d+|#x[\da-fA-F]+);?$/.exec(text.slice(start, end));
  if (entity) end = start + entity.index;
  return end;
}

export function prettyLinks(text) {
  const link = (raw) => {
    const value = linkLabel(raw);
    if (!value || value === raw) return null;
    const label = escapeHTML(value).replace(/[\\[\]`*_~|]/g, "\\$&");
    return `[${label}](<${escapeHTML(raw)}> "${escapeHTML(raw)}")`;
  };
  let result = "";
  for (let i = 0; i < text.length;) {
    // Indented code and reference definitions are not prose URLs. Fenced
    // code is split off by the caller before this function runs.
    if (i === 0 || text[i - 1] === "\n") {
      const line = /^(?: {4}|\t| {0,3}\[[^\]\n]+\]:)[^\n]*(?:\n|$)/.exec(text.slice(i));
      if (line) { result += line[0]; i += line[0].length; continue; }
    }
    if (text[i] === "\\") {
      result += text.slice(i, i + 2); i += 2; continue;
    }
    if (text[i] === "`") {
      const mark = /^`+/.exec(text.slice(i))[0];
      let end = i + mark.length;
      for (;;) {
        const next = text.indexOf(mark, end);
        if (next < 0) { end = text.length; break; }
        end = next + mark.length;
        if (text[next - 1] !== "`" && text[end] !== "`") break;
      }
      result += text.slice(i, end); i = end; continue;
    }
    if (text[i] === "[" || text.slice(i, i + 2) === "![") {
      const start = text[i] === "!" ? i + 1 : i;
      const labelEnd = balancedEnd(text, start, "[", "]");
      let end = labelEnd;
      if (text[end] === "(") end = balancedEnd(text, end, "(", ")");
      else if (text[end] === "[") end = balancedEnd(text, end, "[", "]");
      const raw = text.slice(start + 1, labelEnd - 1);
      const dest = text.slice(labelEnd + 1, end - 1).replace(/^<|>$/g, "");
      // Agents also emit explicit Markdown links whose label is the URL.
      const formatted = start === i && text[labelEnd] === "(" && raw === dest && /^https?:\/\//i.test(raw) ? link(raw) : null;
      result += formatted ?? text.slice(i, end); i = end; continue;
    }
    if (text[i] === "<") {
      const auto = /^<(https?:\/\/[^<>\s]+)>/i.exec(text.slice(i));
      if (auto) { result += link(auto[1]) ?? auto[0]; i += auto[0].length; continue; }
      // Do not touch attributes or the contents of an HTML element. This
      // includes named HTML links and pre/code elements.
      const tag = /^<!--(?:[\s\S]*?-->|[\s\S]*$)|^<\/?[a-zA-Z][^>]*>/.exec(text.slice(i));
      if (tag) {
        let end = i + tag[0].length;
        const name = /^<([a-zA-Z][\w-]*)\b/.exec(tag[0])?.[1];
        if (name && !tag[0].endsWith("/>")) {
          const closing = new RegExp(`</${name}\\s*>`, "ig");
          closing.lastIndex = end;
          const match = closing.exec(text);
          if (match) end = match.index + match[0].length;
        }
        result += text.slice(i, end); i = end; continue;
      }
    }
    if (/^https?:\/\//i.test(text.slice(i, i + 8)) && (i === 0 || !/[\w/@=]/.test(text[i - 1]))) {
      const end = urlEnd(text, i);
      const raw = text.slice(i, end);
      result += link(raw) ?? raw; i = end; continue;
    }
    result += text[i++];
  }
  return result;
}
