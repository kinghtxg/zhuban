# -*- coding: utf-8 -*-
"""构造带图表的 docx 测试样本（不依赖 openpyxl / python-pptx）。

为什么自己拼 XML 而不是找个现成样本：
  1. 图表必须带**显式样式**（系列颜色、线宽），才能断言"只改数据不改样式"；
  2. 需要覆盖折线图（多系列）/ 饼图（单系列）/ 柱状图（多系列）三种主场景；
  3. 需要能精确控制内嵌 xlsx 的内容，用来验证"缓存 + 内嵌数据"两边同步。

产出是一个真正能被 Word / LibreOffice 打开的 docx：
  - word/document.xml 里挂 <w:drawing> 引用 rId100/rId101
  - word/charts/chart1.xml / chart2.xml 带 numCache 与 strCache
  - word/embeddings/data1.xlsx 极简工作簿（Sheet1!A1:C5）
  - [Content_Types].xml 与 word/_rels/document.xml.rels 补齐关系
"""
from __future__ import annotations

import zipfile
from io import BytesIO
from typing import List, Sequence

from docx import Document

C_NS = "http://schemas.openxmlformats.org/drawingml/2006/chart"
A_NS = "http://schemas.openxmlformats.org/drawingml/2006/main"
R_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
W_NS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
WP_NS = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"

CT_CHART = "application/vnd.openxmlformats-officedocument.drawingml.chart+xml"
CT_XLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

# 图的系列配色（显式写进 spPr，用来断言"样式没被改"）
SERIES_COLORS = ["1F77B4", "D62728", "2CA02C"]


# --------------------------------------------------------------------------- #
# 极简 xlsx：只需要 xl/worksheets/sheet1.xml 能读到数据
# --------------------------------------------------------------------------- #
def _col_letter(idx: int) -> str:
    s = ""
    idx += 1
    while idx:
        idx, rem = divmod(idx - 1, 26)
        s = chr(65 + rem) + s
    return s


def build_minimal_xlsx(categories: Sequence[str], series: Sequence[dict]) -> bytes:
    """A1=分类, B1/C1...=系列名; A2..=分类名, B2..=数值。字符串用 inlineStr。"""
    rows: List[List[str]] = []
    header = ["分类"] + [s["name"] for s in series]
    rows.append(header)
    for i, cat in enumerate(categories):
        row = [cat] + [str(s["values"][i]) for s in series]
        rows.append(row)

    cells_xml = []
    for r, row in enumerate(rows, start=1):
        cs = []
        for c, val in enumerate(row):
            ref = "%s%d" % (_col_letter(c), r)
            if r == 1 or c == 0:
                cs.append('<c r="%s" t="inlineStr"><is><t>%s</t></is></c>'
                          % (ref, _esc(val)))
            else:
                cs.append('<c r="%s"><v>%s</v></c>' % (ref, _esc(val)))
        cells_xml.append('<row r="%d">%s</row>' % (r, "".join(cs)))

    sheet = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
        "<sheetData>%s</sheetData></worksheet>" % "".join(cells_xml)
    )

    ct = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/xl/workbook.xml" '
        'ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>'
        '<Override PartName="/xl/worksheets/sheet1.xml" '
        'ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>'
        "</Types>"
    )
    rels = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" '
        'Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" '
        'Target="xl/workbook.xml"/></Relationships>'
    )
    workbook = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
        'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">'
        '<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>'
    )
    wb_rels = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" '
        'Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" '
        'Target="worksheets/sheet1.xml"/></Relationships>'
    )

    buf = BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", ct)
        z.writestr("_rels/.rels", rels)
        z.writestr("xl/workbook.xml", workbook)
        z.writestr("xl/_rels/workbook.xml.rels", wb_rels)
        z.writestr("xl/worksheets/sheet1.xml", sheet)
    return buf.getvalue()


def _esc(s: str) -> str:
    return (str(s).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;"))


# --------------------------------------------------------------------------- #
# chart XML
# --------------------------------------------------------------------------- #
def _ser_xml(idx: int, ser: dict, cats: Sequence[str], color: str,
             with_explicit_style: bool) -> str:
    """一个 c:ser。子元素顺序必须符合 schema：idx, order, tx, spPr, ..., cat, val。"""
    cat_pts = "".join('<c:pt idx="%d"><c:v>%s</c:v></c:pt>' % (i, _esc(c))
                      for i, c in enumerate(cats))
    val_pts = "".join('<c:pt idx="%d"><c:v>%s</c:v></c:pt>' % (i, v)
                      for i, v in enumerate(ser["values"]))
    style = ""
    if with_explicit_style:
        # 显式样式：线的颜色 + 宽度；这就是"不能被改掉"的东西
        style = (
            '<c:spPr><a:ln w="31750" cap="rnd"><a:solidFill><a:srgbClr val="%s"/></a:solidFill>'
            '<a:round/></a:ln></c:spPr>' % color
        )
    marker = '<c:marker><c:symbol val="circle"/><c:size val="6"/></c:marker>'
    return (
        "<c:ser>"
        '<c:idx val="%d"/><c:order val="%d"/>'
        "<c:tx><c:strRef><c:f>Sheet1!$%s$1</c:f>"
        '<c:strCache><c:ptCount val="1"/><c:pt idx="0"><c:v>%s</c:v></c:pt></c:strCache>'
        "</c:strRef></c:tx>"
        "%s"
        "%s"
        "<c:cat><c:strRef><c:f>Sheet1!$A$2:$A$%d</c:f>"
        '<c:strCache><c:ptCount val="%d"/>%s</c:strCache></c:strRef></c:cat>'
        "<c:val><c:numRef><c:f>Sheet1!$%s$2:$%s$%d</c:f>"
        "<c:numCache><c:formatCode>General</c:formatCode>"
        '<c:ptCount val="%d"/>%s</c:numCache></c:numRef></c:val>'
        "%s</c:ser>"
    ) % (
        idx, idx,
        _col_letter(idx + 1), _esc(ser["name"]),
        style, marker,
        len(cats) + 1, len(cats), cat_pts,
        _col_letter(idx + 1), _col_letter(idx + 1), len(cats) + 1,
        len(cats), val_pts,
        '<c:smooth val="0"/>' if True else "",
    )


def build_chart_xml(kind: str, title: str, categories: Sequence[str],
                    series: Sequence[dict], with_explicit_style: bool = True,
                    external_rid: str | None = "rId1") -> bytes:
    """kind in line/pie/bar。"""
    sers = "".join(_ser_xml(i, s, categories, SERIES_COLORS[i % len(SERIES_COLORS)],
                            with_explicit_style)
                   for i, s in enumerate(series))
    title_xml = (
        "<c:title><c:tx><c:rich><a:bodyPr/><a:lstStyle/>"
        "<a:p><a:r><a:t>%s</a:t></a:r></a:p></c:rich></c:tx>"
        '<c:overlay val="0"/></c:title><c:autoTitleDeleted val="0"/>' % _esc(title)
    )
    if kind == "pie":
        plot = ('<c:pieChart><c:varyColors val="1"/>%s'
                '<c:firstSliceAng val="0"/></c:pieChart>' % sers)
        axes = ""
    elif kind == "bar":
        plot = ('<c:barChart><c:barDir val="col"/><c:grouping val="clustered"/>'
                '<c:varyColors val="0"/>%s'
                '<c:axId val="111111111"/><c:axId val="222222222"/></c:barChart>' % sers)
        axes = (
            '<c:catAx><c:axId val="111111111"/><c:scaling><c:orientation val="minMax"/></c:scaling>'
            '<c:delete val="0"/><c:axPos val="b"/><c:crossAx val="222222222"/></c:catAx>'
            '<c:valAx><c:axId val="222222222"/><c:scaling><c:orientation val="minMax"/></c:scaling>'
            '<c:delete val="0"/><c:axPos val="l"/><c:crossAx val="111111111"/></c:valAx>'
        )
    else:
        plot = ('<c:lineChart><c:grouping val="standard"/><c:varyColors val="0"/>%s'
                '<c:marker val="1"/>'
                '<c:axId val="111111111"/><c:axId val="222222222"/></c:lineChart>' % sers)
        axes = (
            '<c:catAx><c:axId val="111111111"/><c:scaling><c:orientation val="minMax"/></c:scaling>'
            '<c:delete val="0"/><c:axPos val="b"/><c:crossAx val="222222222"/></c:catAx>'
            '<c:valAx><c:axId val="222222222"/><c:scaling><c:orientation val="minMax"/></c:scaling>'
            '<c:delete val="0"/><c:axPos val="l"/><c:crossAx val="111111111"/></c:valAx>'
        )
    ext = ('<c:externalData r:id="%s"><c:autoUpdate val="0"/></c:externalData>' % external_rid
           if external_rid else "")
    xml = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<c:chartSpace xmlns:c="%s" xmlns:a="%s" xmlns:r="%s">'
        "<c:chart>%s<c:plotArea><c:layout/>%s%s</c:plotArea>"
        '<c:plotVisOnly val="1"/><c:dispBlanksAs val="gap"/></c:chart>%s</c:chartSpace>'
    ) % (C_NS, A_NS, R_NS, title_xml, plot, axes, ext)
    return xml.encode("utf-8")


# --------------------------------------------------------------------------- #
# 把图表挂进 docx
# --------------------------------------------------------------------------- #
def _drawing_paragraph_xml(rid: str, docpr_id: int, name: str) -> str:
    # 注意：python-docx 默认的 document.xml 根节点没有声明 wp / a 前缀，
    # 所以这里在插入的 <w:p> 上就地声明，避免 "Namespace prefix a not defined"。
    return (
        '<w:p xmlns:wp="%s" xmlns:a="%s" xmlns:c="%s" xmlns:r="%s">'
        "<w:r><w:drawing>"
        '<wp:inline distT="0" distB="0" distL="0" distR="0">'
        '<wp:extent cx="5486400" cy="3200400"/>'
        '<wp:effectExtent l="0" t="0" r="0" b="0"/>'
        '<wp:docPr id="%d" name="%s"/>'
        '<wp:cNvGraphicFramePr/>'
        '<a:graphic><a:graphicData uri="%s">'
        '<c:chart r:id="%s"/>'
        "</a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>"
    ) % (WP_NS, A_NS, C_NS, R_NS, docpr_id, _esc(name), C_NS, rid)


def build_chart_docx(charts: Sequence[dict], with_explicit_style: bool = True,
                     text: str = "销售情况概览", with_workbook: bool = True) -> bytes:
    """charts: [{kind, title, categories, series}]，最多 2 个。返回 docx 字节。

    with_workbook=False 用于构造"图表没有内嵌工作簿"的样本（Word 里删过数据的图表
    会出现这种状态），验证只靠缓存也能改数据。
    """
    doc = Document()
    doc.add_heading(text, level=1)
    doc.add_paragraph("正文段落，用于验证图表不影响其它块。")
    base = BytesIO()
    doc.save(base)
    raw = base.getvalue()

    zin = zipfile.ZipFile(BytesIO(raw))
    parts = {n: zin.read(n) for n in zin.namelist()}
    zin.close()

    ct = parts["[Content_Types].xml"].decode("utf-8")
    doc_rels = parts["word/_rels/document.xml.rels"].decode("utf-8")
    document = parts["word/document.xml"].decode("utf-8")

    new_parts: dict = {}
    for i, ch in enumerate(charts, start=1):
        rid = "rId%d" % (100 + i - 1)
        chart_path = "word/charts/chart%d.xml" % i
        emb_path = "word/embeddings/data%d.xlsx" % i

        new_parts[chart_path] = build_chart_xml(
            ch["kind"], ch["title"], ch["categories"], ch["series"],
            with_explicit_style=with_explicit_style,
            external_rid="rId1" if with_workbook else None,
        )
        if with_workbook:
            new_parts["word/charts/_rels/chart%d.xml.rels" % i] = (
                '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
                '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
                '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/'
                'officeDocument/2006/relationships/package" Target="../embeddings/data%d.xlsx"/>'
                "</Relationships>" % i
            ).encode("utf-8")
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
        document = document.replace(
            "</w:body>",
            _drawing_paragraph_xml(rid, i, "图表 %d" % i) + "</w:body>",
        )

    parts["[Content_Types].xml"] = ct.encode("utf-8")
    parts["word/_rels/document.xml.rels"] = doc_rels.encode("utf-8")
    parts["word/document.xml"] = document.encode("utf-8")
    parts.update(new_parts)

    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for name, data in parts.items():
            z.writestr(name, data)
    return out.getvalue()


# 常用样本
def sample_line_chart() -> dict:
    return {
        "kind": "line",
        "title": "季度销售额",
        "categories": ["Q1", "Q2", "Q3", "Q4"],
        "series": [
            {"name": "2025", "values": [120, 180, 150, 220]},
            {"name": "2026", "values": [140, 210, 190, 260]},
        ],
    }


def sample_pie_chart() -> dict:
    return {
        "kind": "pie",
        "title": "收入构成",
        "categories": ["产品", "服务", "订阅"],
        "series": [{"name": "占比", "values": [55, 30, 15]}],
    }


def sample_bar_chart() -> dict:
    return {
        "kind": "bar",
        "title": "漏洞分布",
        "categories": ["高危", "中危", "低危"],
        "series": [{"name": "数量", "values": [12, 34, 56]}],
    }
