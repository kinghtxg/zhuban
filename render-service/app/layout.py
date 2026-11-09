# -*- coding: utf-8 -*-
"""渲染后的版面修正：注入 run 的字体继承 + 内联图片不许撑破容器。

这两件事**只能在 docxtpl 之后做**，因为它们要的上下文在渲染前并不存在：

  - 富文本注入的 run 该用什么字体字号，取决于它落在哪个段落里。段落是
    ``{%tr for %}`` 循环渲染时才存在的，`render()` 之前根本数不出有几个。
  - 图片该摆多宽，取决于它落在表格的哪个格里；而"格"的宽度又受
    ``gridSpan``/``tcW``/``tcMar`` 影响。渲染前拿到的只是一段数据，
    不知道会被循环塞进哪一格。

所以走"渲染后按 XML 结构纠正"这条路。两个纠正都是**只修不猜**：
找不到可靠依据时原样不动，绝不因为算不出来就套一个默认值上去。
"""
from __future__ import annotations

import re
import zipfile
from io import BytesIO
from typing import Iterator, Optional

from docx.oxml.ns import qn
from lxml import etree

MARK_STYLE = "ZBInlineRich"

_EMU_PER_MM = 36000
_EMU_PER_TWIP = 635
_DEFAULT_CELL_MAR_TWIPS = 108  # OOXML 默认单元格左右边距 0.19cm

# 只处理这几类部件：正文、页眉、页脚。脚注/尾注里也能放图，但当前产品不生成，
# 顺手改会增加"改了不该改的东西"的风险面。
_PART_RE = re.compile(r"^word/(document|header\d*|footer\d*)\.xml$")

# CT_RPr 是 sequence，子元素顺序错乱会让 Word 判定文件损坏。合并属性后必须按
# 规范顺序重排，不能"插到前面就算了"。
_RPR_ORDER = [
    "rStyle", "rFonts", "b", "bCs", "i", "iCs", "caps", "smallCaps", "strike",
    "dstrike", "outline", "shadow", "emboss", "imprint", "noProof", "snapToGrid",
    "vanish", "webHidden", "color", "spacing", "w", "kern", "position", "sz",
    "szCs", "highlight", "u", "effect", "bdr", "shd", "fitText", "vertAlign",
    "rtl", "cs", "em", "lang", "eastAsianLayout", "specVanish", "oMath",
]
_RPR_RANK = {name: i for i, name in enumerate(_RPR_ORDER)}

# 从段落基准 run 往注入 run 上搬的属性。刻意**不含** rStyle：
# 基准 run 的字符样式（比如超链接样式）不该跟着搬，否则注入的正文会变成蓝色下划线。
_INHERIT_TAGS = (
    "rFonts", "b", "bCs", "i", "iCs", "u", "color", "kern",
    "spacing", "sz", "szCs", "highlight", "lang",
)


# --------------------------------------------------------------------------- #
# 通用小工具
# --------------------------------------------------------------------------- #
def _local(el) -> str:
    return etree.QName(el).localname


def _iter_parts(raw: bytes):
    """逐部件产出 (文件名, 解析后的 root 或 None)。非目标部件 yield None 表示直传。"""
    zin = zipfile.ZipFile(BytesIO(raw))
    for item in zin.infolist():
        data = zin.read(item.filename)
        root = None
        if _PART_RE.match(item.filename) and data:
            try:
                root = etree.fromstring(data)
            except etree.XMLSyntaxError:
                root = None
        yield item, data, root
    zin.close()


def _rebuild(raw: bytes, transform) -> bytes:
    """对目标部件套 transform(root)->bool，返回新 docx 字节。

    `transform` 返回 False 表示"这一份什么都没改"，此时按字节原样写回，
    保证没命中的文件一个字节都不动（zip 里其余部件同理）。
    """
    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for item, data, root in _iter_parts(raw):
            if root is not None:
                try:
                    if transform(root):
                        data = etree.tostring(
                            root, xml_declaration=True, encoding="UTF-8", standalone=True
                        )
                except Exception:  # noqa: BLE001 - 版面修正是增强项，失败也不能让生成挂掉
                    pass  # data 仍是原字节，相当于这一份不修
            zout.writestr(item, data)
    return out.getvalue()


# --------------------------------------------------------------------------- #
# 一、注入 run 的字体/字号继承
# --------------------------------------------------------------------------- #
def _run_rpr(r):
    return r.find(qn("w:rPr"))


def _is_marked(r) -> bool:
    rpr = _run_rpr(r)
    if rpr is None:
        return False
    st = rpr.find(qn("w:rStyle"))
    return st is not None and st.get(qn("w:val")) == MARK_STYLE


def _runs_of(p) -> Iterator:
    """段落直属的 run，含 hyperlink 里的（顺序与文档一致）。"""
    for child in p:
        ln = _local(child)
        if ln == "r":
            yield child
        elif ln == "hyperlink":
            for r in child.findall(qn("w:r")):
                yield r


def _base_rpr(p):
    """该段落的"基准字符格式"，也就是注入内容该继承的格式。

    取**紧挨着注入点前面的那个 run** 的 rPr —— 占位符原本就长在它里面，
    内容替换回原位，格式理所当然是它的。

    实现上要一直遍历到第一个标记 run 才停，并**不断覆盖**取到的最后一个：
    "第一个带 rPr 的 run"是错的，混排段落里那往往是段首的加粗小标题，
    拿它当基准会把注入的正文整段变成小标题的字体（黑体/加粗）。

    拿不到才退到 ``w:pPr/w:rPr``（段落标记格式）。作者整段刷字体时 Word 会
    同时写这里和每个 run 的 rPr，两者一致；只有段落标记被单独改过时才不一致。
    """
    base = None
    for r in _runs_of(p):
        if _is_marked(r):
            break
        rpr = _run_rpr(r)
        if rpr is not None:
            base = rpr
    if base is not None:
        return base
    pPr = p.find(qn("w:pPr"))
    if pPr is not None:
        rpr = pPr.find(qn("w:rPr"))
        if rpr is not None:
            return rpr
    return None


def _sort_rpr(rpr) -> None:
    """按 CT_RPr 的规范顺序重排子元素，未知标签稳定排在最后。"""
    children = list(rpr)
    big = len(_RPR_RANK)
    children.sort(key=lambda el: _RPR_RANK.get(_local(el), big))
    for child in children:
        rpr.remove(child)
    for child in children:
        rpr.append(child)


def _apply_inherit(p) -> bool:
    marked = [r for r in _runs_of(p) if _is_marked(r)]
    if not marked:
        return False
    base = _base_rpr(p)
    changed = False
    for r in marked:
        rpr = _run_rpr(r)
        if rpr is None:
            rpr = etree.Element(qn("w:rPr"))
            r.insert(0, rpr)
        have = {_local(c) for c in rpr}
        if base is not None:
            for tag in _INHERIT_TAGS:
                if tag in have:
                    continue
                src = base.find(qn("w:" + tag))
                if src is None:
                    continue
                rpr.append(_clone(src))
                have.add(tag)
        # 标记本身必须删掉：它只是个定位用的记号，落到成品里会让 Word 去找一个
        # 不存在的样式（多数情况被忽略，但这属于"故意留个坑"）。
        for st in rpr.findall(qn("w:rStyle")):
            if st.get(qn("w:val")) == MARK_STYLE:
                rpr.remove(st)
        _sort_rpr(rpr)
        if len(rpr) == 0:
            r.remove(rpr)
        changed = True
    return changed


def _clone(el):
    new = etree.Element(el.tag, attrib=dict(el.attrib), nsmap=el.nsmap)
    new.text = el.text
    for child in el:
        new.append(_clone(child))
    return new


def inherit_inline_format(raw: bytes) -> bytes:
    """把注入 run 的字体/字号从所在段落补上，并抹掉定位标记。"""

    def transform(root) -> bool:
        hit = False
        for p in root.iter(qn("w:p")):
            if _apply_inherit(p):
                hit = True
        return hit

    return _rebuild(raw, transform)


# --------------------------------------------------------------------------- #
# 二、内联图片宽度夹到容器内
# --------------------------------------------------------------------------- #
def _int_attr(el, name: str, default: Optional[int] = None) -> Optional[int]:
    if el is None:
        return default
    v = el.get(qn("w:" + name)) if name else None
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def _table_grid(tbl) -> list:
    """tblGrid 的列宽（twips）。读不到就返回空列表。"""
    grid = tbl.find(qn("w:tblGrid"))
    if grid is None:
        return []
    return [_int_attr(g, "w", 0) or 0 for g in grid.findall(qn("w:gridCol"))]


def _table_width(tbl) -> Optional[int]:
    tblPr = tbl.find(qn("w:tblPr"))
    if tblPr is not None:
        tblW = tblPr.find(qn("w:tblW"))
        if tblW is not None and (tblW.get(qn("w:type")) in (None, "dxa")):
            v = _int_attr(tblW, "w", 0)
            if v:
                return v
    grid = _table_grid(tbl)
    return sum(grid) if grid else None


def _cell_margins(tbl, tc) -> tuple:
    """单元格左右内边距（twips）：tcMar > tblCellMar > OOXML 默认 108。"""
    for holder in (tc, tbl):
        if holder is None:
            continue
        if holder is tc:
            tcPr = tc.find(qn("w:tcPr"))
            mar = tcPr.find(qn("w:tcMar")) if tcPr is not None else None
        else:
            tblPr = tbl.find(qn("w:tblPr"))
            mar = tblPr.find(qn("w:tblCellMar")) if tblPr is not None else None
        if mar is None:
            continue
        left = _int_attr(mar.find(qn("w:left")), "w")
        right = _int_attr(mar.find(qn("w:right")), "w")
        if left is not None or right is not None:
            return (
                left if left is not None else _DEFAULT_CELL_MAR_TWIPS,
                right if right is not None else _DEFAULT_CELL_MAR_TWIPS,
            )
    return _DEFAULT_CELL_MAR_TWIPS, _DEFAULT_CELL_MAR_TWIPS


def _cell_width(tc, tbl) -> Optional[int]:
    """这个单元格能用的宽度（twips）。

    两个来源都算一遍，取**较小值**：
      - ``tcPr/tcW``：单元格自报的宽度；
      - ``tblGrid`` 对应列的宽度之和（按 gridSpan 累加）。

    正常模板两者一致，取谁都一样。不一致时（比如程序生成的表格只改了
    ``gridCol`` 没改 ``tcW``，或者相反）说明这个文件自相矛盾，这时按小的算 ——
    图片缩得比需要的更小一点只是"图小了些"，缩得不够就是**表格撑破版心**，
    后者是不可用的成品，前者只是不好看。
    """
    tcPr = tc.find(qn("w:tcPr"))
    cands = []
    if tcPr is not None:
        tcW = tcPr.find(qn("w:tcW"))
        if tcW is not None:
            typ = tcW.get(qn("w:type"))
            val = _int_attr(tcW, "w", 0) or 0
            if typ in (None, "dxa") and val:
                cands.append(val)
            elif typ == "pct" and val:
                total = _table_width(tbl)
                if total:
                    # pct 是 1/50 百分比（5000 = 100%）
                    cands.append(int(total * val / 5000))

    grid = _table_grid(tbl)
    if grid:
        span = 1
        if tcPr is not None:
            span = _int_attr(tcPr.find(qn("w:gridSpan")), "val", 1) or 1
        tr = tc.getparent()
        if tr is not None:
            idx = 0
            for prev in tr.findall(qn("w:tc")):
                if prev is tc:
                    break
                pspan = 1
                ptcPr = prev.find(qn("w:tcPr"))
                if ptcPr is not None:
                    pspan = _int_attr(ptcPr.find(qn("w:gridSpan")), "val", 1) or 1
                idx += pspan
            if idx < len(grid):
                total = sum(grid[idx: idx + span])
                if total:
                    cands.append(total)

    return min(cands) if cands else None


def _para_indent(p) -> tuple:
    pPr = p.find(qn("w:pPr"))
    if pPr is None:
        return 0, 0
    ind = pPr.find(qn("w:ind"))
    if ind is None:
        return 0, 0
    left = _int_attr(ind, "left") or _int_attr(ind, "start") or 0
    right = _int_attr(ind, "right") or _int_attr(ind, "end") or 0
    return left, right


def _section_text_width(root) -> Optional[int]:
    """版心宽（twips）= 纸张宽 - 左右页边距。

    优先取 ``w:body`` 直属的 sectPr（那是"最后一节"，也是正文主体用的那节）；
    分节符段落里的 sectPr 描述的是"它之前那一节"，拿它算当前段落的可用宽度
    会在多节文档里差出一截。
    """
    body = root.find(qn("w:body"))
    candidates = list(root.iter(qn("w:sectPr")))
    if body is not None:
        own = [s for s in candidates if s.getparent() is body]
        if own:
            candidates = own
    for sectPr in candidates:
        pgSz = sectPr.find(qn("w:pgSz"))
        pgMar = sectPr.find(qn("w:pgMar"))
        if pgSz is None or pgMar is None:
            continue
        w = _int_attr(pgSz, "w")
        left = _int_attr(pgMar, "left")
        right = _int_attr(pgMar, "right")
        if w and left is not None and right is not None:
            return w - left - right
    return None


def _available_twips(p, root) -> Optional[int]:
    """这个段落当前可用的横向空间（twips）。"""
    left, right = _para_indent(p)
    tc = None
    tbl = None
    anc = p.getparent()
    while anc is not None:
        ln = _local(anc)
        if ln == "tc" and tc is None:
            tc = anc
        elif ln == "tbl" and tbl is None:
            tbl = anc
        anc = anc.getparent()
    if tc is not None:
        w = _cell_width(tc, tbl)
        if not w:
            return None
        ml, mr = _cell_margins(tbl, tc)
        return w - ml - mr - left - right
    sec = _section_text_width(root)
    if not sec:
        return None
    return sec - left - right


def _scale_drawing(drawing, limit_emu: int) -> bool:
    """把一张内联图的宽度缩到 limit_emu 以内（等比）。返回是否改过。"""
    inline = drawing.find(qn("wp:inline")) if drawing.tag == qn("w:drawing") else None
    if inline is None:
        return False  # 浮动图（wp:anchor）不动：它的位置是作者排好的
    extent = inline.find(qn("wp:extent"))
    if extent is None:
        return False
    try:
        cx = int(extent.get("cx"))
        cy = int(extent.get("cy"))
    except (TypeError, ValueError):
        return False
    if cx <= 0 or cy <= 0 or cx <= limit_emu:
        return False
    k = limit_emu / cx
    ncx = int(cx * k)
    ncy = max(1, int(cy * k))
    extent.set("cx", str(ncx))
    extent.set("cy", str(ncy))
    # 图形自身的 a:ext 必须同改，否则 Word 会按 a:ext 裁切/拉伸，
    # 表现为"图缩了但内容还是原来的大小，被裁掉一块"。
    for xfrm in drawing.iter(qn("a:xfrm")):
        ext = xfrm.find(qn("a:ext"))
        if ext is not None:
            ext.set("cx", str(ncx))
            ext.set("cy", str(ncy))
    return True


def clamp_inline_images(raw: bytes) -> bytes:
    """把超过容器宽度的内联图等比缩回容器内。

    只管"溢出"这一种情况，绝不放大：模板作者画的图本来就该是那个大小，
    放大等于替作者改版面。
    """

    def transform(root) -> bool:
        hit = False
        for p in root.iter(qn("w:p")):
            drawings = [d for d in p.iter(qn("w:drawing"))]
            if not drawings:
                continue
            avail = _available_twips(p, root)
            if not avail or avail <= 0:
                continue
            limit = avail * _EMU_PER_TWIP
            for d in drawings:
                if _scale_drawing(d, limit):
                    hit = True
        return hit

    return _rebuild(raw, transform)


def fix_rendered_layout(raw: bytes) -> bytes:
    """渲染产物统一走这里：先补字体，再夹图片。"""
    out = inherit_inline_format(raw)
    out = clamp_inline_images(out)
    return out
