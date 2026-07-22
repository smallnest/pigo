#!/usr/bin/env bash
# Build 《庖丁解牛 pigo：用 Go 从零构建命令行 AI Agent》 as a single PDF.
#
# Pipeline: Markdown chapters -> pandoc -> (tectonic | xelatex) -> ElegantBook.
#
# Engine selection (in order):
#   1. tectonic  — self-contained, downloads its own TeX bundle on first run, and
#      needs no system TeX install. PREFERRED because it "just works" cross-platform.
#      tectonic resolves macOS CJK fonts only by file path (see preamble.tex), and
#      its bundled biblatex emits an older .bcf than a modern system biber accepts,
#      so we run it via a two-step build with a small self-adjusting biber shim
#      (see below). tectonic requires an external `biber` on PATH.
#   2. a WORKING xelatex (TeX Live) — used only if tectonic is absent. The script
#      smoke-tests candidate engines and picks the first that compiles a trivial
#      document, so a broken engine (e.g. the MiKTeX build in ~/miktex that aborts
#      with 'Bad parameter value: save_size') is skipped automatically.
#
# Other requirements:
#   - pandoc
#   - ElegantBook document class (elegantbook.cls) — bundled with tectonic
#   - rsvg-convert (librsvg) for SVG figure embedding (SVGs are pre-converted to
#     PDF, so neither Inkscape nor -shell-escape is required)
#   - Chinese fonts: macOS Songti/Heiti, or Linux Noto Sans/Serif CJK SC
#
# Usage:  bash book/build_pdf.sh   (or: cd book && bash build_pdf.sh)
#
# Chapter/section numbers are supplied by the document class + pandoc
# --number-sections; source headings carry no manual numbers.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

OUT="用Go从零构建Pi Agent.pdf"

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

# ── Pick an engine: tectonic preferred, else a WORKING xelatex ───
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

ENGINE_KIND=""   # "tectonic" | "xelatex"
ENGINE=""        # path to the chosen engine

if command -v tectonic >/dev/null 2>&1; then
    ENGINE_KIND="tectonic"
    ENGINE="$(command -v tectonic)"
fi

if [ -z "$ENGINE_KIND" ]; then
    # No tectonic — fall back to smoke-testing xelatex candidates. Some engines
    # (notably the MiKTeX 21.7 build in ~/miktex on this repo's dev machine) start
    # but immediately abort with 'Bad parameter value: save_size'; no config fixes
    # it (the fault is in the engine's memory handler), so we prefer the first
    # candidate that actually produces a PDF.
    CANDIDATE_ENGINES=(
        /Library/TeX/texbin/xelatex
        /usr/local/texlive/bin/xelatex
        "$HOME/Library/TinyTeX/bin/universal-darwin/xelatex"
        "$(command -v xelatex 2>/dev/null || true)"
        "$HOME/miktex/bin/xelatex"
    )
    seen=""
    for eng in "${CANDIDATE_ENGINES[@]}"; do
        [ -n "$eng" ] || continue
        case "$seen" in *"|$eng|"*) continue;; esac
        seen="$seen|$eng|"
        if [ -x "$eng" ] || command -v "$eng" >/dev/null 2>&1; then
            if smoke_ok "$eng"; then
                ENGINE_KIND="xelatex"
                ENGINE="$eng"
                break
            else
                echo "Note: xelatex at '$eng' failed a smoke test (broken engine); trying next." >&2
            fi
        fi
    done
fi

if [ -z "$ENGINE_KIND" ]; then
    echo "Error: no usable TeX engine found." >&2
    echo "       Install tectonic (brew install tectonic) — the simplest option, no" >&2
    echo "       system TeX required — or a healthy TeX Live/TinyTeX xelatex. The MiKTeX" >&2
    echo "       build in ~/miktex on this machine aborts with 'Bad parameter value:" >&2
    echo "       save_size' (engine-level fault). See book/README.md → 已知问题 / 构建环境." >&2
    missing=1
fi

# tectonic drives biber as an external tool; it is a hard dependency there.
REAL_BIBER=""
if [ "$ENGINE_KIND" = "tectonic" ]; then
    if command -v biber >/dev/null 2>&1; then
        REAL_BIBER="$(command -v biber)"
    else
        echo "Error: tectonic needs an external 'biber' (brew install biber / apt-get install biber)." >&2
        missing=1
    fi
fi

if ! command -v rsvg-convert >/dev/null 2>&1; then
    echo "Warning: 'rsvg-convert' (librsvg) not found; SVG figures may not embed." >&2
fi
if [ "$missing" -ne 0 ]; then
    echo "Aborting: install the missing dependencies above and re-run." >&2
    exit 1
fi

echo "Using engine: $ENGINE_KIND ($ENGINE)"

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

# Shared pandoc options for both engines.
PANDOC_COMMON=(
    --from markdown+lists_without_preceding_blankline
    --lua-filter=svg2pdf.lua
    --lua-filter=crossref.lua
    --lua-filter=experiment_box.lua
    --toc
    --toc-depth=3
    --number-sections
    -V documentclass=elegantbook
    -V classoption=lang=cn
    -V classoption=cyan
    -V classoption=device=normal
    -V classoption=twoside
    -V classoption=openright
    -V author="pigo"
    --metadata title-meta="用 Go 从零构建 Pi Agent"
    --metadata author-meta="pigo"
    -H preamble.tex
    --include-before-body=cover.tex
    --highlight-style=tango
    --columns=80
)

if [ "$ENGINE_KIND" = "tectonic" ]; then
    # ── Two-step build so tectonic runs INSIDE book/ ──────────────
    # pandoc's --pdf-engine copies the .tex to a private temp dir; reference.bib
    # and images/*.pdf would then be out of reach. Instead we emit a standalone
    # .tex and compile it here, where the class's \addbibresource{reference.bib}
    # and the figure PDFs resolve.
    #
    # biber shim: ElegantBook hardcodes biblatex+biber, but tectonic's bundled
    # biblatex emits an older .bcf control-file version than a modern system biber
    # accepts. The book has no citations, so we run the real biber; only if it
    # reports a version mismatch do we rewrite the .bcf's declared version to
    # exactly what biber asked for and retry. No mismatch → transparent passthrough.
    SHIM_DIR="$SCRIPT_DIR/.build-bin"
    mkdir -p "$SHIM_DIR"
    cat > "$SHIM_DIR/biber" <<'SHIM'
#!/usr/bin/env bash
REAL="${PIGO_REAL_BIBER:-biber}"
out="$("$REAL" "$@" 2>&1)"; rc=$?
printf '%s\n' "$out"
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'control file version'; then
    want="$(printf '%s' "$out" | grep -oE 'expected version [0-9]+\.[0-9]+' | grep -oE '[0-9]+\.[0-9]+$' | head -1)"
    if [ -n "$want" ]; then
        for bcf in *.bcf; do
            [ -f "$bcf" ] || continue
            perl -i -pe 's/(controlfile version=")[0-9]+\.[0-9]+(")/${1}'"$want"'${2}/' "$bcf"
        done
        "$REAL" "$@"; exit $?
    fi
fi
exit "$rc"
SHIM
    chmod +x "$SHIM_DIR/biber"

    TEXFILE="book.tex"
    pandoc "${INPUTS[@]}" -o "$TEXFILE" --standalone "${PANDOC_COMMON[@]}"

    PIGO_REAL_BIBER="$REAL_BIBER" \
    PATH="$SHIM_DIR:$PATH" \
        "$ENGINE" --outdir "$SCRIPT_DIR" "$TEXFILE"

    # tectonic names the output after the .tex jobname.
    if [ -f "book.pdf" ]; then
        mv -f "book.pdf" "$OUT"
    fi
    rm -f "$TEXFILE"
else
    # ── Healthy xelatex: let pandoc drive it directly ─────────────
    # Prepend the engine's directory so its companion tools (kpsewhich, biber, …)
    # resolve from the same TeX installation.
    ENGINE_BIN_DIR="$(cd "$(dirname "$ENGINE")" && pwd)"
    export PATH="$ENGINE_BIN_DIR:$PATH"
    pandoc "${INPUTS[@]}" -o "$OUT" --pdf-engine="$ENGINE" "${PANDOC_COMMON[@]}"
fi

if [ -f "$OUT" ]; then
    SIZE="$(du -h "$OUT" | cut -f1)"
    echo ""
    echo "Done: $SCRIPT_DIR/$OUT ($SIZE)"
else
    echo "Error: PDF generation failed." >&2
    exit 1
fi
