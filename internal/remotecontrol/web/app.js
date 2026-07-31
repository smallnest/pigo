// pigo remote-control SPA.
//
// Connects to /ws, renders streamed session output, submits prompts, and
// answers confirmation requests. Vanilla JS, no build step — the server embeds
// this file and serves it as a static asset.
(function () {
  "use strict";

  var out = document.getElementById("output");
  var form = document.getElementById("composer");
  var prompt = document.getElementById("prompt");
  var sendBtn = document.getElementById("send");
  var dot = document.getElementById("dot");
  var statusText = document.getElementById("statusText");

  var confirmEl = document.getElementById("confirm");
  var confirmTool = document.getElementById("confirmTool");
  var confirmSummary = document.getElementById("confirmSummary");
  var confirmAlways = document.getElementById("confirmAlways");
  var btnApprove = document.getElementById("btnApprove");
  var btnReject = document.getElementById("btnReject");

  var ws = null;
  var backoff = 500; // ms, exponential up to 10s
  var pendingConfirmId = null;

  // Strip ANSI escape sequences so terminal color codes don't clutter mobile.
  var ansi = /\x1b\[[0-9;?]*[ -/]*[@-~]/g;
  function clean(s) { return s.replace(ansi, ""); }

  function atBottom() {
    return out.scrollHeight - out.scrollTop - out.clientHeight < 40;
  }
  function appendOutput(text) {
    var stick = atBottom();
    out.appendChild(document.createTextNode(clean(text)));
    // Cap the scrollback so long sessions don't exhaust memory.
    while (out.childNodes.length > 4000) {
      out.removeChild(out.firstChild);
    }
    if (stick) out.scrollTop = out.scrollHeight;
  }

  function setStatus(state, label) {
    dot.className = state === "on" ? "on" : state === "off" ? "off" : "";
    statusText.textContent = label;
    var live = state === "on";
    prompt.disabled = !live;
    sendBtn.disabled = !live;
  }

  function send(obj) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(obj));
      return true;
    }
    return false;
  }

  function showConfirm(frame) {
    pendingConfirmId = frame.confirmId;
    confirmTool.textContent = frame.tool || "tool";
    confirmSummary.textContent = clean(frame.summary || "");
    confirmAlways.checked = false;
    confirmEl.classList.add("show");
  }
  function hideConfirm() {
    confirmEl.classList.remove("show");
    pendingConfirmId = null;
  }
  function decide(approve) {
    if (pendingConfirmId == null) return;
    send({ type: "decide", confirmId: pendingConfirmId, approve: approve, always: confirmAlways.checked });
    hideConfirm();
  }
  btnApprove.addEventListener("click", function () { decide(true); });
  btnReject.addEventListener("click", function () { decide(false); });

  function handleFrame(frame) {
    switch (frame.type) {
      case "output":
        appendOutput(frame.text || "");
        break;
      case "confirm":
        showConfirm(frame);
        break;
      case "status":
        if (frame.state === "connected") {
          setStatus("on", "connected");
        } else if (frame.state === "ended") {
          setStatus("off", "session ended");
        } else if (frame.state === "disconnected") {
          setStatus("off", frame.reason || "disconnected");
        }
        break;
    }
  }

  function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(proto + "//" + location.host + "/ws");

    ws.onopen = function () {
      backoff = 500;
      setStatus("on", "connected");
    };
    ws.onmessage = function (ev) {
      var frame;
      try { frame = JSON.parse(ev.data); } catch (e) { return; }
      handleFrame(frame);
    };
    ws.onclose = function () {
      setStatus("off", "reconnecting…");
      hideConfirm();
      scheduleReconnect();
    };
    ws.onerror = function () {
      try { ws.close(); } catch (e) {}
    };
  }

  function scheduleReconnect() {
    setTimeout(function () {
      backoff = Math.min(backoff * 2, 10000);
      connect();
    }, backoff);
  }

  // Auto-grow the textarea and submit on Enter (Shift+Enter = newline).
  prompt.addEventListener("input", function () {
    prompt.style.height = "auto";
    prompt.style.height = Math.min(prompt.scrollHeight, window.innerHeight * 0.4) + "px";
  });
  prompt.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var text = prompt.value;
    if (!text.trim()) return;
    if (send({ type: "input", text: text })) {
      prompt.value = "";
      prompt.style.height = "auto";
    }
  });

  setStatus("", "connecting…");
  connect();
})();
