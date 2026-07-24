#!/usr/bin/env python3
"""审查 insight-diagram 生成的 SVG 图，做几何校验。

检查项（对应三条要求）：
  1. 箭头两端是否落在框图边缘线上 —— 既不"深入"框内，也不"空接"悬空。
  2. 非嵌套框图之间是否重叠。
  3. 框图之间是否留出足够间距。

用法:
  python3 review_svg.py docs/architecture.html [more.html ...]
  python3 review_svg.py docs/*.html --min-gap 8 --json

退出码: 发现 ERROR 时为 1；仅 WARNING（或干净）为 0；加 --strict 让 WARNING 也返回 1。

实现说明：用 html.parser（而非 XML 解析器）遍历，以兼容 SVG-in-HTML 中
未转义的 & 或文本里的 <<include>> 等；箭头端点允许落在任意图形边缘、
任意连线/生命线上（汇合点），故序列图/通信图等不会误报"空接"。
"""
import argparse
import json
import math
import re
import sys
from html.parser import HTMLParser

# ---- 几何容差（像素）----
BOUNDARY_TOL = 6.0    # 端点距图形边 <= 此值视为"落在边上"，正常
PENETRATION = 8.0     # 端点在框内且距最近边 > 此值视为"深入"（ERROR）
FLOATING = 8.0        # 端点距所有图形 > 此值且不接任何连线 → "空接"（WARNING）
JUNCTION_TOL = 6.0    # 端点距其他连线/生命线 <= 此值视为合法汇合点
OVERLAP_EPS = 2.0     # 两方向交叠都 > 此值才算重叠
CONTAIN_MARGIN = 2.0  # 判定包含时允许内框略微超出
SAME_BOX_TOL = 3.0    # 各边相差 <= 此值视为同一个框（去重）

# 只有 w/h 同时达到阈值的图形才算"框图节点"，参与重叠/间距校验；
# 更小的（标签底衬、终止圆点、图例色块）仅作为箭头落点目标。
MIN_BOX_W = 36.0
MIN_BOX_H = 22.0

# 这些子树内的图形只是装饰/定义，不收集
SKIP_SUBTREES = {'defs', 'marker', 'pattern', 'clippath', 'lineargradient',
                 'radialgradient', 'symbol', 'mask'}


# =====================================================================
# 基础工具
# =====================================================================
def _floats(s):
    return [float(x) for x in re.findall(r'-?\d+(?:\.\d+)?', s or '')]


def _num(v):
    """解析 SVG 数值属性；含 '%' 或无法解析时返回 None。"""
    if v is None or '%' in v:
        return None
    m = re.match(r'\s*(-?\d+(?:\.\d+)?)', v)
    return float(m.group(1)) if m else None


def _parse_translate(transform):
    """累加 transform 中的 translate 偏移，返回 (dx, dy)。"""
    dx = dy = 0.0
    for m in re.finditer(r'translate\(\s*([-\d.]+)[\s,]*([-\d.]+)?\s*\)', transform or ''):
        dx += float(m.group(1))
        dy += float(m.group(2)) if m.group(2) is not None else 0.0
    return dx, dy


def _point_seg_dist(px, py, ax, ay, bx, by):
    """点到线段的最短距离。"""
    dx, dy = bx - ax, by - ay
    if dx == 0 and dy == 0:
        return math.hypot(px - ax, py - ay)
    t = ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy)
    t = max(0.0, min(1.0, t))
    return math.hypot(px - (ax + t * dx), py - (ay + t * dy))


def parse_path_points(d):
    """提取 path 各命令的落点（曲线只取终点），用于求首/末锚点。"""
    pts = []
    cx = cy = 0.0
    cmd, nums = None, []
    tokens = re.findall(r'([MmLlHhVvCcSsQqTtAaZz])|(-?\d+(?:\.\d+)?)', d or '')

    def flush():
        nonlocal cx, cy
        if cmd in ('M', 'L', 'T'):
            for i in range(0, len(nums) - 1, 2):
                cx, cy = nums[i], nums[i + 1]; pts.append((cx, cy))
        elif cmd in ('m', 'l', 't'):
            for i in range(0, len(nums) - 1, 2):
                cx, cy = cx + nums[i], cy + nums[i + 1]; pts.append((cx, cy))
        elif cmd == 'H':
            for v in nums: cx = v; pts.append((cx, cy))
        elif cmd == 'h':
            for v in nums: cx += v; pts.append((cx, cy))
        elif cmd == 'V':
            for v in nums: cy = v; pts.append((cx, cy))
        elif cmd == 'v':
            for v in nums: cy += v; pts.append((cx, cy))
        elif cmd in ('C', 'S', 'Q') and len(nums) >= 2:
            cx, cy = nums[-2], nums[-1]; pts.append((cx, cy))
        elif cmd in ('c', 's', 'q') and len(nums) >= 2:
            cx, cy = cx + nums[-2], cy + nums[-1]; pts.append((cx, cy))
    for tok_cmd, tok_num in tokens:
        if tok_cmd:
            if cmd is not None:
                flush()
            cmd, nums = tok_cmd, []
        else:
            nums.append(float(tok_num))
    if cmd is not None:
        flush()
    return pts


# =====================================================================
# 图形对象
# =====================================================================
class Shape:
    """任意几何图形的统一表示，用 bbox + 类型描述。"""
    __slots__ = ('kind', 'x0', 'y0', 'x1', 'y1', 'cx', 'cy', 'rx', 'ry')

    def __init__(self, kind, x0, y0, x1, y1):
        self.kind = kind
        self.x0, self.y0, self.x1, self.y1 = x0, y0, x1, y1
        self.cx, self.cy = (x0 + x1) / 2, (y0 + y1) / 2
        self.rx, self.ry = (x1 - x0) / 2, (y1 - y0) / 2

    @property
    def w(self): return self.x1 - self.x0
    @property
    def h(self): return self.y1 - self.y0

    def is_box(self):
        return self.w >= MIN_BOX_W and self.h >= MIN_BOX_H

    def signed_dist(self, px, py):
        """点到边界的有符号距离：内部为负、外部为正、≈0 在边上。"""
        if self.kind == 'ellipse' and self.rx > 0 and self.ry > 0:
            nx, ny = (px - self.cx) / self.rx, (py - self.cy) / self.ry
            return (math.hypot(nx, ny) - 1.0) * ((self.rx + self.ry) / 2.0)
        if self.kind == 'circle' and self.rx > 0:
            return math.hypot(px - self.cx, py - self.cy) - self.rx
        dx = max(self.x0 - px, 0, px - self.x1)
        dy = max(self.y0 - py, 0, py - self.y1)
        if dx > 0 or dy > 0:
            return math.hypot(dx, dy)
        return -min(px - self.x0, self.x1 - px, py - self.y0, self.y1 - py)

    def bbox_key(self):
        return (round(self.x0, 1), round(self.y0, 1),
                round(self.x1, 1), round(self.y1, 1))


def _contains(a, b):
    """框 a 是否（在容差内）包含框 b 且二者不等大。"""
    return (b.x0 >= a.x0 - CONTAIN_MARGIN and b.x1 <= a.x1 + CONTAIN_MARGIN and
            b.y0 >= a.y0 - CONTAIN_MARGIN and b.y1 <= a.y1 + CONTAIN_MARGIN and
            not (abs(a.x0 - b.x0) < SAME_BOX_TOL and abs(a.x1 - b.x1) < SAME_BOX_TOL and
                 abs(a.y0 - b.y0) < SAME_BOX_TOL and abs(a.y1 - b.y1) < SAME_BOX_TOL))


# =====================================================================
# 用 HTMLParser 遍历 SVG（容忍未转义字符）
# =====================================================================
class SvgCollector(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack = [{'ox': 0.0, 'oy': 0.0, 'skip': False}]
        self.shapes = []        # 所有图形（含小图形），用于箭头落点目标
        self.connectors = []    # 带 marker 的 line/path
        self.segments = []      # 所有 line/path 折线段，用于汇合点判定

    # void/自闭合元素
    def handle_startendtag(self, tag, attrs):
        self._emit(tag.lower(), dict(attrs))

    def handle_starttag(self, tag, attrs):
        tag = tag.lower()
        a = dict(attrs)
        parent = self.stack[-1]
        dx, dy = _parse_translate(a.get('transform', ''))
        node = {'ox': parent['ox'] + dx, 'oy': parent['oy'] + dy,
                'skip': parent['skip'] or tag in SKIP_SUBTREES}
        self.stack.append(node)
        self._emit(tag, a, ctx=node)

    def handle_endtag(self, tag):
        if len(self.stack) > 1:
            self.stack.pop()

    def _emit(self, tag, a, ctx=None):
        ctx = ctx or self.stack[-1]
        if ctx['skip'] or tag in SKIP_SUBTREES:
            return
        ox, oy = ctx['ox'], ctx['oy']
        dx, dy = _parse_translate(a.get('transform', ''))  # 自闭合元素自身的 translate
        if tag in ('rect', 'ellipse', 'circle', 'polygon'):
            ox, oy = ox + dx, oy + dy

        if tag == 'rect':
            x, y = _num(a.get('x', '0')), _num(a.get('y', '0'))
            w, h = _num(a.get('width')), _num(a.get('height'))
            if None not in (x, y, w, h):
                self.shapes.append(Shape('rect', ox + x, oy + y, ox + x + w, oy + y + h))
        elif tag == 'ellipse':
            cx, cy = _num(a.get('cx', '0')), _num(a.get('cy', '0'))
            rx, ry = _num(a.get('rx')), _num(a.get('ry'))
            if None not in (cx, cy, rx, ry):
                self.shapes.append(Shape('ellipse', ox + cx - rx, oy + cy - ry,
                                         ox + cx + rx, oy + cy + ry))
        elif tag == 'circle':
            cx, cy = _num(a.get('cx', '0')), _num(a.get('cy', '0'))
            r = _num(a.get('r'))
            if None not in (cx, cy, r):
                self.shapes.append(Shape('circle', ox + cx - r, oy + cy - r,
                                         ox + cx + r, oy + cy + r))
        elif tag == 'polygon':
            nums = _floats(a.get('points', ''))
            pts = list(zip(nums[0::2], nums[1::2]))
            if len(pts) >= 3:
                xs = [ox + p[0] for p in pts]; ys = [oy + p[1] for p in pts]
                self.shapes.append(Shape('polygon', min(xs), min(ys), max(xs), max(ys)))
        elif tag == 'line':
            x1, y1 = _num(a.get('x1', '0')), _num(a.get('y1', '0'))
            x2, y2 = _num(a.get('x2', '0')), _num(a.get('y2', '0'))
            if None not in (x1, y1, x2, y2):
                seg = [(ox + x1, oy + y1), (ox + x2, oy + y2)]
                self.segments.append(seg)
                if a.get('marker-end') or a.get('marker-start'):
                    self.connectors.append({
                        'a': seg[0], 'b': seg[-1], 'seg': seg,
                        'arrow_a': bool(a.get('marker-start')),
                        'arrow_b': bool(a.get('marker-end'))})
        elif tag == 'path':
            pts = [(ox + px, oy + py) for px, py in parse_path_points(a.get('d', ''))]
            if len(pts) >= 2:
                self.segments.append(pts)
                if a.get('marker-end') or a.get('marker-start'):
                    self.connectors.append({
                        'a': pts[0], 'b': pts[-1], 'seg': pts,
                        'arrow_a': bool(a.get('marker-start')),
                        'arrow_b': bool(a.get('marker-end'))})


def collect(svg_text):
    """返回 (boxes, all_shapes, connectors, segments)。"""
    p = SvgCollector()
    p.feed(svg_text)
    # 框去重（描边 + 遮罩底衬常画两层完全重合的 rect）
    seen, boxes = set(), []
    for s in p.shapes:
        if s.is_box():
            k = s.bbox_key()
            if k not in seen:
                seen.add(k)
                boxes.append(s)
    return boxes, p.shapes, p.connectors, p.segments


def extract_svg(text):
    m = re.search(r'<svg\b.*?</svg>', text, re.DOTALL | re.IGNORECASE)
    return m.group(0) if m else None


# =====================================================================
# 三项检查
# =====================================================================
def check_arrow_endpoints(shapes, connectors, segments):
    """检查 1：箭头端点应恰好落在某图形边缘，或合法汇入另一连线。

    会先剔除"装饰性"连线：两端都既不贴任何图形边、也不汇入其它线段
    （典型如图例 Legend 里的示例箭头 / 独立标注线），不参与判定。
    """
    issues = []

    def status(px, py):
        dists = [s.signed_dist(px, py) for s in shapes]
        on_edge = bool(dists) and any(abs(d) <= BOUNDARY_TOL for d in dists)
        deepest = min(dists) if dists else 0.0
        nearest_out = min((d for d in dists if d >= 0), default=None)
        return on_edge, deepest, nearest_out

    for i, c in enumerate(connectors):
        sa = status(*c['a'])
        sb = status(*c['b'])
        a_anchored = sa[0] or _near_other_segment(*c['a'], segments, c['seg'])
        b_anchored = sb[0] or _near_other_segment(*c['b'], segments, c['seg'])
        # 两端都不锚定 → 视为图例/装饰线，跳过
        if not a_anchored and not b_anchored:
            continue

        ends = []
        if c['arrow_b']:
            ends.append(('终点', c['b'], sb))
        if c['arrow_a']:
            ends.append(('起点', c['a'], sa))
        for label, (px, py), (on_edge, deepest, nearest_out) in ends:
            if on_edge:
                continue                                  # 落在某图形边上：正常
            if deepest < -PENETRATION:
                issues.append(('ERROR',
                    f'连线#{i+1} {label}({px:.0f},{py:.0f}) 深入框内 '
                    f'{-deepest:.0f}px，应止于框边缘'))
                continue
            if nearest_out is not None and nearest_out > FLOATING:
                if _near_other_segment(px, py, segments, c['seg']):
                    continue                              # 汇入另一连线/生命线
                issues.append(('WARNING',
                    f'连线#{i+1} {label}({px:.0f},{py:.0f}) 悬空，'
                    f'距最近框边 {nearest_out:.0f}px（空接）'))
    return issues


def _near_other_segment(px, py, segments, own):
    for seg in segments:
        if seg is own:
            continue
        for k in range(len(seg) - 1):
            if _point_seg_dist(px, py, *seg[k], *seg[k + 1]) <= JUNCTION_TOL:
                return True
    return False


def check_overlap(boxes):
    """检查 2：非嵌套框之间不得重叠。"""
    issues = []
    for i in range(len(boxes)):
        for j in range(i + 1, len(boxes)):
            a, b = boxes[i], boxes[j]
            if _contains(a, b) or _contains(b, a):
                continue
            ox = min(a.x1, b.x1) - max(a.x0, b.x0)
            oy = min(a.y1, b.y1) - max(a.y0, b.y0)
            if ox > OVERLAP_EPS and oy > OVERLAP_EPS:
                issues.append(('ERROR',
                    f'框[{a.x0:.0f},{a.y0:.0f} {a.w:.0f}x{a.h:.0f}] 与 '
                    f'[{b.x0:.0f},{b.y0:.0f} {b.w:.0f}x{b.h:.0f}] '
                    f'重叠 {ox:.0f}x{oy:.0f}px'))
    return issues


def check_spacing(boxes, min_gap):
    """检查 3：投影相邻、不嵌套、不重叠的框，净间距需 >= min_gap。"""
    issues = []
    for i in range(len(boxes)):
        for j in range(i + 1, len(boxes)):
            a, b = boxes[i], boxes[j]
            if _contains(a, b) or _contains(b, a):
                continue
            xo = min(a.x1, b.x1) - max(a.x0, b.x0)
            yo = min(a.y1, b.y1) - max(a.y0, b.y0)
            if xo > OVERLAP_EPS and yo > OVERLAP_EPS:
                continue                                  # 重叠交给 check_overlap
            gap, axis = None, ''
            if xo > OVERLAP_EPS:
                gap, axis = max(a.y0, b.y0) - min(a.y1, b.y1), '垂直'
            elif yo > OVERLAP_EPS:
                gap, axis = max(a.x0, b.x0) - min(a.x1, b.x1), '水平'
            if gap is not None and 0 <= gap < min_gap:
                issues.append(('WARNING',
                    f'框[{a.x0:.0f},{a.y0:.0f}] 与 [{b.x0:.0f},{b.y0:.0f}] '
                    f'{axis}间距仅 {gap:.0f}px (< {min_gap:.0f}px)'))
    return issues


def review_file(path, min_gap):
    try:
        with open(path, encoding='utf-8') as f:
            text = f.read()
    except OSError as e:
        return {'file': path, 'issues': [('ERROR', f'无法读取: {e}')],
                'boxes': 0, 'connectors': 0}
    svg = extract_svg(text)
    if not svg:
        return {'file': path, 'issues': [('ERROR', '未找到 <svg> 块')],
                'boxes': 0, 'connectors': 0}
    boxes, shapes, connectors, segments = collect(svg)
    issues = (check_arrow_endpoints(shapes, connectors, segments)
              + check_overlap(boxes)
              + check_spacing(boxes, min_gap))
    return {'file': path, 'issues': issues,
            'boxes': len(boxes), 'connectors': len(connectors)}


# =====================================================================
# CLI
# =====================================================================
def main(argv=None):
    ap = argparse.ArgumentParser(
        description='审查 insight-diagram 生成的 SVG（箭头落点 / 框重叠 / 框间距）')
    ap.add_argument('files', nargs='+', help='待检查的 HTML/SVG 文件')
    ap.add_argument('--min-gap', type=float, default=8.0,
                    help='相邻框最小净间距阈值 px，默认 8')
    ap.add_argument('--json', action='store_true', help='以 JSON 输出')
    ap.add_argument('--strict', action='store_true',
                    help='存在 WARNING 时也以非零码退出')
    args = ap.parse_args(argv)

    results = [review_file(p, args.min_gap) for p in args.files]

    if args.json:
        print(json.dumps([
            {'file': r['file'], 'boxes': r['boxes'], 'connectors': r['connectors'],
             'issues': [{'level': lv, 'message': m} for lv, m in r['issues']]}
            for r in results], ensure_ascii=False, indent=2))
    else:
        for r in results:
            errs = [m for lv, m in r['issues'] if lv == 'ERROR']
            warns = [m for lv, m in r['issues'] if lv == 'WARNING']
            mark = '✗' if errs else ('⚠' if warns else '✓')
            print(f'\n{mark} {r["file"]}  '
                  f'({r["boxes"]} 框 / {r["connectors"]} 箭头连线)')
            for m in errs:
                print(f'    ERROR   {m}')
            for m in warns:
                print(f'    WARNING {m}')
            if not errs and not warns:
                print('    通过：箭头落点、框重叠、框间距均无异常')

    total_err = sum(1 for r in results for lv, _ in r['issues'] if lv == 'ERROR')
    total_warn = sum(1 for r in results for lv, _ in r['issues'] if lv == 'WARNING')
    if not args.json:
        print(f'\n汇总：{total_err} 个 ERROR，{total_warn} 个 WARNING，'
              f'共 {len(results)} 个文件')
    return 1 if (total_err or (args.strict and total_warn)) else 0


if __name__ == '__main__':
    sys.exit(main())
