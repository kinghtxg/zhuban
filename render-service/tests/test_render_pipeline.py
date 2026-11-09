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


def test_whole_table_loop():
    """整表循环：表被 {%p for %} 包住时，一张表连表头一起按 items 复制。

    与「行循环」是两回事，别混：行循环（{%tr for %}）复制的是 <w:tr>，
    表头留在原地；整表循环复制的是整张 <w:tbl>，每份都带表头。
    内置业务样例用的是行循环，所以这里用合成模板单独守住整表这条路径。
    """
    doc = Document()
    doc.add_paragraph("核心指标")
    doc.add_paragraph("{%p for it in items %}")
    t = doc.add_table(rows=2, cols=2)
    t.rows[0].cells[0].text = "指标名称"
    t.rows[0].cells[1].text = "本期值"
    t.rows[1].cells[0].text = "{{ it.指标名称 }}"
    t.rows[1].cells[1].text = "{{ it.本期值 }}"
    doc.add_paragraph("{%p endfor %}")
    buf = BytesIO()
    doc.save(buf)

    items = [
        {"指标名称": "营业收入", "本期值": "1,286 万元"},
        {"指标名称": "续约率", "本期值": "92.1%"},
        {"指标名称": "毛利率", "本期值": "41.2%"},
    ]
    out = render_template(buf.getvalue(), {"items": items})
    body, _ = _body_children(out)
    text = "".join(body.itertext())
    assert "{{" not in text and "{%" not in text

    tbls = body.findall(qn("tbl"))
    assert len(tbls) == len(items) == 3, "整表循环：一张表/条"
    for tbl, it in zip(tbls, items):
        cell_text = "".join(tbl.itertext())
        assert "指标名称" in cell_text, "每份副本都要带表头"
        assert it["指标名称"] in cell_text


def test_business_template_row_loop(business_template_bytes, sample_data):
    """内置业务样例走完整管道（扫描→行标签外移→docxtpl）：
    1 张指标表 = 1 表头行 + N 条数据行，示例值全部落地，无残留标签。
    """
    out = render_template(business_template_bytes, sample_data)
    body, _ = _body_children(out)
    text = "".join(body.itertext())
    assert "{{" not in text, "有占位符没被替换"
    assert "{%" not in text, "有 jinja 标签漏进成品"

    tbls = body.findall(qn("tbl"))
    assert len(tbls) == 1
    trs = tbls[0].findall(qn("tr"))
    assert len(trs) == 1 + len(sample_data["items"])
    for it in sample_data["items"]:
        for v in it.values():
            assert str(v) in text, "示例值 %r 没出现在正文里" % v


def test_builtin_samples_render_clean():
    """两份内置样例（要塞进用户库当「一键试用」的）必须渲染出合法结构。

    这条防的是「样例自己就是坏的」：markdown 富文本的 run 若被塞进 <w:t>，
    用户点开缩略图看到的是空白段，却没有任何报错。
    """
    import json
    import os

    # 路径锚在用例文件上，不要用相对 CWD 的 ".."：从仓库根跑 pytest 时
    # CWD 是 docforge/，"../samples" 会指到仓库外，报「内置样例模板不见了」的假错。
    here = os.path.dirname(os.path.abspath(__file__))
    samples = os.path.join(here, "..", "..", "samples")
    pairs = [
        ("demo-report", os.path.join(samples, "templates", "demo-report.docx"),
         os.path.join(samples, "demo-report.sample.json")),
        ("biz-report", os.path.join(samples, "templates", "biz-report.docx"),
         os.path.join(samples, "biz-report.sample.json")),
    ]
    for code, tpl_path, data_path in pairs:
        assert os.path.exists(tpl_path), "内置样例模板不见了：%s" % tpl_path
        with open(tpl_path, "rb") as f:
            raw = f.read()
        with open(data_path, encoding="utf-8") as f:
            data = json.load(f)
        root = _doc_root(render_template(raw, data))
        bad = _nested_text_nodes(root)
        assert not bad, "%s 渲染后有 %d 个 <w:t> 装了元素" % (code, len(bad))


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


def _nested_text_nodes(root):
    """找出里头装了子元素的 <w:t>（<w:t> 只允许纯文本，装了就是非法嵌套）。"""
    return [t for t in root.iter(qn("t")) if len(t)]


def test_richtext_runs_land_at_paragraph_level():
    """富文本的 <w:r> 必须落在段落层，不能塞进 <w:t> 里面。

    docxtpl 的 RichText 只产出 <w:r>…</w:r>，本来要配 `{{r 变量}}` 用；直接拿
    `{{ 变量 }}` 交给它，这些 run 会被原样塞进 <w:t>。后果是 Word 报「内容有
    问题」，LibreOffice 干脆把整段当空——PDF 里那一段是空白，而 document.xml
    里文字一字不少，所以只断言「文本包含 XXX」的用例全绿也发现不了。
    """
    tpl = _make_docx_with_paragraphs(["修复：{{rich_x}}"])
    data = {"rich_x": {"type": "markdown", "content": "**粗体**\n\n- 甲\n- 乙"}}
    root = _doc_root(render_template(tpl, data))

    bad = _nested_text_nodes(root)
    assert not bad, "有 <w:t> 里装了元素：%s" % [
        etree.tostring(t)[:160] for t in bad[:3]
    ]

    p = root.find(".//" + qn("p"))
    runs = p.findall(qn("r"))
    assert len(runs) >= 3, "富文本应拆成多个并列 run，实际 %d 个" % len(runs)
    assert any(r.find(".//" + qn("b")) is not None for r in runs), "加粗 run 丢了"
    # 前缀文字必须还在第一个 run 里，没有被吞掉
    assert "修复：" in "".join(x.text or "" for x in runs[0].iter(qn("t")))
