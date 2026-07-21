-- svg2pdf.lua — pandoc Lua filter for the 《庖丁解牛 pigo》 book build.
--
-- Purpose: when producing PDF via xelatex, rewrite image references that point
-- at an `.svg` file to the sibling `.pdf` that `build_pdf.sh` pre-generates with
-- rsvg-convert. This lets the book embed vector figures through the documented
-- `rsvg-convert` dependency (librsvg) instead of pandoc's default `\usepackage{svg}`
-- + `\includesvg`, which would require Inkscape and `-shell-escape` at compile time.
--
-- The rewrite only happens for the LaTeX/PDF writer and only when the pre-converted
-- `.pdf` actually exists on disk, so other output formats (e.g. HTML) keep the SVG.

local function file_exists(path)
  local f = io.open(path, "r")
  if f then f:close(); return true end
  return false
end

function Image(img)
  -- Only rewrite for LaTeX/PDF output.
  if not (FORMAT:match("latex") or FORMAT:match("beamer")) then
    return nil
  end
  local src = img.src
  if not src then return nil end
  local base = src:match("^(.*)%.svg$")
  if not base then return nil end
  local pdf = base .. ".pdf"
  -- Resolve relative to the build directory (build_pdf.sh cd's into book/).
  if file_exists(pdf) then
    img.src = pdf
    return img
  end
  return nil
end
