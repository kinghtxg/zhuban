# -*- coding: utf-8 -*-
"""渲染管道：整表循环 / 条件 / 二维码 / 富文本（契约 §3.9 ②③④⑤）。"""
import zipfile
from io import BytesIO

from docx import Document
from lxml import etree

from app.render import render_template
from app.preprocess import qn


def _body_children(docx_bytes):
    zf = zipfile.ZipFile(BytesIO(docx_bytes))
    root = etree.fromstring(zf.read("word/document.xml"))
    return root.find(qn("body")), zf


def test_whole_table_loop(migrated_template_bytes, sample_data):
    """迁移模板 + 3 条 items：渲染后表数 == 3，body 直属附录 numPr 段 == 3，无残留占位符。"""
    out = render_template(migrated_template_bytes, sample_data)
    body, zf = _body_children(out)
    text = "".join(body.itertext())
    assert "{{" not in text
    assert "{%" not in text
    tbls = body.findall(qn("tbl"))
    assert len(tbls) == len(sample_data["items"]) == 3
    # body 直属（不含表格内）含 numPr 的附录段落
    direct_numpr = [
        ch for ch in body
        if etree.QName(ch).localname == "p" and ch.find(".//" + qn("numPr")) is not None
    ]
    assert len(direct_numpr) == 3


def _make_docx_with_paragraphs(texts):
    doc = Document()
    for t in texts:
        doc.add_paragraph(t)
    buf = BytesIO()
    doc.save(buf)
    return buf.getvalue()


def _doc_root(docx_bytes):
    zf = zipfile.ZipFile(BytesIO(docx_bytes))
    return etree.fromstring(zf.read("word/document.xml"))


def test_condition_block():
    tpl = _make_docx_with_paragraphs(["开始", "{% if show %}YES_VISIBLE{% endif %}", "结束"])
    out_true = render_template(tpl, {"show": True})
    assert "YES_VISIBLE" in "".join(_doc_root(out_true).itertext())
    out_false = render_template(tpl, {"show": False})
    assert "YES_VISIBLE" not in "".join(_doc_root(out_false).itertext())


def test_qrcode_inserts_media():
    tpl = _make_docx_with_paragraphs(["二维码：{{qr_x}}"])
    out = render_template(tpl, {"qr_x": {"type": "qrcode", "text": "hello-world", "size": 20}})
    zf = zipfile.ZipFile(BytesIO(out))
    media = [n for n in zf.namelist() if n.startswith("word/media/")]
    assert len(media) >= 1


def test_richtext_bold_and_list():
    tpl = _make_docx_with_paragraphs(["修复：{{rich_x}}"])
    data = {"rich_x": {"type": "markdown", "content": "**粗体重点**\n\n- 列表项一\n- 列表项二"}}
    out = render_template(tpl, data)
    root = _doc_root(out)
    assert root.findall(".//" + qn("b")), "应产生加粗 <w:b>"
    text = "".join(root.itertext())
    assert "•" in text, "应产生列表项符号"
    assert "粗体重点" in text
