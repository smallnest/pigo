#!/usr/bin/env bash
# Build 《庖丁解牛 pigo：用 Go 从零构建命令行 AI Agent》 as a single PDF.
#
# Pipeline: Markdown chapters -> pandoc -> xelatex -> ElegantBook (cyan theme).
#
# Requirements:
#   - pandoc
#   - a WORKING xelatex (TeX Live recommended). The script smoke-tests candidate
#     engines and picks the first that compiles a trivial document, so a broken
#     engine (e.g. the MiKTeX build in ~/miktex that aborts with
#     'Bad parameter value: save_size') is skipped automatically. See README.md.
#   - ElegantBook document class (elegantbook.cls)
#   - rsvg-convert (librsvg) for SVG figure embedding (SVGs are pre-converted to
#     PDF, so neither Inkscape nor -shell-escape is required)
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
missing=0
if ! command -v pandoc >/dev/null 2>&1; then
    echo "Error: 'pandoc' not found on PATH. Install it (brew install pandoc / apt-get install pandoc)." >&2
    missing=1
fi

# ── Pick a WORKING xelatex ───────────────────────────────────────
# Some engines (notably the MiKTeX 21.7 build shipped in ~/miktex on this repo's
# dev machine) start but immediately abort with a fatal engine fault:
#     FATAL xelatex.core - Bad parameter value.  parameterName="save_size"
# No config value (initexmf --set-config-value '[xetex]save_size=...') fixes it —
# the fault is in the engine's memory handler, not the config. So instead of
# trusting whatever xelatex is first on PATH, we smoke-test each candidate on a
# trivial document and prefer the first one that actually produces a PDF.
smoke_ok() {  # $1 = path to a xelatex binary
    local eng="$1" tmp
    tmp="$(mktemp -d)"
    printf '\\documentclass{article}\\begin{document}ok\\end{document}' > "$tmp/_smoke.tex"
    ( cd "$tmp" && "$eng" -interaction=nonstopmode -halt-on-error _smoke.tex ) >/dev/null 2>&1
    local rc=$?
    local ok=1
    [ "$rc" -eq 0 ] && [ -f "$tmp/_smoke.pdf" ] && ok=0
    rm -rf "$tmp"
    return $ok
}

# Candidate engines, in preference order: healthy TeX Live locations first, then
# whatever is on PATH, then this machine's MiKTeX. First that passes wins.
XELATEX=""
CANDIDATE_ENGINES=(
    /Library/TeX/texbin/xelatex
    /usr/local/texlive/bin/xelatex
    "$(command -v xelatex 2>/dev/null || true)"
    "$HOME/miktex/bin/xelatex"
)
# De-dup while preserving order.
seen=""
for eng in "${CANDIDATE_ENGINES[@]}"; do
    [ -n "$eng" ] || continue
    case "$seen" in *"|$eng|"*) continue;; esac
    seen="$seen|$eng|"
    if [ -x "$eng" ] || command -v "$eng" >/dev/null 2>&1; then
        if smoke_ok "$eng"; then
            XELATEX="$eng"
            break
        else
            echo "Note: xelatex at '$eng' failed a smoke test (broken engine); trying next." >&2
        fi
    fi
done

if [ -z "$XELATEX" ]; then
    echo "Error: no WORKING xelatex found. Every candidate engine either is missing or" >&2
    echo "       fails to compile a trivial document. The MiKTeX build in ~/miktex on this" >&2
    echo "       machine aborts with 'Bad parameter value: save_size' (engine-level fault)." >&2
    echo "       Install a healthy TeX Live (see book/README.md → 已知问题 / 构建环境) and re-run." >&2
    missing=1
fi

if ! command -v rsvg-convert >/dev/null 2>&1; then
    echo "Warning: 'rsvg-convert' (librsvg) not found; SVG figures may not embed." >&2
fi
if [ "$missing" -ne 0 ]; then
    echo "Aborting: install the missing dependencies above and re-run." >&2
    exit 1
fi

echo "Using xelatex: $XELATEX"

# Prepend the chosen engine's directory so its companion tools (kpsewhich,
# mktextfm, …) resolve from the same TeX installation.
XELATEX_BIN_DIR="$(cd "$(dirname "$XELATEX")" && pwd)"
export PATH="$XELATEX_BIN_DIR:$PATH"

# ── Pre-convert SVG figures to PDF via rsvg-convert ──────────────
# pandoc emits \usepackage{svg} + \includesvg for .svg images, which needs
# Inkscape + -shell-escape at compile time. Instead we rasterise each SVG to a
# vector PDF up front with the documented rsvg-convert dependency; svg2pdf.lua
# then rewrites the image reference to the .pdf, so the build needs neither
# Inkscape nor shell-escape.
if command -v rsvg-convert >/dev/null 2>&1; then
    for svg in images/*.svg; do
        [ -f "$svg" ] || continue
        rsvg-convert -f pdf -o "${svg%.svg}.pdf" "$svg" \
            && echo "Converted $svg -> ${svg%.svg}.pdf"
    done
fi

echo "Building PDF from ${#INPUTS[@]} file(s): ${INPUTS[*]}"

pandoc "${INPUTS[@]}" \
    -o "$OUT" \
    --from markdown+lists_without_preceding_blankline \
    --pdf-engine="$XELATEX" \
    --lua-filter=svg2pdf.lua \
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
