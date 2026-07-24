#!/usr/bin/env python3
"""Render a Claude-style light-theme graph.html dashboard from a .graph_state state file.

Usage:
    render_graph_html.py [.graph_state] [graph.html]

Defaults to reading ./.graph_state and writing ./graph.html.
Called by the /graph skill at every checkpoint (initial plan + each fan-in barrier),
so opening graph.html in a browser (it self-refreshes) tracks execution live.
No third-party dependencies — stdlib only.
"""
import html
import json
import sys
from datetime import datetime

STATUS = {
    "pending":     ("Pending",     "#8C8579", "#EFECE3"),
    "in_progress": ("In Progress", "#CC785C", "#F7E9E2"),
    "shipped":     ("Shipped",     "#3D7A5A", "#DFEEE4"),
    "failed":      ("Failed",      "#B54A3E", "#F6DEDA"),
    "blocked":     ("Blocked",     "#9A6C3A", "#F2E6D4"),
    "skipped":     ("Skipped",     "#8C8579", "#EFECE3"),
}


def esc(s):
    return html.escape(str(s if s is not None else ""))


def node_card(nid, n):
    st = n.get("status", "pending")
    label, fg, bg = STATUS.get(st, STATUS["pending"])
    deps = n.get("deps") or []
    deps_str = ", ".join(f"#{d}" for d in deps) if deps else "no deps"
    meta = []
    if n.get("pr"):
        meta.append(f'PR #{esc(n["pr"])}')
    if n.get("branch"):
        meta.append(f'<code>{esc(n["branch"])}</code>')
    if n.get("attempts"):
        meta.append(f'attempt {esc(n["attempts"])}')
    meta_html = " · ".join(meta)
    err = f'<div class="err">{esc(n["error"])}</div>' if n.get("error") else ""
    return f"""
      <div class="node" style="border-left:4px solid {fg}">
        <div class="node-top">
          <span class="nid">#{esc(nid)}</span>
          <span class="badge" style="color:{fg};background:{bg}">{label}</span>
        </div>
        <div class="title">{esc(n.get('title','(untitled)'))}</div>
        <div class="deps">{esc(deps_str)}</div>
        {f'<div class="meta">{meta_html}</div>' if meta_html else ''}
        {err}
      </div>"""


def mermaid(state):
    lines = ["graph LR"]
    nodes = state.get("nodes", {})
    for nid, n in nodes.items():
        t = n.get("title", "")
        lines.append(f'  n{nid}["#{nid} {t}"]')
    for nid, n in nodes.items():
        for d in (n.get("deps") or []):
            lines.append(f"  n{d} --> n{nid}")
    # color by status
    for st, (_, fg, bg) in STATUS.items():
        ids = [f"n{nid}" for nid, n in nodes.items() if n.get("status") == st]
        if ids:
            lines.append(f"  classDef {st} fill:{bg},stroke:{fg},color:#33312B;")
            lines.append(f"  class {','.join(ids)} {st};")
    return "\n".join(lines)


def render(state):
    nodes = state.get("nodes", {})
    total = len(nodes)
    counts = {k: 0 for k in STATUS}
    for n in nodes.values():
        counts[n.get("status", "pending")] = counts.get(n.get("status", "pending"), 0) + 1
    shipped = counts.get("shipped", 0)
    pct = int(shipped / total * 100) if total else 0
    waves = state.get("waves", [])
    cur = state.get("current_wave", 0)

    legend = "".join(
        f'<span class="lg"><i style="background:{bg};border-color:{fg}"></i>{label}</span>'
        for label, fg, bg in STATUS.values()
    )

    wave_html = ""
    for wi, wave in enumerate(waves):
        state_cls = "cur" if wi == cur else ("done" if wi < cur else "future")
        cards = "".join(node_card(str(nid), nodes.get(str(nid), {"title": f"#{nid}"})) for nid in wave)
        wave_html += f"""
      <section class="wave {state_cls}">
        <h2>Wave {wi} <span class="wcount">×{len(wave)} parallel</span>
          {'<span class="pill">running</span>' if wi == cur else ''}</h2>
        <div class="nodes">{cards}</div>
      </section>"""

    stat = lambda k: f'<b style="color:{STATUS[k][1]}">{counts.get(k,0)}</b> {STATUS[k][0].lower()}'
    stats = " · ".join(stat(k) for k in ["shipped", "in_progress", "failed", "blocked", "skipped", "pending"])

    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>graph · {esc(state.get('task','execution'))}</title>
<script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>
<style>
  :root {{ --paper:#F5F4EE; --card:#FFFFFF; --ink:#33312B; --muted:#8C8579;
           --coral:#CC785C; --line:#E7E3D9; }}
  * {{ box-sizing:border-box; }}
  body {{ margin:0; background:var(--paper); color:var(--ink);
    font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif; }}
  .wrap {{ max-width:1080px; margin:0 auto; padding:32px 24px 64px; }}
  header {{ border-bottom:1px solid var(--line); padding-bottom:20px; margin-bottom:24px; }}
  h1 {{ font-size:22px; margin:0 0 4px; font-weight:650; }}
  .sub {{ color:var(--muted); font-size:13px; }}
  .bar {{ height:8px; background:var(--line); border-radius:99px; margin:16px 0 8px; overflow:hidden; }}
  .bar>i {{ display:block; height:100%; width:{pct}%; background:var(--coral); border-radius:99px; }}
  .stats {{ font-size:13px; color:var(--muted); }}
  .legend {{ display:flex; gap:14px; flex-wrap:wrap; margin:14px 0 4px; font-size:12px; color:var(--muted); }}
  .lg {{ display:inline-flex; align-items:center; gap:6px; }}
  .lg i {{ width:12px; height:12px; border-radius:3px; border:1px solid; display:inline-block; }}
  .diagram {{ background:var(--card); border:1px solid var(--line); border-radius:14px; padding:18px; margin:20px 0; overflow:auto; }}
  .wave {{ margin:22px 0; }}
  .wave h2 {{ font-size:15px; margin:0 0 12px; display:flex; align-items:center; gap:10px; }}
  .wcount {{ font-weight:400; color:var(--muted); font-size:12px; }}
  .pill {{ font-size:11px; color:#fff; background:var(--coral); padding:2px 9px; border-radius:99px; }}
  .wave.future {{ opacity:.55; }}
  .nodes {{ display:grid; grid-template-columns:repeat(auto-fill,minmax(240px,1fr)); gap:12px; }}
  .node {{ background:var(--card); border:1px solid var(--line); border-radius:12px; padding:12px 14px; }}
  .node-top {{ display:flex; justify-content:space-between; align-items:center; margin-bottom:6px; }}
  .nid {{ font-weight:650; color:var(--muted); font-size:13px; }}
  .badge {{ font-size:11px; padding:2px 8px; border-radius:99px; font-weight:600; }}
  .title {{ font-weight:550; margin-bottom:6px; }}
  .deps {{ font-size:12px; color:var(--muted); }}
  .meta {{ font-size:12px; color:var(--muted); margin-top:6px; }}
  .meta code, .node code {{ background:var(--paper); padding:1px 5px; border-radius:5px; font-size:11px; }}
  .err {{ font-size:12px; color:#B54A3E; margin-top:6px; white-space:pre-wrap; }}
  footer {{ margin-top:32px; color:var(--muted); font-size:12px; text-align:center; }}
</style>
</head>
<body>
  <div class="wrap">
    <header>
      <h1>{esc(state.get('task','Task Graph Execution'))}</h1>
      <div class="sub">{esc(state.get('repo',''))} · wave {cur} of {max(len(waves)-1,0)} · updated {esc(state.get('updated_at',''))}</div>
      <div class="bar"><i></i></div>
      <div class="stats">{shipped}/{total} shipped ({pct}%) &nbsp;—&nbsp; {stats}</div>
      <div class="legend">{legend}</div>
    </header>
    <div class="diagram"><pre class="mermaid">{esc(mermaid(state))}</pre></div>
    {wave_html}
    <footer>Auto-refreshes every 5s · generated by /graph from <code>.graph_state</code></footer>
  </div>
  <script>mermaid.initialize({{ startOnLoad:true, theme:"neutral" }});</script>
</body>
</html>"""


def main():
    src = sys.argv[1] if len(sys.argv) > 1 else ".graph_state"
    dst = sys.argv[2] if len(sys.argv) > 2 else "graph.html"
    with open(src, encoding="utf-8") as f:
        state = json.load(f)
    state.setdefault("updated_at", datetime.now().isoformat(timespec="seconds"))
    with open(dst, "w", encoding="utf-8") as f:
        f.write(render(state))
    print(f"wrote {dst} from {src}")


if __name__ == "__main__":
    main()
