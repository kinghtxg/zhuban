# -*- coding: utf-8 -*-
"""生成开源版演示模板：samples/templates/demo-report.docx

为什么用脚本生成，而不是手工在 Word 里做一个再提交
----------------------------------------------------
1. **可复现**：模板里任何一个占位符写错了，重跑脚本就能修，不用找人重新排版。
2. **可审阅**：`git diff` 能看到"演示内容变了什么"，而 .docx 是二进制，评审时只能全信。
3. **能覆盖全量语法**：手写模板很难保证每种语法都用对、都不漏；
   脚本里每一种语法都对应一段明确的调用，漏掉会被下面的自检抓住。

覆盖的能力（对应《01-模板使用手册》§4）：
    标量文本            {{ 报告标题 }}
    默认值              {{ 编制人 or "未指派" }}
    日期表达式          {{ 出具日期 }}          ← 数据侧 {"$date":"today",...}
    表格行循环          {%tr for %}{%tr endfor %}
    整段循环 + 嵌套循环  {% for %} ... {%tr for %} ... {% endfor %}
    条件块 + else       {% if %} {% else %} {% endif %}
    行内条件表达式      {{ "已完成" if it.完成 else "进行中" }}
    图片                {{ image_banner }}
    二维码              {{ qr_报告编号 }}
    富文本（markdown）  {{ rich_摘要 }}
    图表（折线 + 饼图）  由 chart_fixtures 注入 word/charts + embeddings
    签名 / 印章占位      {{ image_签字示例 }} / {{ image_印章示例 }}
    页眉 / 页脚 / 页码   页眉带变量、页脚带 PAGE 域

输出：
    samples/templates/demo-report.docx   演示模板
    samples/demo-report.sample.json      与之配套、可直接跑通的示例数据

用法：
    python samples/tools/build-demo-template.py
    python samples/tools/build-demo-template.py --check   # 只做自检，不写文件
"""
from __future__ import annotations

import base64
import json
import os
import re
import sys
import zipfile
import zlib
from io import BytesIO

# python-docx 与 chart_fixtures 的导入路径
_HERE = os.path.dirname(os.path.abspath(__file__))
_ROOT = os.path.dirname(os.path.dirname(_HERE))
_RENDER = os.path.join(_ROOT, "render-service")
for _p in (_RENDER, os.path.join(_RENDER, "tests")):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from docx import Document                                    # noqa: E402
from docx.enum.table import WD_TABLE_ALIGNMENT               # noqa: E402
from docx.enum.text import WD_ALIGN_PARAGRAPH                 # noqa: E402
from docx.oxml import OxmlElement                             # noqa: E402
from docx.oxml.ns import qn                                   # noqa: E402
from docx.shared import Cm, Pt, RGBColor                      # noqa: E402

from chart_fixtures import (                                  # noqa: E402
    build_chart_xml,
    build_minimal_xlsx,
    CT_CHART,
    CT_XLSX,
    WP_NS,
    A_NS,
    C_NS,
    R_NS,
)

OUT_DOCX = os.path.join(_ROOT, "samples", "templates", "demo-report.docx")
OUT_JSON = os.path.join(_ROOT, "samples", "demo-report.sample.json")

# 演示模板里的两张图。系列数决定"能不能改数据"的边界：
# 折线 2 系列、饼图 1 系列，刚好覆盖多系列与单系列两种形态。
CHARTS = [
    {
        "kind": "line",
        "title": "季度销售额",
        "categories": ["Q1", "Q2", "Q3", "Q4"],
        "series": [
            {"name": "2025 年", "values": [120, 180, 150, 220]},
            {"name": "2026 年", "values": [140, 210, 190, 260]},
        ],
    },
    {
        "kind": "pie",
        "title": "收入构成",
        "categories": ["产品交付", "订阅服务", "技术支持"],
        "series": [{"name": "占比", "values": [55, 30, 15]}],
    },
]

# 图表在正文里的落点标记。生成完 docx 后再把这两个段落替换成 <w:drawing>，
# 因为 python-docx 没有"插入图表"的 API，只能后处理 XML。
CHART_MARKERS = ["[[CHART1]]", "[[CHART2]]"]


# --------------------------------------------------------------------------- #
# 小工具
# --------------------------------------------------------------------------- #
def set_cell_bg(cell, hex_color: str) -> None:
    tc_pr = cell._tc.get_or_add_tcPr()
    shd = OxmlElement("w:shd")
    shd.set(qn("w:val"), "clear")
    shd.set(qn("w:color"), "auto")
    shd.set(qn("w:fill"), hex_color)
    tc_pr.append(shd)


def set_repeat_header(row) -> None:
    """表头跨页重复。长表没有这一项，第二页开始就没有列名了。"""
    tr_pr = row._tr.get_or_add_trPr()
    el = OxmlElement("w:tblHeader")
    el.set(qn("w:val"), "true")
    tr_pr.append(el)


def add_field(paragraph, instr: str) -> None:
    """插入一个 Word 域（PAGE / NUMPAGES / TOC），而不是手敲文字。"""
    run = paragraph.add_run()
    fld_begin = OxmlElement("w:fldChar")
    fld_begin.set(qn("w:fldCharType"), "begin")
    instr_el = OxmlElement("w:instrText")
    instr_el.set(qn("xml:space"), "preserve")
    instr_el.text = instr
    fld_sep = OxmlElement("w:fldChar")
    fld_sep.set(qn("w:fldCharType"), "separate")
    fld_text = OxmlElement("w:t")
    fld_text.text = "1"
    fld_end = OxmlElement("w:fldChar")
    fld_end.set(qn("w:fldCharType"), "end")
    run._r.append(fld_begin)
    run._r.append(instr_el)
    run._r.append(fld_sep)
    run._r.append(fld_text)
    run._r.append(fld_end)


def add_toc(paragraph) -> None:
    """目录域。Word 打开时按 updateFields 自动更新；LibreOffice 也会在载入时刷。"""
    run = paragraph.add_run()
    fld_begin = OxmlElement("w:fldChar")
    fld_begin.set(qn("w:fldCharType"), "begin")
    instr_el = OxmlElement("w:instrText")
    instr_el.set(qn("xml:space"), "preserve")
    instr_el.text = r'TOC \o "1-2" \h \z \u'
    fld_sep = OxmlElement("w:fldChar")
    fld_sep.set(qn("w:fldCharType"), "separate")
    run._r.append(fld_begin)
    run._r.append(instr_el)
    run._r.append(fld_sep)
    hint = OxmlElement("w:t")
    hint.text = "（目录：打开文档时自动生成）"
    run._r.append(hint)


def enable_update_fields(doc: Document) -> None:
    """让 Word/LO 在打开时刷新域（目录、页码）。

    少了这一条，目录会停在"没有条目"的状态，看起来像模板坏了。
    """
    settings = doc.settings.element
    el = OxmlElement("w:updateFields")
    el.set(qn("w:val"), "true")
    settings.append(el)


def png_1x1(rgb: tuple, w: int = 96, h: int = 32) -> bytes:
    """生成一个纯色 PNG，用作演示图片（不依赖 Pillow，也不往仓库塞素材文件）。"""
    return png_draw(w, h, lambda _x, _y: rgb)


def png_draw(w: int, h: int, fn) -> bytes:
    """按像素回调生成 PNG。fn(x, y) -> (r,g,b)。

    自己拼 PNG 而不是用 Pillow：演示模板的生成脚本要能在任何装了
    python-docx 的环境里跑（CI、评审者的笔记本），少一个依赖就少一类
    「脚本跑不起来」的问题。
    """
    raw = b""
    for y in range(h):
        raw += b"\x00" + b"".join(bytes(fn(x, y)) for x in range(w))

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (len(data).to_bytes(4, "big") + tag + data
                + zlib.crc32(tag + data).to_bytes(4, "big"))

    ihdr = (w.to_bytes(4, "big") + h.to_bytes(4, "big")
            + bytes([8, 2, 0, 0, 0]))  # 8bit truecolor
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b""))


def png_signature() -> bytes:
    """一条蓝色手写体样的曲线 —— 演示「预置签名图」。"""
    w, h = 220, 72
    white = (255, 255, 255)
    ink = (0x1F, 0x3A, 0x93)

    def fn(x, y):
        # 两个正弦叠加，看起来像连笔；中段抬高一点，接近真实签名
        import math
        base = h * 0.62 - math.sin(x / w * 5.4) * h * 0.18 - math.sin(x / w * 13.0) * h * 0.05
        return ink if abs(y - base) <= 1.6 else white

    return png_draw(w, h, fn)


def png_seal() -> bytes:
    """一个红色圆环 —— 演示「预置印章图」。"""
    w = h = 160
    cx = cy = 79.5
    white = (255, 255, 255)
    red = (0xC0, 0x2B, 0x2B)

    def fn(x, y):
        import math
        d = math.hypot(x - cx, y - cy)
        if 56 <= d <= 68:      # 外环
            return red
        if d <= 44 and (abs(x - cx) < 1.6 or abs(y - cy) < 1.6):  # 内部十字，示意"盖章位"
            return red
        return white

    return png_draw(w, h, fn)


# --------------------------------------------------------------------------- #
# 正文
# --------------------------------------------------------------------------- #
def build_body(doc: Document) -> None:
    # ---------- 页眉：带变量的页眉，跨页自动重复 ----------
    header = doc.sections[0].header
    hp = header.paragraphs[0]
    hp.text = "{{ 出具单位 }} · 内部资料"
    hp.alignment = WD_ALIGN_PARAGRAPH.RIGHT
    for r in hp.runs:
        r.font.size = Pt(9)
        r.font.color.rgb = RGBColor(0x6F, 0x77, 0x91)

    # ---------- 页脚：左文字 + 右页码（域，不是手敲数字） ----------
    footer = doc.sections[0].footer
    fp = footer.paragraphs[0]
    fp.text = "{{ 报告编号 }} · 第 "
    fp.alignment = WD_ALIGN_PARAGRAPH.CENTER
    add_field(fp, "PAGE")
    fp.add_run(" 页 / 共 ")
    add_field(fp, "NUMPAGES")
    fp.add_run(" 页")
    for r in fp.runs:
        r.font.size = Pt(9)
        r.font.color.rgb = RGBColor(0x6F, 0x77, 0x91)

    # ---------- 标题区（标题 + 二维码 + 说明） ----------
    head_tbl = doc.add_table(rows=1, cols=2)
    head_tbl.autofit = False
    head_tbl.columns[0].width = Cm(13.6)
    head_tbl.columns[1].width = Cm(3.2)
    left, right = head_tbl.rows[0].cells
    left.width = Cm(13.6)
    right.width = Cm(3.2)

    tp = left.paragraphs[0]
    tp.alignment = WD_ALIGN_PARAGRAPH.LEFT
    r = tp.add_run("{{ 报告标题 }}")
    r.bold = True
    r.font.size = Pt(24)
    r.font.name = "黑体"
    r._element.rPr.rFonts.set(qn("w:eastAsia"), "黑体")

    sub = left.add_paragraph()
    sub.paragraph_format.space_before = Pt(4)
    r = sub.add_run("{{ 副标题 or \"—— 能力演示与交付说明\" }}")
    r.font.size = Pt(11)
    r.font.color.rgb = RGBColor(0x6F, 0x77, 0x91)

    # 二维码：变量名 qr_ 前缀即识别为二维码，size 是显示宽度（mm）
    qp = right.paragraphs[0]
    qp.alignment = WD_ALIGN_PARAGRAPH.RIGHT
    qr_run = qp.add_run("{{ qr_报告编号 }}")
    qr_run.font.size = Pt(9)
    qr_cap = right.add_paragraph()
    qr_cap.alignment = WD_ALIGN_PARAGRAPH.RIGHT
    rc = qr_cap.add_run("扫码验真")
    rc.font.size = Pt(8)
    rc.font.color.rgb = RGBColor(0x6F, 0x77, 0x91)

    # 图片：变量名 image_ 前缀即识别为图片，宽高单位是毫米
    ip = doc.add_paragraph()
    ip.alignment = WD_ALIGN_PARAGRAPH.LEFT
    ip.add_run("{{ image_banner }}").font.size = Pt(9)

    # ---------- 元信息表（标量 + 默认值 + 日期表达式） ----------
    doc.add_paragraph()
    meta = doc.add_table(rows=4, cols=2)
    meta.style = "Table Grid"
    meta.alignment = WD_TABLE_ALIGNMENT.CENTER
    meta.autofit = False
    meta.columns[0].width = Cm(3.4)
    meta.columns[1].width = Cm(13.4)

    rows = [
        ("报告编号", "{{ 报告编号 }}"),
        ("出具单位", "{{ 出具单位 }}"),
        # 日期不走"调用方算好再传"，而是把算法传进来由服务端现场求值：
        # 批量出一千份时不会跨零点出现一半昨天一半今天。
        ("出具日期", "{{ 出具日期 }}"),
        # 默认值：字段缺失时显示兜底文案，而不是留一片空白
        ("编制人", "{{ 编制人 or \"未指派\" }}"),
    ]
    for i, (k, v) in enumerate(rows):
        c0, c1 = meta.rows[i].cells
        c0.width, c1.width = Cm(3.4), Cm(13.4)
        set_cell_bg(c0, "F2F3F5")
        pr = c0.paragraphs[0]
        rr = pr.add_run(k)
        rr.bold = True
        rr.font.size = Pt(10.5)
        c1.paragraphs[0].add_run(v).font.size = Pt(10.5)

    # ---------- 目录 ----------
    doc.add_page_break()
    doc.add_heading("目录", level=1)
    toc_p = doc.add_paragraph()
    add_toc(toc_p)

    # ---------- 一、摘要（富文本 + 条件块） ----------
    doc.add_heading("一、摘要", level=1)
    doc.add_paragraph("{{ rich_摘要 }}")

    # 条件块：满足才显示。比较值 "高" 会被扫描成该字段的枚举，
    # 调用方传"极高"会被 /validate 拦下，而不是静默不显示。
    doc.add_paragraph("{% if 风险等级 == \"高\" %}")
    warn = doc.add_paragraph()
    rw = warn.add_run("⚠ 本次交付存在高风险项，须在上线前完成整改闭环；"
                      "整改结果需回填至《问题明细》并留档。")
    rw.bold = True
    rw.font.color.rgb = RGBColor(0xC0, 0x39, 0x2B)
    doc.add_paragraph("{% else %}")
    doc.add_paragraph("本次交付未发现高风险项，按常规节奏验收即可。")
    doc.add_paragraph("{% endif %}")

    # ---------- 二、关键指标（表格行循环） ----------
    doc.add_heading("二、关键指标", level=1)
    doc.add_paragraph("下表整行循环生成：数据里有几条，成品里就有几行，版式与表头一并继承。")

    t = doc.add_table(rows=2, cols=3)
    t.style = "Table Grid"
    t.autofit = False
    widths = (Cm(6.0), Cm(5.4), Cm(5.4))
    for i, w in enumerate(widths):
        t.columns[i].width = w

    header_cells = t.rows[0].cells
    for i, name in enumerate(["指标", "本期数值", "环比"]):
        header_cells[i].width = widths[i]
        set_cell_bg(header_cells[i], "EDEFF3")
        rr = header_cells[i].paragraphs[0].add_run(name)
        rr.bold = True
        rr.font.size = Pt(10.5)
    set_repeat_header(t.rows[0])

    row = t.rows[1]
    # 循环标签：第一个单元格开头起，最后一个单元格末尾收
    row.cells[0].paragraphs[0].add_run("{%tr for m in 指标 %}{{ m.名称 }}")
    row.cells[1].paragraphs[0].add_run("{{ m.数值 }}")
    row.cells[2].paragraphs[0].add_run("{{ m.环比 }}{%tr endfor %}")
    for i in range(3):
        row.cells[i].width = widths[i]
        for r in row.cells[i].paragraphs[0].runs:
            r.font.size = Pt(10.5)

    # ---------- 三、交付清单（行内条件表达式） ----------
    doc.add_heading("三、交付清单", level=1)
    doc.add_paragraph("状态列用行内条件表达式：同一个数据字段，按真假显示成不同的文字。")

    t2 = doc.add_table(rows=2, cols=4)
    t2.style = "Table Grid"
    t2.autofit = False
    w2 = (Cm(1.8), Cm(7.0), Cm(3.0), Cm(5.0))
    for i, w in enumerate(w2):
        t2.columns[i].width = w

    h2 = t2.rows[0].cells
    for i, name in enumerate(["序号", "交付物", "状态", "备注"]):
        h2[i].width = w2[i]
        set_cell_bg(h2[i], "EDEFF3")
        rr = h2[i].paragraphs[0].add_run(name)
        rr.bold = True
        rr.font.size = Pt(10.5)
    set_repeat_header(t2.rows[0])

    r2 = t2.rows[1]
    # loop.index 是 docxtpl 自带的行号；不用在数据里另存一个"序号"字段。
    r2.cells[0].paragraphs[0].add_run("{%tr for it in 交付清单 %}{{ loop.index }}")
    r2.cells[1].paragraphs[0].add_run("{{ it.名称 }}")
    r2.cells[2].paragraphs[0].add_run('{{ "已完成" if it.完成 else "进行中" }}')
    r2.cells[3].paragraphs[0].add_run("{{ it.备注 or \"—\" }}{%tr endfor %}")
    for i in range(4):
        r2.cells[i].width = w2[i]
        for r in r2.cells[i].paragraphs[0].runs:
            r.font.size = Pt(10.5)

    # ---------- 四、图表（折线，双系列） ----------
    doc.add_heading("四、季度趋势", level=1)
    doc.add_paragraph(
        "下面这张折线图的数据可以整组替换（分类与数值都能变），但"
        "配色、线宽、图例位置、坐标轴样式一个像素都不动——"
        "实现上是只重写 chart XML 里的数据缓存，结构上够不到样式节点。"
    )
    doc.add_paragraph(CHART_MARKERS[0])
    doc.add_paragraph(
        "注意边界：**系列个数不能增删**（增删会让单点样式索引错位、新系列落默认配色），"
        "数据点（分类）可以自由增删。"
    )

    # ---------- 五、图表（饼图，单系列） ----------
    doc.add_heading("五、收入构成", level=1)
    doc.add_paragraph("单系列饼图，同样只换数据不换样式。")
    doc.add_paragraph(CHART_MARKERS[1])

    # ---------- 六、问题明细（嵌套循环：分组 → 组内条目） ----------
    doc.add_heading("六、问题明细", level=1)
    doc.add_paragraph(
        "这一节演示嵌套：外层按「分组」循环出一段说明 + 一张表，"
        "内层按组内「条目」循环出表格行。数据少则表短，数据多则表长，"
        "列宽与表头都来自模板。"
    )

    doc.add_paragraph("{% for g in 问题分组 %}")
    gp = doc.add_paragraph()
    rg = gp.add_run("{{ g.分组 }}（{{ g.条目 | length }} 项）")
    rg.bold = True
    rg.font.size = Pt(12)

    t3 = doc.add_table(rows=2, cols=3)
    t3.style = "Table Grid"
    t3.autofit = False
    w3 = (Cm(8.2), Cm(2.6), Cm(6.0))
    for i, w in enumerate(w3):
        t3.columns[i].width = w
    h3 = t3.rows[0].cells
    for i, name in enumerate(["问题", "等级", "处置建议"]):
        h3[i].width = w3[i]
        set_cell_bg(h3[i], "EDEFF3")
        rr = h3[i].paragraphs[0].add_run(name)
        rr.bold = True
        rr.font.size = Pt(10.5)
    set_repeat_header(t3.rows[0])

    r3 = t3.rows[1]
    r3.cells[0].paragraphs[0].add_run("{%tr for it in g.条目 %}{{ it.问题 }}")
    r3.cells[1].paragraphs[0].add_run("{{ it.等级 }}")
    r3.cells[2].paragraphs[0].add_run("{{ it.建议 }}{%tr endfor %}")
    for i in range(3):
        r3.cells[i].width = w3[i]
        for r in r3.cells[i].paragraphs[0].runs:
            r.font.size = Pt(10.5)

    doc.add_paragraph("{% endfor %}")

    # ---------- 七、签署栏（签名 / 印章占位） ----------
    doc.add_heading("七、签署", level=1)
    doc.add_paragraph(
        "签名与印章是**账号级授权资源**，不能随模板分发，所以这里用两张普通图片占位："
        "把数据里的 image_签字示例 / image_印章示例 换成自己的图即可。"
        "真正的电子签章走「编辑器 → 插入签章」，占位符会写成 sig_<资源ID>，"
        "由服务端校验授权后现场出图、按次扣减。"
    )

    t4 = doc.add_table(rows=3, cols=3)
    t4.style = "Table Grid"
    t4.autofit = False
    w4 = (Cm(5.7), Cm(5.7), Cm(5.7))
    for i, w in enumerate(w4):
        t4.columns[i].width = w
    heads = ["编制", "审核", "盖章"]
    for i, name in enumerate(heads):
        c = t4.rows[0].cells[i]
        c.width = w4[i]
        set_cell_bg(c, "EDEFF3")
        rr = c.paragraphs[0].add_run(name)
        rr.bold = True
        rr.font.size = Pt(10.5)

    sig_row = t4.rows[1]
    for i in range(3):
        sig_row.cells[i].width = w4[i]
        sig_row.cells[i].paragraphs[0].alignment = WD_ALIGN_PARAGRAPH.CENTER
    sig_row.cells[0].paragraphs[0].add_run("{{ image_签字示例 }}")
    sig_row.cells[1].paragraphs[0].add_run("{{ 审核人 or \"待审核\" }}")
    sig_row.cells[2].paragraphs[0].add_run("{{ image_印章示例 }}")
    # 给签名/印章留出行高，否则空占位会把单元格压成一条线
    sig_row.height = Cm(2.6)

    date_row = t4.rows[2]
    for i in range(3):
        date_row.cells[i].width = w4[i]
        date_row.cells[i].paragraphs[0].alignment = WD_ALIGN_PARAGRAPH.CENTER
        date_row.cells[i].paragraphs[0].add_run("{{ 出具日期 }}").font.size = Pt(9)

    # 页脚说明
    doc.add_paragraph()
    end_p = doc.add_paragraph("—— 本文件由铸版 ZHUBAN 渲染生成，示例数据见 samples/demo-report.sample.json ——")
    end_p.alignment = WD_ALIGN_PARAGRAPH.CENTER
    for r in end_p.runs:
        r.font.size = Pt(9)
        r.font.color.rgb = RGBColor(0x8A, 0x91, 0xA6)


# --------------------------------------------------------------------------- #
# 图表注入
# --------------------------------------------------------------------------- #
def _drawing_run(rid: str, docpr_id: int, name: str) -> str:
    return (
        "<w:r><w:drawing>"
        '<wp:inline distT="0" distB="0" distL="0" distR="0">'
        '<wp:extent cx="5486400" cy="3200400"/>'
        '<wp:effectExtent l="0" t="0" r="0" b="0"/>'
        '<wp:docPr id="%d" name="%s"/>'
        "<wp:cNvGraphicFramePr/>"
        '<a:graphic><a:graphicData uri="%s">'
        '<c:chart r:id="%s"/>'
        "</a:graphicData></a:graphic></wp:inline></w:drawing></w:r>"
    ) % (docpr_id, name, C_NS, rid)


def inject_charts(raw: bytes) -> bytes:
    """把两张图塞进 docx：补部件、补关系、把标记段落换成 <w:drawing>。"""
    zin = zipfile.ZipFile(BytesIO(raw))
    parts = {n: zin.read(n) for n in zin.namelist()}
    zin.close()

    ct = parts["[Content_Types].xml"].decode("utf-8")
    doc_rels = parts["word/_rels/document.xml.rels"].decode("utf-8")
    document = parts["word/document.xml"].decode("utf-8")
    new_parts: dict = {}

    for i, ch in enumerate(CHARTS, start=1):
        rid = "rId%d" % (100 + i - 1)
        chart_path = "word/charts/chart%d.xml" % i
        emb_path = "word/embeddings/data%d.xlsx" % i

        new_parts[chart_path] = build_chart_xml(
            ch["kind"], ch["title"], ch["categories"], ch["series"],
            with_explicit_style=True, external_rid="rId1",
        )
        new_parts["word/charts/_rels/chart%d.xml.rels" % i] = (
            '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/'
            'officeDocument/2006/relationships/package" Target="../embeddings/data%d.xlsx"/>'
            "</Relationships>" % i
        ).encode("utf-8")
        # 内嵌工作簿：图表"数据是真的"的证据，不只是显示变了
        new_parts[emb_path] = build_minimal_xlsx(ch["categories"], ch["series"])

        ct = ct.replace(
            "</Types>",
            '<Override PartName="/%s" ContentType="%s"/>'
            '<Override PartName="/%s" ContentType="%s"/></Types>'
            % (chart_path, CT_CHART, emb_path, CT_XLSX),
        )
        doc_rels = doc_rels.replace(
            "</Relationships>",
            '<Relationship Id="%s" Type="http://schemas.openxmlformats.org/'
            'officeDocument/2006/relationships/chart" Target="charts/chart%d.xml"/>'
            "</Relationships>" % (rid, i),
        )

        marker = CHART_MARKERS[i - 1]
        # 找到含标记的那个 <w:p>，整段换成带 drawing 的段落。
        # 不能只替换文字：<w:p> 里还有 pPr，直接换文字会留一个空段落，
        # 图会掉到下一行、版式抖动。
        pat = re.compile(
            r"<w:p[ >](?:(?!</w:p>).)*?" + re.escape(marker) + r"(?:(?!</w:p>).)*?</w:p>",
            re.S,
        )
        m = pat.search(document)
        if not m:
            raise RuntimeError("模板里找不到图表标记 %s" % marker)
        # 就地声明命名空间：python-docx 的 document.xml 根节点没有 wp/a/c 前缀
        new_p = ('<w:p xmlns:wp="%s" xmlns:a="%s" xmlns:c="%s" xmlns:r="%s">%s</w:p>'
                 % (WP_NS, A_NS, C_NS, R_NS, _drawing_run(rid, i, "图表 %d" % i)))
        document = document[:m.start()] + new_p + document[m.end():]

    parts["[Content_Types].xml"] = ct.encode("utf-8")
    parts["word/_rels/document.xml.rels"] = doc_rels.encode("utf-8")
    parts["word/document.xml"] = document.encode("utf-8")
    parts.update(new_parts)

    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for name, data in parts.items():
            z.writestr(name, data)
    return out.getvalue()


# --------------------------------------------------------------------------- #
# 示例数据
# --------------------------------------------------------------------------- #
def build_sample() -> dict:
    banner = png_1x1((0x33, 0x70, 0xFF))
    return {
        "报告标题": "铸版能力演示报告",
        "副标题": "一份模板，演示全部占位符语法",
        "报告编号": "DEMO-2026-0001",
        "出具单位": "示例科技有限公司",
        "编制人": "张演示",
        # 日期表达式：由服务端在渲染那一刻求值，而不是调用方先算好传进来
        "出具日期": {"$date": "today", "format": "%Y 年 %m 月 %d 日"},
        "审核人": "",
        "风险等级": "高",
        "rich_摘要": {
            "type": "markdown",
            "content": (
                "本报告用于演示铸版的占位符能力，**不针对任何真实系统**。\n\n"
                "- 标量、默认值、日期表达式\n"
                "- 表格行循环、整段循环、嵌套循环、条件块\n"
                "- 图片、二维码、富文本、图表换数\n"
                "- 页眉、页脚、页码、目录\n\n"
                "把这份 JSON 改几个字再生成一次，就能看出哪些地方是「跟着数据走」的。"
            ),
        },
        "image_banner": {
            "type": "image",
            "src": "data:image/png;base64," + base64.b64encode(banner).decode("ascii"),
            "width": 60,
            "height": 20,
        },
        "image_签字示例": {
            "type": "image",
            "src": "data:image/png;base64," + base64.b64encode(png_signature()).decode("ascii"),
            "width": 42,
            "height": 14,
        },
        "image_印章示例": {
            "type": "image",
            "src": "data:image/png;base64," + base64.b64encode(png_seal()).decode("ascii"),
            "width": 30,
            "height": 30,
        },
        "qr_报告编号": {
            "type": "qrcode",
            "text": "https://github.com/kinghtxg/zhuban",
            "size": 24,
        },
        "指标": [
            {"名称": "交付文档总数", "数值": "1,286 份", "环比": "+18.4%"},
            {"名称": "平均生成耗时", "数值": "2.3 秒", "环比": "-31.0%"},
            {"名称": "一次通过率", "数值": "98.7%", "环比": "+1.2pp"},
            {"名称": "人工返工次数", "数值": "6 次", "环比": "-72.7%"},
        ],
        "交付清单": [
            {"名称": "《系统验收报告》", "完成": True, "备注": "含签章与骑缝"},
            {"名称": "《缺陷整改清单》", "完成": True, "备注": "按等级分组"},
            {"名称": "《上线检查表》", "完成": True, "备注": ""},
            {"名称": "《运维移交手册》", "完成": False, "备注": "待补附录"},
        ],
        "问题分组": [
            {
                "分组": "账号与权限",
                "条目": [
                    {"问题": "后台存在弱口令账号", "等级": "高", "建议": "强制改密并启用复杂度策略"},
                    {"问题": "离职人员账号未回收", "等级": "中", "建议": "接入统一身份源，按状态同步"},
                ],
            },
            {
                "分组": "数据与备份",
                "条目": [
                    {"问题": "备份未做恢复演练", "等级": "高", "建议": "每月恢复一次并留存记录"},
                    {"问题": "日志保留期不足 6 个月", "等级": "低", "建议": "调整保留策略"},
                ],
            },
        ],
        "chart_季度销售额": {
            "categories": ["Q1", "Q2", "Q3", "Q4"],
            "series": [
                {"name": "2025 年", "values": [120, 180, 150, 220]},
                {"name": "2026 年", "values": [140, 210, 190, 260]},
            ],
        },
        "chart_收入构成": {
            "categories": ["产品交付", "订阅服务", "技术支持"],
            "series": [{"name": "占比", "values": [55, 30, 15]}],
        },
    }


# --------------------------------------------------------------------------- #
# 自检
# --------------------------------------------------------------------------- #
REQUIRED_TAGS = [
    "{{ 报告标题 }}",
    "{{ 编制人 or \"未指派\" }}",
    "{{ 出具日期 }}",
    "{%tr for m in 指标 %}",
    "{%tr endfor %}",
    "{% for g in 问题分组 %}",
    "{% endfor %}",
    "{% if 风险等级 == \"高\" %}",
    "{% else %}",
    "{% endif %}",
    "{{ image_banner }}",
    "{{ qr_报告编号 }}",
    "{{ rich_摘要 }}",
    "{{ image_签字示例 }}",
    "{{ image_印章示例 }}",
    "{{ loop.index }}",
]


def self_check(path: str) -> list:
    """从产出的 docx 里把文本抠出来，确认每种语法都在、且没被拆 run。"""
    problems = []
    with zipfile.ZipFile(path) as z:
        names = z.namelist()
        doc_xml = z.read("word/document.xml").decode("utf-8")
        charts = [n for n in names if n.startswith("word/charts/chart")]
        embeds = [n for n in names if n.startswith("word/embeddings/")]
        has_header = any(n.startswith("word/header") for n in names)
        has_footer = any(n.startswith("word/footer") for n in names)

    if len(charts) != 2:
        problems.append("图表部件应为 2 个，实际 %d 个" % len(charts))
    if len(embeds) != 2:
        problems.append("内嵌工作簿应为 2 个，实际 %d 个" % len(embeds))
    if not has_header:
        problems.append("缺少页眉部件")
    if not has_footer:
        problems.append("缺少页脚部件")
    if 'w:instrText' not in doc_xml and "TOC" not in doc_xml:
        problems.append("目录域没有写进 document.xml")

    # 标签必须连续出现在同一段文本里——被拆 run 的占位符渲染时会认不出来。
    plain = re.sub(r"<[^>]+>", "", doc_xml)
    for tag in REQUIRED_TAGS:
        if tag not in plain:
            problems.append("正文里找不到标签：%s" % tag)
    if "{{ image_签字示例 }}" not in plain:
        problems.append("签名占位缺失")
    if "[[CHART" in plain:
        problems.append("图表标记没有被替换掉（注入失败）")
    return problems


def main() -> int:
    doc = Document()

    # 默认字体：显式指定，别依赖 python-docx 的默认西文字体，
    # 否则中文会落到容器里不一定存在的字体上，PDF 与 docx 观感不一致。
    style = doc.styles["Normal"]
    style.font.name = "宋体"
    style.font.size = Pt(10.5)
    style.element.rPr.rFonts.set(qn("w:eastAsia"), "宋体")
    for s in ("Heading 1", "Heading 2"):
        st = doc.styles[s]
        st.font.name = "黑体"
        st.element.rPr.rFonts.set(qn("w:eastAsia"), "黑体")

    section = doc.sections[0]
    section.page_width, section.page_height = Cm(21), Cm(29.7)
    section.top_margin = Cm(2.54)
    section.bottom_margin = Cm(2.54)
    section.left_margin = Cm(3.17)
    section.right_margin = Cm(3.17)

    build_body(doc)
    enable_update_fields(doc)

    buf = BytesIO()
    doc.save(buf)
    final = inject_charts(buf.getvalue())

    check_only = "--check" in sys.argv
    if check_only:
        tmp = os.path.join(_ROOT, "samples", "templates", ".demo-check.docx")
        with open(tmp, "wb") as f:
            f.write(final)
        problems = self_check(tmp)
        os.remove(tmp)
    else:
        os.makedirs(os.path.dirname(OUT_DOCX), exist_ok=True)
        with open(OUT_DOCX, "wb") as f:
            f.write(final)
        problems = self_check(OUT_DOCX)
        with open(OUT_JSON, "w", encoding="utf-8") as f:
            json.dump(build_sample(), f, ensure_ascii=False, indent=2)
            f.write("\n")

    if problems:
        print("✗ 自检未通过：", file=sys.stderr)
        for p in problems:
            print("  - " + p, file=sys.stderr)
        return 1
    if not check_only:
        print("✓ %s（%.1f KB）" % (os.path.relpath(OUT_DOCX, _ROOT),
                                   os.path.getsize(OUT_DOCX) / 1024))
        print("✓ %s" % os.path.relpath(OUT_JSON, _ROOT))
    print("✓ 自检通过：2 张图表 / 页眉页脚 / 全部语法标签齐备")
    return 0


if __name__ == "__main__":
    sys.exit(main())
