// Ctrl+W in the text boxes of the Galpon mods, as in a shell: it deletes the
// word before the cursor, or the selected text.
//
// The text box binds the delete of a word only to Ctrl+Backspace, and a mod
// cannot bind a key to that action or read the cursor. So deleteWord puts a
// mark at the cursor, finds the word before it, and selects and deletes the
// word together with the mark. One Ctrl+Z gives back the word; a second one
// also removes the mark, which shows nothing.

const MARK = "\u2060";

// deleteWord deletes the word before the cursor of an InputState or a
// TextareaState: the spaces before the cursor and the text up to the next
// space before them. An inline token, such as an image chip, goes as one
// word. With a selection, it deletes only the selection. It gives false when
// it found no cursor.
export function deleteWord(state) {
  if (!state) return false;
  const before = state.value();
  // The mark replaces a selection, if there is one.
  state.replace(MARK);
  const content = state.content();
  const text = content.text;
  const at = text.indexOf(MARK);
  if (at < 0) return false;
  let start = at;
  if (text.length - MARK.length >= before.length) {
    while (start > 0 && /\s/.test(text[start - 1])) start--;
    while (start > 0 && !/\s/.test(text[start - 1])) start--;
    for (const { range } of content.tokens ?? []) {
      if (range.start < at && range.end > start) start = Math.min(start, range.start);
    }
  }
  state.set_selected_range({ start, end: at + MARK.length });
  state.replace("");
  return true;
}
