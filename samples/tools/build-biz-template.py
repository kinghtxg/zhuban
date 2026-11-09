# -*- coding: utf-8 -*-
"""生成内置的业务样例模板：samples/templates/biz-report.docx

这份模板替代了早期的「代码审计报告」样例。替换的原因不是技术性的：
那份 docx 与配套示例数据来自另一个项目（code-audit-report），属于别人家的交付物，
不该跟着这个产品的内置模板一起分发。

新的样例要满足三件事：
1. **是业务报告，不是功能清单**——演示模板（demo-report）负责"每个语法各摆一处"，
   这份负责"像真报告一样能用"，两份互补。
2. **够简单**：5 个标量 + 1 个表格行循环 + 2 段富文本，正好是《模板使用手册》§7
   逐字段对照需要的规模。语法再多，读者就得在示例里挑重点了。
3. **可复现**：和 demo-report 一样用脚本生成，占位符写错了重跑一次即可，
   评审时 `git diff` 也能看出改了哪句话。

输出：
    samples/templates/biz-report.docx   业务样例模板
    samples/biz-report.sample.json      与之配套、可直接跑通的示例数据

用法：
    python samples/tools/build-biz-template.py
    python samples/tools/build-biz-template.py --check   # 只自检，不写文件
"""
from __future__ import annotations

import json
import os
import sys
import zipfile

_HERE = os.path.dirname(os.path.abspath(__file__))
_ROOT = os.path.dirname(os.path.dirname(_HERE))

from docx import Document                                    # noqa: E402
from docx.enum.table import WD_TABLE_ALIGNMENT               # noqa: E402
from docx.enum.text import WD_ALIGN_PARAGRAPH                # noqa: E402
from docx.oxml import OxmlElement                            # noqa: E402
from docx.oxml.ns import qn                                  # noqa: E402
from docx.shared import Cm, Pt, RGBColor                      # noqa: E402

OUT_DOCX = os.path.join(_ROOT, "samples", "templates", "biz-report.docx")
OUT_JSON = os.path.join(_ROOT, "samples", "biz-report.sample.json")

# 表格里必须出现的占位符。自检照这个清单查 docx，漏一个就报错——
# 「模板里写错了占位符」在扫描阶段只会表现为"变量少了一个"，很容易被忽略。
REQUIRED = [
    "{{ 报告期 }}",
    "{{ 编制单位 }}",
    "{{ 经营概况 }}",
    "{{ 原因分析 }}",
    "{{ 措施建议 }}",
    '{{ 编制人 or "未指派" }}',
    "{{ 出具日期 }}",
    "{%tr for it in items %}",
    "{{ it.指标名称 }}",
    "{{ it.本期值 }}",
    "{{ it.同比 }}",
    "{{ it.达成率 }}",
    "{{ it.说明 }}",
    "{%tr endfor %}",
]


def set_cell_bg(cell, hex_color: str) -> None:
    tcPr = cell._tc.get_or_add_tcPr()
    shd = OxmlElement("w:shd")
    shd.set(qn("w:val"), "clear")
    shd.set(qn("w:color"), "auto")
    shd.set(qn("w:fill"), hex_color)
    tcPr.append(shd)


def set_repeat_header(row) -> None:
    """表头跨页重复。经营指标表通常不止一页，表头掉了读者就得回去翻。"""
    trPr = row._tr.get_or_add_trPr()
    el = OxmlElement("w:tblHeader")
    el.set(qn("w:val"), "true")
    trPr.append(el)


def add_page_field(paragraph) -> None:
    """页脚"第 X 页 共 Y 页"。PAGE / NUMPAGES 是 Word 域，转 PDF 时按实际页数算。"""
    run = paragraph.add_run("第 ")
    _field(paragraph, "PAGE")
    paragraph.add_run(" 页 共 ")
    _field(paragraph, "NUMPAGES")
    paragraph.add_run(" 页")
    del run


def _field(paragraph, instr: str) -> None:
    run = paragraph.add_run()
    begin = OxmlElement("w:fldChar")
    begin.set(qn("w:fldCharType"), "begin")
    instr_el = OxmlElement("w:instrText")
    instr_el.set(qn("xml:space"), "preserve")
    instr_el.text = f" {instr} "
    sep = OxmlElement("w:fldChar")
    sep.set(qn("w:fldCharType"), "separate")
    txt = OxmlElement("w:t")
    txt.text = "1"
    end = OxmlElement("w:fldChar")
    end.set(qn("w:fldCharType"), "end")
    for el in (begin, instr_el, sep, txt, end):
        run._r.append(el)


def heading(doc: Document, text: str) -> None:
    """章节标题用 Word 的 Heading 2 样式，而不是"加粗加大的一行字"。

    两个理由：导航窗格/目录域认的是样式，不是字号；解析侧也能据此区分
    「标题」与「正文」，工作台的章节树才不会把标题混进正文。
    """
    p = doc.add_paragraph(style="Heading 2")
    p.paragraph_format.space_before = Pt(10)
    p.paragraph_format.space_after = Pt(4)
    p.add_run(text)


def build_body(doc: Document) -> None:
    # ---- 标题区 -------------------------------------------------------------
    t = doc.add_paragraph()
    t.alignment = WD_ALIGN_PARAGRAPH.CENTER
    r = t.add_run("月度经营分析报告")
    r.bold = True
    r.font.size = Pt(18)

    sub = doc.add_paragraph()
    sub.alignment = WD_ALIGN_PARAGRAPH.CENTER
    s = sub.add_run("报告期：{{ 报告期 }}　　编制单位：{{ 编制单位 }}")
    s.font.size = Pt(10.5)

    # ---- 一、经营概况 -------------------------------------------------------
    heading(doc, "一、经营概况")
    # 富文本：数据侧传 {"type":"markdown","content":"..."}，由 render-service 转成
    # 带加粗/列表的段落。用富文本而不是纯文本，是因为"概况"这一段天然要突出重点数字。
    doc.add_paragraph("{{ 经营概况 }}")

    # ---- 二、核心指标 -------------------------------------------------------
    heading(doc, "二、核心指标")
    tbl = doc.add_table(rows=2, cols=5)
    tbl.style = "Table Grid"
    tbl.alignment = WD_TABLE_ALIGNMENT.CENTER

    widths = [Cm(4.2), Cm(2.6), Cm(2.2), Cm(2.2), Cm(5.0)]
    hdr = ["指标名称", "本期值", "同比", "达成率", "说明"]
    for i, name in enumerate(hdr):
        cell = tbl.rows[0].cells[i]
        cell.width = widths[i]
        cell.paragraphs[0].add_run(name).bold = True
        cell.paragraphs[0].alignment = WD_ALIGN_PARAGRAPH.CENTER
        set_cell_bg(cell, "EEF2F8")
    set_repeat_header(tbl.rows[0])

    row = tbl.rows[1]
    for i, w in enumerate(widths):
        row.cells[i].width = w
    # 行循环占位符必须与单元格内容同一段落（{%tr%} 认的是"整行"），
    # 放进独立段落会让 docxtpl 找不到要复制的 <w:tr>。
    row.cells[0].paragraphs[0].add_run("{%tr for it in items %}{{ it.指标名称 }}")
    row.cells[1].paragraphs[0].add_run("{{ it.本期值 }}")
    row.cells[2].paragraphs[0].add_run("{{ it.同比 }}")
    row.cells[3].paragraphs[0].add_run("{{ it.达成率 }}")
    row.cells[4].paragraphs[0].add_run("{{ it.说明 }}{%tr endfor %}")

    # ---- 三、原因分析 -------------------------------------------------------
    heading(doc, "三、原因分析")
    doc.add_paragraph("{{ 原因分析 }}")

    # ---- 四、措施与建议 -----------------------------------------------------
    heading(doc, "四、措施与建议")
    doc.add_paragraph("{{ 措施建议 }}")

    # ---- 落款 ---------------------------------------------------------------
    doc.add_paragraph()
    sign = doc.add_paragraph()
    sign.alignment = WD_ALIGN_PARAGRAPH.RIGHT
    sign.add_run("编制人：{{ 编制人 or \"未指派\" }}　　出具日期：{{ 出具日期 }}")

    # ---- 页脚 ---------------------------------------------------------------
    footer_p = doc.sections[0].footer.paragraphs[0]
    footer_p.alignment = WD_ALIGN_PARAGRAPH.CENTER
    add_page_field(footer_p)


def build_sample() -> dict:
    return {
        "报告期": "2026 年 10 月",
        "编制单位": "运营管理中心",
        "经营概况": {
            "type": "markdown",
            "content": (
                "本月营业收入 **1,286 万元**，完成月度目标的 103.4%，同比增长 12.7%。"
                "新增签约客户 18 家，其中制造业客户 11 家；续约率 92.1%，较上月提升 1.8 个百分点。"
                "成本侧受交付人力投入增加影响，毛利率回落至 41.2%。"
            ),
        },
        "原因分析": "收入增长主要来自华东区制造行业订单集中交付；毛利率回落系交付团队扩编与两个项目延期验收，人力成本提前计入本月。",
        "措施建议": {
            "type": "markdown",
            "content": (
                "- 对延期验收的 2 个项目建立周跟踪，明确验收责任人与时间点\n"
                "- **交付人力按项目饱和度排班**，避免为赶节点集中加人\n"
                "- 下月目标：收入 1,320 万元、毛利率回到 43% 以上"
            ),
        },
        "编制人": "王敏",
        "出具日期": "2026-11-01",
        "items": [
            {
                "指标名称": "营业收入",
                "本期值": "1,286 万元",
                "同比": "+12.7%",
                "达成率": "103.4%",
                "说明": "华东区制造行业订单集中交付",
            },
            {
                "指标名称": "新增签约客户",
                "本期值": "18 家",
                "同比": "+3 家",
                "达成率": "90.0%",
                "说明": "目标 20 家，缺口集中在政府行业",
            },
            {
                "指标名称": "客户续约率",
                "本期值": "92.1%",
                "同比": "+1.8pt",
                "达成率": "102.3%",
                "说明": "两家大客户提前续约",
            },
            {
                "指标名称": "毛利率",
                "本期值": "41.2%",
                "同比": "-2.6pt",
                "达成率": "95.8%",
                "说明": "交付人力投入增加、两个项目延期验收",
            },
            {
                "指标名称": "人均产值",
                "本期值": "8.6 万元",
                "同比": "-4.4%",
                "达成率": "93.5%",
                "说明": "团队扩编在先、产出在后",
            },
        ],
    }


def self_check(path: str) -> list:
    """从 docx 里把正文/表格文本全捞出来，逐一核对占位符在不在。"""
    with zipfile.ZipFile(path) as z:
        xml = z.read("word/document.xml").decode("utf-8")
    # 占位符可能被 Word 拆成多个 run，生成时是整体写入的，这里直接查原文即可。
    text = xml.replace("<w:t>", "").replace("</w:t>", "")
    missing = [p for p in REQUIRED if p not in text]
    return missing


def main() -> int:
    check_only = "--check" in sys.argv

    doc = Document()
    sec = doc.sections[0]
    sec.page_height, sec.page_width = Cm(29.7), Cm(21.0)   # A4
    sec.left_margin = sec.right_margin = Cm(2.6)
    sec.top_margin = sec.bottom_margin = Cm(2.4)

    style = doc.styles["Normal"]
    style.font.name = "宋体"
    style.font.size = Pt(10.5)
    style.element.rPr.rFonts.set(qn("w:eastAsia"), "宋体")

    # Heading 2 的默认样式是深蓝细体，用在中文公文里像"网页标题"。
    # 这里改回宋体加粗黑色：视觉上仍是章节标题，但读起来是报告。
    h2 = doc.styles["Heading 2"]
    h2.font.name = "宋体"
    h2.font.size = Pt(12)
    h2.font.bold = True
    h2.font.color.rgb = RGBColor(0, 0, 0)
    h2.element.rPr.rFonts.set(qn("w:eastAsia"), "宋体")

    build_body(doc)

    if check_only:
        tmp = os.path.join(_ROOT, "samples", "_check-biz.docx")
        doc.save(tmp)
        missing = self_check(tmp)
        os.remove(tmp)
        print("缺失占位符：", missing or "无")
        return 1 if missing else 0

    doc.save(OUT_DOCX)
    with open(OUT_JSON, "w", encoding="utf-8") as f:
        json.dump(build_sample(), f, ensure_ascii=False, indent=2)
        f.write("\n")

    missing = self_check(OUT_DOCX)
    print(f"写出 {OUT_DOCX}  ({os.path.getsize(OUT_DOCX)} B)")
    print(f"写出 {OUT_JSON}  ({os.path.getsize(OUT_JSON)} B)")
    print("缺失占位符：", missing or "无")
    return 1 if missing else 0


if __name__ == "__main__":
    raise SystemExit(main())
