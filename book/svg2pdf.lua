-- svg2pdf.lua — pandoc Lua filter for the 《Write Pi Agent in Go》 book build.
--
-- Purpose: when producing PDF via xelatex, rewrite image references that point
-- at an `.svg` file to the sibling `.pdf` that `build_pdf.sh` pre-generates with
-- rsvg-convert. This lets the book embed vector figures through the documented
-- `rsvg-convert` dependency (librsvg) instead of pandoc's default `\usepackage{svg}`
-- + `\includesvg`, which would require Inkscape and `-shell-escape` at compile time.
--
-- The rewrite only happens for the LaTeX/PDF writer and only when the pre-converted
-- `.pdf` actually exists on disk, so other output formats (e.g. HTML) keep the SVG.
--
-- Missing figures degrade gracefully: if a referenced figure has neither a
-- pre-converted `.pdf` nor a source `.svg` on disk, we emit a framed placeholder
-- carrying the caption instead of letting `\includesvg` hard-fail the whole build.
-- This mirrors build_pdf.sh's "compile only the chapters that exist" philosophy,
-- so the book builds incrementally as art is added.

local function file_exists(path)
  local f = io.open(path, "r")
  if f then f:close(); return true end
  return false
end

-- Escape a plain string for safe use in LaTeX (single pass — no re-processing
-- of the backslashes/braces we introduce).
local tex_special = {
  ["\\"] = "\\textbackslash{}", ["{"] = "\\{", ["}"] = "\\}",
  ["$"] = "\\$", ["&"] = "\\&", ["#"] = "\\#", ["_"] = "\\_",
  ["%"] = "\\%", ["~"] = "\\textasciitilde{}", ["^"] = "\\textasciicircum{}",
}
local function tex_escape(s)
  return (s:gsub("[\\{}$&#_%%~%^]", tex_special))
end

function Image(img)
  -- Only rewrite for LaTeX/PDF output.
  if not (FORMAT:match("latex") or FORMAT:match("beamer")) then
    return nil
  end
  local src = img.src
  if not src then return nil end

  -- SVG figures: prefer the sibling .pdf that build_pdf.sh pre-generates.
  local base = src:match("^(.*)%.svg$")
  if base then
    local pdf = base .. ".pdf"
    if file_exists(pdf) then
      img.src = pdf
      return img
    end
  end

  -- If the referenced file (svg/png/pdf/…) exists on disk, let pandoc embed it.
  if file_exists(src) then
    return nil
  end

  -- Missing figure: emit a placeholder box so the build proceeds instead of
  -- aborting on a missing file.
  local caption = pandoc.utils.stringify(img.caption or "")
  local label = tex_escape(caption ~= "" and caption or src)
  local box = "\\begin{center}\\fbox{\\parbox[c][3.2cm][c]{0.86\\linewidth}"
    .. "{\\centering\\textbf{[\\,图待补 / figure pending\\,]}\\\\[0.4em]"
    .. "{\\small " .. label .. "}}}\\end{center}"
  return pandoc.RawInline("latex", box)
end
