// Agent names from a first message. `galpon herdr-app install` copies this
// file into each Galpon mod.

const SMALL_WORDS = new Set(["a", "an", "the", "of", "on", "in", "to", "for", "and", "or", "with", "at", "by", "from", "into", "my", "our", "your", "this", "that"]);

const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");

// titleFrom makes a short agent name from the first message: its first words.
export function titleFrom(text) {
  const line = String(text ?? "").split("\n").map((part) => part.trim()).find(Boolean) ?? "";
  if (!line || line.startsWith("/")) return "";
  const words = line.replace(/^[#>\s]+/, "").replace(/[`*[\]()"]/g, "").split(/\s+/).filter(Boolean).slice(0, 7);
  // A name does not end with a small word: "Fix the login of the" is "Fix the login".
  while (words.length > 2 && SMALL_WORDS.has(words[words.length - 1].toLowerCase().replace(/[.,;:!?]+$/, ""))) words.pop();
  let title = words.join(" ").replace(/[.,;:!?]+$/, "");
  if (title.length > 48) title = `${title.slice(0, 47).replace(/\s+\S*$/, "")}…`;
  return sentence(title);
}
