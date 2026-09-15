-- Focused headless tests for the native Neovim review UI.
-- Run with an isolated HOME/XDG environment and: nvim --clean --headless -l this-file
local api = vim.api

local function fail(message)
  error("neovim-review-test: " .. message, 2)
end

local function expect(value, message)
  if not value then fail(message) end
end

local function equal(actual, expected, message)
  if not vim.deep_equal(actual, expected) then
    fail(string.format("%s\nexpected: %s\nactual:   %s", message, vim.inspect(expected), vim.inspect(actual)))
  end
end

local function wait_for(predicate, message)
  expect(vim.wait(1000, predicate, 10), message)
end

local function keys(value)
  api.nvim_feedkeys(api.nvim_replace_termcodes(value, true, false, true), "xt", false)
  vim.wait(20)
end

local function replace_comment(text, save)
  local suffix = save and "<C-s>" or "<Esc>"
  keys("<Esc>") -- Also cancels a pending startinsert in headless feedkeys.
  keys("ggVGc" .. text .. suffix)
end

local function read_json(path)
  local file = assert(io.open(path, "rb"))
  local value = vim.json.decode(file:read("*a"))
  file:close()
  return value
end

local function write_json(path, value)
  local file = assert(io.open(path, "wb"))
  file:write(vim.json.encode(value))
  file:close()
end

local script = debug.getinfo(1, "S").source:sub(2)
local directory = vim.fs.dirname(script)
local implementation = vim.fs.normalize(directory .. "/../neovim-review.lua")
local temporary = vim.fn.tempname()
vim.fn.mkdir(temporary, "p", 448)
local input_path = temporary .. "/input.json"
local output_path = temporary .. "/output.json"

local source_lines = {
  "# Title",
  "Alpha beta gamma",
  "Family 👨‍👩‍👧‍👦 done",
  "Last line",
}
for index = 5, 40 do
  source_lines[index] = index == 20 and "A searchable needle is here" or ("Filler line " .. index)
end
local text = table.concat(source_lines, "\n")
local colors = {
  Background = "#101010", Surface = "#111111", SurfaceRaised = "#222222",
  Prompt = "#181818", Selection = "#333355", Border = "#446688",
  Foreground = "#eeeeee", Muted = "#999999", Comment = "#888888",
  Status = "#7799ff", StatusInk = "#101010", Blue = "#82aaff",
  Cyan = "#65bcff", Purple = "#c099ff", Green = "#c3e88d",
  Orange = "#ff966c", Red = "#ff757f", Yellow = "#ffc777", Teal = "#4fd6be",
}
write_json(input_path, {
  version = 1,
  runId = "test-run",
  sourceEntryId = "source-entry",
  sourceHash = "original-source-hash",
  sourceTextHash = vim.fn.sha256(text),
  text = text,
  items = {
    {
      id = "existing", start = 3, ["end"] = 3, startColumn = 0, endColumn = 9,
      quote = "Last line", comment = "Old comment",
    },
  },
  graphemes = {
    { line = 2, startColumn = 7, endColumn = 18 },
  },
  palette = colors,
  limits = { items = 8, selectionBytes = 1024, draftBytes = 8192 },
})

vim.g.galpon_review_no_autostart = 1
local review = assert(loadfile(implementation))()
local exits = {}
review.start({
  input_path = input_path,
  output_path = output_path,
  runtime = vim.env.GALPON_REVIEW_TEST_RUNTIME,
  run_id = "test-run",
  allow_missing_runtime = true,
  debounce_ms = 30,
  on_exit = function(status) exits[#exits + 1] = status end,
})
local state = review._state

-- Startup, isolation, palette, and the versioned atomic output contract.
expect(state.source_buffer ~= state.annotation_buffer, "the source and annotations must use separate buffers")
expect(vim.bo[state.source_buffer].readonly and not vim.bo[state.source_buffer].modifiable, "the source must be read-only")
equal(vim.bo[state.source_buffer].buftype, "nofile", "the source must not read or write a file")
equal(vim.o.shadafile, "NONE", "ShaDa must be disabled")
expect(not vim.o.modeline and not vim.bo[state.source_buffer].modeline, "modelines must be disabled")
equal(api.nvim_get_hl(0, { name = "GalponStatus", link = false }).bg, tonumber("7799ff", 16), "the input palette must style the UI")
local initial = read_json(output_path)
equal(initial.version, 1, "the snapshot version must be 1")
equal(initial.revision, 1, "the first snapshot revision must be positive")
equal(initial.status, "open", "startup must save an open draft")
equal(initial.sourceHash, "original-source-hash", "the original source identity must be retained")
expect(vim.fn.glob(output_path .. ".tmp.*") == "", "atomic snapshot temporary files must not remain")
if vim.env.GALPON_REVIEW_TEST_RUNTIME and vim.env.GALPON_REVIEW_TEST_RUNTIME ~= "" then
  expect(state.render_markdown, "the prepared render-markdown runtime must load")
  equal(vim.opt.runtimepath:get(), {
    vim.env.GALPON_REVIEW_TEST_RUNTIME .. "/plugins/render-markdown.nvim",
    vim.env.GALPON_REVIEW_TEST_RUNTIME .. "/plugins/mini.icons",
    vim.env.GALPON_REVIEW_TEST_RUNTIME .. "/runtime",
    vim.env.VIMRUNTIME,
  }, "runtimepath must contain only the supplied dependencies and clean Neovim runtime")
  equal(vim.opt.packpath:get(), { vim.env.GALPON_REVIEW_TEST_RUNTIME .. "/runtime" }, "packpath must not contain user paths")
  expect(package.loaded["mini.icons"] ~= nil, "the pinned mini.icons dependency must load")
  local render_config = require("render-markdown.state").get(state.source_buffer)
  local render_modes = render_config.render_modes
  expect(vim.tbl_contains(render_modes, "n") and vim.tbl_contains(render_modes, "c") and vim.tbl_contains(render_modes, "t"), "normal renderer modes are incomplete")
  expect(not vim.tbl_contains(render_modes, "v") and not vim.tbl_contains(render_modes, "V") and not vim.tbl_contains(render_modes, "i"), "visual and insert modes must expose raw Markdown")
  expect(not render_config.html.enabled and not render_config.latex.enabled and not render_config.yaml.enabled, "unused renderer features must stay disabled")
end

-- An empty review cannot prepare or leave the UI.
local original_items = state.items
state.items = {}
api.nvim_set_current_win(state.source_window)
local exits_before_empty = #exits
keys("s")
equal(#exits, exits_before_empty, "s exited for an empty review")
expect(read_json(output_path).status ~= "prepare", "empty s emitted a prepare snapshot")
expect(state.notice:find("at least one annotation", 1, true) ~= nil, "empty prepare did not show a clear notice")
state.items = original_items

-- Native layouts use columns, stacked panes, and a tiny single-pane toggle.
local function resize(columns, lines, mode)
  state.test_dimensions = { columns = columns, lines = lines }
  vim.cmd("doautocmd VimResized")
  wait_for(function() return state.layout_mode == mode end, mode .. " resize did not select its native layout")
end
resize(120, 30, "wide")
local source_position = api.nvim_win_get_position(state.source_window)
local annotation_position = api.nvim_win_get_position(state.annotation_window)
equal(source_position[1], annotation_position[1], "wide panes are not side by side")
api.nvim_win_set_cursor(state.source_window, { 8, 2 })
resize(80, 30, "narrow")
source_position = api.nvim_win_get_position(state.source_window)
annotation_position = api.nvim_win_get_position(state.annotation_window)
expect(source_position[2] == annotation_position[2] and source_position[1] < annotation_position[1], "narrow panes are not stacked")
resize(50, 10, "tiny")
expect(state.source_window and not state.annotation_window, "tiny source pane is not single-pane")
local tiny_window = state.main_window
keys("<Tab>")
equal(api.nvim_get_current_buf(), state.annotation_buffer, "tiny Tab did not show annotations")
equal(state.annotation_window, tiny_window, "tiny Tab created a second main pane")
keys("<Tab>")
equal(api.nvim_get_current_buf(), state.source_buffer, "tiny Tab did not restore source")
resize(120, 30, "wide")
equal(api.nvim_win_get_cursor(state.source_window), { 8, 2 }, "resize did not preserve the source cursor")

-- UTF-16 conversion uses Neovim's explicit utf-16 APIs.
equal(review.utf16_from_byte("A😀B", 5), 3, "byte-to-UTF-16 conversion is incorrect")
equal(review.byte_from_utf16("A😀B", 3), 5, "UTF-16-to-byte conversion is incorrect")

-- Native motions and search are processed by Neovim, not by a Lua cursor model.
api.nvim_set_current_win(state.source_window)
api.nvim_win_set_cursor(state.source_window, { 1, 0 })
keys("w")
equal(api.nvim_win_get_cursor(state.source_window)[2], 2, "native w did not move to the next word")
keys("b")
equal(api.nvim_win_get_cursor(state.source_window)[2], 0, "native b did not move to the prior word")
keys("10Gzt")
equal(vim.fn.winsaveview().topline, 10, "native zt did not align the source")
keys("20Gzz")
local centered = vim.fn.winsaveview().topline
expect(centered > 1 and centered < 20, "native zz did not center the source")
keys("30Gzb")
local bottom = vim.fn.winsaveview().topline
expect(bottom > 1 and bottom <= 30, "native zb did not align the source at the bottom")
keys("<C-e>")
expect(vim.fn.winsaveview().topline >= bottom, "native Ctrl-e did not scroll the source")
keys("/needle<CR>")
equal(api.nvim_win_get_cursor(state.source_window)[1], 20, "native search did not find source text")

-- Normal c captures the complete logical line, then real insert input and Ctrl-s save it.
api.nvim_win_set_cursor(state.source_window, { 2, 6 })
keys("c")
wait_for(function() return state.comment ~= nil end, "normal c did not open a comment buffer")
equal(state.comment.draft.start, 1, "normal c captured the wrong line")
equal(state.comment.draft.startColumn, 0, "normal c did not start at the line boundary")
equal(state.comment.draft.endColumn, 16, "normal c did not use a UTF-16 exclusive line end")
replace_comment("Draft text", false)
keys("<C-w>k")
expect(api.nvim_get_current_buf() ~= state.comment.buffer, "C-w did not leave the active comment")
local exits_before_editing = #exits
keys("s")
equal(#exits, exits_before_editing, "s exited with an unfinished comment")
expect(read_json(output_path).status ~= "prepare", "unfinished s emitted a prepare snapshot")
expect(state.notice:find("Save the unfinished comment", 1, true) ~= nil, "unfinished prepare did not show a clear notice")
vim.cmd("doautocmd VimEnter")
wait_for(function() return api.nvim_get_current_buf() == state.comment.buffer end, "post-startup hook did not focus the restored comment")
vim.wait(20)
keys("<Esc>")
api.nvim_set_current_win(state.source_window)
keys("c")
wait_for(function() return api.nvim_get_current_buf() == state.comment.buffer end, "c did not reopen the active comment")
equal(table.concat(api.nvim_buf_get_lines(state.comment.buffer, 0, -1, true), "\n"), "Draft text", "c discarded the active comment")
api.nvim_set_current_win(state.annotation_window)
keys("e")
wait_for(function() return api.nvim_get_current_buf() == state.comment.buffer end, "e did not reopen the active comment")
equal(table.concat(api.nvim_buf_get_lines(state.comment.buffer, 0, -1, true), "\n"), "Draft text", "e discarded the active comment")
api.nvim_win_set_cursor(state.comment.window, { 1, 3 })
local selected_before_resize = state.item_index
resize(80, 30, "narrow")
equal(table.concat(api.nvim_buf_get_lines(state.comment.buffer, 0, -1, true), "\n"), "Draft text", "resize changed unfinished editor text")
resize(50, 10, "tiny")
wait_for(function() return state.comment and api.nvim_win_is_valid(state.comment.window) end, "tiny active editor resize timed out")
equal(#api.nvim_tabpage_list_wins(0), 2, "tiny comment layout must have one main pane and one editor")
equal(table.concat(api.nvim_buf_get_lines(state.comment.buffer, 0, -1, true), "\n"), "Draft text", "tiny resize changed unfinished editor text")
resize(120, 30, "wide")
equal(state.item_index, selected_before_resize, "resize changed the selected annotation")
equal(api.nvim_win_get_cursor(state.comment.window), { 1, 3 }, "resize did not preserve the comment cursor")
replace_comment("Line comment", true)
wait_for(function() return state.comment == nil end, "Ctrl-s did not save and close the comment buffer")
equal(#state.items, 2, "saving a new line comment did not add one annotation")
equal(state.items[2].quote, "Alpha beta gamma", "the line quote was not exact")
equal(state.items[2].comment, "Line comment", "the comment text was not saved")
local after_line = read_json(output_path)
expect(after_line.revision > initial.revision and after_line.editing == nil, "a completed change was not saved immediately")

-- Characterwise selection endpoints inside a supplied grapheme expand to the grapheme boundary.
api.nvim_set_current_win(state.source_window)
api.nvim_win_set_cursor(state.source_window, { 3, 7 })
vim.cmd("normal! v")
api.nvim_win_set_cursor(state.source_window, { 3, 11 }) -- Put the endpoint on an internal ZWJ byte.
local snapped = review.capture(true)
equal(snapped.startColumn, 7, "the grapheme selection start moved incorrectly")
equal(snapped.endColumn, 18, "an endpoint inside a grapheme did not expand")
keys("<Esc>")
api.nvim_win_set_cursor(state.source_window, { 3, 7 })
keys("vcFamily note<C-s>")
wait_for(function() return state.comment == nil and #state.items == 3 end, "visual c did not enter Insert and save the comment")
equal(state.items[3].startColumn, 7, "the visual grapheme start moved incorrectly")
equal(state.items[3].endColumn, 18, "the visual grapheme end moved incorrectly")
equal(state.items[3].quote, "👨‍👩‍👧‍👦", "the grapheme quote was not exact")
equal(state.items[3].comment, "Family note", "visual c did not send following keys to Insert")

-- Linewise V then c captures complete source lines through Neovim's visual mode.
api.nvim_set_current_win(state.source_window)
api.nvim_win_set_cursor(state.source_window, { 4, 3 })
keys("VcLinewise note<C-s>")
wait_for(function() return state.comment == nil and #state.items == 4 end, "linewise V then c did not enter Insert and save")
equal(state.items[4].startColumn, 0, "linewise selection did not start at column zero")
equal(state.items[4].endColumn, 9, "linewise selection did not use the exclusive line end")
equal(state.items[4].quote, "Last line", "the linewise quote was not exact")
equal(state.items[4].comment, "Linewise note", "linewise c did not send following keys to Insert")

-- Blockwise selections are rejected instead of producing an inaccurate quote.
api.nvim_set_current_win(state.source_window)
api.nvim_win_set_cursor(state.source_window, { 2, 0 })
keys("<C-v>lc")
expect(state.comment == nil, "a block selection incorrectly opened a comment")
expect(state.notice:find("Block selections", 1, true) ~= nil, "a block selection did not explain the rejection")
keys("<Esc>")

-- Tab, e/Enter, x, u, and annotation jumps use actual mapped key processing.
keys("<Tab>")
equal(api.nvim_get_current_buf(), state.annotation_buffer, "Tab did not focus annotations")
api.nvim_win_set_cursor(state.annotation_window, { 2, 0 })
keys("e")
wait_for(function() return state.comment ~= nil end, "e did not edit the selected annotation")
replace_comment("Native close draft", false)
vim.cmd("close")
wait_for(function() return not state.comment.window or not api.nvim_win_is_valid(state.comment.window) end, "native close kept a stale comment window")
local protected = read_json(output_path)
expect(protected.editing and protected.editing.buffer == "Native close draft", "native close did not protect the unfinished comment")
api.nvim_set_current_win(state.annotation_window)
keys("e")
wait_for(function() return api.nvim_get_current_buf() == state.comment.buffer end, "e did not reopen a natively closed comment")
replace_comment("Native wipe draft", false)
vim.cmd("bwipeout!")
wait_for(function() return not state.comment.buffer or not api.nvim_buf_is_valid(state.comment.buffer) end, "native wipe kept a stale comment buffer")
local wiped = read_json(output_path)
equal(wiped.editing and wiped.editing.buffer, "Native wipe draft", "native buffer wipe did not protect the unfinished comment")
api.nvim_set_current_win(state.annotation_window)
keys("e")
wait_for(function() return state.comment.buffer and api.nvim_get_current_buf() == state.comment.buffer end, "e did not recreate a natively wiped comment")
equal(table.concat(api.nvim_buf_get_lines(state.comment.buffer, 0, -1, true), "\n"), "Native wipe draft", "recreated comment lost protected text")
replace_comment("Edited line comment", true)
wait_for(function() return state.comment == nil end, "Ctrl-s did not finish annotation editing")
equal(state.items[2].comment, "Edited line comment", "annotation editing did not replace the comment")

api.nvim_set_current_win(state.annotation_window)
api.nvim_win_set_cursor(state.annotation_window, { 2, 0 })
local before_delete_revision = state.revision
keys("x")
equal(#state.items, 3, "x did not delete an annotation")
expect(state.revision > before_delete_revision, "deletion was not saved immediately")
keys("u")
equal(#state.items, 4, "u did not restore an annotation change")
equal(state.items[2].comment, "Edited line comment", "undo restored the wrong annotation state")

keys("]a")
local next_index = state.item_index
expect(next_index >= 1 and next_index <= #state.items, "]a selected an invalid annotation")
equal(api.nvim_win_get_cursor(state.source_window)[1], state.items[next_index].start + 1, "]a did not move the source cursor")
keys("[a")
equal(api.nvim_win_get_cursor(state.source_window)[1], state.items[state.item_index].start + 1, "[a did not move the source cursor")

-- Prepare uses an explicit status and leaves process termination to the launcher callback.
keys("s")
equal(exits[#exits], "prepare", "s did not request a prepare exit")
local prepared = read_json(output_path)
equal(prepared.status, "prepare", "s did not write a prepare snapshot")
vim.cmd("doautocmd VimLeavePre")
local after_prepare_leave = read_json(output_path)
equal(after_prepare_leave.status, "prepare", "VimLeavePre overwrote a successful prepare")
equal(after_prepare_leave.revision, prepared.revision, "VimLeavePre rewrote a successful prepare")
state.exiting = false -- The injected test exit callback does not terminate Neovim.
state.final_status = nil

-- A changed source blocks capture and snapshots, and preserves the prior valid file.
local valid_snapshot = vim.fn.readfile(output_path, "b")
vim.bo[state.source_buffer].readonly = false
vim.bo[state.source_buffer].modifiable = true
api.nvim_buf_set_text(state.source_buffer, 0, 0, 0, 0, { "tampered " })
vim.bo[state.source_buffer].modifiable = false
vim.bo[state.source_buffer].readonly = true
expect(not review.source_matches(), "source tampering was not detected")
expect(not review.snapshot("open"), "a snapshot was written for changed source text")
equal(vim.fn.readfile(output_path, "b"), valid_snapshot, "an invalid snapshot replaced the valid file")
expect(review.capture(false) == nil, "capture succeeded for changed source text")
vim.bo[state.source_buffer].readonly = false
vim.bo[state.source_buffer].modifiable = true
api.nvim_buf_set_lines(state.source_buffer, 0, -1, true, source_lines)
vim.bo[state.source_buffer].modifiable = false
vim.bo[state.source_buffer].readonly = true
expect(review.source_matches(), "the restored source did not match")

-- VimLeavePre and q both flush a debounced, unfinished edit.
api.nvim_set_current_win(state.annotation_window)
api.nvim_win_set_cursor(state.annotation_window, { 1, 0 })
keys("<CR>")
wait_for(function() return state.comment ~= nil end, "Enter did not edit an annotation")
replace_comment("Exit pending", false)
vim.cmd("doautocmd VimLeavePre")
local orderly = read_json(output_path)
equal(orderly.status, "cancel", "VimLeavePre did not save a cancel snapshot")
expect(orderly.editing and orderly.editing.buffer:find("Exit pending", 1, true), "VimLeavePre lost text inside the debounce interval")
state.final_status = nil
keys("A more<Esc>q")
wait_for(function() return exits[#exits] == "cancel" end, "q did not invoke the cancel exit")
local cancelled = read_json(output_path)
equal(cancelled.status, "cancel", "q did not write a cancel snapshot")
expect(cancelled.editing ~= nil, "q discarded the unfinished comment")
expect(cancelled.editing.buffer:find("Exit pending more", 1, true) ~= nil, "q did not flush the unfinished comment text")
local cancel_revision = cancelled.revision
vim.wait(80)
equal(read_json(output_path).revision, cancel_revision, "a stale debounce replaced the final exit snapshot")

vim.fn.delete(temporary, "rf")
print("neovim-review-test: ok")
vim.cmd("qa!")
