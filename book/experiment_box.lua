-- experiment_box.lua — render「实验 N-M」callouts into a bordered tcolorbox.
--
-- Two authoring styles are supported:
--
--   1. A heading whose text starts with「实验 N-M」(any level). Everything from
--      that heading up to the next heading of the same-or-higher level is
--      wrapped in \begin{experimentbox}...\end{experimentbox}.
--
--   2. A fenced Div carrying the class `experiment`:
--          ::: experiment
--          实验 1-1：...
--          :::
--      Its contents are wrapped in the same box.
--
-- `experimentbox` is defined in preamble.tex. Boxes are opened/closed with
-- RawBlock LaTeX so the styling lives entirely in the preamble.

local function open()  return pandoc.RawBlock('latex', '\\begin{experimentbox}') end
local function close() return pandoc.RawBlock('latex', '\\end{experimentbox}') end

-- Style 2: Div with class `experiment`.
local function Div(el)
  if el.classes:includes('experiment') then
    local blocks = { open() }
    for _, b in ipairs(el.content) do table.insert(blocks, b) end
    table.insert(blocks, close())
    return blocks
  end
end

-- Style 1: heading-delimited experiment sections, handled document-wide.
local function Pandoc(doc)
  local out = {}
  local in_box = false
  local box_level = 0

  local function close_if_open()
    if in_box then
      table.insert(out, close())
      in_box = false
    end
  end

  for _, block in ipairs(doc.blocks) do
    if block.t == 'Header' then
      local text = pandoc.utils.stringify(block)
      if text:match('^实验%s*%d') then
        close_if_open()
        box_level = block.level
        block.classes:insert('unnumbered')
        table.insert(out, open())
        table.insert(out, block)
        in_box = true
      elseif in_box and block.level <= box_level then
        close_if_open()
        table.insert(out, block)
      else
        table.insert(out, block)
      end
    else
      table.insert(out, block)
    end
  end

  close_if_open()
  doc.blocks = out
  return doc
end

-- Div runs first (per-element), then Pandoc runs the document-wide pass.
return {
  { Div = Div },
  { Pandoc = Pandoc },
}
