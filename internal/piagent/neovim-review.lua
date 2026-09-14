-- Galpon's isolated native Neovim review UI.
-- The launcher supplies all data through the versioned JSON file contract.
local M = {}
_G.GalponReview = M

local api = vim.api
local uv = vim.uv or vim.loop
local state
local namespace = api.nvim_create_namespace("galpon-review")

local fallback_palette = {
  Background = "#222436", Surface = "#1e2030", SurfaceRaised = "#2f334d",
  Prompt = "#2d3149", Selection = "#2d3f76", Border = "#589ed7",
  Foreground = "#c8d3f5", Muted = "#9aa5ce", Comment = "#828bb8",
  Status = "#7aa2f7", StatusInk = "#222436", Blue = "#82aaff",
  Cyan = "#65bcff", Purple = "#c099ff", Green = "#c3e88d",
  Orange = "#ff966c", Red = "#ff757f", Yellow = "#ffc777", Teal = "#4fd6be",
}

local function copy(value)
  return vim.deepcopy(value)
end

local function valid_color(value)
  return type(value) == "string" and value:match("^#%x%x%x%x%x%x$") ~= nil
end

local function palette(input)
  local result = {}
  input = type(input) == "table" and input or {}
  for name, default in pairs(fallback_palette) do
    result[name] = valid_color(input[name]) and input[name] or default
  end
  return result
end

local function split_lines(text)
  local lines = vim.split(text, "\n", { plain = true })
  return #lines > 0 and lines or { "" }
end

local function buffer_text(buffer)
  return table.concat(api.nvim_buf_get_lines(buffer, 0, -1, true), "\n")
end

local function sanitize_line(line)
  line = line:gsub("\27%][^\7]*\7", "")
  line = line:gsub("\27%[[0-?]*[ -/]*[@-~]", "")
  line = line:gsub("\27[@-_]", "")
  line = line:gsub("\t", "    ")
  return line:gsub("[%z\1-\8\11\12\14-\31\127]", "")
end

local function comment_text(buffer)
  local lines = api.nvim_buf_get_lines(buffer, 0, -1, true)
  for index, line in ipairs(lines) do lines[index] = sanitize_line(line) end
  return table.concat(lines, "\n")
end

local function trim(value)
  return (value:gsub("^%s+", ""):gsub("%s+$", ""))
end

local function quote_markdown(value)
  local output = {}
  for _, line in ipairs(split_lines(value)) do
    output[#output + 1] = line == "" and ">" or "> " .. line
  end
  return table.concat(output, "\n")
end

local function compiled_review(items)
  local sections = { "I reviewed this response. Here is my feedback:" }
  for index, item in ipairs(items) do
    sections[#sections + 1] = table.concat({
      "### " .. index, "", quote_markdown(item.quote), "", trim(item.comment),
    }, "\n")
  end
  return trim(table.concat(sections, "\n\n"))
end

local function source_matches()
  if not state or not api.nvim_buf_is_valid(state.source_buffer) then return false end
  local text = buffer_text(state.source_buffer)
  return text == state.input.text and vim.fn.sha256(text) == state.input.sourceTextHash
end

local function notify(message, level)
  if not state then return end
  state.notice = sanitize_line(message):gsub("\n", " ")
  vim.notify(state.notice, level or vim.log.levels.INFO, { title = "Galpon Review" })
  if state.refresh_status then state.refresh_status() end
end

local function draft_size(items, editing)
  return #compiled_review(items) + (editing and #editing.buffer or 0)
end

local function valid_limits(items, editing)
  if #items > state.input.limits.items then
    return false, "The review has too many annotations."
  end
  for _, item in ipairs(items) do
    if #item.quote > state.input.limits.selectionBytes then
      return false, "An annotation quote is too large."
    end
  end
  if draft_size(items, editing) > state.input.limits.draftBytes then
    return false, "The review draft is too large."
  end
  return true
end

local function editing_draft()
  if not state.comment or not api.nvim_buf_is_valid(state.comment.buffer) then return nil end
  local result = copy(state.comment.draft)
  result.buffer = comment_text(state.comment.buffer)
  return result
end

local function write_all(file, data)
  local offset = 0
  while offset < #data do
    local written, err = uv.fs_write(file, data:sub(offset + 1), offset)
    if not written then return nil, err end
    offset = offset + written
  end
  return true
end

local function atomic_write(path, data)
  local temporary = string.format("%s.tmp.%d.%d", path, vim.fn.getpid(), uv.hrtime())
  local file, open_error = uv.fs_open(temporary, "w", 384) -- 0600
  if not file then return nil, open_error end
  local ok, write_error = write_all(file, data)
  if ok then ok, write_error = uv.fs_fsync(file) end
  local close_ok, close_error = uv.fs_close(file)
  if not ok or not close_ok then
    pcall(uv.fs_unlink, temporary)
    return nil, write_error or close_error
  end
  local renamed, rename_error = uv.fs_rename(temporary, path)
  if not renamed then
    pcall(uv.fs_unlink, temporary)
    return nil, rename_error
  end
  return true
end

local function snapshot(status, editing)
  if not source_matches() then
    notify("The source changed. The previous valid draft was kept.", vim.log.levels.ERROR)
    return false
  end
  editing = editing == nil and editing_draft() or editing
  local ok, limit_error = valid_limits(state.items, editing)
  if not ok then
    notify(limit_error, vim.log.levels.ERROR)
    return false
  end
  local output = {
    version = 1,
    runId = state.input.runId,
    sourceEntryId = state.input.sourceEntryId,
    sourceHash = state.input.sourceHash,
    revision = state.revision + 1,
    status = status or "open",
    items = state.items,
    editing = editing,
  }
  local encoded_ok, encoded = pcall(vim.json.encode, output)
  if not encoded_ok then
    notify("The review snapshot could not be encoded.", vim.log.levels.ERROR)
    return false
  end
  local wrote, write_error = atomic_write(state.output, encoded)
  if not wrote then
    notify("The review snapshot could not be saved: " .. tostring(write_error), vim.log.levels.ERROR)
    return false
  end
  state.revision = output.revision
  state.last_snapshot = copy(output)
  return true
end

local function debounce_editing()
  if not state.comment then return end
  state.edit_generation = state.edit_generation + 1
  local generation = state.edit_generation
  vim.defer_fn(function()
    if state and not state.exiting and state.comment and state.edit_generation == generation then
      snapshot("open")
    end
  end, state.debounce_ms)
end

local function utf16_from_byte(text, byte_column)
  byte_column = math.max(0, math.min(byte_column, #text))
  return vim.str_utfindex(text, "utf-16", byte_column, false)
end

local function byte_from_utf16(text, column)
  column = math.max(0, column)
  local ok, result = pcall(vim.str_byteindex, text, "utf-16", column, false)
  return ok and math.min(result, #text) or #text
end

local function utf16_length(text)
  return utf16_from_byte(text, #text)
end

local function snap_graphemes(line, column, is_start)
  for _, grapheme in ipairs(state.graphemes[line] or {}) do
    if column > grapheme.startColumn and column < grapheme.endColumn then
      return is_start and grapheme.startColumn or grapheme.endColumn
    end
  end
  return column
end

local function quote_for_range(range)
  local lines = state.source_lines
  local first = lines[range.start + 1]
  local last = lines[range["end"] + 1]
  if range.start == range["end"] then
    return first:sub(byte_from_utf16(first, range.startColumn) + 1, byte_from_utf16(first, range.endColumn))
  end
  local output = { first:sub(byte_from_utf16(first, range.startColumn) + 1) }
  for line = range.start + 1, range["end"] - 1 do output[#output + 1] = lines[line + 1] end
  output[#output + 1] = last:sub(1, byte_from_utf16(last, range.endColumn))
  return table.concat(output, "\n")
end

local function char_end_byte(text, column)
  if column >= #text then return #text end
  local tail = text:sub(column + 1)
  local character = vim.fn.strcharpart(tail, 0, 1)
  return column + #character
end

local function capture_range(visual)
  if not source_matches() then
    notify("The source changed. A selection cannot be captured.", vim.log.levels.ERROR)
    return nil
  end
  local range
  if not visual then
    local cursor = api.nvim_win_get_cursor(state.source_window)
    local line = math.max(0, cursor[1] - 1)
    range = {
      start = line, ["end"] = line, startColumn = 0,
      endColumn = utf16_length(state.source_lines[line + 1]), visualMode = "line",
    }
  else
    local mode = vim.fn.mode(1)
    if mode:sub(1, 1) == "\22" then
      notify("Block selections cannot create accurate quotes.", vim.log.levels.WARN)
      return nil
    end
    local anchor = vim.fn.getpos("v")
    local cursor = api.nvim_win_get_cursor(state.source_window)
    local first = { line = anchor[2] - 1, byte = math.max(0, anchor[3] - 1) }
    local last = { line = cursor[1] - 1, byte = cursor[2] }
    if first.line > last.line or (first.line == last.line and first.byte > last.byte) then
      first, last = last, first
    end
    if mode:sub(1, 1) == "V" then
      range = {
        start = first.line, ["end"] = last.line, startColumn = 0,
        endColumn = utf16_length(state.source_lines[last.line + 1]), visualMode = "line",
      }
    else
      last.byte = char_end_byte(state.source_lines[last.line + 1], last.byte)
      range = {
        start = first.line, ["end"] = last.line,
        startColumn = utf16_from_byte(state.source_lines[first.line + 1], first.byte),
        endColumn = utf16_from_byte(state.source_lines[last.line + 1], last.byte),
        visualMode = "character",
      }
    end
  end
  range.startColumn = snap_graphemes(range.start, range.startColumn, true)
  range.endColumn = snap_graphemes(range["end"], range.endColumn, false)
  range.quote = quote_for_range(range)
  if range.quote == "" then
    notify("Select non-empty source text.", vim.log.levels.WARN)
    return nil
  end
  if #range.quote > state.input.limits.selectionBytes then
    notify("The selected passage is too large. Select less text.", vim.log.levels.WARN)
    return nil
  end
  return range
end

local function range_label(item)
  local start_column = tonumber(item.startColumn) or 0
  local end_line = tonumber(item["end"]) or tonumber(item.start) or 0
  if tonumber(item.start) == end_line then
    return string.format("L%d:%d-%d", item.start + 1, start_column + 1, (tonumber(item.endColumn) or 0) + 1)
  end
  return string.format("L%d:%d-L%d:%d", item.start + 1, start_column + 1, end_line + 1, (tonumber(item.endColumn) or 0) + 1)
end

local function refresh_annotations()
  if not state.annotation_buffer or not api.nvim_buf_is_valid(state.annotation_buffer) then return end
  local lines = {}
  for index, item in ipairs(state.items) do
    local summary = trim(item.comment):gsub("\n+", " ↵ ")
    if vim.fn.strchars(summary) > 120 then summary = vim.fn.strcharpart(summary, 0, 117) .. "..." end
    lines[index] = string.format(" %2d  %-18s  %s", index, range_label(item), summary)
  end
  if #lines == 0 then lines = { "  No annotations. Press c in the source pane." } end
  vim.bo[state.annotation_buffer].modifiable = true
  api.nvim_buf_set_lines(state.annotation_buffer, 0, -1, true, lines)
  vim.bo[state.annotation_buffer].modifiable = false
  vim.bo[state.annotation_buffer].modified = false
  api.nvim_buf_clear_namespace(state.annotation_buffer, namespace, 0, -1)
  if #state.items > 0 then
    state.item_index = math.max(1, math.min(state.item_index, #state.items))
    api.nvim_buf_add_highlight(state.annotation_buffer, namespace, "GalponSelection", state.item_index - 1, 0, -1)
  else
    state.item_index = 1
  end
end

local function highlight_source()
  if not state.source_buffer or not api.nvim_buf_is_valid(state.source_buffer) then return end
  api.nvim_buf_clear_namespace(state.source_buffer, namespace, 0, -1)
  for _, item in ipairs(state.items) do
    for line = item.start, item["end"] do
      local text = state.source_lines[line + 1]
      local start_column = line == item.start and (item.startColumn or 0) or 0
      local end_column = line == item["end"] and (item.endColumn or utf16_length(text)) or utf16_length(text)
      api.nvim_buf_set_extmark(state.source_buffer, namespace, line, byte_from_utf16(text, start_column), {
        end_row = line,
        end_col = byte_from_utf16(text, end_column),
        hl_group = "GalponAnnotated",
        hl_mode = "combine",
        priority = 120,
      })
    end
  end
end

local function refresh_status()
  if not state then return end
  local focus = state.focus == "annotations" and "ANNOTATIONS" or "SOURCE"
  local notice = state.notice ~= "" and ("  " .. state.notice) or ""
  vim.o.statusline = table.concat({
    "%#GalponStatus# GALPON REVIEW ",
    "%#GalponStatusText#  " .. focus .. "  ",
    "%#GalponMuted#  Tab pane   c comment   ]a/[a jump   s prepare   q keep draft",
    notice,
    "%=",
    "%#GalponStatus# " .. #state.items .. " annotation" .. (#state.items == 1 and "" or "s") .. " ",
  })
end

local function set_focus(focus)
  if focus == "annotations" and api.nvim_win_is_valid(state.annotation_window) then
    state.focus = focus
    api.nvim_set_current_win(state.annotation_window)
  elseif api.nvim_win_is_valid(state.source_window) then
    state.focus = "source"
    api.nvim_set_current_win(state.source_window)
  end
  refresh_status()
end

local function select_item(index, focus)
  if #state.items == 0 then return end
  state.item_index = ((index - 1) % #state.items) + 1
  local item = state.items[state.item_index]
  if api.nvim_win_is_valid(state.source_window) then
    local line = state.source_lines[item.start + 1]
    pcall(api.nvim_win_set_cursor, state.source_window, { item.start + 1, byte_from_utf16(line, item.startColumn or 0) })
    pcall(api.nvim_win_call, state.source_window, function() vim.cmd("normal! zz") end)
  end
  refresh_annotations()
  if focus then set_focus(focus) end
end

local function item_under_cursor()
  if api.nvim_get_current_buf() ~= state.annotation_buffer then return state.item_index end
  return math.max(1, math.min(api.nvim_win_get_cursor(0)[1], math.max(1, #state.items)))
end

local function push_history()
  state.history[#state.history + 1] = copy(state.items)
  if #state.history > 64 then table.remove(state.history, 1) end
end

local function close_comment()
  if not state.comment then return end
  state.edit_generation = state.edit_generation + 1
  local window, buffer = state.comment.window, state.comment.buffer
  state.comment = nil
  if window and api.nvim_win_is_valid(window) then pcall(api.nvim_win_close, window, true) end
  if buffer and api.nvim_buf_is_valid(buffer) then pcall(api.nvim_buf_delete, buffer, { force = true }) end
end

local function next_id()
  state.id_counter = state.id_counter + 1
  return vim.fn.sha256(table.concat({ state.input.runId, tostring(uv.hrtime()), tostring(state.id_counter) }, ":")):sub(1, 24)
end

local function save_comment()
  if not state.comment then return false end
  if not source_matches() then
    notify("The source changed. The annotation was not saved.", vim.log.levels.ERROR)
    return false
  end
  local draft = editing_draft()
  local comment = trim(draft.buffer)
  if comment == "" then
    notify("Write a comment before you save the annotation.", vim.log.levels.WARN)
    return false
  end
  local next_items = copy(state.items)
  local selected
  if draft.kind == "edit" then
    for index, item in ipairs(next_items) do
      if item.id == draft.itemId then selected = index; item.comment = comment; break end
    end
    if not selected then
      notify("The annotation to edit no longer exists.", vim.log.levels.ERROR)
      return false
    end
  else
    if #next_items >= state.input.limits.items then
      notify("The review has the maximum number of annotations.", vim.log.levels.WARN)
      return false
    end
    local item = {
      id = next_id(), start = draft.start, ["end"] = draft["end"],
      startColumn = draft.startColumn, endColumn = draft.endColumn,
      quote = quote_for_range(draft), comment = comment,
    }
    next_items[#next_items + 1] = item
    selected = #next_items
  end
  local ok, limit_error = valid_limits(next_items, nil)
  if not ok then notify(limit_error, vim.log.levels.WARN); return false end
  push_history()
  state.items = next_items
  state.item_index = selected
  close_comment()
  refresh_annotations()
  highlight_source()
  snapshot("open", nil)
  set_focus(draft.kind == "edit" and "annotations" or "source")
  return true
end

local function exit_review(status)
  local editing = editing_draft()
  if not snapshot(status, editing) then
    if status == "prepare" then return false end
    -- q must still close. The preceding valid atomic snapshot remains available.
  end
  state.exiting = true
  if state.on_exit then state.on_exit(status) else vim.cmd("qa!") end
  return true
end

local function open_comment(draft)
  if state.comment then close_comment() end
  local previous = api.nvim_get_current_win()
  vim.cmd("botright 7split")
  local window = api.nvim_get_current_win()
  local buffer = api.nvim_create_buf(false, true)
  api.nvim_win_set_buf(window, buffer)
  vim.bo[buffer].buftype = "nofile"
  vim.bo[buffer].bufhidden = "wipe"
  vim.bo[buffer].swapfile = false
  vim.bo[buffer].undofile = false
  vim.bo[buffer].modeline = false
  vim.bo[buffer].filetype = "galpon-review-comment"
  vim.bo[buffer].modifiable = true
  vim.wo[window].wrap = true
  vim.wo[window].number = false
  vim.wo[window].relativenumber = false
  vim.wo[window].signcolumn = "no"
  vim.wo[window].winhighlight = "Normal:GalponPrompt,StatusLine:GalponPrompt"
  vim.wo[window].winbar = "%#GalponPromptTitle# COMMENT  %#GalponMuted#Ctrl-s save   q keep unfinished draft"
  api.nvim_buf_set_lines(buffer, 0, -1, true, split_lines(draft.buffer or ""))
  state.comment = { buffer = buffer, window = window, draft = copy(draft), previous_window = previous }
  api.nvim_create_autocmd({ "TextChanged", "TextChangedI" }, {
    group = state.augroup, buffer = buffer, callback = debounce_editing,
  })
  vim.keymap.set({ "n", "i" }, "<C-s>", function()
    if api.nvim_get_mode().mode:sub(1, 1) == "i" then vim.cmd("stopinsert") end
    save_comment()
  end, { buffer = buffer, nowait = true, silent = true })
  vim.keymap.set("n", "q", function() exit_review("cancel") end, { buffer = buffer, nowait = true, silent = true })
  api.nvim_win_set_cursor(window, { math.max(1, api.nvim_buf_line_count(buffer)), 0 })
  vim.cmd("startinsert!")
  snapshot("open")
end

local function start_new_comment(visual)
  local range = capture_range(visual)
  if not range then return end
  open_comment({
    kind = "new", start = range.start, ["end"] = range["end"],
    startColumn = range.startColumn, endColumn = range.endColumn,
    visualMode = range.visualMode, buffer = "",
  })
end

local function edit_item(index)
  local item = state.items[index]
  if not item then return end
  state.item_index = index
  open_comment({
    kind = "edit", itemId = item.id, start = item.start, ["end"] = item["end"],
    startColumn = item.startColumn, endColumn = item.endColumn,
    visualMode = item.visualMode, buffer = item.comment,
  })
end

local function delete_item()
  local index = item_under_cursor()
  if not state.items[index] or not source_matches() then
    if not source_matches() then notify("The source changed. The annotation was not deleted.", vim.log.levels.ERROR) end
    return
  end
  push_history()
  table.remove(state.items, index)
  state.item_index = math.max(1, math.min(index, #state.items))
  refresh_annotations()
  highlight_source()
  snapshot("open", nil)
end

local function undo_items()
  local previous = table.remove(state.history)
  if not previous then notify("There is no annotation change to undo."); return end
  if not source_matches() then
    state.history[#state.history + 1] = previous
    notify("The source changed. The annotation change was not restored.", vim.log.levels.ERROR)
    return
  end
  state.items = previous
  state.item_index = math.max(1, math.min(state.item_index, math.max(1, #state.items)))
  refresh_annotations()
  highlight_source()
  snapshot("open", nil)
end

local function map_ui()
  local function options(buffer) return { buffer = buffer, nowait = true, silent = true } end
  local source = state.source_buffer
  local annotations = state.annotation_buffer
  vim.keymap.set("n", "c", function() start_new_comment(false) end, options(source))
  vim.keymap.set("x", "c", function() start_new_comment(true) end, options(source))
  vim.keymap.set("n", "<Tab>", function() set_focus("annotations") end, options(source))
  vim.keymap.set("n", "<Tab>", function() set_focus("source") end, options(annotations))
  for _, buffer in ipairs({ source, annotations }) do
    vim.keymap.set("n", "q", function() exit_review("cancel") end, options(buffer))
    vim.keymap.set("n", "s", function() exit_review("prepare") end, options(buffer))
    vim.keymap.set("n", "]a", function() select_item(state.item_index + 1) end, options(buffer))
    vim.keymap.set("n", "[a", function() select_item(state.item_index - 1) end, options(buffer))
  end
  vim.keymap.set("n", "<CR>", function() edit_item(item_under_cursor()) end, options(annotations))
  vim.keymap.set("n", "e", function() edit_item(item_under_cursor()) end, options(annotations))
  vim.keymap.set("n", "x", delete_item, options(annotations))
  vim.keymap.set("n", "u", undo_items, options(annotations))
end

local function apply_palette(colors)
  vim.o.termguicolors = true
  local set = api.nvim_set_hl
  set(0, "Normal", { fg = colors.Foreground, bg = colors.Background })
  set(0, "NormalNC", { fg = colors.Muted, bg = colors.Background })
  set(0, "GalponSource", { fg = colors.Foreground, bg = colors.Surface })
  set(0, "GalponAnnotations", { fg = colors.Foreground, bg = colors.Surface })
  set(0, "GalponPrompt", { fg = colors.Foreground, bg = colors.Prompt })
  set(0, "GalponPromptTitle", { fg = colors.Cyan, bg = colors.Prompt, bold = true })
  set(0, "GalponTitle", { fg = colors.Foreground, bg = colors.SurfaceRaised, bold = true })
  set(0, "GalponMuted", { fg = colors.Muted, bg = colors.SurfaceRaised })
  set(0, "GalponStatus", { fg = colors.StatusInk, bg = colors.Status, bold = true })
  set(0, "GalponStatusText", { fg = colors.Foreground, bg = colors.SurfaceRaised, bold = true })
  set(0, "GalponSelection", { fg = colors.Foreground, bg = colors.Selection, bold = true })
  set(0, "GalponAnnotated", { bg = colors.Selection, underline = true, sp = colors.Cyan })
  set(0, "Visual", { fg = colors.Foreground, bg = colors.Selection })
  set(0, "Search", { fg = colors.StatusInk, bg = colors.Yellow })
  set(0, "IncSearch", { fg = colors.StatusInk, bg = colors.Orange })
  set(0, "StatusLine", { fg = colors.Foreground, bg = colors.SurfaceRaised })
  set(0, "StatusLineNC", { fg = colors.Muted, bg = colors.SurfaceRaised })
  set(0, "WinSeparator", { fg = colors.Border, bg = colors.Background })
  set(0, "EndOfBuffer", { fg = colors.Surface, bg = colors.Surface })
end

local function setup_render_markdown(runtime)
  if type(runtime) ~= "string" or runtime == "" then return false end
  local plugin = runtime .. "/plugins/render-markdown.nvim"
  local icons_plugin = runtime .. "/plugins/mini.icons"
  local parser_runtime = runtime .. "/runtime"
  vim.opt.runtimepath = { plugin, icons_plugin, parser_runtime, vim.env.VIMRUNTIME }
  vim.opt.packpath = { parser_runtime }
  if vim.fn.isdirectory(plugin) ~= 1
    or vim.fn.isdirectory(icons_plugin) ~= 1
    or vim.fn.filereadable(parser_runtime .. "/parser/markdown.so") ~= 1
    or vim.fn.filereadable(parser_runtime .. "/parser/markdown_inline.so") ~= 1 then
    return false
  end
  local icons_ok, icons = pcall(require, "mini.icons")
  if not icons_ok or not pcall(icons.setup, {}) then return false end
  local ok, renderer = pcall(require, "render-markdown")
  if not ok then return false end
  local configured = pcall(renderer.setup, {
    enabled = true,
    render_modes = { "n", "c", "t" }, -- Visual and insert modes expose exact source Markdown.
    file_types = { "markdown" },
    debounce = 20,
    anti_conceal = { enabled = false },
    sign = { enabled = false },
    completions = { blink = { enabled = false }, coq = { enabled = false }, lsp = { enabled = false } },
    heading = { backgrounds = { "GalponSource" }, foregrounds = { "GalponTitle" } },
    code = { style = "full", width = "block", border = "thin", highlight = "GalponPrompt" },
    bullet = { icons = { "•", "◦", "▪", "▫" } },
    quote = { highlight = "GalponMuted" },
    html = { enabled = false },
    latex = { enabled = false },
    yaml = { enabled = false },
    overrides = { buftype = { nofile = { render_modes = { "n", "c", "t" }, padding = { highlight = "GalponSource" }, sign = { enabled = false } } } },
    win_options = { conceallevel = { default = 2, rendered = 2 }, concealcursor = { default = "", rendered = "nct" } },
  })
  return configured
end

local function validate_input(input)
  if type(input) ~= "table" or input.version ~= 1 then error("unsupported or missing review input version") end
  for _, name in ipairs({ "runId", "sourceEntryId", "sourceHash", "sourceTextHash", "text" }) do
    if type(input[name]) ~= "string" then error("invalid review input field: " .. name) end
  end
  if type(input.items) ~= "table" or type(input.graphemes) ~= "table" or type(input.limits) ~= "table" then
    error("invalid review arrays or limits")
  end
  for _, name in ipairs({ "items", "selectionBytes", "draftBytes" }) do
    if type(input.limits[name]) ~= "number" or input.limits[name] < 1 then error("invalid review limit: " .. name) end
  end
  if vim.fn.sha256(input.text) ~= input.sourceTextHash then error("review source text hash does not match the input") end
end

local function read_input(path)
  local file, open_error = io.open(path, "rb")
  if not file then error("cannot open review input: " .. tostring(open_error)) end
  local content = file:read("*a")
  file:close()
  local ok, input = pcall(vim.json.decode, content)
  if not ok then error("cannot decode review input: " .. tostring(input)) end
  validate_input(input)
  return input
end

local function configure_isolation()
  vim.o.modeline = false
  vim.o.modelines = 0
  vim.o.shadafile = "NONE"
  vim.o.swapfile = false
  vim.o.backup = false
  vim.o.writebackup = false
  vim.o.undofile = false
  vim.o.mouse = "a"
  vim.o.laststatus = 3
  vim.o.showtabline = 0
  vim.o.ruler = false
  vim.o.showmode = false
  vim.o.shortmess = vim.o.shortmess .. "I"
  vim.g.loaded_python3_provider = 0
  vim.g.loaded_python_provider = 0
  vim.g.loaded_ruby_provider = 0
  vim.g.loaded_perl_provider = 0
  vim.g.loaded_node_provider = 0
  vim.g.loaded_remote_plugins = 1
end

function M.start(options)
  if state then error("Galpon Review is already running") end
  options = options or {}
  configure_isolation()
  local input_path = options.input_path or vim.env.GALPON_REVIEW_NVIM_INPUT
  local output_path = options.output_path or vim.env.GALPON_REVIEW_NVIM_OUTPUT
  local runtime = options.runtime or vim.env.GALPON_REVIEW_NVIM_RUNTIME
  if type(input_path) ~= "string" or input_path == "" then error("GALPON_REVIEW_NVIM_INPUT is required") end
  if type(output_path) ~= "string" or output_path == "" then error("GALPON_REVIEW_NVIM_OUTPUT is required") end
  local input = read_input(input_path)
  local graphemes = {}
  for _, item in ipairs(input.graphemes) do
    local line = tonumber(item.line)
    if line and line >= 0 then
      graphemes[line] = graphemes[line] or {}
      graphemes[line][#graphemes[line] + 1] = item
    end
  end
  state = {
    input = input, output = output_path, source_lines = split_lines(input.text),
    items = copy(input.items), graphemes = graphemes, revision = 0,
    item_index = 1, history = {}, notice = "", focus = "source",
    id_counter = 0, edit_generation = 0, debounce_ms = options.debounce_ms or 180,
    on_exit = options.on_exit, augroup = api.nvim_create_augroup("GalponReview", { clear = true }),
  }
  M._state = state
  apply_palette(palette(input.palette))
  state.render_markdown = setup_render_markdown(runtime)
  if not state.render_markdown and not options.allow_missing_runtime then
    error("the prepared render-markdown runtime is missing or invalid")
  end

  local source = api.nvim_get_current_buf()
  state.source_buffer = source
  vim.bo[source].modifiable = true
  api.nvim_buf_set_lines(source, 0, -1, true, state.source_lines)
  vim.bo[source].buftype = "nofile"
  vim.bo[source].bufhidden = "hide"
  vim.bo[source].buflisted = false
  vim.bo[source].swapfile = false
  vim.bo[source].undofile = false
  vim.bo[source].modeline = false
  vim.bo[source].filetype = "markdown"
  vim.bo[source].modifiable = false
  vim.bo[source].readonly = true
  vim.bo[source].modified = false
  state.source_window = api.nvim_get_current_win()
  vim.wo[state.source_window].wrap = true
  vim.wo[state.source_window].linebreak = true
  vim.wo[state.source_window].breakindent = true
  vim.wo[state.source_window].number = true
  vim.wo[state.source_window].relativenumber = false
  vim.wo[state.source_window].signcolumn = "no"
  vim.wo[state.source_window].winhighlight = "Normal:GalponSource,EndOfBuffer:GalponSource"
  vim.wo[state.source_window].winbar = "%#GalponTitle# SOURCE  %#GalponMuted#read-only Markdown   v/V select   / search"

  vim.cmd("botright vsplit")
  state.annotation_window = api.nvim_get_current_win()
  state.annotation_buffer = api.nvim_create_buf(false, true)
  api.nvim_win_set_buf(state.annotation_window, state.annotation_buffer)
  vim.bo[state.annotation_buffer].buftype = "nofile"
  vim.bo[state.annotation_buffer].bufhidden = "hide"
  vim.bo[state.annotation_buffer].swapfile = false
  vim.bo[state.annotation_buffer].undofile = false
  vim.bo[state.annotation_buffer].modeline = false
  vim.bo[state.annotation_buffer].filetype = "galpon-review-annotations"
  vim.bo[state.annotation_buffer].modifiable = false
  vim.wo[state.annotation_window].wrap = false
  vim.wo[state.annotation_window].number = false
  vim.wo[state.annotation_window].relativenumber = false
  vim.wo[state.annotation_window].signcolumn = "no"
  vim.wo[state.annotation_window].winhighlight = "Normal:GalponAnnotations,EndOfBuffer:GalponAnnotations"
  vim.wo[state.annotation_window].winbar = "%#GalponTitle# ANNOTATIONS  %#GalponMuted#Enter/e edit   x delete   u undo"

  state.refresh_status = refresh_status
  refresh_annotations()
  highlight_source()
  map_ui()
  set_focus("source")
  if input.editing then open_comment(copy(input.editing)) end
  if not snapshot("open") then error("cannot write the initial review snapshot") end
  return M
end

function M.snapshot(status) return snapshot(status or "open") end
function M.capture(visual) return capture_range(visual == true) end
function M.save_comment() return save_comment() end
function M.exit(status) return exit_review(status or "cancel") end
function M.select_item(index, focus) return select_item(index, focus) end
function M.undo() return undo_items() end
function M.source_matches() return source_matches() end
function M.compiled_review() return compiled_review(state and state.items or {}) end
function M.utf16_from_byte(text, column) return utf16_from_byte(text, column) end
function M.byte_from_utf16(text, column) return byte_from_utf16(text, column) end

if not vim.g.galpon_review_no_autostart then
  local ok, err = pcall(M.start)
  if not ok then
    api.nvim_err_writeln("Galpon Review: " .. tostring(err))
    vim.cmd("cquit 1")
  end
end

return M
