# -*- coding: utf-8 -*-
"""格式不变性：/apply 回写的硬约束。

用户的诉求是「改文字，格式一个字都不许动」。这里把它拆成可判定的断言：

  A. 模型未改动时，正文每个元素的 XML **逐字节**一致，其余 zip 部件也逐字节一致；
  B. 被改动的段落，pPr 逐字节一致（首行缩进 / 行距 / 段前段后 / 对齐 / 编号）；
  C. 被改动的段落，每个 run 的 rPr 逐字节一致（字体 / 字号 / 加粗 / 颜色），
     run 数量不乱增不乱减；
  D. 域 / 超链接 / 书签 / 分节符 / 图片 / 编号的数量一致；
  E. 文字不重复、不串位（域结果不会被搬到段首）；
  F. 列表块整体回写后每一项都还在，编号格式还在。

夹具刻意造齐了公文会踩的格式陷阱：首行缩进 2 字符、固定行距 28 磅、两端对齐、
仿宋_GB2312 三号、黑体加粗小标题、目录域、超链接、修订痕迹、书签、横向分节、
编号列表、固定列宽 + 固定行高的表格、内联图片。

每条断言都对着一个**真实踩过或探针复现过**的坏行为，删掉实现就会被判红。
"""
import base64
import copy
import zipfile
from io import BytesIO

import pytest
from docx import Document
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.oxml import OxmlElement
from docx.oxml.ns import qn as dqn
from docx.shared import Mm, Pt
from lxml import etree

from app.docapply import apply_model, _rewrite_paragraph_text
from app.docmodel import parse_docx, paragraph_text, qn

# 1x1 透明 PNG：够 python-docx 建出真的图片部件与 rId
_PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
)


# --------------------------------------------------------------------------- #
# 夹具：一份"格式陷阱全家桶"
# --------------------------------------------------------------------------- #
def _body_para(doc, text, *, indent=True, line=560, before=0, after=0):
    p = doc.add_paragraph()
    pPr = p._p.get_or_add_pPr()
    if indent:
        ind = OxmlElement("w:ind")
        ind.set(dqn("w:firstLineChars"), "200")     # 首行缩进 2 字符
        ind.set(dqn("w:firstLine"), "640")
        pPr.append(ind)
    sp = OxmlElement("w:spacing")
    sp.set(dqn("w:line"), str(line))                # 固定行距
    sp.set(dqn("w:lineRule"), "exact")
    sp.set(dqn("w:before"), str(before))
    sp.set(dqn("w:after"), str(after))
    pPr.append(sp)
    jc = OxmlElement("w:jc")
    jc.set(dqn("w:val"), "both")                    # 两端对齐
    pPr.append(jc)
    if text:
        r = p.add_run(text)
        r.font.size = Pt(16)                        # 三号
        r.font.name = "宋体"
        r._element.rPr.rFonts.set(dqn("w:eastAsia"), "仿宋_GB2312")
    return p


def build_format_trap_docx() -> bytes:
    """造模板（含域 / 超链接 / 修订 / 书签 / 分节 / 列表 / 表格 / 图片）。"""
    doc = Document()
    sec = doc.sections[0]
    sec.page_width = Mm(210)
    sec.page_height = Mm(297)
    sec.left_margin = Mm(28)
    sec.right_margin = Mm(26)

    # p0 标题：方正小标宋二号加粗居中
    t = doc.add_paragraph()
    t.alignment = WD_ALIGN_PARAGRAPH.CENTER
    tr = t.add_run("关于开展年度安全生产检查的通知")
    tr.font.size = Pt(22)
    tr.font.bold = True
    tr.font.name = "Times New Roman"
    tr._element.rPr.rFonts.set(dqn("w:eastAsia"), "方正小标宋简体")

    # p1 正文 + 占位符（单 run）
    _body_para(doc, "各单位：现将{{ YEAR }}年度检查事项通知如下，请遵照执行。")

    # p2 混排：黑体加粗小标题 + 仿宋正文（两个 run，格式不同）
    p2 = _body_para(doc, "")
    r1 = p2.add_run("一、总体要求")
    r1.font.size = Pt(16)
    r1.font.bold = True
    r1.font.name = "宋体"
    r1._element.rPr.rFonts.set(dqn("w:eastAsia"), "黑体")
    r2 = p2.add_run("。坚持问题导向，全面排查隐患，确保不留死角。")
    r2.font.size = Pt(16)
    r2.font.name = "宋体"
    r2._element.rPr.rFonts.set(dqn("w:eastAsia"), "仿宋_GB2312")

    # p3 后面会被注入目录域；p4 超链接；p5 修订痕迹；p6 横向分节；p7 图片
    _body_para(doc, "二、检查范围与重点内容。")
    _body_para(doc, "依据《安全生产法》相关规定执行。")
    _body_para(doc, "三、工作要求。")
    _body_para(doc, "", indent=False)
    _body_para(doc, "附：现场示意图。")

    # p8~p10 编号列表
    for txt in ("消防设施是否完好", "疏散通道是否畅通", "值班记录是否完整"):
        p = doc.add_paragraph()
        pPr = p._p.get_or_add_pPr()
        numPr = OxmlElement("w:numPr")
        ilvl = OxmlElement("w:ilvl")
        ilvl.set(dqn("w:val"), "0")
        numPr.append(ilvl)
        numId = OxmlElement("w:numId")
        numId.set(dqn("w:val"), "1")
        numPr.append(numId)
        pPr.append(numPr)
        r = p.add_run(txt)
        r.font.size = Pt(16)
        r.font.name = "宋体"
        r._element.rPr.rFonts.set(dqn("w:eastAsia"), "仿宋_GB2312")

    # t0 表格：固定列宽 + 固定行高
    tbl = doc.add_table(rows=2, cols=3)
    tbl.style = "Table Grid"
    for i, w in enumerate((30, 40, 50)):
        tbl.columns[i].width = Mm(w)
    tbl.rows[0].height = Mm(12)
    for j, h in enumerate(("序号", "检查项目", "结论")):
        tbl.cell(0, j).text = h
    for j, h in enumerate(("1", "消防设施", "合格")):
        tbl.cell(1, j).text = h

    # 图片（真图片部件）
    doc.add_picture(BytesIO(_PNG), width=Mm(10))

    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    # 后处理 XML：注入域 / 超链接 / 修订 / 书签 / 横向分节
    zf = zipfile.ZipFile(BytesIO(raw))
    root = etree.fromstring(zf.read("word/document.xml"))
    body = root.find(qn("body"))
    ps = body.findall(qn("p"))

    # p3 追加目录域（fldSimple）
    fld = etree.SubElement(ps[3], qn("fldSimple"))
    fld.set(qn("instr"), r'TOC \o "1-3" \h \z \u')
    fr = etree.SubElement(fld, qn("r"))
    frpr = etree.SubElement(fr, qn("rPr"))
    rf = etree.SubElement(frpr, qn("rFonts"))
    rf.set(qn("ascii"), "Times New Roman")
    rf.set(qn("eastAsia"), "仿宋_GB2312")
    sz = etree.SubElement(frpr, qn("sz"))
    sz.set(qn("val"), "32")
    ft = etree.SubElement(fr, qn("t"))
    ft.text = "目录占位"

    # p4 整体包进超链接
    p4 = ps[4]
    link = etree.SubElement(p4, qn("hyperlink"))
    link.set(qn("anchor"), "_Toc12345")
    for r in p4.findall(qn("r")):
        p4.remove(r)
        link.append(r)

    # p5 整体包进修订插入
    p5 = ps[5]
    ins = etree.Element(qn("ins"))
    ins.set(qn("id"), "101")
    ins.set(qn("author"), "张三")
    ins.set(qn("date"), "2026-11-09T00:00:00Z")
    for r in p5.findall(qn("r")):
        p5.remove(r)
        ins.append(r)
    p5.append(ins)

    # p2 加书签（目录跳转目标）
    bms = etree.Element(qn("bookmarkStart"))
    bms.set(qn("id"), "7")
    bms.set(qn("name"), "_Toc00001")
    ps[2].insert(0, bms)
    bme = etree.SubElement(ps[2], qn("bookmarkEnd"))
    bme.set(qn("id"), "7")

    # p6 变成横向分节（公文里表格横排就靠它）
    p6 = ps[6]
    pPr = p6.find(qn("pPr"))
    if pPr is None:
        pPr = etree.Element(qn("pPr"))
        p6.insert(0, pPr)
    sect = etree.SubElement(pPr, qn("sectPr"))
    pgSz = etree.SubElement(sect, qn("pgSz"))
    pgSz.set(qn("w"), "16838")
    pgSz.set(qn("h"), "11906")
    pgSz.set(qn("orient"), "landscape")

    new_xml = etree.tostring(root, xml_declaration=True, encoding="UTF-8", standalone=True)
    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zf.infolist():
            data = zf.read(item.filename)
            if item.filename == "word/document.xml":
                data = new_xml
            zout.writestr(item, data)
    return out.getvalue()


# --------------------------------------------------------------------------- #
# 指纹工具
# --------------------------------------------------------------------------- #
def _zip_map(raw: bytes):
    zf = zipfile.ZipFile(BytesIO(raw))
    return {n: zf.read(n) for n in zf.namelist()}


def _body_children(raw: bytes):
    """正文每个顶层元素单独序列化（命名空间前缀相同，可直接比字节）。"""
    root = etree.fromstring(zipfile.ZipFile(BytesIO(raw)).read("word/document.xml"))
    return [etree.tostring(c) for c in root.find(qn("body"))]


def _ppr(p_xml: bytes) -> bytes:
    p = etree.fromstring(p_xml)
    pPr = p.find(qn("pPr"))
    return etree.tostring(pPr) if pPr is not None else b""


def _runs(p_xml: bytes):
    """[(rPr 字节, 文字)]，文档顺序，含容器里的 run。"""
    p = etree.fromstring(p_xml)
    out = []
    for r in p.iter(qn("r")):
        rpr = r.find(qn("rPr"))
        txt = "".join(t.text or "" for t in r.findall(qn("t")))
        out.append((etree.tostring(rpr) if rpr is not None else b"", txt))
    return out


def _run_props(p_xml: bytes):
    """逐 run 的可见格式 [(eastAsia 字体, 字号, 是否加粗, 颜色)]。

    不比字节，读属性——lxml 序列化中文属性值会转成 &#NNNNN; 实体，直接
    ``b"仿宋_GB2312" in xml`` 永远不成立，那样的断言只是自欺欺人。
    """
    p = etree.fromstring(p_xml)
    out = []
    for r in p.iter(qn("r")):
        rpr = r.find(qn("rPr"))
        ea = sz = color = None
        bold = False
        if rpr is not None:
            rf = rpr.find(qn("rFonts"))
            ea = rf.get(qn("eastAsia")) if rf is not None else None
            s = rpr.find(qn("sz"))
            sz = s.get(qn("val")) if s is not None else None
            bold = rpr.find(qn("b")) is not None
            c = rpr.find(qn("color"))
            color = c.get(qn("val")) if c is not None else None
        out.append((ea, sz, bold, color))
    return out


def _text(p_xml: bytes) -> str:
    return paragraph_text(etree.fromstring(p_xml))


def _counts(raw: bytes):
    root = etree.fromstring(zipfile.ZipFile(BytesIO(raw)).read("word/document.xml"))
    out = {}
    for tag in ("fldSimple", "instrText", "fldChar", "hyperlink", "drawing",
                "bookmarkStart", "bookmarkEnd", "sectPr", "ins", "numPr",
                "ind", "spacing", "jc", "trHeight", "gridCol", "tcW"):
        out[tag] = len(root.findall(".//" + qn(tag)))
    return out


def _block(model, bid):
    return next(b for b in model["blocks"] if b["id"] == bid)


def _edited(raw: bytes, **texts):
    """按 {块 id: 新文字} 改模型（不碰传入的原模型）。"""
    model = copy.deepcopy(parse_docx(raw))
    for bid, txt in texts.items():
        _block(model, bid)["text"] = txt
    return model


@pytest.fixture(scope="module")
def trap_docx() -> bytes:
    return build_format_trap_docx()


# --------------------------------------------------------------------------- #
# A. 原样回写：一个字节都不许变
# --------------------------------------------------------------------------- #
def test_untouched_document_keeps_every_element_bytes(trap_docx):
    """模型没改动 -> 正文每个元素逐字节一致。

    这条是"格式不变"的地基：只要它红，说明回写在用户没编辑的地方动了东西。
    """
    out = apply_model(trap_docx, parse_docx(trap_docx))
    before = _body_children(trap_docx)
    after = _body_children(out)
    assert len(after) == len(before)
    for i, (b, a) in enumerate(zip(before, after)):
        assert a == b, "第 %d 个正文元素被改动（模型未变）" % i


def test_untouched_document_keeps_other_parts_bytes(trap_docx):
    """样式表 / 编号 / 页眉页脚 / 字体表等部件必须逐字节原样复制。"""
    out = apply_model(trap_docx, parse_docx(trap_docx))
    before, after = _zip_map(trap_docx), _zip_map(out)
    assert set(after) == set(before)
    for name in before:
        if name == "word/document.xml":
            continue
        assert after[name] == before[name], name


def test_output_opens_as_docx(trap_docx):
    """回写结果必须是 Word/python-docx 都能打开的合法包。"""
    out = apply_model(trap_docx, _edited(trap_docx, p1="各单位：现将2026年度检查事项通知如下，请遵照执行。"))
    doc = Document(BytesIO(out))
    assert len(doc.paragraphs) >= 8


def test_real_business_template_roundtrip_is_byte_stable(business_template_bytes):
    """拿仓库里真实的业务模板（标题/表格/循环行/图表）走一遍原样回写。

    合成夹具能控制变量，真实模板才能证明"没被合成数据哄过去"。
    """
    raw = business_template_bytes
    out = apply_model(raw, parse_docx(raw))
    before, after = _body_children(raw), _body_children(out)
    assert len(after) == len(before)
    for i, (b, a) in enumerate(zip(before, after)):
        assert a == b, "第 %d 个正文元素被改动（模型未变）" % i


# --------------------------------------------------------------------------- #
# B. 段落属性：缩进 / 行距 / 段前段后 / 对齐
# --------------------------------------------------------------------------- #
def test_edited_paragraph_keeps_ppr_bytes(trap_docx):
    """改文字后，pPr 一个字节都不许变（首行缩进 2 字符、固定行距 28 磅、两端对齐）。"""
    out = apply_model(trap_docx, _edited(
        trap_docx, p1="各单位：现将2026年度检查事项通知如下，请各单位遵照执行。"))
    before, after = _body_children(trap_docx), _body_children(out)
    assert _ppr(after[1]) == _ppr(before[1])

    ppr = _ppr(after[1])
    assert b'w:firstLineChars="200"' in ppr, "首行缩进 2 字符丢了"
    assert b'w:firstLine="640"' in ppr
    assert b'w:line="560"' in ppr, "固定行距丢了"
    assert b'w:lineRule="exact"' in ppr
    assert b'w:jc' in ppr and b'w:val="both"' in ppr


def test_paragraph_ppr_unchanged_for_all_untouched_paragraphs(trap_docx):
    """只改 p1，其余段落的 pPr 全部逐字节一致。"""
    out = apply_model(trap_docx, _edited(trap_docx, p1="各单位：现将2026年度检查事项通知如下。"))
    before, after = _body_children(trap_docx), _body_children(out)
    for i in range(len(before)):
        if i == 1:
            continue
        assert _ppr(after[i]) == _ppr(before[i]), "第 %d 段 pPr 被改动" % i


# --------------------------------------------------------------------------- #
# C. 字符属性：字体 / 字号 / 加粗 / 每个 run 的 rPr
# --------------------------------------------------------------------------- #
def test_edited_paragraph_keeps_every_run_rpr(trap_docx):
    """混排段落：黑体加粗小标题 + 仿宋正文，改正文后两个 run 的 rPr 都不变。"""
    out = apply_model(trap_docx, _edited(
        trap_docx, p2="一、总体要求。坚持问题导向，全面排查风险隐患，确保不留死角。"))
    before, after = _body_children(trap_docx), _body_children(out)

    rb, ra = _runs(before[2]), _runs(after[2])
    assert len(ra) == len(rb), "run 数量变了：%d -> %d" % (len(rb), len(ra))
    assert [x[0] for x in ra] == [x[0] for x in rb], "run 的 rPr 序列被改动"
    # 小标题仍是黑体加粗，正文仍是仿宋
    pb, pa = _run_props(before[2]), _run_props(after[2])
    assert pa == pb
    assert pa[0] == ("黑体", "32", True, None), "加粗小标题的格式变了"
    assert pa[1][0] == "仿宋_GB2312"


def test_font_size_and_east_asia_survive_edit(trap_docx):
    """三号（sz=32）与 eastAsia 字体在改文字后必须原样在。"""
    out = apply_model(trap_docx, _edited(
        trap_docx, p1="各单位：现将2026年度检查事项通知如下，请各单位遵照执行。"))
    after = _body_children(out)
    props = [x for x, (_rpr, t) in zip(_run_props(after[1]), _runs(after[1])) if t]
    assert props, "段落被写空了"
    ea, sz, _bold, _color = props[0]
    assert sz == "32", "字号丢了：%r" % (sz,)
    assert ea == "仿宋_GB2312", "中文字体丢了：%r" % (ea,)


def test_title_paragraph_untouched_when_other_paragraph_edited(trap_docx):
    """标题（方正小标宋二号加粗）不被顺带改动。"""
    out = apply_model(trap_docx, _edited(trap_docx, p1="各单位：现将2026年度检查事项通知如下。"))
    before, after = _body_children(trap_docx), _body_children(out)
    assert after[0] == before[0]


# --------------------------------------------------------------------------- #
# D/E. 域、超链接、修订、书签、分节符
# --------------------------------------------------------------------------- #
def test_field_paragraph_is_marked_read_only_at_parse(trap_docx):
    """含域的段落在 parse 阶段就要标成只读，别让用户在编辑器里白改一场。

    域里的文字是 Word 算出来的：改了下次更新域就回滚，清掉结构则立刻生效
    （目录变白、页码变「第 页 共 页」）。两种都是静默失败，所以直接不给改。
    """
    blk = _block(parse_docx(trap_docx), "p3")
    assert blk["type"] == "unsupported"
    assert "域" in blk["note"]
    assert "目录占位" in blk["text"]


def test_field_guard_refuses_rewrite_that_eats_field(trap_docx):
    """绕过模型直接调回写：改动吃掉落域结果文字时，整段拒改、一个字节不动。

    这是 apply 侧的最后一道闸——parse 标只读之外，回写函数自己也不许碰域，
    因为"结构解析与回写是两套独立代码，不能指望另一处刚好挡住了"。
    """
    root = etree.fromstring(zipfile.ZipFile(BytesIO(trap_docx)).read("word/document.xml"))
    p = [c for c in root.find(qn("body")) if etree.QName(c).localname == "p"][3]
    before = etree.tostring(p)
    ok = _rewrite_paragraph_text(p, "二、检查范围与重点内容（修订）。")  # 丢掉了"目录占位"
    assert ok is False, "含域的段落被改写了"
    assert etree.tostring(p) == before, "拒改时却动了 XML"


def test_field_guard_allows_edit_beside_field(trap_docx):
    """只改域前面的文字：该改的要改到，域结构必须毫发无损。"""
    root = etree.fromstring(zipfile.ZipFile(BytesIO(trap_docx)).read("word/document.xml"))
    p = [c for c in root.find(qn("body")) if etree.QName(c).localname == "p"][3]
    fld_before = etree.tostring(p.find(qn("fldSimple")))

    ok = _rewrite_paragraph_text(p, "二、检查范围与重点内容（修订）。目录占位")
    assert ok is True
    assert paragraph_text(p) == "二、检查范围与重点内容（修订）。目录占位"
    assert etree.tostring(p.find(qn("fldSimple"))) == fld_before


def test_tracked_insertion_text_not_duplicated(trap_docx):
    """修订痕迹里的段落：改文字不许重复原文，也不许丢字体字号。

    老实现把 w:ins 里的 run 全部删掉重建，结果是
    「三、工作要求。三、工作要求与保障措施。」+ 一个没有 rFonts 没有 sz 的 run。
    """
    out = apply_model(trap_docx, _edited(trap_docx, p5="三、工作要求与保障措施。"))
    after = _body_children(out)
    assert _text(after[5]) == "三、工作要求与保障措施。"
    assert _counts(out)["ins"] == _counts(trap_docx)["ins"]
    runs = _runs(after[5])
    assert len([x for x in runs if x[1]]) == 1, "修订段落被拆成了多个 run"
    props = [pp for pp, (_rpr, t) in zip(_run_props(after[5]), runs) if t]
    assert props[0][1] == "32", "修订段落的字号丢了"
    assert props[0][0] == "仿宋_GB2312", "修订段落的字体丢了"


def test_hyperlink_survives_text_edit(trap_docx):
    """超链接段落改文字后，链接本身还在。"""
    out = apply_model(trap_docx, _edited(
        trap_docx, p4="依据《安全生产法》及配套规章执行。"))
    after = _body_children(out)
    assert _counts(out)["hyperlink"] == _counts(trap_docx)["hyperlink"]
    pa = etree.fromstring(after[4])
    assert pa.find(qn("hyperlink")) is not None
    assert _text(after[4]) == "依据《安全生产法》及配套规章执行。"


def test_deleting_section_paragraph_keeps_section(trap_docx):
    """删掉承载分节符的段落时，分节符必须留下（否则横向页/页边距一起没）。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    model["blocks"] = [b for b in model["blocks"] if b["id"] != "p6"]
    out = apply_model(trap_docx, model)
    assert _counts(out)["sectPr"] == _counts(trap_docx)["sectPr"]
    xml = zipfile.ZipFile(BytesIO(out)).read("word/document.xml")
    assert b'w:orient="landscape"' in xml, "横向页面设置丢了"


def test_deleted_paragraph_bookmarks_are_relocated(trap_docx):
    """删掉带书签的段落时，书签要搬到邻居上，不能凭空消失。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    model["blocks"] = [b for b in model["blocks"] if b["id"] != "p2"]
    out = apply_model(trap_docx, model)
    assert _counts(out)["bookmarkStart"] == _counts(trap_docx)["bookmarkStart"]
    assert _counts(out)["bookmarkEnd"] == _counts(trap_docx)["bookmarkEnd"]


# --------------------------------------------------------------------------- #
# 新建段落：只继承格式，不继承身份
# --------------------------------------------------------------------------- #
def test_new_paragraph_inherits_format_but_not_bookmark(trap_docx):
    """在带书签的段落后面插新段：继承缩进/行距/字体，但书签不许重名、不许复制。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    idx = [i for i, b in enumerate(model["blocks"]) if b["id"] == "p2"][0]
    model["blocks"].insert(idx + 1, {"id": "new-1", "type": "paragraph",
                                     "text": "（新增一段）"})
    out = apply_model(trap_docx, model)

    assert _counts(out)["bookmarkStart"] == _counts(trap_docx)["bookmarkStart"]
    xml = zipfile.ZipFile(BytesIO(out)).read("word/document.xml")
    assert xml.count(b"_Toc00001") == 1, "书签名被复制成多份"

    after = _body_children(out)
    assert len(after) == len(_body_children(trap_docx)) + 1
    newp = after[3]
    assert _text(newp) == "（新增一段）"
    assert b'w:firstLineChars="200"' in _ppr(newp), "新段落没继承首行缩进"
    assert b'w:line="560"' in _ppr(newp), "新段落没继承行距"
    runs = [x for x in _runs(newp) if x[1]]
    assert runs and b'w:sz w:val="32"' in runs[0][0], "新段落没继承字号"
    assert _run_props(newp)[0][0] == "仿宋_GB2312", "新段落没继承字体"


def test_new_paragraph_does_not_clone_image(trap_docx):
    """在图片段落后面插新段：绝不能把图片复制一份。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    idx = [i for i, b in enumerate(model["blocks"]) if b["id"] == "p7"][0]
    model["blocks"].insert(idx + 1, {"id": "new-1", "type": "paragraph",
                                     "text": "图注：现场照片"})
    out = apply_model(trap_docx, model)
    assert _counts(out)["drawing"] == _counts(trap_docx)["drawing"]
    after = _body_children(out)
    newp = etree.fromstring(after[idx + 1])
    assert len(newp.findall(".//" + qn("drawing"))) == 0


def test_new_paragraph_does_not_duplicate_section(trap_docx):
    """在承载分节符的段落后面插新段：不许再多一个分节符。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    idx = [i for i, b in enumerate(model["blocks"]) if b["id"] == "p6"][0]
    model["blocks"].insert(idx + 1, {"id": "new-1", "type": "paragraph", "text": "（说明）"})
    out = apply_model(trap_docx, model)
    assert _counts(out)["sectPr"] == _counts(trap_docx)["sectPr"]


# --------------------------------------------------------------------------- #
# 列表：编号与内容（老实现一存就整段丢）
# --------------------------------------------------------------------------- #
def test_list_items_survive_roundtrip(trap_docx):
    """原样回写：3 个列表项一段都不能少，编号属性也不能丢。"""
    out = apply_model(trap_docx, parse_docx(trap_docx))
    assert _counts(out)["numPr"] == _counts(trap_docx)["numPr"] == 3
    texts = [_text(x) for x in _body_children(out)]
    for t in ("消防设施是否完好", "疏散通道是否畅通", "值班记录是否完整"):
        assert t in texts


def test_list_item_edit_keeps_numbering_and_siblings(trap_docx):
    """改列表第二项：只改这一项，其余项与编号格式不动。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    lst = next(b for b in model["blocks"] if b["type"] == "list")
    lst["items"][1]["text"] = "疏散通道是否畅通（已整改）"
    out = apply_model(trap_docx, model)

    texts = [_text(x) for x in _body_children(out)]
    assert "疏散通道是否畅通（已整改）" in texts
    assert "消防设施是否完好" in texts
    assert "值班记录是否完整" in texts
    assert _counts(out)["numPr"] == 3


def test_list_block_without_item_ids_still_applies(trap_docx):
    """老客户端（缓存里的前端版本）不带 item.id：按起始序号兜底，也不许丢列表。

    兼容路径也要有断言——它是最容易在"新代码写好了、老请求还在路上"时出事的地方。
    """
    model = copy.deepcopy(parse_docx(trap_docx))
    lst = next(b for b in model["blocks"] if b["type"] == "list")
    for it in lst["items"]:
        it.pop("id", None)
    lst["items"][0]["text"] = "消防设施是否完好（复查）"
    out = apply_model(trap_docx, model)

    texts = [_text(x) for x in _body_children(out)]
    assert "消防设施是否完好（复查）" in texts
    assert "值班记录是否完整" in texts
    assert _counts(out)["numPr"] == 3


# --------------------------------------------------------------------------- #
# 表格：列宽 / 行高 / 单元格格式
# --------------------------------------------------------------------------- #
def test_table_geometry_unchanged_when_cell_text_edited(trap_docx):
    """改单元格文字后，列宽（gridCol/tcW）与行高（trHeight）必须原样。"""
    model = copy.deepcopy(parse_docx(trap_docx))
    tbl = next(b for b in model["blocks"] if b["type"] == "table")
    tbl["rows"][1]["cells"][1]["blocks"][0]["text"] = "消防设施（复查）"
    out = apply_model(trap_docx, model)

    cb, ca = _counts(trap_docx), _counts(out)
    for tag in ("gridCol", "tcW", "trHeight"):
        assert ca[tag] == cb[tag], "%s 数量变了" % tag

    rb = etree.fromstring(zipfile.ZipFile(BytesIO(trap_docx)).read("word/document.xml"))
    ra = etree.fromstring(zipfile.ZipFile(BytesIO(out)).read("word/document.xml"))
    assert [g.get(qn("w")) for g in ra.iter(qn("gridCol"))] == \
           [g.get(qn("w")) for g in rb.iter(qn("gridCol"))]
    assert [h.get(qn("val")) for h in ra.iter(qn("trHeight"))] == \
           [h.get(qn("val")) for h in rb.iter(qn("trHeight"))]


def test_table_untouched_when_only_body_text_edited(trap_docx):
    """只改正文段落时，表格元素逐字节一致。"""
    out = apply_model(trap_docx, _edited(trap_docx, p1="各单位：现将2026年度检查事项通知如下。"))
    before, after = _body_children(trap_docx), _body_children(out)
    tb = [x for x in before if b"<w:tbl" in x]
    ta = [x for x in after if b"<w:tbl" in x]
    assert ta == tb


# --------------------------------------------------------------------------- #
# 渲染链路（docxtpl）也不能顺手改格式
# --------------------------------------------------------------------------- #
def test_render_keeps_every_paragraph_but_the_substituted_one(trap_docx):
    """填数据渲染：只有含占位符的那一段会变，其余段落逐字节一致。

    这条防的是"改一处渲染逻辑，顺手把所有人的模板格式改了"。图片段落是例外：
    docxtpl 会把 wp:docPr 的 id 重新编号（避免同文档 id 冲突），属语义等价改动，
    因此单独断言它的尺寸与数量不变。
    """
    from app.render import render_template

    out = render_template(trap_docx, {"YEAR": "2026"}, fmt="docx")
    before, after = _body_children(trap_docx), _body_children(out)
    assert len(after) == len(before)

    for i, (b, a) in enumerate(zip(before, after)):
        if i in (1, 12):          # 1 = 被替换的占位符段；12 = 图片段（docPr 重编号）
            continue
        assert a == b, "渲染改动了第 %d 段" % i

    # 被替换的那段：文字换了，段落与字符格式都没换
    assert _text(after[1]) == "各单位：现将2026年度检查事项通知如下，请遵照执行。"
    assert _ppr(after[1]) == _ppr(before[1])
    assert _run_props(after[1]) == _run_props(before[1])

    # 图片段：图片还在，尺寸没变
    pa = etree.fromstring(after[12])
    assert len(pa.findall(".//" + qn("drawing"))) == 1
    ext = [e.get("cx") for e in pa.iter(qn("extent"))]
    pb = etree.fromstring(before[12])
    assert ext == [e.get("cx") for e in pb.iter(qn("extent"))]
