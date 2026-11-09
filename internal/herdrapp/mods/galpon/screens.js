// The screens of the Galpon popup besides the list: a form, a choice, and the
// places where a terminal can open. main.js gives these methods to the
// Galpon view. They have the look of the list: a field at the top, rows
// below it, and a footer of keys. They have no side panes.
//
// In a form, the top field holds the main text, such as the name of a new
// agent. Each other field is a row with a label and its value. The last row
// sends the form.

import { div } from "gpui-kit";
import { h_flex, v_flex, Input, v_virtual_list, Scrollbar } from "gpui-base";
import { S, icon, kbd } from "./look.js";

const ROW = 36;
const HEADING = 30;
const LABEL = 120;

const sentence = (text) => (text ? String(text)[0].toUpperCase() + String(text).slice(1) : "");

export const ScreenMethods = {
  // headerBar is the top of a screen: a chip that goes back, then a field or
  // a quiet text.
  headerBar(title, body = null) {
    const P = this.P;
    return h_flex().id("gs-head").h(50).flex_none().px(16).gap(10).items_center().border_b(1).border_color(P.line)
      .child(h_flex().id("gs-back").h(24).px(8).gap(6).flex_none().items_center().rounded(6).bg(P.raised).cursor_pointer()
        .hover((s) => s.bg(P.raised2))
        .on_click((_e, cx) => this.runKey("escape", cx))
        .child(icon("chevron-left", 12).text_color(P.text2))
        .child(div().text_size(S.ui).text_color(P.text).child(title)))
      .child(body ?? div().flex_1());
  },

  headerText(text) {
    return div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(this.P.text2).child(text ?? "");
  },

  headerInput(state) {
    return Input.new(state).flex_1().h_full().text_size(15).text_color(this.P.text);
  },

  // footer shows a notice, or the keys of the screen. A key with a name runs
  // on a click.
  footer(hints, position = "", notice = null) {
    const P = this.P;
    const tone = notice?.tone;
    const color = tone === "error" ? P.red : tone === "warn" ? P.yellow : tone === "quiet" ? P.text3 : P.text2;
    return h_flex().id("gs-footer").h(34).flex_none().px(14).gap(14).items_center().bg(P.side).border_t(1).border_color(P.line)
      .when(Boolean(notice), (el) => el.child(h_flex().flex_1().min_w(0).gap(8).items_center()
        .child(tone === "busy" ? icon("spinner", 12).text_color(P.text2).spin(900) : icon(tone === "error" || tone === "warn" ? "alert" : "info", 12).text_color(color))
        .child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(color).child(notice.text))))
      .when(!notice, (el) => el.child(h_flex().flex_1().min_w(0).gap(14).items_center().overflow_hidden()
        .children(hints.map(([keys, label, name], index) => h_flex().id(`gs-hint-${index}`).flex_none().gap(6).items_center()
          .when(Boolean(name), (it) => it.cursor_pointer().on_click((_e, cx) => this.runKey(name, cx)))
          .child(div().text_size(S.meta).text_color(P.text2).child(label))
          .child(kbd(P, keys))))))
      .when(Boolean(position), (el) => el.child(div().flex_none().text_size(S.small).text_color(P.text3).child(position)));
  },

  // --- a form

  formScreen() {
    const P = this.P;
    const form = this.form;
    const rows = this.formRows();
    const head = rows.find((field) => field.strong);
    const state = head ? this.formInputs.get(head.key) : null;
    return [
      this.headerBar(form.title, state ? this.headerInput(state) : this.headerText(form.subtitle)),
      v_flex().relative().flex_1().min_h(0).w_full()
        .child(v_flex().id("gs-form").size_full().px(6).py(4).overflow_y_scroll()
          .when(Boolean(form.description), (el) => el.child(div().px(10).pt(8).pb(8).text_size(S.meta).text_color(P.text2).child(form.description)))
          .children(rows.filter((field) => field !== head).map((field) => this.formRow(field))))
        .child(Scrollbar.vertical("gs-form").absolute().inset_0()),
      this.footer(this.formHints(), "", this.formNotice()),
    ];
  },

  // formRow is one row of a form. The selected row has the selection band
  // of the list; a text field also gets an accent underline.
  formRow(field) {
    const P = this.P;
    const selected = field.key === this.focusKey;
    const marked = this.formError?.field === field.key;
    let row = h_flex().id(`gs-field-${field.key}`).w_full().h(ROW).px(10).gap(10).flex_none().items_center().rounded(7).cursor_pointer()
      .when(selected, (el) => el.bg(P.raised2)).when(!selected, (el) => el.hover((s) => s.bg(P.raised)))
      .on_click((_e, cx) => {
        this.clickField(field, cx);
        cx.notify();
      });
    const mark = (child) => h_flex().w(14).flex_none().justify_center().child(child);
    switch (field.type) {
      case "submit":
        return row.mt(6)
          .child(mark(this.busy ? icon("spinner", 13).text_color(P.text2).spin(900) : icon("check", 13).text_color(P.accent)))
          .child(div().flex_1().min_w(0).truncate().text_size(S.ui).font_weight(500).text_color(P.text).child(field.value))
          .child(kbd(P, selected ? "↵" : "Ctrl S"));
      case "more":
        return row
          .child(mark(icon(this.moreOptions ? "chevron-down" : "chevron-right", 13).text_color(P.text3)))
          .child(div().flex_none().text_size(S.ui).text_color(selected ? P.text : P.text2).child(field.label))
          .child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(P.text3).child(this.moreOptions ? "" : field.value));
      case "action":
        return row
          .child(mark(icon("plus", 13).text_color(marked ? P.red : P.text3)))
          .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(selected ? P.text : P.text2).child(field.value))
          .when(selected, (el) => el.child(kbd(P, "↵")));
    }
    row = row.child(div().w(LABEL).flex_none().truncate().text_size(S.meta).text_color(marked ? P.red : selected ? P.text2 : P.text3).child(field.label));
    switch (field.type) {
      case "text": {
        const state = this.formInputs.get(field.key);
        return row.child(h_flex().flex_1().min_w(0).h(26).items_center().border_b(1).border_color(marked ? P.red : selected ? P.accent : P.line)
          .child(state ? Input.new(state).flex_1().h_full().text_size(S.ui).text_color(P.text) : div()));
      }
      case "choice":
        return row
          .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(P.text).child(field.value))
          .child(icon("chevron-right", 12).text_color(selected ? P.text2 : P.text3));
      case "toggle":
        return row.child(h_flex().flex_1().min_w(0).gap(8).items_center()
          .child(h_flex().w(28).h(16).flex_none().px(2).items_center().rounded_full().bg(field.on ? P.accent : P.raised)
            .when(Boolean(field.on), (el) => el.justify_end())
            .child(div().w(12).h(12).rounded_full().bg(field.on ? P.accentInk : P.text3)))
          .child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(P.text2).child(field.value)));
      default:
        return row.child(div().flex_1().min_w(0).truncate().text_size(S.ui).text_color(P.text3).child(field.value));
    }
  },

  // formNotice is the work in progress, an error, or the hint of the
  // selected field. Without one, the footer shows the keys.
  formNotice() {
    if (this.busy) return { text: this.busy, tone: "busy" };
    if (this.formError) return { text: this.formError.text, tone: "error" };
    if (this.status) return { text: this.status.text, tone: this.status.tone === "error" ? "error" : "warn" };
    const hint = this.currentField()?.hint;
    return hint ? { text: sentence(hint), tone: "quiet" } : null;
  },

  formHints() {
    const field = this.currentField();
    const verb = this.formRows().find((candidate) => candidate.type === "submit")?.label ?? "Save";
    const hints = [];
    switch (field?.type) {
      case "choice": hints.push(["↵", "Choose", "enter"], ["← →", "Change"]); break;
      case "toggle": hints.push(["Space", "Switch"]); break;
      case "action": hints.push(["↵", "Add", "enter"]); break;
      case "more": hints.push(["↵", field.label, "enter"]); break;
      default: hints.push(["↵", verb, "enter"]);
    }
    if (field && field.type !== "text" && field.type !== "submit") hints.push(["Ctrl S", verb, "ctrl-s"]);
    if (this.form.kind === "repository" && !this.hostMachine) hints.push(["Ctrl O", "Folder", "ctrl-o"]);
    hints.push(["Esc", this.formOrigin === "close" ? "Close" : "Back", "escape"]);
    return hints;
  },

  // --- a choice of a form field

  choiceScreen() {
    const P = this.P;
    const choice = this.choice;
    const options = choice.filtered;
    const body = options.length
      ? v_virtual_list(
        "gs-choices",
        options.length,
        ROW,
        (index) => `option:${options[index].value}`,
        (range) => options.slice(range.start, range.end).map((option, offset) => this.optionRow(option, range.start + offset === choice.cursor, option.value === choice.current)),
      ).track_scroll(this.choiceScroller)
        .on_item_click((key, cx) => {
          this.applyChoice(options.find((option) => `option:${option.value}` === key));
          cx.notify();
        })
        .size_full()
      : this.emptyText("No option matches.");
    return [
      this.headerBar(choice.label, this.headerInput(this.choiceInput)),
      v_flex().relative().flex_1().min_h(0).w_full().px(6).py(4).child(body).child(Scrollbar.vertical("gs-choices").absolute().inset_0()),
      this.footer([["↵", "Choose", "enter"], ["Esc", "Back", "escape"]], options.length ? `${choice.cursor + 1} of ${options.length}` : ""),
    ];
  },

  // optionRow is an option of a choice or a place of a terminal. A check
  // marks the current value.
  optionRow(option, selected, current = false) {
    const P = this.P;
    return h_flex().h(ROW).px(10).gap(10).items_center().rounded(7).when(selected, (el) => el.bg(P.raised2))
      .child(h_flex().w(14).flex_none().justify_center().when(current, (el) => el.child(icon("check", 12).text_color(P.accent))))
      .child(div().flex_none().max_w(Math.round(this.listWidth() * 0.55)).truncate().text_size(S.ui).text_color(P.text)
        .when(selected, (el) => el.font_weight(500)).child(option.label))
      .child(div().flex_1().min_w(0).truncate().text_size(S.meta).text_color(P.text3).child(option.detail ?? ""))
      .when(selected, (el) => el.child(kbd(P, "↵")));
  },

  emptyText(text) {
    return v_flex().flex_1().items_center().justify_center().child(div().text_size(S.ui).text_color(this.P.text3).child(text));
  },

  // --- the places where a terminal or the editor can open

  targetsScreen() {
    const P = this.P;
    const targets = this.targets;
    const rows = targets.rows;
    const body = rows.length
      ? v_virtual_list(
        "gs-targets",
        rows.length,
        rows.map((row) => (row.type === "header" ? HEADING : ROW)),
        (index) => rows[index].key,
        (range) => rows.slice(range.start, range.end).map((row, offset) => {
          if (row.type === "header") {
            return h_flex().h(HEADING).px(10).pb(4).items_end().text_size(S.small).font_weight(600).text_color(P.text3).child(row.title);
          }
          return this.optionRow({ label: row.target.label, detail: row.target.detail }, range.start + offset === targets.cursor);
        }),
      ).track_scroll(this.targetScroller)
        .on_item_click((key, cx) => {
          const row = rows.find((candidate) => candidate.key === key);
          if (row?.type === "target") this.openTarget(row.target, targets.editor, cx);
          cx.notify();
        })
        .size_full()
      : this.emptyText("No place matches.");
    return [
      this.headerBar(targets.editor ? "Open editor" : "Open terminal", this.headerInput(this.choiceInput)),
      v_flex().relative().flex_1().min_h(0).w_full().px(6).py(4).child(body).child(Scrollbar.vertical("gs-targets").absolute().inset_0()),
      this.footer([["↵", "Open", "enter"], ["Esc", "Back", "escape"]], "", this.busy ? { text: this.busy, tone: "busy" } : this.status),
    ];
  },
};
