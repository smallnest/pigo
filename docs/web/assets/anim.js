/* pigo site — intro animations (anime.js, zero-build, offline)
 *
 * Two effects, both graceful when JS/anime is unavailable:
 *   1. Hero intro   — a one-time fade + rise stagger over the hero elements.
 *   2. Typewriter   — each .feature-text h2 reveals character-by-character
 *                     when it scrolls into view (IntersectionObserver).
 *
 * i18n.js rewrites innerHTML on every language toggle, which wipes the
 * per-char spans we inject. So we listen for the `pigo:lang` custom event
 * it dispatches and re-run the typewriter for any h2 already on screen.
 *
 * prefers-reduced-motion is honored: we leave text/opacity fully visible
 * and skip all animation.
 */
(function () {
  "use strict";

  var reduce = window.matchMedia &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function run() {
    if (typeof anime === "undefined") return; // vendored lib missing → no-op
    heroIntro();
    setupHeroTitle();
    setupTypewriter();
  }

  function heroIntro() {
    var targets = [
      ".hero-badge", ".hero p.lead",
      ".hero-cta", ".hero-cmd", ".hero-shot"
    ].map(function (s) { return document.querySelector(s); })
     .filter(Boolean);
    if (!targets.length) return;

    if (reduce) return; // elements are visible by default; nothing to do

    anime.set(targets, { opacity: 0, translateY: 14 });
    anime({
      targets: targets,
      opacity: [0, 1],
      translateY: [14, 0],
      duration: 620,
      delay: anime.stagger(90, { start: 120 }),
      easing: "easeOutQuad"
    });
  }

  // Markup-aware typewriter: preserves inline elements (e.g. <span class="grad">)
  // by walking child nodes and splitting only text into per-char spans.
  function typewriterRich(el) {
    if (reduce) return;
    var original = el.innerHTML;
    var spans = [];

    function build(src, dst) {
      var kids = src.childNodes;
      for (var i = 0; i < kids.length; i++) {
        var n = kids[i];
        if (n.nodeType === 3) { // text
          var text = n.nodeValue;
          for (var j = 0; j < text.length; j++) {
            var ch = text.charAt(j);
            var s = document.createElement("span");
            s.textContent = ch === " " ? " " : ch;
            // Plain inline (not inline-block) + opacity-only: keeps the line
            // layout byte-identical to the restored markup, so the innerHTML
            // swap in `complete` produces no reflow / end-of-animation jump.
            s.style.opacity = "0";
            s.style.willChange = "opacity";
            dst.appendChild(s);
            spans.push(s);
          }
        } else if (n.nodeType === 1) { // element — clone shallow, recurse
          var clone = n.cloneNode(false);
          dst.appendChild(clone);
          build(n, clone);
        }
      }
    }

    var frag = document.createDocumentFragment();
    build(el, frag);
    el.innerHTML = "";
    el.appendChild(frag);

    anime({
      targets: spans,
      opacity: [0, 1],
      duration: 420,
      delay: anime.stagger(38),
      easing: "linear",
      complete: function () {
        el.innerHTML = original; // restore clean markup for later lang toggles
        el.style.opacity = "";
      }
    });
  }

  function setupHeroTitle() {
    var h1 = document.querySelector(".hero h1");
    if (!h1) return;
    if (reduce) return; // visible by default
    typewriterRich(h1);
    window.addEventListener("pigo:lang", function () {
      if (reduce) return;
      typewriterRich(h1);
    });
  }

  // Split an element's plain text into per-char inline-block spans (opacity 0),
  // then stagger them in. Only safe on plain-text nodes (no inline markup).
  function typewriter(el) {
    if (reduce) return;
    if (el.dataset.twDone === "1" && !el.dataset.twReset) return;
    var text = el.textContent;
    if (!text) return;

    el.textContent = "";
    var frag = document.createDocumentFragment();
    var spans = [];
    for (var i = 0; i < text.length; i++) {
      var ch = text.charAt(i);
      var s = document.createElement("span");
      s.textContent = ch === " " ? " " : ch;
      s.style.display = "inline-block";
      s.style.opacity = "0";
      s.style.willChange = "opacity, transform";
      frag.appendChild(s);
      spans.push(s);
    }
    el.appendChild(frag);
    el.dataset.twDone = "1";
    el.dataset.twReset = "";

    anime({
      targets: spans,
      opacity: [0, 1],
      translateY: [6, 0],
      duration: 340,
      delay: anime.stagger(24),
      easing: "easeOutQuad",
      complete: function () {
        // flatten back to plain text so a later language toggle re-reads cleanly
        el.textContent = text;
        el.style.opacity = "";
      }
    });
  }

  function headings() {
    return Array.prototype.slice.call(
      document.querySelectorAll(".feature-text h2")
    );
  }

  function setupTypewriter() {
    var hs = headings();
    if (!hs.length) return;

    if (reduce || !("IntersectionObserver" in window)) return;

    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) {
          typewriter(e.target);
          io.unobserve(e.target);
        }
      });
    }, { threshold: 0.5 });

    hs.forEach(function (h) { io.observe(h); });

    // On language switch, i18n rewrites innerHTML (plain text restored).
    // Re-run the typewriter for headings currently in the viewport.
    window.addEventListener("pigo:lang", function () {
      if (reduce) return;
      headings().forEach(function (h) {
        var r = h.getBoundingClientRect();
        var visible = r.top < window.innerHeight && r.bottom > 0;
        if (visible) {
          h.dataset.twReset = "1";
          typewriter(h);
        }
      });
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", run);
  } else {
    run();
  }
})();
