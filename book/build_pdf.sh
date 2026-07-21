#!/usr/bin/env bash
# Build 《庖丁解牛 pigo：用 Go 从零构建命令行 AI Agent》 as a single PDF.
#
# Pipeline: Markdown chapters -> pandoc -> xelatex -> ElegantBook (cyan theme).
#
# Requirements:
#   - pandoc
#   - xelatex (TeX Live or MiKTeX; this repo's dev machine ships MiKTeX at
#     ~/miktex/bin/xelatex, which this script auto-detects and prepends to PATH)
#   - ElegantBook document class (elegantbook.cls)
#   - rsvg-convert (librsvg) for SVG figure embedding (optional)
#   - Chinese fonts: macOS Songti SC / Heiti SC, or Linux Noto Sans/Serif CJK SC
#
# Usage:  bash book/build_pdf.sh   (or: cd book && bash build_pdf.sh)
#
# Chapter/section numbers are supplied by the document class + pandoc
# --number-sections; source headings carry no manual numbers.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

OUT="庖丁解牛-pigo.pdf"

# ── Ordered candidate inputs. Only the ones that exist are compiled, so the
#    book can be built incrementally as downstream nodes add chapters. ──
CANDIDATES=(
    introduction.md
    chapter1.md
    chapter2.md
    chapter3.md
    chapter4.md
    chapter5.md
    chapter6.md
    chapter7.md
    chapter8.md
    chapter9.md
    chapter10.md
    afterword.md
)

INPUTS=()
for ch in "${CANDIDATES[@]}"; do
    if [ -f "$ch" ]; then
        INPUTS+=("$ch")
    fi
done

if [ "${#INPUTS[@]}" -eq 0 ]; then
    echo "Error: no input markdown found (introduction.md / chapter*.md)." >&2
    echo "       Add at least one source file before building." >&2
    exit 1
fi

# ── Dependency detection ─────────────────────────────────────────
# MiKTeX on this machine lives here; prepend so xelatex + kpsewhich resolve.
if [ -x "$HOME/miktex/bin/xelatex" ]; then
    export PATH="$HOME/miktex/bin:$PATH"
fi

missing=0
if ! command -v pandoc >/dev/null 2>&1; then
    echo "Error: 'pandoc' not found on PATH. Install it (brew install pandoc / apt-get install pandoc)." >&2
    missing=1
fi
if ! command -v xelatex >/dev/null 2>&1; then
    echo "Error: 'xelatex' not found on PATH. Install TeX Live or MiKTeX." >&2
    missing=1
fi
if ! command -v rsvg-convert >/dev/null 2>&1; then
    echo "Warning: 'rsvg-convert' (librsvg) not found; SVG figures may not embed." >&2
fi
if [ "$missing" -ne 0 ]; then
    echo "Aborting: install the missing dependencies above and re-run." >&2
    exit 1
fi

echo "Building PDF from ${#INPUTS[@]} file(s): ${INPUTS[*]}"

pandoc "${INPUTS[@]}" \
    -o "$OUT" \
    --from markdown+lists_without_preceding_blankline \
    --pdf-engine=xelatex \
    --lua-filter=crossref.lua \
    --lua-filter=experiment_box.lua \
    --toc \
    --toc-depth=3 \
    --number-sections \
    -V documentclass=elegantbook \
    -V classoption=lang=cn \
    -V classoption=cyan \
    -V classoption=device=normal \
    -V author="pigo" \
    --metadata title-meta="庖丁解牛 pigo：用 Go 从零构建命令行 AI Agent" \
    --metadata author-meta="pigo" \
    -H preamble.tex \
    --include-before-body=cover.tex \
    --highlight-style=kate \
    --columns=80

if [ -f "$OUT" ]; then
    SIZE="$(du -h "$OUT" | cut -f1)"
    echo ""
    echo "Done: $SCRIPT_DIR/$OUT ($SIZE)"
else
    echo "Error: PDF generation failed." >&2
    exit 1
fi
