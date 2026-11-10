# -*- coding: utf-8 -*-
"""渲染管道：整表循环 / 条件 / 二维码 / 富文本（契约 §3.9 ②③④⑤）。"""
import base64
import zipfile
from io import BytesIO

from docx import Document
from docx.oxml.ns import qn as dqn
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


def test_richtext_line_breaks_become_real_breaks():
    """富文本里的换行必须是 <w:br/>，不能是 <w:t> 里的裸 \\n。

    裸 \\n 在 <w:t> 里**不表示换行**：Word 当空白吃掉，LibreOffice 同样。
    于是用户敲的每处回车在成稿里全变成空格，几段内容挤成一坨。
    接口 200、文字一个不少，所以「文本包含 XXX」那类断言一条都不会红。
    """
    tpl = _make_docx_with_paragraphs(["详情：{{rich_x}}"])
    data = {"rich_x": {"type": "html", "content": "第一行<br/>第二行<br/>第三行"}}
    root = _doc_root(render_template(tpl, data))

    assert root.findall(".//" + qn("br")), "换行没有变成 <w:br/>"
    # <w:t> 里不许残留换行：那是"看着有、其实没有"的换行
    for t in root.iter(qn("t")):
        assert "\n" not in (t.text or ""), "还有裸换行留在 <w:t> 里：%r" % t.text
    assert "第一行" in "".join(root.itertext())


def test_richtext_blank_line_keeps_empty_paragraph_gap():
    """两段之间空一行（\\n\\n）要能看出间隔，不能被压成一行。"""
    tpl = _make_docx_with_paragraphs(["详情：{{rich_x}}"])
    data = {"rich_x": {"type": "html", "content": "甲<br/><br/>乙"}}
    root = _doc_root(render_template(tpl, data))
    assert len(root.findall(".//" + qn("br"))) >= 2


def _png_uri(px_w: int = 400, px_h: int = 200) -> str:
    """造一张真 PNG 的 data URI：宽高比明确，才能验"没被压扁"。"""
    from PIL import Image
    buf = BytesIO()
    Image.new("RGB", (px_w, px_h), (200, 40, 40)).save(
        buf, format="PNG", dpi=(96, 96)
    )
    return "data:image/png;base64," + base64.b64encode(buf.getvalue()).decode()


def test_px_to_mm_conversion():
    """HTML 的 px 是像素，不是毫米。当毫米用会让图放大 3.78 倍、直接撑出版面。"""
    from app.richdata import _px_to_mm

    assert _px_to_mm("96") == 25.4        # 无单位按 px
    assert _px_to_mm("96px") == 25.4
    assert _px_to_mm("72pt") == 25.4
    assert _px_to_mm("2.54cm") == 25.4
    assert _px_to_mm("25.4mm") == 25.4
    assert _px_to_mm("50%") is None       # 百分比/auto 表达不了，按"没给"处理
    assert _px_to_mm("0") is None and _px_to_mm("") is None and _px_to_mm(None) is None


def test_richtext_inline_image_becomes_run_level_drawing():
    """富文本里的 <img> 必须真的变成图片，且是**段落层**的 run。

    安全报告的正文主体就是截图，只能打字不能贴图的编辑器等于逼用户
    "先导出再手工插图"，那格式一定乱。这里同时钉住三件事：
      ① 图真进了 word/media/（不是被静默丢掉）；
      ② <w:drawing> 挂在 <w:r> 下、且没有任何 <w:t> 里塞了元素；
      ③ 宽高比按原图，宽度取 HTML 声明的 px 而不是原图像素。
    """
    uri = _png_uri(400, 200)  # 2:1
    tpl = _make_docx_with_paragraphs(["现场：{{rich_x}}"])
    out = render_template(tpl, {
        "rich_x": {"type": "html",
                   "content": "前<img src='%s' width='40'/>后" % uri},
    })

    media = [n for n in zipfile.ZipFile(BytesIO(out)).namelist()
             if n.startswith("word/media/")]
    assert len(media) == 1, "图片没进包：%s" % media

    root = _doc_root(out)
    assert not _nested_text_nodes(root), "有 <w:t> 里装了元素"

    p = root.find(".//" + qn("p"))
    drawings = p.findall(".//" + qn("drawing"))
    assert len(drawings) == 1, "应恰好一张图，实际 %d" % len(drawings)
    assert drawings[0].getparent().tag == qn("r"), \
        "drawing 不在 run 里，挂在了 %s" % drawings[0].getparent().tag

    ext = p.find(".//" + dqn("wp:extent"))
    cx, cy = int(ext.get("cx")), int(ext.get("cy"))
    assert abs(cx / cy - 2.0) < 0.02, "宽高比被改了：%d x %d" % (cx, cy)
    # 40px -> 10.58mm -> 380952 EMU（1mm = 36000 EMU）
    assert abs(cx / 36000 - 10.58) < 0.15, "没按声明的 px 换算宽度：%.2fmm" % (cx / 36000)

    text = "".join(p.itertext())
    assert "前" in text and "后" in text, "图两侧的文字丢了：%r" % text
    # 图文顺序不能乱：前 -> 图 -> 后
    kids = list(p)
    idx_text = [i for i, el in enumerate(kids)
                if el.tag == qn("r") and "前" in "".join(x.text or "" for x in el.iter(qn("t")))]
    idx_img = [i for i, el in enumerate(kids) if el.find(".//" + qn("drawing")) is not None]
    assert idx_text and idx_img and idx_text[0] < idx_img[0], "图文顺序乱了"


def test_richtext_markdown_image_also_works():
    """markdown 写法的图片（``![](…)``）走同一条路，不能只有 html 能用。"""
    tpl = _make_docx_with_paragraphs(["截图：{{rich_x}}"])
    out = render_template(tpl, {
        "rich_x": {"type": "markdown", "content": "说明文字\n\n![](%s)" % _png_uri(200, 200)},
    })
    root = _doc_root(out)
    assert len(root.findall(".//" + qn("drawing"))) == 1
    media = [n for n in zipfile.ZipFile(BytesIO(out)).namelist()
             if n.startswith("word/media/")]
    assert len(media) == 1


def test_richtext_image_without_doc_degrades_to_text_only():
    """没有 docxtpl 实例时（单元测试/预览场景）只保留文字，不许抛异常。"""
    from app.richdata import html_to_richtext

    rt = html_to_richtext("文字<img src='%s'/>尾巴" % _png_uri(), "html")
    xml = str(rt)
    assert "drawing" not in xml
    assert "文字" in xml and "尾巴" in xml
