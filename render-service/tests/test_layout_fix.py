# -*- coding: utf-8 -*-
"""渲染后的版面纠正（3.4.14）。

这一组盯的全是**静默失败**——接口 200、文字一字不差、`document.xml` 里也查得到，
只有肉眼比对成品才发现不对：

  1. 富文本注进去的 run 没有 ``rFonts``/``sz`` → 整段回落到主题字体。
     模板里明明写着「仿宋_GB2312 三号」，生成出来是「宋体 小四」。
  2. 编辑器回写把混排段落压平 → 加粗那半段不再加粗，红色强调词变黑。
  3. 表格单元格里的图按版心宽（146mm）摆放 → 行高被顶开、表格撑变形。

前两条是"改完就永久丢了"，第三条是"每次生成都重犯"。所以用例不仅断言
"结果对"，还要断言"证据在"：字体属性真的写在 run 上、run 数没被合并、
`wp:extent` 真的小于单元格宽。
"""
from __future__ import annotations

import io
import zipfile
from io import BytesIO

import pytest
from docx import Document
from docx.oxml.ns import qn
from docx.shared import Mm, Pt
from lxml import etree
from PIL import Image

from app import layout
from app.render import render_template

FANGSONG = "仿宋_GB2312"
MARK = "ZBInlineRich"


# --------------------------------------------------------------------------- #
# 造素材
# --------------------------------------------------------------------------- #
def png_bytes(w: int, h: int) -> bytes:
    im = Image.new("RGB", (w, h), (255, 255, 255))
    buf = io.BytesIO()
    im.save(buf, format="PNG")
    return buf.getvalue()


def _set_run_font(run, family=FANGSONG, half_pt=32):
    """把字体刷在 run 上（直接格式），模拟公文模板的真实做法。"""
    rpr = run._element.get_or_add_rPr()
    rf = rpr.makeelement(
        qn("w:rFonts"),
        {qn("w:eastAsia"): family, qn("w:ascii"): "Times New Roman", qn("w:hAnsi"): "Times New Roman"},
    )
    rpr.insert(0, rf)
    sz = rpr.makeelement(qn("w:sz"), {qn("w:val"): str(half_pt)})
    rpr.append(sz)


def docx_with_rich_placeholder() -> bytes:
    """一段「前缀 {{ content }} 后缀」，三处字体都是仿宋三号。"""
    doc = Document()
    p = doc.add_paragraph()
    for text in ("前缀 ", "{{ content }}", " 后缀"):
        _set_run_font(p.add_run(text))
    buf = BytesIO()
    doc.save(buf)
    return buf.getvalue()


def docx_single_run_with_rich() -> bytes:
    """整段只有一个 run —— Word 里最常见的形态，占位符前后文字都在这个 run 里。

    这个形态才是"尾 run 丢格式"的真现场：占位符被替换后，后面的文字落进
    我们补的那对垫片里，rPr 跟着原来的 run 一起被切走了。
    """
    doc = Document()
    p = doc.add_paragraph()
    _set_run_font(p.add_run("前缀 {{ content }} 后缀"))
    buf = BytesIO()
    doc.save(buf)
    return buf.getvalue()


def body_para(root):
    return next(root.iter(qn("w:p")))


def runs_with_text(p):
    return [r for r in p.findall(qn("w:r")) if "".join(t.text or "" for t in r.findall(qn("w:t")))]


def run_props(r):
    rpr = r.find(qn("w:rPr"))
    if rpr is None:
        return {"eastAsia": None, "sz": None, "bold": False, "color": None, "marker": None}
    rf = rpr.find(qn("w:rFonts"))
    sz = rpr.find(qn("w:sz"))
    return {
        "eastAsia": rf.get(qn("w:eastAsia")) if rf is not None else None,
        "sz": sz.get(qn("w:val")) if sz is not None else None,
        "bold": rpr.find(qn("w:b")) is not None,
        "color": (rpr.find(qn("w:color")).get(qn("w:val"))
                  if rpr.find(qn("w:color")) is not None else None),
        "marker": (rpr.find(qn("w:rStyle")).get(qn("w:val"))
                   if rpr.find(qn("w:rStyle")) is not None else None),
    }


def render(raw, data):
    return render_template(raw, data)


# --------------------------------------------------------------------------- #
# 一、注入的富文本必须继承所在段落的字体字号
# --------------------------------------------------------------------------- #
def test_injected_richtext_inherits_paragraph_font():
    """缺这一条时：整段回落到主题字体，字形字号全变，且不报任何错。"""
    out = render(docx_with_rich_placeholder(), {
        "content": {"type": "markdown", "content": "**加粗**普通"},
    })
    root = etree.fromstring(zipfile.ZipFile(BytesIO(out)).read("word/document.xml"))
    p = body_para(root)
    assert "加粗普通" in "".join(t.text or "" for t in p.iter(qn("w:t")))

    runs = runs_with_text(p)
    assert len(runs) >= 2, "markdown 里的加粗应产生独立 run"
    for r in runs:
        props = run_props(r)
        assert props["eastAsia"] == FANGSONG, (
            "run 丢了 eastAsia 字体就会回落主题字体：%r" % (props,))
        assert props["sz"] == "32", "run 丢了字号会变成默认小四：%r" % (props,)


def test_injected_richtext_keeps_its_own_bold_and_color():
    """继承字体不能把 markdown 自己的加粗/颜色盖掉。"""
    out = render(docx_with_rich_placeholder(), {
        "content": {"type": "html", "content": '<b>加粗</b><span style="color:#c00">红字</span>'},
    })
    root = etree.fromstring(zipfile.ZipFile(BytesIO(out)).read("word/document.xml"))
    runs = runs_with_text(body_para(root))
    bold = [r for r in runs if run_props(r)["bold"]]
    colored = [r for r in runs if run_props(r)["color"]]
    assert bold, "加粗丢了 —— 继承不应该覆盖注入 run 自己的 b"
    assert colored, "颜色丢了"
    assert run_props(bold[0])["eastAsia"] == FANGSONG
    assert run_props(colored[0])["sz"] == "32"


def test_injected_richtext_uses_the_placeholder_run_not_the_first_run():
    """混排段落：段首小标题是黑体，占位符在后面的仿宋 run 里。

    注入内容必须继承**占位符那个 run**的仿宋，而不是「段落第一个 run」的黑体。
    这条是端到端实测先发现的：基准格式取成"第一个带 rPr 的 run"，注入的正文
    整段变成小标题的黑体，粗细字号都不对，而所有"字体非空"的断言依然全绿。
    """
    doc = Document()
    p = doc.add_paragraph()
    _set_run_font(p.add_run("一、总体情况："), family="黑体", half_pt=32)
    p.runs[0].bold = True
    _set_run_font(p.add_run("{{ content }}"), family=FANGSONG, half_pt=32)
    buf = BytesIO()
    doc.save(buf)

    out = render(buf.getvalue(), {
        "content": {"type": "markdown", "content": "本期收入增长。"},
    })
    root = etree.fromstring(zipfile.ZipFile(BytesIO(out)).read("word/document.xml"))
    runs = runs_with_text(body_para(root))
    body = [r for r in runs if "本期收入增长" in "".join(t.text or "" for t in r.findall(qn("w:t")))]
    assert body, "注入内容没渲染出来"
    assert run_props(body[0])["eastAsia"] == FANGSONG, "继承了段首小标题的黑体"
    assert run_props(body[0])["bold"] is False, "继承了段首小标题的加粗"
    head = [r for r in runs if "总体情况" in "".join(t.text or "" for t in r.findall(qn("w:t")))]
    assert run_props(head[0])["eastAsia"] == "黑体", "小标题自己的字体被改了"


def test_injected_richtext_tail_run_also_inherits():
    """占位符后面那段文字（重开的尾 run）同样会丢 rPr，必须一起补回来。

    用单 run 形态构造：这是唯一能让"尾 run 丢格式"复现的形态，三 run 分开写
    时尾文字本来就还是它自己那个 run，测不出问题。
    """
    out = render(docx_single_run_with_rich(), {
        "content": {"type": "markdown", "content": "中间"},
    })
    root = etree.fromstring(zipfile.ZipFile(BytesIO(out)).read("word/document.xml"))
    p = body_para(root)
    tail = [r for r in runs_with_text(p) if "后缀" in "".join(t.text or "" for t in r.findall(qn("w:t")))]
    assert len(tail) == 1
    assert run_props(tail[0])["eastAsia"] == FANGSONG
    assert run_props(tail[0])["sz"] == "32"


def test_marker_never_reaches_output():
    """定位标记必须抹掉：成品里留着 rStyle 指向不存在的样式是故意挖坑。"""
    out = render(docx_with_rich_placeholder(), {
        "content": {"type": "markdown", "content": "**加粗**"},
    })
    z = zipfile.ZipFile(BytesIO(out))
    xml = z.read("word/document.xml").decode("utf-8")
    assert MARK not in xml, "标记漏到成品里了"
    # 顺带确认产出的 docx 仍是合法的 OOXML（能被 python-docx 打开）
    Document(BytesIO(out))


def test_richtext_in_loop_inherits():
    """循环体里的富文本：run 是渲染时才复制出来的，继承必须照样生效。

    这是最容易被漏掉的场景——`{% for %}` 把整段 XML 复制 N 份，每份都带一份
    注入 run；如果后处理只按"文档里第一次出现"去打补丁，第二次之后就没格式了。
    """
    doc = Document()
    p = doc.add_paragraph()
    for text in ("{% for it in items %}", "{{ it.text }}", "{% endfor %}"):
        _set_run_font(p.add_run(text))
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    out = render(raw, {"items": [
        {"text": {"type": "markdown", "content": "**甲段**"}},
        {"text": {"type": "markdown", "content": "**乙段**"}},
    ]})
    root = etree.fromstring(zipfile.ZipFile(BytesIO(out)).read("word/document.xml"))
    for token in ("甲段", "乙段"):
        hits = [
            r for r in root.iter(qn("w:r"))
            if token in "".join(t.text or "" for t in r.iter(qn("w:t")))
        ]
        assert hits, "循环体里的富文本没渲染出来：%s" % token
        assert run_props(hits[0])["eastAsia"] == FANGSONG, "%s 的字体没继承" % token
        assert run_props(hits[0])["sz"] == "32"


# --------------------------------------------------------------------------- #
# 二、表格单元格里的图片不许撑破格子
# --------------------------------------------------------------------------- #
def docx_with_image_in_cell(img_w_mm=100, cell_w_mm=40) -> bytes:
    doc = Document()
    tbl = doc.add_table(rows=1, cols=2)
    tbl.autofit = False
    for col in tbl.columns:
        col.width = Mm(cell_w_mm)
    p = tbl.cell(0, 0).paragraphs[0]
    p.add_run().add_picture(BytesIO(png_bytes(800, 400)), width=Mm(img_w_mm))
    buf = BytesIO()
    doc.save(buf)
    return buf.getvalue()


def image_extents(raw):
    root = etree.fromstring(zipfile.ZipFile(BytesIO(raw)).read("word/document.xml"))
    out = []
    for drawing in root.iter(qn("w:drawing")):
        inline = drawing.find(qn("wp:inline"))
        if inline is None:
            continue
        ext = inline.find(qn("wp:extent"))
        out.append((int(ext.get("cx")), int(ext.get("cy"))))
    return out


def test_cell_image_clamped_to_cell_width():
    """窄格里塞 100mm 宽的图：必须等比缩到格内，而不是把整行顶开。"""
    raw = docx_with_image_in_cell(img_w_mm=100, cell_w_mm=40)
    before = image_extents(raw)
    assert before and before[0][0] == int(100 * 36000)

    out = render(raw, {})
    after = image_extents(out)
    assert after, "图丢了"
    cx, cy = after[0]
    limit = int(40 * 36000)  # 40mm 格宽是上限，实际还会扣掉单元格边距
    assert cx < before[0][0], "没有缩小"
    assert cx <= limit, "缩完还比格子宽：%d EMU > %d" % (cx, limit)
    # 宽高比不能变（变等比就是压扁）
    assert abs((cx / cy) - (before[0][0] / before[0][1])) < 0.02


def test_cell_image_fits_cell_minus_margins():
    """扣掉单元格左右内边距（默认 0.19cm×2）后才算"放得下"。"""
    raw = docx_with_image_in_cell(img_w_mm=100, cell_w_mm=40)
    out = render(raw, {})
    cx = image_extents(out)[0][0]
    cell_twips = int(40 * 1440 / 25.4)
    expect = (cell_twips - 216) * 635  # 108 twips × 2 边
    assert cx <= expect + 1000, "没扣单元格内边距：%d > %d" % (cx, expect)


def test_body_image_uses_text_area_width():
    """正文里的图按版心宽夹，而不是按纸张宽——页边距加宽后必须跟着收。"""
    doc = Document()
    sec = doc.sections[0]
    sec.page_width = Mm(210)   # A4，不然 python-docx 默认是 Letter
    sec.page_height = Mm(297)
    sec.left_margin = Mm(40)
    sec.right_margin = Mm(40)  # 版心只剩 130mm
    doc.add_paragraph().add_run().add_picture(BytesIO(png_bytes(800, 400)), width=Mm(146))
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    out = render(raw, {})
    cx = image_extents(out)[0][0]
    assert cx <= int(130 * 36000) + 1000, "正文图没跟着版心收：%d" % cx


def test_small_image_is_not_enlarged():
    """只修溢出，绝不放大：图多大就多大，放大等于替作者改版面。"""
    doc = Document()
    doc.add_paragraph().add_run().add_picture(BytesIO(png_bytes(400, 200)), width=Mm(30))
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()
    before = image_extents(raw)[0][0]
    out = render(raw, {})
    assert image_extents(out)[0][0] == before


# --------------------------------------------------------------------------- #
# 三、没有可继承的格式时不能硬套
# --------------------------------------------------------------------------- #
def test_richtext_without_paragraph_format_is_untouched():
    """段落本身没刷字体（全靠样式）：不许凭空写一个字体进去。"""
    doc = Document()
    p = doc.add_paragraph()
    p.add_run("{{ content }}")
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    out = render(raw, {"content": {"type": "markdown", "content": "普通文字"}})
    z = zipfile.ZipFile(BytesIO(out))
    root = etree.fromstring(z.read("word/document.xml"))
    texts = [r for r in root.iter(qn("w:r")) if "普通文字" in "".join(t.text or "" for t in r.iter(qn("w:t")))]
    assert texts
    for r in texts:
        props = run_props(r)
        assert props["eastAsia"] is None, "无依据时不应凭空写入字体"
        assert props["marker"] is None
    Document(BytesIO(out))


def test_plain_render_without_richtext_is_byte_stable():
    """不含富文本/图片的普通渲染，版面修正阶段不应改动 document.xml。"""
    doc = Document()
    doc.add_paragraph("固定文本")
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()
    out = render(raw, {})
    assert layout.inherit_inline_format(out) == out
    assert layout.clamp_inline_images(out) == out
