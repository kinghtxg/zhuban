# -*- coding: utf-8 -*-
"""docx -> 块级结构化文档模型（PRD V2 §7.1，/parse 核心）。

设计要点（与 app/docapply.py 成对出现，id 规则两侧完全一致）：

块 id 规则（同一份 docx 多次 parse 必须稳定）：
  - 按 document.xml body 的直属子节点顺序遍历；
  - 直属段落 <w:p>      -> p0, p1, p2 ...（按 body 直属段落计数）
  - 直属表格 <w:tbl>    -> t0, t1 ...；其行 -> t0r0, t0r1 ...；
    行内单元格 -> t0r0c0, t0r0c1 ...；单元格内段落 -> t0r0c0p0 ...
  - 前端新增块 id 一律带 ``new-`` 前缀（如 new-p1），由 apply 按相邻锚点插入。

块类型（严格对齐 PRD §7.1）：
  heading    {id,type:"heading",level,text,tokens}
  paragraph  {id,type:"paragraph",text,tokens}
  list       {id,type:"list",ordered,items:[{text,tokens}]}
  table      {id,type:"table",rows:[{id,cells:[{blocks:[...]}],loop_row,loop?}]}
  placeholder{id,type:"placeholder",kind,name,width_mm?,height_mm?}
             （独立成段、整段就是一个 {{image_/qr_/rich_/html_/md_}} 标签）
  unsupported{id,type:"unsupported",note}（文本框/形状/复杂域，XML 原样保留）

本模块只读 document.xml + styles.xml，不修改任何部件；warnings 汇总不支持元素数量。
"""
from __future__ import annotations

import re
import zipfile
from io import BytesIO
from typing import Any, Dict, List, Optional, Tuple

from lxml import etree

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
WP = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
A = "http://schemas.openxmlformats.org/drawingml/2006/main"
R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
XML_SPACE = "{http://www.w3.org/XML/1998/namespace}space"


def qn(tag: str) -> str:
    return f"{{{W}}}{tag}"


# 与 scanner.py 保持同一套标签口径（{{ }} {% %} {# #}，含 docxtpl 的 p/tr 前缀）
TOKEN_RE = re.compile(r"\{\{.*?\}\}|\{%.*?%\}|\{#.*?#\}", re.DOTALL)

# 整段独立占位符：整段文本剥去空白后恰为一个 {{name}}
STANDALONE_TAG_RE = re.compile(r"^\s*\{\{\s*([A-Za-z_][A-Za-z0-9_.一-鿿]*)\s*\}\}\s*$")

# {%tr for var in iterable %} / {%tr endfor %} 整行标记
TR_FOR_RE = re.compile(r"^\s*\{%tr\s+for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(.+?)\s*%\}\s*$")
TR_END_RE = re.compile(r"^\s*\{%tr\s+endfor\s*%\}\s*$")


def _kind_by_name(name: str, default: str = "scalar") -> str:
    """占位符前缀 → 编辑器块类型。

    必须与 scanner._kind_by_name 保持同一口径（富文本返回 "rich" 是对
    scanner 的 "richtext" 的编辑器侧别名，两者指同一类）。前缀规则一旦
    分叉，会出现「扫描说有、编辑器不认」的情况 —— sig_/seal_ 就曾因此
    在编辑器里退化成普通文本。防回归见 tests/test_kind_consistency.py。
    """
    if name.startswith("qr_"):
        return "qrcode"
    if name.startswith("sig_") or name.startswith("seal_"):
        # 服务端 sigauth 注入的签名/印章占位，编辑器需按图片块呈现
        return "image"
    if name.startswith("image_") or name.startswith("img_"):
        return "image"
    if name.startswith("rich_") or name.startswith("html_") or name.startswith("md_"):
        return "rich"
    return default


def paragraph_text(p) -> str:
    """段落可见文本（所有 w:t 拼接，保持顺序）。"""
    return "".join(t.text or "" for t in p.iter(qn("t")))


def extract_tokens(text: str) -> List[Dict[str, Any]]:
    """在段落原文上标注 jinja 标签区间（只读，供前端高亮）。

    返回 [{start,end,kind,text}]，kind in var({{ }}) / block({% %}) / comment({# #})。
    """
    out: List[Dict[str, Any]] = []
    for m in TOKEN_RE.finditer(text):
        tok = m.group(0)
        if tok.startswith("{{"):
            kind = "var"
        elif tok.startswith("{%"):
            kind = "block"
        else:
            kind = "comment"
        out.append({"start": m.start(), "end": m.end(), "kind": kind, "text": tok})
    return out


# --------------------------------------------------------------------------- #
# 样式解析：把 styles.xml 的 styleId 解析成 outlineLvl（heading 层级）
# --------------------------------------------------------------------------- #
def _build_style_outline(root) -> Dict[str, Optional[int]]:
    """styleId -> outlineLvl（0..8），沿 basedOn 链解析；无 outline 则 None。"""
    by_id: Dict[str, dict] = {}
    for st in root.findall(qn("style")):
        sid = st.get(qn("styleId"))
        if not sid:
            continue
        outline = None
        pPr = st.find(qn("pPr"))
        if pPr is not None:
            ol = pPr.find(qn("outlineLvl"))
            if ol is not None:
                v = ol.get(qn("val"))
                try:
                    outline = int(v)
                except (TypeError, ValueError):
                    outline = None
        based = None
        based_el = st.find(qn("basedOn"))
        if based_el is not None:
            based = based_el.get(qn("val"))
        by_id[sid] = {"outline": outline, "based": based}

    resolved: Dict[str, Optional[int]] = {}

    def resolve(sid: str, seen: Tuple[str, ...]) -> Optional[int]:
        if sid in resolved:
            return resolved[sid]
        info = by_id.get(sid)
        if info is None:
            resolved[sid] = None
            return None
        if info["outline"] is not None:
            resolved[sid] = info["outline"]
            return info["outline"]
        if info["based"] and info["based"] not in seen:
            return resolve(info["based"], seen + (sid,))
        resolved[sid] = None
        return None

    for sid in by_id:
        resolve(sid, ())
    return resolved


def _paragraph_outline_level(p, outline_map: Dict[str, Optional[int]]) -> Optional[int]:
    pPr = p.find(qn("pPr"))
    if pPr is None:
        return None
    st = pPr.find(qn("pStyle"))
    if st is None:
        return None
    sid = st.get(qn("val"))
    lvl = outline_map.get(sid)
    if lvl is None:
        return None
    return lvl + 1  # outlineLvl 0 -> 标题 1


def _has_numpr(p) -> bool:
    pPr = p.find(qn("pPr"))
    return pPr is not None and pPr.find(qn("numPr")) is not None


# --------------------------------------------------------------------------- #
# 版式解析（V4）：为什么必须做这件事
#
# 老版本只把段落文字取出来，前端按「一行一个块」平铺 —— 结果画布上
# 对齐、字号、首行缩进、表格列宽、合并单元格全部消失，用户看到的是
# 「格式完全是乱的」。对一个「在线模板编辑器」来说，看不见版式就等于没法
# 在正确的位置插入占位符，所以这里把影响视觉的 OOXML 属性显式解析出来。
#
# 取舍：不做完整的样式继承引擎（那是 Word 的活）。只沿
#   run.rPr -> 段落样式链(styles.xml) -> docDefaults
# 三级回溯，覆盖公文/报告里 95% 的用法。制表位、浮动图、公式不在此列，
# 它们仍走 unsupported 原样保留。
# --------------------------------------------------------------------------- #

TWIPS_PER_MM = 1440.0 / 25.4


def _twips_to_mm(v: Optional[str]) -> Optional[float]:
    """OOXML 长度单位（twip = 1/1440 英寸）-> 毫米。"""
    if v is None:
        return None
    try:
        return round(int(float(v)) / TWIPS_PER_MM, 2)
    except (TypeError, ValueError):
        return None


def _halfpt_to_pt(v: Optional[str]) -> Optional[float]:
    """w:sz 是半磅（half-point），24 表示 12pt。"""
    if v is None:
        return None
    try:
        return round(int(float(v)) / 2.0, 1)
    except (TypeError, ValueError):
        return None


# w:jc 的取值 -> CSS text-align。Word 2013 之后用 start/end，老文档用 left/right。
JC_MAP = {
    "left": "left", "start": "left",
    "right": "right", "end": "right",
    "center": "center",
    "both": "justify", "distribute": "justify",
    "thaiDistribute": "justify",
    "lowKashida": "justify", "mediumKashida": "justify", "highKashida": "justify",
}


def _style_map(root) -> Dict[str, Dict[str, Any]]:
    """styles.xml -> {styleId: {basedOn, rPr 摘要, pPr 摘要}}。

    只提取与视觉有关的几项，够用即可：
      sz(半磅) / b / i / u / color / rFonts / jc / ind / spacing
    """
    out: Dict[str, Dict[str, Any]] = {}
    if root is None:
        return out
    for st in root.findall(qn("style")):
        sid = st.get(qn("styleId"))
        if not sid:
            continue
        info: Dict[str, Any] = {}
        based = st.find(qn("basedOn"))
        if based is not None:
            info["basedOn"] = based.get(qn("val"))
        rPr = st.find(qn("rPr"))
        if rPr is not None:
            fonts = rPr.find(qn("rFonts"))
            if fonts is not None:
                info["font"] = fonts.get(qn("eastAsia")) or fonts.get(qn("ascii"))
            sz = rPr.find(qn("sz"))
            if sz is not None:
                info["size_pt"] = _halfpt_to_pt(sz.get(qn("val")))
            if rPr.find(qn("b")) is not None:
                info["bold"] = _on(rPr.find(qn("b")))
            if rPr.find(qn("i")) is not None:
                info["italic"] = _on(rPr.find(qn("i")))
            if rPr.find(qn("u")) is not None:
                info["underline"] = rPr.find(qn("u")).get(qn("val")) not in (None, "none")
            col = rPr.find(qn("color"))
            if col is not None and col.get(qn("val")) not in (None, "auto"):
                info["color"] = "#" + str(col.get(qn("val")))
        pPr = st.find(qn("pPr"))
        if pPr is not None:
            jc = pPr.find(qn("jc"))
            if jc is not None:
                info["align"] = JC_MAP.get(str(jc.get(qn("val"))))
        out[sid] = info
    return out


def _on(el) -> bool:
    """OOXML 开关属性：元素存在且 w:val 不是 false/0/off 即为真。"""
    if el is None:
        return False
    v = el.get(qn("val"))
    return str(v).lower() not in ("0", "false", "off")


def _doc_defaults(root) -> Dict[str, Any]:
    """docDefaults -> 默认字号/字体。公文模板几乎全靠它，读不到字号会全部变 14px。"""
    out: Dict[str, Any] = {}
    if root is None:
        return out
    dd = root.find(qn("docDefaults"))
    if dd is None:
        return out
    rPr = dd.find(qn("rPrDefault"))
    if rPr is not None:
        r = rPr.find(qn("rPr"))
        if r is not None:
            fonts = r.find(qn("rFonts"))
            if fonts is not None:
                out["font"] = fonts.get(qn("eastAsia")) or fonts.get(qn("ascii"))
            sz = r.find(qn("sz"))
            if sz is not None:
                out["size_pt"] = _halfpt_to_pt(sz.get(qn("val")))
    return out


def _resolve_style(sid: Optional[str], styles: Dict[str, Dict[str, Any]],
                  defaults: Dict[str, Any]) -> Dict[str, Any]:
    """沿 basedOn 链把样式合并成一份有效属性（子优先）。"""
    chain: List[Dict[str, Any]] = []
    seen = set()
    cur = sid
    while cur and cur in styles and cur not in seen:
        seen.add(cur)
        chain.append(styles[cur])
        cur = styles[cur].get("basedOn")
    merged: Dict[str, Any] = {}
    for info in reversed(chain):
        for k, v in info.items():
            if k == "basedOn" or v is None:
                continue
            merged[k] = v
    for k, v in defaults.items():
        merged.setdefault(k, v)
    return merged


def _para_style_id(p) -> Optional[str]:
    pPr = p.find(qn("pPr"))
    if pPr is None:
        return None
    st = pPr.find(qn("pStyle"))
    return st.get(qn("val")) if st is not None else None


def _run_effective(r, style_props: Dict[str, Any]) -> Dict[str, Any]:
    """单个 run 的有效字符格式：rPr 覆盖样式，样式覆盖文档默认。"""
    props = dict(style_props)
    rPr = r.find(qn("rPr"))
    if rPr is None:
        return props
    fonts = rPr.find(qn("rFonts"))
    if fonts is not None:
        f = fonts.get(qn("eastAsia")) or fonts.get(qn("ascii"))
        if f:
            props["font"] = f
    sz = rPr.find(qn("sz"))
    if sz is not None:
        pt = _halfpt_to_pt(sz.get(qn("val")))
        if pt:
            props["size_pt"] = pt
    for tag, key in (("b", "bold"), ("i", "italic")):
        el = rPr.find(qn(tag))
        if el is not None:
            props[key] = _on(el)
    u = rPr.find(qn("u"))
    if u is not None:
        props["underline"] = u.get(qn("val")) not in (None, "none")
    col = rPr.find(qn("color"))
    if col is not None:
        v = col.get(qn("val"))
        if v and v != "auto":
            props["color"] = "#" + str(v)
    return props


def _paragraph_runs(p, style_props: Dict[str, Any]) -> Optional[List[Dict[str, Any]]]:
    """段落按 run 切分成带格式的片段。

    返回 None 表示「拿不到可靠的 run 切分」，调用方退回纯文本渲染。
    为什么要这个判据：``paragraph_text`` 用的是 p.iter(w:t)，会把文本框之类的
    嵌套段落一起拼进来；而这里只走直属 r/hyperlink。两者不一致时若仍按 runs
    渲染，画布上显示的字和回写进 docx 的字就会不同 —— 那种偏差比样式丢失更危险。
    """
    runs: List[Dict[str, Any]] = []
    for child in p:
        ln = etree.QName(child).localname
        if ln == "r":
            runs.append(child)
        elif ln == "hyperlink":
            runs.extend(child.findall(qn("r")))
    if not runs:
        return None
    out: List[Dict[str, Any]] = []
    for r in runs:
        text = "".join(t.text or "" for t in r.iter(qn("t")))
        if text == "":
            # 空 run 不产生可见文字，但对制表符/换行有意义；这里只取文字，
            # 所以保留为空格以免相邻文字被粘在一起。
            continue
        props = _run_effective(r, style_props)
        item: Dict[str, Any] = {"text": text}
        if props.get("size_pt"):
            item["size_pt"] = props["size_pt"]
        if props.get("bold"):
            item["bold"] = True
        if props.get("italic"):
            item["italic"] = True
        if props.get("underline"):
            item["underline"] = True
        if props.get("color"):
            item["color"] = props["color"]
        if props.get("font"):
            item["font"] = props["font"]
        out.append(item)
    if "".join(x["text"] for x in out) != paragraph_text(p):
        return None
    return out


def _para_format(p, style_props: Dict[str, Any]) -> Dict[str, Any]:
    """段落级版式：对齐 / 左右缩进 / 首行缩进 / 段前段后 / 行距。"""
    fmt: Dict[str, Any] = {}
    pPr = p.find(qn("pPr"))
    if pPr is not None:
        jc = pPr.find(qn("jc"))
        if jc is not None:
            a = JC_MAP.get(str(jc.get(qn("val"))))
            if a:
                fmt["align"] = a
        ind = pPr.find(qn("ind"))
        if ind is not None:
            # start/end 是新版写法，left/right 是旧版；两套都要认。
            left = _twips_to_mm(ind.get(qn("left")) or ind.get(qn("start")))
            right = _twips_to_mm(ind.get(qn("right")) or ind.get(qn("end")))
            first = _twips_to_mm(ind.get(qn("firstLine")))
            hang = _twips_to_mm(ind.get(qn("hanging")))
            if left is not None:
                fmt["indent_left_mm"] = left
            if right is not None:
                fmt["indent_right_mm"] = right
            if first is not None:
                fmt["indent_first_mm"] = first
            elif hang is not None:
                fmt["indent_first_mm"] = -hang
        sp = pPr.find(qn("spacing"))
        if sp is not None:
            before = _twips_to_mm(sp.get(qn("before")))
            after = _twips_to_mm(sp.get(qn("after")))
            if before is not None:
                fmt["space_before_mm"] = before
            if after is not None:
                fmt["space_after_mm"] = after
            line = sp.get(qn("line"))
            rule = sp.get(qn("lineRule")) or "auto"
            if line is not None:
                try:
                    lv = float(line)
                except ValueError:
                    lv = None
                if lv:
                    if rule == "auto":
                        # auto 的 line 是 240 分之一行：360 = 1.5 倍
                        fmt["line_height"] = round(lv / 240.0, 2)
                    else:
                        # exact / atLeast：绝对值（twip）
                        mm = round(lv / TWIPS_PER_MM, 2)
                        fmt["line_height_mm"] = mm
    # 样式里的对齐作为兜底（很多公文模板把「居中」写在样式上）
    if "align" not in fmt and style_props.get("align"):
        fmt["align"] = style_props["align"]
    return fmt


def _page_geometry(body) -> Dict[str, Any]:
    """纸张尺寸与页边距（mm）。画布要靠它还原 A4 版心。

    sectPr 可能在 body 末尾，也可能挂在某个段落的 pPr 里（分节符）。
    这里取 body 直属的那个 —— 首节与常见单节文档都覆盖到了。
    """
    out: Dict[str, Any] = {}
    sect = body.find(qn("sectPr"))
    if sect is None:
        # 退化：找最后一个段落 pPr 里的 sectPr
        for p in reversed(body.findall(qn("p"))):
            pPr = p.find(qn("pPr"))
            if pPr is not None and pPr.find(qn("sectPr")) is not None:
                sect = pPr.find(qn("sectPr"))
                break
    if sect is None:
        return out
    sz = sect.find(qn("pgSz"))
    if sz is not None:
        w = _twips_to_mm(sz.get(qn("w")))
        h = _twips_to_mm(sz.get(qn("h")))
        if w:
            out["width_mm"] = w
        if h:
            out["height_mm"] = h
    mar = sect.find(qn("pgMar"))
    if mar is not None:
        for src, dst in (("top", "margin_top_mm"), ("right", "margin_right_mm"),
                         ("bottom", "margin_bottom_mm"), ("left", "margin_left_mm")):
            v = _twips_to_mm(mar.get(qn(src)))
            if v is not None:
                out[dst] = v
    return out


def _chart_rid(p) -> Optional[str]:
    """段落里是否有内联图表（a:graphicData uri=.../chart 下的 c:chart），返回其 r:id。"""
    for gd in p.iter("{%s}graphicData" % A):
        if "chart" not in (gd.get("uri") or ""):
            continue
        for child in gd:
            if etree.QName(child).localname == "chart":
                rid = child.get("{%s}id" % R)
                if rid:
                    return rid
    return None


def _doc_rels(zf) -> dict:
    """document.xml.rels -> {rId: target}。"""
    try:
        root = etree.fromstring(zf.read("word/_rels/document.xml.rels"))
    except (KeyError, etree.LxmlError):
        return {}
    return {(rel.get("Id") or ""): (rel.get("Target") or "") for rel in root}


def _unsupported_info(p) -> Optional[Dict[str, str]]:
    """段落若含"不在块模型内"的元素，返回 {note, text}；可编辑则 None。

    这里说的"不支持"**不是"文档用不了"**，只是"这一段的内容没法在网页里改"：
    docapply 对这些块从不触碰（原样保留一个字节），渲染链路走的是占位符替换，
    与这里的结构解析完全无关。

    历史上 note 文案写的是"需在 Word 处理"，用户只能看到一个空白方块，不知道
    里面到底是什么，于是被引导去 Word 里反复删东西 —— 其实没必要。所以现在
    把盒子里的原文一起带出去（text），至少让用户看得见内容。
    """
    # 内联图表：数据可以被传参替换，但**段落本身绝不能被改写**——
    # 一旦走了 _rewrite_paragraph_text，整个 <w:drawing> 就没了，图表直接消失。
    # 这里返回非 None 是为了让 docapply 的 _is_unsupported_paragraph 把它排除在
    # 可编辑集合之外；parse 侧会在走到这里之前先判成 chart 块。
    if _chart_rid(p) is not None:
        return {"note": "内联图表：数据可传参替换，样式随文档保留", "text": ""}
    # 文本框：内容其实是嵌套的 w:p，文本可用 paragraph_text 取到
    boxes = p.findall(".//" + qn("txbxContent"))
    if boxes:
        inner = "".join(paragraph_text(b) for b in boxes).strip()
        return {"note": "文本框（w:txbxContent）：内容随文档原样保留，不参与在线排版",
                "text": inner}
    # 图形/形状（VML）：老式 Word 的图片框、艺术字、线条
    picts = p.findall(".//" + qn("pict"))
    if picts:
        inner = "".join(paragraph_text(x) for x in picts).strip()
        return {"note": "形状/图片框（w:pict）：随文档原样保留，不参与在线排版",
                "text": inner}
    # 浮动锚定图片（anchor）——不做行内编辑，整体保留
    if p.findall(".//{%s}anchor" % WP):
        return {"note": "浮动锚定对象（wp:anchor）：随文档原样保留，不参与在线排版",
                "text": ""}
    return None


def _is_unsupported_paragraph(p) -> Optional[str]:
    """docapply 侧只关心"要不要碰"，返回说明字符串即可。"""
    info = _unsupported_info(p)
    return info["note"] if info else None


def _placeholder_dims(p) -> Tuple[Optional[float], Optional[float]]:
    """从行内 drawing 的 wp:extent 读 EMU -> mm；读不到返回 (None,None)。"""
    extent = p.find(".//{%s}extent" % WP)
    if extent is not None:
        cx = extent.get("cx")
        cy = extent.get("cy")
        try:
            # 1 mm = 36000 EMU
            return round(int(cx) / 36000, 2), round(int(cy) / 36000, 2)
        except (TypeError, ValueError):
            pass
    return None, None


def _para_style_props(p, ctx: Dict[str, Any]) -> Dict[str, Any]:
    """段落自身的有效字符格式（不含各 run 的覆盖）。"""
    return _resolve_style(_para_style_id(p), ctx.get("styles") or {}, ctx.get("defaults") or {})


def _cell_props(tc) -> Dict[str, Any]:
    """单元格 -> 跨列 / 纵向合并 / 底纹 / 垂直对齐。

    注意：**这些字段只影响渲染，不影响回写**。cells 数组必须与
    tr.findall(tc) 一一对应（docapply 按索引写格），所以「被纵向合并吞掉的格」
    仍然保留在数组里，只是额外打 hidden=true 让前端跳过绘制。
    """
    out: Dict[str, Any] = {}
    tcPr = tc.find(qn("tcPr"))
    if tcPr is None:
        return out
    gs = tcPr.find(qn("gridSpan"))
    if gs is not None:
        try:
            n = int(gs.get(qn("val")) or 1)
            if n > 1:
                out["grid_span"] = n
        except (TypeError, ValueError):
            pass
    vm = tcPr.find(qn("vMerge"))
    if vm is not None:
        # w:val 缺失 = continue（被上方那格纵向合并吞掉）
        out["v_merge"] = "restart" if vm.get(qn("val")) == "restart" else "continue"
    shd = tcPr.find(qn("shd"))
    if shd is not None:
        fill = shd.get(qn("fill"))
        if fill and fill not in ("auto", "FFFFFF"):
            out["shading"] = "#" + str(fill)
    va = tcPr.find(qn("vAlign"))
    if va is not None:
        v = va.get(qn("val"))
        if v in ("center", "bottom", "top"):
            out["valign"] = v
    return out


def _compute_row_spans(rows: List[Dict[str, Any]]) -> None:
    """给纵向合并的 restart 格写上 row_span，给被吞掉的 continue 格打 hidden。

    做法是按「逻辑列」对齐：每行从左往右累计列偏移（跨列格占多列），
    记下每个逻辑列当前「开口」的 restart 格，遇到 continue 就在那一列上
    给它续一行。
    """
    open_at: Dict[int, Dict[str, Any]] = {}
    for row in rows:
        col = 0
        for cell in row.get("cells", []):
            span = int(cell.get("grid_span") or 1)
            vm = cell.get("v_merge")
            if vm == "continue":
                touched = [open_at[c] for c in range(col, col + span) if c in open_at]
                for c in range(col, col + span):
                    holder = open_at.get(c)
                    if holder is not None:
                        holder["row_span"] = int(holder.get("row_span") or 1) + 1
                cell["hidden"] = True
                if not touched:
                    # 上边没有 restart 的孤儿 continue：让它自己显示，别吞掉内容
                    cell.pop("hidden", None)
            elif vm == "restart":
                cell["row_span"] = 1
                for c in range(col, col + span):
                    open_at[c] = cell
            else:
                for c in range(col, col + span):
                    open_at.pop(c, None)
            col += span


# --------------------------------------------------------------------------- #
# 单元格解析
# --------------------------------------------------------------------------- #
def _parse_cell(tc, cell_id: str, ctx: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    """单元格 -> {blocks:[...]}。每个单元格内段落都成为带 id 的可编辑块。"""
    ctx = ctx or {}
    blocks: List[Dict[str, Any]] = []
    para_idx = 0
    for child in tc:
        if etree.QName(child).localname != "p":
            continue
        pid = f"{cell_id}p{para_idx}"
        para_idx += 1
        note = _is_unsupported_paragraph(child)
        text = paragraph_text(child)
        if note:
            info = _unsupported_info(child) or {"note": note, "text": ""}
            blocks.append({"id": pid, "type": "unsupported",
                           "note": info["note"], "text": info["text"]})
            continue
        style_props = _para_style_props(child, ctx)
        fmt = _para_format(child, style_props)
        runs = _paragraph_runs(child, style_props)
        m = STANDALONE_TAG_RE.match(text)
        if m:
            name = m.group(1)
            kind = _kind_by_name(name)
            if kind != "scalar":
                w_mm, h_mm = _placeholder_dims(child)
                ph: Dict[str, Any] = {
                    "id": pid, "type": "placeholder", "kind": kind, "name": name,
                }
                if w_mm:
                    ph["width_mm"] = w_mm
                if h_mm:
                    ph["height_mm"] = h_mm
                if fmt:
                    ph["fmt"] = fmt
                blocks.append(ph)
                continue
        blk: Dict[str, Any] = {"id": pid, "type": "paragraph", "text": text,
                               "tokens": extract_tokens(text)}
        if fmt:
            blk["fmt"] = fmt
        if runs:
            blk["runs"] = runs
        blocks.append(blk)
    return {"blocks": blocks}


def _table_layout(tbl) -> Dict[str, Any]:
    """表格列宽（mm）与布局模式。

    Word 的列宽写在 w:tblGrid/w:gridCol/@w:w（twip）。不还原它，浏览器就用
    auto 布局按内容分配列宽 —— 每一行的列宽都不一样，表格看上去像歪的。
    这是「格式乱」里最刺眼的一条，所以单独提成表格级字段。
    """
    out: Dict[str, Any] = {}
    grid = tbl.find(qn("tblGrid"))
    if grid is not None:
        cols: List[float] = []
        for gc in grid.findall(qn("gridCol")):
            mm = _twips_to_mm(gc.get(qn("w")))
            if mm:
                cols.append(mm)
        if cols:
            out["col_width_mm"] = cols
    tblPr = tbl.find(qn("tblPr"))
    if tblPr is not None:
        layout = tblPr.find(qn("tblLayout"))
        if layout is not None:
            out["fixed_layout"] = layout.get(qn("type")) == "fixed"
        tw = tblPr.find(qn("tblW"))
        if tw is not None and tw.get(qn("type")) in (None, "dxa"):
            mm = _twips_to_mm(tw.get(qn("w")))
            if mm:
                out["width_mm"] = mm
        jc = tblPr.find(qn("jc"))
        if jc is not None and jc.get(qn("val")) in ("center", "right", "left"):
            out["align"] = jc.get(qn("val"))
    return out


def _parse_table(tbl, tid: str, warnings: List[str],
                 ctx: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    ctx = ctx or {}
    rows_el = tbl.findall(qn("tr"))
    parsed_rows: List[Dict[str, Any]] = []
    # 第一遍：把每行解析成 cells，并记录是否 tr 标记行
    raw_rows: List[Dict[str, Any]] = []
    for ri, tr in enumerate(rows_el):
        rid = f"{tid}r{ri}"
        cells = tr.findall(qn("tc"))
        cell_objs = []
        for ci, tc in enumerate(cells):
            cobj = _parse_cell(tc, f"{rid}c{ci}", ctx)
            cprops = _cell_props(tc)
            if cprops:
                cobj.update(cprops)
            cell_objs.append(cobj)
        row_text = "".join(t.text or "" for t in tr.iter(qn("t")))
        raw_rows.append({"id": rid, "cells": cell_objs, "text": row_text})

    # 第一遍半：算 rowSpan / hidden。必须在前面做，因为下面 loop_row 的处理
    # 只是贴标记，不影响合并结构。
    _compute_row_spans(raw_rows)

    # 第二遍：识别 {%tr for %}/{%tr endfor %} 包裹行，给中间数据行打 loop_row
    i = 0
    n = len(raw_rows)
    while i < n:
        rr = raw_rows[i]
        m_for = TR_FOR_RE.match(rr["text"])
        if m_for:
            var, iterable = m_for.group(1), m_for.group(2).strip()
            # 找配对的 {%tr endfor %}
            j = i + 1
            end_idx = None
            while j < n:
                if TR_END_RE.match(raw_rows[j]["text"]):
                    end_idx = j
                    break
                j += 1
            if end_idx is None:
                warnings.append(
                    "表格 " + tid + " 中 {%tr for %} 缺少配对 {%tr endfor %}，已按普通行保留"
                )
                i += 1
                continue
            # 标记行 i 与 end_idx 不暴露为可编辑行（结构标记，原样保留）；
            # 中间的数据行打 loop_row。
            for k in range(i + 1, end_idx):
                raw_rows[k]["loop_row"] = True
                raw_rows[k]["loop"] = {"var": var, "iterable": iterable}
            i = end_idx + 1
            continue
        i += 1

    for rr in raw_rows:
        row_obj: Dict[str, Any] = {
            "id": rr["id"], "cells": rr["cells"],
            "loop_row": rr.get("loop_row", False),
        }
        if rr.get("loop"):
            row_obj["loop"] = rr["loop"]
        parsed_rows.append(row_obj)

    out: Dict[str, Any] = {"id": tid, "type": "table", "rows": parsed_rows}
    # 列宽/布局提到表格级，前端用它渲染 <colgroup> 并锁定 table-layout:fixed。
    # 不还原它，浏览器会按内容自动分配列宽，同一张表的每一行列宽都不一样。
    out.update(_table_layout(tbl))
    return out


# --------------------------------------------------------------------------- #
# 主解析入口
# --------------------------------------------------------------------------- #
def _header_footer_tokens(zf) -> Dict[str, List[str]]:
    """页眉/页脚里出现的 jinja 占位符。{'header': [...], 'footer': [...]}。

    编辑画布只渲染 word/document.xml 的 body，页眉页脚不在其中 —— 但它们的占位符
    是会被生成链路替换的（scanner 现在也扫这些部件）。如果不告诉用户，用户在画布里
    找不到这个变量，会以为模板里没有它。
    """
    out: Dict[str, List[str]] = {"header": [], "footer": []}
    for name in zf.namelist():
        if name.startswith("word/header"):
            bucket = "header"
        elif name.startswith("word/footer"):
            bucket = "footer"
        else:
            continue
        try:
            root = etree.fromstring(zf.read(name))
        except etree.LxmlError:
            continue
        for p in root.iter(qn("p")):
            for m in TOKEN_RE.finditer(paragraph_text(p)):
                tok = m.group(0)
                if tok.startswith("{{"):
                    v = tok[2:-2].strip()
                    if v and v not in out[bucket]:
                        out[bucket].append(v)
    return out


def parse_docx(raw: bytes) -> Dict[str, Any]:
    """docx 字节 -> {blocks, warnings}。只读，不修改任何部件。"""
    zf = zipfile.ZipFile(BytesIO(raw))
    warnings: List[str] = []

    styles_xml = zf.read("word/styles.xml") if "word/styles.xml" in zf.namelist() else None
    outline_map: Dict[str, Optional[int]] = {}
    # 版式上下文：样式表 + 文档默认值。两者都缺失时 _resolve_style 会退化成
    # 空字典，渲染端按浏览器默认排版 —— 比整份文档解析失败好。
    ctx: Dict[str, Any] = {"styles": {}, "defaults": {}}
    if styles_xml is not None:
        try:
            _styles_root = etree.fromstring(styles_xml)
            outline_map = _build_style_outline(_styles_root)
            ctx["styles"] = _style_map(_styles_root)
            ctx["defaults"] = _doc_defaults(_styles_root)
        except etree.LxmlError:
            pass

    root = etree.fromstring(zf.read("word/document.xml"))
    body = root.find(qn("body"))

    # 页眉/页脚不在画布内，但其中的占位符会被正常替换 —— 提前说一声，别让用户以为丢了
    try:
        hf = _header_footer_tokens(zf)
    except Exception:  # noqa: BLE001
        hf = {"header": [], "footer": []}
    for bucket, label in (("header", "页眉"), ("footer", "页脚")):
        vs = hf.get(bucket) or []
        if vs:
            warnings.append("%s含 %d 个占位符（%s），不在下方画布中展示，填数据生成时会正常替换"
                            % (label, len(vs), "、".join(vs[:6]) + ("…" if len(vs) > 6 else "")))

    blocks: List[Dict[str, Any]] = []
    p_counter = 0
    tbl_counter = 0
    list_open: List[Dict[str, Any]] = []  # 正在聚合的连续 numPr 段落

    # 图表元信息：rId -> chart 部件 -> 类型/标题/系列数。用来在画布里把图表
    # 显示成"折线图·季度销售额·2 系列 4 点"，而不是一个空白段落。
    rels = _doc_rels(zf)
    chart_meta: Dict[str, Dict[str, Any]] = {}
    try:
        from . import chartdata as _cd
        _used: set = set()
        for cm in _cd.list_charts(raw):
            # 变量名与 scanner 用同一套派生规则（chart_<标题>），这样画布上标注的
            # 变量名与变量面板/API 里的一致。scanner 的 used 集合还包含文本变量，
            # 但文本变量叫 chart_xxx 的概率可以忽略，这里做尽力对齐即可。
            cm["variable_name"] = _cd.chart_var_name(cm, _used)
            chart_meta[cm["part"]] = cm
    except Exception:  # noqa: BLE001 - 图表元信息拿不到只是显示降级，不影响解析
        pass

    def _chart_block(pid: str, child) -> Optional[Dict[str, Any]]:
        rid = _chart_rid(child)
        if rid is None:
            return None
        target = (rels.get(rid) or "").replace("../", "")
        part = target if target.startswith("word/") else "word/" + target.lstrip("./")
        meta = chart_meta.get(part) or {}
        return {
            "id": pid,
            "type": "chart",
            "chart_part": part,
            "chart_kind": meta.get("kind") or "",
            "kind_name": meta.get("kind_name") or "图表",
            "title": meta.get("title") or "",
            "variable": meta.get("variable_name") or "",
            "series_count": meta.get("series_count") or 0,
            "point_count": meta.get("point_count") or 0,
            "categories": meta.get("categories") or [],
            "series": meta.get("series") or [],
            "note": "图表数据可通过变量传参替换，配色/字体/图例等样式保持不变",
        }

    def flush_list():
        if list_open:
            blocks.append(list_open.pop(0))
            list_open.clear()

    def degrade(child, bid: str, label: str, exc: Exception) -> None:
        """单个元素解析失败 -> 只降级它自己，不连累整份文档。"""
        try:
            preview = paragraph_text(child)[:120]
        except Exception:  # noqa: BLE001 - 取不到预览文本不影响降级本身
            preview = ""
        note = "%s结构异常（%s），已原样保留、不可在线编辑" % (label, type(exc).__name__)
        warnings.append(note)
        blocks.append({"id": bid, "type": "unsupported", "note": note, "text": preview})

    for child in body:
        try:
            ln = etree.QName(child).localname
        except Exception:  # noqa: BLE001
            continue
        if ln == "sectPr":
            continue  # 节属性，永不作为块
        if ln != "p" and ln != "tbl":
            # 其他直属元素（如 bookmarkStart 等）：跳过，不动
            continue

        # 快照：任一元素解析炸了，回滚到"进入本元素时"的状态，再把它自己降级成
        # 一个 unsupported 块。这样「一个奇怪的表格/段落」最坏也只是「这一段改不了」，
        # 绝不会变成「整份模板打不开、只能回 Word」。id 仍按 body 顺序稳定分配，
        # 所以同一份 docx 多次 parse 得到的 id 与 apply 侧完全一致。
        at_blocks = len(blocks)
        at_p, at_t = p_counter, tbl_counter
        try:
            if ln == "tbl":
                flush_list()
                tid = f"t{tbl_counter}"
                tbl_counter += 1
                blocks.append(_parse_table(child, tid, warnings, ctx))
                continue

            # 段落
            # 图表要在"不可编辑"判定之前拦下来：图表段落同样是不可编辑的
            # （否则 _rewrite_paragraph_text 会把整个 <w:drawing> 删掉），
            # 但它值得单独一种块类型，好让画布显示出图表类型和数据摘要。
            chart_blk = _chart_block(f"p{p_counter}", child)
            if chart_blk is not None:
                flush_list()
                p_counter += 1
                blocks.append(chart_blk)
                continue

            info = _unsupported_info(child)
            text = paragraph_text(child)

            # 图表块的 note 不进 warnings（它是正常能力，不是降级），
            # 其余 unsupported 才提示
            if info:
                flush_list()
                pid = f"p{p_counter}"
                p_counter += 1
                warnings.append(info["note"])
                blocks.append({"id": pid, "type": "unsupported",
                               "note": info["note"], "text": info["text"]})
                continue

            outline = _paragraph_outline_level(child, outline_map)
            style_props = _para_style_props(child, ctx)
            fmt = _para_format(child, style_props)
            runs = _paragraph_runs(child, style_props)

            # 独立占位符（整段一个 image/qrcode/rich 标签）
            m = STANDALONE_TAG_RE.match(text)
            if m:
                name = m.group(1)
                kind = _kind_by_name(name)
                if kind != "scalar" and outline is None:
                    flush_list()
                    pid = f"p{p_counter}"
                    p_counter += 1
                    w_mm, h_mm = _placeholder_dims(child)
                    ph: Dict[str, Any] = {"id": pid, "type": "placeholder",
                                          "kind": kind, "name": name}
                    if w_mm:
                        ph["width_mm"] = w_mm
                    if h_mm:
                        ph["height_mm"] = h_mm
                    if fmt:
                        ph["fmt"] = fmt
                    blocks.append(ph)
                    continue

            # 标题（按 outlineLvl 解析）
            if outline is not None:
                flush_list()
                pid = f"p{p_counter}"
                p_counter += 1
                hb: Dict[str, Any] = {"id": pid, "type": "heading", "level": outline,
                                      "text": text, "tokens": extract_tokens(text)}
                if fmt:
                    hb["fmt"] = fmt
                if runs:
                    hb["runs"] = runs
                blocks.append(hb)
                continue

            # 列表项（numPr 且非标题）：连续同组聚合为一个 list 块
            if _has_numpr(child):
                pid = f"p{p_counter}"
                p_counter += 1
                item: Dict[str, Any] = {"text": text, "tokens": extract_tokens(text)}
                if fmt:
                    item["fmt"] = fmt
                if runs:
                    item["runs"] = runs
                if list_open:
                    list_open[0]["items"].append(item)
                else:
                    # ordered 无法仅凭 numId 可靠区分，按 numPr 存在即视为列表；
                    # ordered 留 True 近似（编号列表在 numbering.xml 定义，本期不深读）
                    list_open.append({"id": pid, "type": "list", "ordered": True,
                                      "items": [item]})
                continue

            # 普通段落
            flush_list()
            pid = f"p{p_counter}"
            p_counter += 1
            pb: Dict[str, Any] = {"id": pid, "type": "paragraph", "text": text,
                                  "tokens": extract_tokens(text)}
            if fmt:
                pb["fmt"] = fmt
            if runs:
                pb["runs"] = runs
            blocks.append(pb)
        except Exception as e:  # noqa: BLE001
            del blocks[at_blocks:]
            p_counter, tbl_counter = at_p, at_t
            if ln == "tbl":
                degrade(child, f"t{tbl_counter}", "表格", e)
                tbl_counter += 1
            else:
                degrade(child, f"p{p_counter}", "段落", e)
                p_counter += 1

    flush_list()
    zf.close()
    # 同一类降级往往重复出现几十次，全量塞进告警只会把有用的信息淹掉
    seen = set()
    deduped = []
    for w in warnings:
        if w in seen:
            continue
        seen.add(w)
        deduped.append(w)
    # 纸张与页边距：画布要靠它还原版心。拿不到就退回 A4 默认值，
    # 总比让前端自己猜（然后猜成「一个白盒子」）好。
    page = _page_geometry(body) or {
        "width_mm": 210.0, "height_mm": 297.0,
        "margin_top_mm": 25.4, "margin_right_mm": 31.8,
        "margin_bottom_mm": 25.4, "margin_left_mm": 31.8,
    }
    return {"blocks": blocks, "warnings": deduped, "page": page}
