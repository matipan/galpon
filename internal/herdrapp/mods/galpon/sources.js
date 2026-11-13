// Structured source lookup is separate from title search. The daemon checks
// the URL and repository access when the form is submitted.
export function repositoryURL(value) {
  const text = String(value ?? "").trim();
  if (!text || /[\s\u0000-\u001f\u007f]/.test(text)) return false;
  if (!text.includes("://") && /^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9.-]*:[^:\s][^\s]*$/.test(text)) return true;
  return /^(?:https?|ssh|git):\/\/[^/\s]+\/[^?#\s]+$/.test(text) || /^file:\/\/\/[^?#\s]+$/.test(text);
}

export function repositorySourceKey(value) {
  let text = String(value ?? "").trim();
  const github = text.match(/^(?:[A-Za-z0-9._-]+@)?github\.com:([^\s]+)$/i)
    ?? text.match(/^(?:https?|ssh|git):\/\/(?:[^/@]*@)?github\.com\/([^?#]+)$/i);
  if (github) return `github.com/${github[1].replace(/^\/+|\/+$/g, "").replace(/\.git$/, "").toLowerCase()}`;
  if (text.startsWith("file:///")) {
    try { text = decodeURIComponent(text.slice(7)); } catch {}
  }
  return text.replace(/\/+$/, "");
}
