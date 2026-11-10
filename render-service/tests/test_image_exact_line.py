# -*- coding: utf-8 -*-
r"""图片 + 固定行距：图片会被裁成一行高，扫描期必须告警。

## 为什么会这样

GB/T 9704 的公文正文用「固定值 28.8 磅」行距。固定行距的含义是**这一行就是
28.8 磅高**，行内超出的内容不撑高行、直接裁掉 —— 一张 30mm（85 磅）高的印章
放进这样的段落，纸面上只剩顶部一条弧。

这不是渲染出错：转换成功、接口 200、字数正确、文本断言全绿，只有肉眼看才发现
「图片没了」。所以判据只能定在扫描期的一句告警上。

（本轮 10 式实测：① 标准通知的印章、⑦ 图片式的插图，各只剩一条。）
"""
from io import BytesIO

from docx import Document
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from docx.shared import Twips

from app.scanner import scan_docx


def _docx(paragraphs, exact_line=None):
    """在段落里放 {{ image_印章 }}；exact_line 给定时给该段设固定行距（磅）。"""
    d = Document()
    for t in paragraphs:
        p = d.add_paragraph(t)
        if exact_line and "image_" in t:
            pPr = p._p.get_or_add_pPr()
            sp = OxmlElement("w:spacing")
            sp.set(qn("w:line"), str(int(exact_line * 20)))
            sp.set(qn("w:lineRule"), "exact")
            pPr.append(sp)
    buf = BytesIO()
    d.save(buf)
    return buf.getvalue()


def _warns(raw):
    return [d for d in scan_docx(raw)["diagnostics"] if d["severity"] == "warning"]


def test_exact_line_on_image_paragraph_warns():
    raw = _docx(["正文第一段。", "{{ image_印章 }}"], exact_line=28.8)
    w = _warns(raw)
    assert any("image_印章" in d["message"] and "固定行距" in d["message"] for d in w), w


def test_warning_quotes_the_actual_line_height():
    """告警要说出「多少磅」，否则用户不知道该改什么。"""
    raw = _docx(["{{ image_印章 }}"], exact_line=28.8)
    w = [d for d in _warns(raw) if "image_印章" in d["message"]]
    assert w and "28.8" in w[0]["message"], w


def test_auto_line_does_not_warn():
    """单倍行距（不写 lineRule 或写 auto）是正常做法，不能报警。"""
    d = Document()
    d.add_paragraph("{{ image_印章 }}")
    buf = BytesIO()
    d.save(buf)
    assert not [x for x in _warns(buf.getvalue()) if "image_印章" in x["message"]]


def test_huge_exact_line_does_not_warn():
    """>60 磅的固定行距容得下常见小图，别吵。"""
    raw = _docx(["{{ image_印章 }}"], exact_line=120)
    assert not [x for x in _warns(raw) if "image_印章" in x["message"]]


def test_warning_deduped_per_variable():
    raw = _docx(["{{ image_印章 }}", "{{ image_印章 }}"], exact_line=28.8)
    hits = [d for d in _warns(raw) if "image_印章" in d["message"]]
    assert len(hits) == 1, hits


def test_plain_text_paragraph_never_warns():
    raw = _docx(["这是一段普通正文，没有任何图片。"], exact_line=28.8)
    assert not [x for x in _warns(raw) if "图片变量" in x["message"]]
