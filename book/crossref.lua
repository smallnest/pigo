-- crossref.lua — minimal figure numbering + internal cross-references for
-- 《庖丁解牛 pigo》.
--
-- Behaviour:
--   * Each level-1 heading (chapter) gets a \label{chap:N} anchor.
--   * Each figure whose caption contains 图N-M gets a \label{fig:N-M} anchor.
--   * In body text, occurrences of 图N-M and 第N章 become clickable internal
--     links (\crossreflink, defined in preamble.tex) pointing at those anchors.
--
-- The displayed number is taken verbatim from the source text, so this filter
-- does not manage LaTeX counters itself — it only wires up the hyperlinks.

-- The document class prints an automatic "图 c.f" number in each caption, so we
-- must NOT also show the hand-written 图N-M (that produced 图 1.2: 图 1-2 …).
-- The manual number is still read to build the \label anchor and to render
-- matching prose links, but it is stripped from the displayed caption text.

local chapter_no = 0

local function fig_label(n, m) return 'fig:' .. n .. '-' .. m end
local function chap_label(n) return 'chap:' .. n end

-- Remove a leading "图 N-M" (with optional trailing colon/period/space) from a
-- caption string, leaving only the descriptive text for display.
local function strip_fig_prefix(text)
  return (text:gsub('^%s*图%s*%d+%s*%-%s*%d+%s*[:：.]?%s*', ''))
end

-- Turn a plain string into a list of inlines, replacing 图N-M / 第N章 spans
-- with RawInline hyperref links and keeping the surrounding text intact.
local function linkify(text)
  local out = {}
  local pos = 1
  local len = #text
  while pos <= len do
    local fs, fe, fn, fm = text:find('图(%d+)%-(%d+)', pos)
    local cs, ce, cn = text:find('第(%d+)章', pos)

    local kind
    if fs and (not cs or fs <= cs) then
      kind = 'fig'
    elseif cs then
      kind = 'chap'
    end

    if not kind then
      table.insert(out, pandoc.Str(text:sub(pos)))
      break
    end

    local ms = (kind == 'fig') and fs or cs
    local me = (kind == 'fig') and fe or ce
    if ms > pos then
      table.insert(out, pandoc.Str(text:sub(pos, ms - 1)))
    end
    if kind == 'fig' then
      table.insert(out, pandoc.RawInline('latex',
        '\\crossreflink{' .. fig_label(fn, fm) .. '}{图 ' .. fn .. '.' .. fm .. '}'))
    else
      table.insert(out, pandoc.RawInline('latex',
        '\\crossreflink{' .. chap_label(cn) .. '}{第' .. cn .. '章}'))
    end
    pos = me + 1
  end
  return out
end

return {
  {
    traverse = 'topdown',

    Header = function(el)
      if el.level == 1 and not el.classes:includes('unnumbered') then
        chapter_no = chapter_no + 1
        el.content:insert(pandoc.RawInline('latex', '\\label{' .. chap_label(chapter_no) .. '}'))
      end
      return el
    end,

    -- pandoc 3.x wraps a standalone captioned image in a Figure block.
    Figure = function(el)
      local cap = pandoc.utils.stringify(el.caption.long)
      local n, m = cap:match('图%s*(%d+)%-(%d+)')
      if n and m then
        el.identifier = fig_label(n, m)
        el.caption.long = pandoc.Blocks{ pandoc.Plain(pandoc.Str(strip_fig_prefix(cap))) }
      end
      return el, false -- don't descend into caption (avoid self-links)
    end,

    -- Fallback: an inline image still carrying its own caption.
    Image = function(el)
      local cap = pandoc.utils.stringify(el.caption)
      local n, m = cap:match('图%s*(%d+)%-(%d+)')
      if n and m and el.identifier == '' then
        el.identifier = fig_label(n, m)
        el.caption = { pandoc.Str(strip_fig_prefix(cap)) }
      end
      return el, false
    end,

    Str = function(el)
      if el.text:find('图%d') or el.text:find('第%d+章') then
        return linkify(el.text)
      end
    end,
  }
}
