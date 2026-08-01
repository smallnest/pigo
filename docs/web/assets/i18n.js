/* pigo site — i18n + theme (zero-build, no deps)
 *
 * i18n model: put both languages directly on elements as data-en / data-zh.
 *   <h1 data-en="Hello" data-zh="你好"></h1>
 *   <p data-en="A &lt;b&gt;bold&lt;/b&gt; line" data-zh="一行 &lt;b&gt;加粗&lt;/b&gt;"></p>
 * The active language's value is written via innerHTML so inline markup works.
 * Attributes can be localized too: data-en-attr="placeholder|Search" etc. (optional, unused by default)
 *
 * Persistence: localStorage keys `pigo-lang` (en|zh) and `pigo-theme` (dark|light).
 * Default language follows navigator.language (zh* -> zh, else en). Default theme: light.
 */
(function () {
  "use strict";

  var LANG_KEY = "pigo-lang";
  var THEME_KEY = "pigo-theme";

  function detectLang() {
    var saved = null;
    try { saved = localStorage.getItem(LANG_KEY); } catch (e) {}
    if (saved === "en" || saved === "zh") return saved;
    var nav = (navigator.language || navigator.userLanguage || "en").toLowerCase();
    return nav.indexOf("zh") === 0 ? "zh" : "en";
  }

  function detectTheme() {
    var saved = null;
    try { saved = localStorage.getItem(THEME_KEY); } catch (e) {}
    return saved === "dark" ? "dark" : "light";
  }

  function applyLang(lang) {
    var nodes = document.querySelectorAll("[data-en],[data-zh]");
    for (var i = 0; i < nodes.length; i++) {
      var el = nodes[i];
      var val = el.getAttribute("data-" + lang);
      if (val === null) val = el.getAttribute("data-en"); // fallback
      if (val !== null) el.innerHTML = val;
    }
    document.documentElement.setAttribute("lang", lang === "zh" ? "zh-CN" : "en");
    // reflect on toggles
    var toggles = document.querySelectorAll("[data-lang-toggle]");
    for (var j = 0; j < toggles.length; j++) {
      toggles[j].textContent = lang === "zh" ? "EN" : "中文";
      toggles[j].setAttribute("aria-label", lang === "zh" ? "Switch to English" : "切换到中文");
    }
    try { localStorage.setItem(LANG_KEY, lang); } catch (e) {}
    window.PIGO_LANG = lang;
    try { window.dispatchEvent(new CustomEvent("pigo:lang", { detail: { lang: lang } })); } catch (e) {}
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    var toggles = document.querySelectorAll("[data-theme-toggle]");
    for (var i = 0; i < toggles.length; i++) {
      toggles[i].textContent = theme === "light" ? "🌙" : "☀️";
    }
    try { localStorage.setItem(THEME_KEY, theme); } catch (e) {}
  }

  function wire() {
    document.addEventListener("click", function (ev) {
      var t = ev.target.closest ? ev.target.closest("[data-lang-toggle],[data-theme-toggle],.copy-btn") : null;
      if (!t) return;
      if (t.hasAttribute("data-lang-toggle")) {
        applyLang(window.PIGO_LANG === "zh" ? "en" : "zh");
      } else if (t.hasAttribute("data-theme-toggle")) {
        var cur = document.documentElement.getAttribute("data-theme");
        applyTheme(cur === "light" ? "dark" : "light");
      } else if (t.classList.contains("copy-btn")) {
        copyFrom(t);
      }
    });
  }

  function copyFrom(btn) {
    var wrap = btn.closest(".copy-wrap") || btn.parentElement;
    var pre = wrap ? wrap.querySelector("pre, code") : null;
    var text = btn.getAttribute("data-copy") || (pre ? pre.innerText : "");
    if (!text) return;
    var done = function () {
      var old = btn.textContent;
      btn.textContent = window.PIGO_LANG === "zh" ? "已复制" : "Copied";
      setTimeout(function () { btn.textContent = old; }, 1400);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () {});
    } else {
      try {
        var ta = document.createElement("textarea");
        ta.value = text; document.body.appendChild(ta); ta.select();
        document.execCommand("copy"); document.body.removeChild(ta); done();
      } catch (e) {}
    }
  }

  // apply theme + lang ASAP (script is loaded in <head> with defer, DOM parsed by run)
  function init() {
    applyTheme(detectTheme());
    applyLang(detectLang());
    wire();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
