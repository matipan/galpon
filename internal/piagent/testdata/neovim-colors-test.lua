-- Loaded only by the isolated real-terminal test, after native Review starts.
-- Check final screen attributes, not just named highlight definitions. The
-- experimental cell API is used only in tests, on both supported test versions.
local api = vim.api
local state = GalponReview._state
-- The first inspection enables hlstate and invalidates cached attribute IDs.
-- Prime it before the screen checks, then redraw with the new attribute table.
api.nvim__inspect_cell(1, 0, 0)
vim.cmd("redraw!")
local function equal(actual, expected, message)
  assert(vim.deep_equal(actual, expected), message .. ": expected " .. vim.inspect(expected) .. ", got " .. vim.inspect(actual))
end
local function cell(row, text)
  local position = vim.fn.screenpos(state.source_window, row, 1)
  assert(position.row > 0, "fixture row is not visible")
  -- Conceal and inline icons change screen columns. Find the displayed text
  -- within the source pane instead of treating byte columns as screen columns.
  local left = api.nvim_win_get_position(state.source_window)[2]
  local cells, parts = {}, {}
  for column = left, left + api.nvim_win_get_width(state.source_window) - 1 do
    local displayed = api.nvim__inspect_cell(1, position.row - 1, column)
    cells[#cells + 1], parts[#parts + 1] = displayed, displayed[1]
  end
  local offset = assert(table.concat(parts):find(text, 1, true), "missing displayed text " .. text)
  local start = 1
  for _, displayed in ipairs(cells) do
    if start == offset then return displayed end
    start = start + #displayed[1]
  end
  error("displayed text did not start at a cell boundary")
end
local function color(row, text, field, name)
  local displayed = cell(row, text)
  equal(displayed[1], text:sub(1, 1), "the inspected cell must contain source text")
  equal(displayed[2][field], tonumber(state.input.palette[name]:sub(2), 16), text .. " " .. field)
end
local function check(mode)
  vim.cmd("redraw!")
  assert(vim.treesitter.highlighter.active[state.source_buffer], "Markdown syntax highlighting is not active")
  if mode == "n" then
    equal(vim.wo[state.source_window].conceallevel, 2, "Normal mode must render Markdown")
    color(1, "Native", "foreground", "Blue")
    color(7, "Colors", "foreground", "Purple")
    color(9, "link", "foreground", "Blue")
    color(9, "inline", "foreground", "Cyan")
    color(9, "inline", "background", "Prompt")
    color(3, "idea", "foreground", "Foreground")
    equal(cell(3, "bold")[2].bold, true, "strong text must be bold")
  else
    equal(vim.wo[state.source_window].conceallevel, 0, mode .. " mode must expose all source Markdown")
    for _, item in ipairs({ { 3, "**" }, { 9, "[" }, { 9, "`" }, { 11, "```" } }) do
      equal(cell(item[1], item[2])[1], item[2]:sub(1, 1), "raw Markdown marker must be visible")
    end
  end
  equal(table.concat(api.nvim_buf_get_lines(state.source_buffer, 0, -1, true), "\n"), state.input.text, "color rendering changed source text")
end
local checks = 0
vim.keymap.set({ "n", "x" }, "<F8>", function()
  checks = checks + 1
  local mode = api.nvim_get_mode().mode
  local ok, err = pcall(check, mode)
  vim.fn.writefile({ vim.json.encode({ ok = ok, error = ok and "" or tostring(err), mode = mode }) },
    vim.env.GALPON_REVIEW_NVIM_OUTPUT .. ".colors-" .. checks)
end, { buffer = state.source_buffer, silent = true })
