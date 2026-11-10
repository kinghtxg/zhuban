# -*- coding: utf-8 -*-
"""V2 块级结构化模型：/parse 与 /apply（PRD §7.1）。

覆盖：
  1. parse 业务样例模板：标题层级/段落/表格行列/占位符/tokens 快照断言；
  2. 原样回写：未编辑段落 XML 逐段一致、styles/numbering 部件字节不变、python-docx 可开；
  3. 改文字：段落/标题文本生效，其余段落 XML 不动，中文与 eastAsia 字体保留；
  4. 占位符：插入标量、删除占位符后 docxtpl 渲染正确；未配对标签报清晰错误；
  5. 表格：增行/删行/改单元格/标记循环行后 docxtpl 渲染出正确行数；
  6. unsupported：含 w:txbxContent 的段落 parse 标记 unsupported，apply 后 XML 原样保留；
  7. 安全：docm/宏、超大文件仍被 security.validate_docx 拒绝。
"""
import copy
import json
import zipfile
from io import BytesIO

import pytest
from fastapi.testclient import TestClient
from lxml import etree
from docx import Document
from docxtpl import DocxTemplate

from app.main import app
from app.docmodel import parse_docx
from app.docapply import apply_model
from app.preprocess import qn, normalize_row_tags_docx
from app.security import validate_docx, SecurityError

client = TestClient(app)

DOCX_CT = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


def _blocks(resp_json):
    return resp_json["blocks"]


# --------------------------------------------------------------------------- #
# 1. parse 业务样例模板快照
# --------------------------------------------------------------------------- #
def test_parse_business_report_structure(business_template_bytes):
    model = parse_docx(business_template_bytes)
    blocks = model["blocks"]

    # 顶层应有：标题段 p0、抬头段 p1、章节标题 p2（Heading 2）、指标表 t0
    types = [(b["id"], b["type"]) for b in blocks]
    assert ("p0", "paragraph") in types
    assert ("p2", "heading") in types

    # 章节标题是样式标题（不是"加粗加大的一行字"）：导航窗格与章节树都靠它
    p2 = next(b for b in blocks if b["id"] == "p2")
    assert p2["level"] == 2
    assert p2["text"] == "一、经营概况"

    # 表格 t0：1 行表头 + 1 行模板行（行循环在模板里只写一行，渲染时才复制）
    t0 = next(b for b in blocks if b["id"] == "t0")
    assert t0["type"] == "table"
    assert len(t0["rows"]) == 2

    # 模板行首格同时带行循环开始标签与首个占位符
    row1 = next(r for r in t0["rows"] if r["id"] == "t0r1")
    cell0 = row1["cells"][0]["blocks"][0]
    assert cell0["text"] == "{%tr for it in items %}{{ it.指标名称 }}"
    kinds = {tok["kind"] for tok in cell0["tokens"]}
    assert "var" in kinds
    # tokens 区间应落在标签上
    for tok in cell0["tokens"]:
        assert cell0["text"][tok["start"]:tok["end"]] == tok["text"]


def test_parse_stable(business_template_bytes):
    """同一份 docx 两次 parse，块 id 与文本完全一致。"""
    m1 = parse_docx(business_template_bytes)
    m2 = parse_docx(business_template_bytes)
    assert m1 == m2


# --------------------------------------------------------------------------- #
# 2. 原样回写
# --------------------------------------------------------------------------- #
def test_apply_noop_preserves_xml(business_template_bytes):
    model = parse_docx(business_template_bytes)
    out = apply_model(business_template_bytes, model)

    # python-docx 能打开
    doc = Document(BytesIO(out))
    assert len(doc.tables) == 1

    # 未编辑段落 p1 的 XML 与原文件逐段一致
    def para_xml(b, idx):
        z = zipfile.ZipFile(BytesIO(b))
        root = etree.fromstring(z.read("word/document.xml"))
        body = root.find(qn("body"))
        ps = [ch for ch in body if etree.QName(ch).localname == "p"]
        return etree.tostring(ps[idx])

    assert para_xml(business_template_bytes, 1) == para_xml(out, 1)

    # styles.xml / numbering.xml 字节不变
    zo = zipfile.ZipFile(BytesIO(business_template_bytes))
    zn = zipfile.ZipFile(BytesIO(out))
    for part in ("word/styles.xml", "word/numbering.xml", "word/settings.xml",
                 "word/fontTable.xml"):
        assert zo.read(part) == zn.read(part), part + " 被改动"


def _render_like_service(raw, data):
    """按**线上那条链路**渲染：先外移行级 {%tr %}，再交给 docxtpl。

    直接用裸 DocxTemplate 渲染含 `{%tr %}` 的模板是不对的：docxtpl 0.20.2
    自己处理 `{%tr %}` 的正则是贪婪的，同一行里同时有 `{%tr for %}` 和
    `{%tr endfor %}` 时它会把整行换成 `{% endfor %}`（详见
    app/preprocess.py 里 _normalize_row_tags 的注释）。线上由
    render_template 调 normalize_row_tags_docx 兜住，测试若绕过这一步，
    断言的就等于一条产品根本不走的路径。
    """
    normalized, _ = normalize_row_tags_docx(raw)
    tpl = DocxTemplate(BytesIO(normalized))
    tpl.render(data)
    buf = BytesIO()
    tpl.save(buf)
    return buf.getvalue()


def test_apply_noop_still_renderable(business_template_bytes, sample_data):
    """原样 apply 后仍可渲染：1 张指标表 = 1 表头行 + N 条数据行。"""
    model = parse_docx(business_template_bytes)
    out = apply_model(business_template_bytes, model)
    rendered = _render_like_service(out, sample_data)

    z = zipfile.ZipFile(BytesIO(rendered))
    root = etree.fromstring(z.read("word/document.xml"))
    body = root.find(qn("body"))
    tbls = body.findall(qn("tbl"))
    assert len(tbls) == 1, "模板里只有一张指标表，循环复制的是行不是表"
    trs = tbls[0].findall(qn("tr"))
    # 1 行表头 + 示例数据条数（模板行只写了一行，渲染时才复制）
    assert len(trs) == 1 + len(sample_data["items"]) == 6


# --------------------------------------------------------------------------- #
# 3. 改文字保样式
# --------------------------------------------------------------------------- #
def test_apply_change_text_preserves_style(business_template_bytes):
    model = parse_docx(business_template_bytes)
    # 改 p0 标题段文字
    for b in model["blocks"]:
        if b["id"] == "p0":
            b["text"] = "{{ 报告名称 }}"
    out = apply_model(business_template_bytes, model)

    # 生效
    z = zipfile.ZipFile(BytesIO(out))
    root = etree.fromstring(z.read("word/document.xml"))
    body = root.find(qn("body"))
    ps = [ch for ch in body if etree.QName(ch).localname == "p"]
    assert "".join(t.text or "" for t in ps[0].iter(qn("t"))) == "{{ 报告名称 }}"

    # p1（未改）XML 逐段一致
    def para_xml(b, idx):
        zz = zipfile.ZipFile(BytesIO(b))
        rr = etree.fromstring(zz.read("word/document.xml"))
        bb = rr.find(qn("body"))
        pps = [ch for ch in bb if etree.QName(ch).localname == "p"]
        return etree.tostring(pps[idx])
    assert para_xml(business_template_bytes, 1) == para_xml(out, 1)


def test_apply_chinese_font_eastasia_kept():
    """改中文段落后，首个 run 的 eastAsia 字体 rPr 被保留。"""
    doc = Document()
    p = doc.add_paragraph()
    r = p.add_run("旧文本")
    rpr = r._element.get_or_add_rPr()
    rfonts = rpr.makeelement(qn("rFonts"), {qn("eastAsia"): "宋体"})
    rpr.insert(0, rfonts)
    buf = BytesIO(); doc.save(buf); raw = buf.getvalue()

    model = parse_docx(raw)
    model["blocks"][0]["text"] = "新中文文本"
    out = apply_model(raw, model)

    z = zipfile.ZipFile(BytesIO(out))
    root = etree.fromstring(z.read("word/document.xml"))
    east = root.find(".//" + qn("rFonts"))
    assert east.get(qn("eastAsia")) == "宋体"
    assert "新中文文本" in "".join(root.itertext())


# --------------------------------------------------------------------------- #
# 3b. 混排段落：只改被改到的那几个字，其它 run 的格式不许动
# --------------------------------------------------------------------------- #
def _first_para(raw):
    root = etree.fromstring(zipfile.ZipFile(BytesIO(raw)).read("word/document.xml"))
    return root.find(qn("body")).findall(qn("p"))[0]


def _run_texts(p):
    out = []
    for r in p.findall(qn("r")):
        txt = "".join(t.text or "" for t in r.findall(qn("t")))
        if txt:
            out.append((r, txt))
    return out


def _is_bold(r) -> bool:
    rpr = r.find(qn("rPr"))
    if rpr is None:
        return False
    b = rpr.find(qn("b"))
    return b is not None and b.get(qn("val")) not in ("0", "false")


def test_apply_edit_keeps_mixed_run_formatting():
    """「加粗小标题 + 普通正文」改正文里的一个词，加粗必须还在。

    老实现把整段压成一个 run，加粗小标题变普通字重——编辑器画布上看得出，
    存回 docx 就永久丢了，重开也回不来，全程不报错。
    """
    doc = Document()
    p = doc.add_paragraph()
    r1 = p.add_run("一、总体情况")
    r1.bold = True
    p.add_run("：本期营业收入同比增长。")
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    model = parse_docx(raw)
    blk = model["blocks"][0]
    assert blk["text"] == "一、总体情况：本期营业收入同比增长。"
    blk["text"] = "一、总体情况：本期营业总收入同比增长。"
    out = apply_model(raw, model)

    p0 = _first_para(out)
    runs = _run_texts(p0)
    assert "".join(t for _, t in runs) == "一、总体情况：本期营业总收入同比增长。"
    assert len(runs) == 2, "run 被合并了：%r" % ([t for _, t in runs],)
    assert runs[0][1] == "一、总体情况" and _is_bold(runs[0][0]), "加粗小标题被压平"
    assert not _is_bold(runs[1][0])


def test_apply_edit_inside_one_run_keeps_other_runs_bytes():
    """只改中间那个 run 时，左右两个 run 的 XML 一个字节都不该变。"""
    doc = Document()
    p = doc.add_paragraph()
    a = p.add_run("开头")
    a.italic = True
    p.add_run("中间原文")
    c = p.add_run("结尾")
    c.underline = True
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    before = {t: etree.tostring(r) for r, t in _run_texts(_first_para(raw))}

    model = parse_docx(raw)
    model["blocks"][0]["text"] = "开头中间改过结尾"
    out = apply_model(raw, model)

    after = {t: etree.tostring(r) for r, t in _run_texts(_first_para(out))}
    assert "开头" in after and "结尾" in after
    assert after["开头"] == before["开头"], "被改范围之外的 run 被重写了"
    assert after["结尾"] == before["结尾"]


def test_apply_edit_does_not_delete_tab_run():
    """带制表符的 run 即使文字被删空也不能删——删了制表位就没了。"""
    doc = Document()
    p = doc.add_paragraph()
    p.add_run("前")
    p.add_run().add_tab()
    p.add_run("后")
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()

    model = parse_docx(raw)
    model["blocks"][0]["text"] = "只有这一句"
    out = apply_model(raw, model)

    p0 = _first_para(out)
    assert p0.findall(".//" + qn("tab")), "制表符 run 被删掉了"
    assert "只有这一句" in "".join(t.text or "" for t in p0.iter(qn("t")))


# --------------------------------------------------------------------------- #
# 4. 占位符插入/删除 + 渲染
# --------------------------------------------------------------------------- #
def test_apply_insert_scalar_and_render(business_template_bytes, sample_data):
    model = parse_docx(business_template_bytes)
    # 在文首插入一个新段落，含新标量 {{新变量}}
    new_block = {"id": "new-p0", "type": "paragraph", "text": "补充说明：{{新变量}}"}
    model["blocks"].insert(0, new_block)
    out = apply_model(business_template_bytes, model)

    # 渲染时提供新变量
    data = dict(sample_data)
    data["新变量"] = "插入的标量值"
    rendered = _render_like_service(out, data)
    z = zipfile.ZipFile(BytesIO(rendered))
    text = "".join(etree.fromstring(z.read("word/document.xml")).itertext())
    assert "插入的标量值" in text
    assert "{{新变量}}" not in text


def test_apply_unclosed_block_tag_raises(business_template_bytes):
    """控制标签只有 {% if %} 没有 {% endif %}：渲染必须报错，且错误里指得出问题标签。

    原用例是把审计模板里那句 {%p if not loop.last %} 改成未闭合写法；
    新业务样例没有 {%p %} 控制段，改成把普通段落整段写成控制标签再破坏它，
    验的还是同一件事：半截标签不许静默通过。
    """
    model = parse_docx(business_template_bytes)
    for b in model["blocks"]:
        if b["id"] == "p1":
            b["text"] = "{%p if show %}"
    out = apply_model(business_template_bytes, model)
    with pytest.raises(Exception) as ei:
        DocxTemplate(BytesIO(out)).render({})
    msg = str(ei.value).lower()
    assert "endif" in msg or "if" in msg


# --------------------------------------------------------------------------- #
# 5. 表格增删行 / 改单元格 / 循环行
# --------------------------------------------------------------------------- #
def _make_table_doc():
    doc = Document()
    doc.add_paragraph("表头说明")
    t = doc.add_table(rows=2, cols=2)
    t.rows[0].cells[0].text = "名称"
    t.rows[0].cells[1].text = "等级"
    t.rows[1].cells[0].text = "{{item.name}}"
    t.rows[1].cells[1].text = "{{item.grade}}"
    buf = BytesIO(); doc.save(buf); return buf.getvalue()


def test_apply_table_loop_row_renders_count():
    raw = _make_table_doc()
    model = parse_docx(raw)
    for b in model["blocks"]:
        if b["type"] == "table":
            for row in b["rows"]:
                if row["id"] == "t0r1":
                    row["loop_row"] = True
                    row["loop"] = {"var": "item", "iterable": "items"}
    out = apply_model(raw, model)

    tpl = DocxTemplate(BytesIO(out))
    tpl.render({"items": [{"name": "A", "grade": "高"},
                          {"name": "B", "grade": "中"},
                          {"name": "C", "grade": "低"}]})
    buf = BytesIO(); tpl.save(buf)
    z = zipfile.ZipFile(BytesIO(buf.getvalue()))
    root = etree.fromstring(z.read("word/document.xml"))
    rows = root.find(".//" + qn("tbl")).findall(qn("tr"))
    # 表头 1 行 + 数据 3 行
    assert len(rows) == 4


def test_apply_table_add_delete_edit_row():
    raw = _make_table_doc()
    model = parse_docx(raw)
    for b in model["blocks"]:
        if b["type"] == "table":
            rows = b["rows"]
            # 改表头单元格
            rows[0]["cells"][0]["blocks"][0]["text"] = "指标名称"
            # 删数据行 t0r1
            b["rows"] = [rows[0]]
    out = apply_model(raw, model)
    z = zipfile.ZipFile(BytesIO(out))
    root = etree.fromstring(z.read("word/document.xml"))
    rows = root.find(".//" + qn("tbl")).findall(qn("tr"))
    # 只剩表头 1 行
    assert len(rows) == 1
    assert "指标名称" in "".join(rows[0].itertext())


# --------------------------------------------------------------------------- #
# 6. unsupported 文本框原样保留
# --------------------------------------------------------------------------- #
def _make_docx_with_textbox():
    doc = Document()
    doc.add_paragraph("正常段落")
    # 手工注入 w:txbxContent 到第二个段落
    p = doc.add_paragraph()
    r = p.add_run()
    txbx = r._element.makeelement(qn("pict"), {})
    txbxContent = txbx.makeelement(qn("txbxContent"), {})
    inner_p = txbxContent.makeelement(qn("p"), {})
    inner_r = inner_p.makeelement(qn("r"), {})
    inner_t = inner_r.makeelement(qn("t"), {})
    inner_t.text = "我是文本框"
    inner_r.append(inner_t); inner_p.append(inner_r); txbxContent.append(inner_p)
    txbx.append(txbxContent); r._element.append(txbx)
    buf = BytesIO(); doc.save(buf); return buf.getvalue()


def test_parse_and_apply_textbox_unsupported():
    raw = _make_docx_with_textbox()
    model = parse_docx(raw)
    kinds = [b["type"] for b in model["blocks"]]
    assert "unsupported" in kinds
    # 文本框段落在模型里是 unsupported
    uns = [b for b in model["blocks"] if b["type"] == "unsupported"]
    assert uns and "文本框" in uns[0]["note"]

    # 即使模型里"忘了"回传 unsupported 块，apply 也必须原样保留它的 XML
    stripped = {"blocks": [b for b in model["blocks"] if b["type"] != "unsupported"]}
    out = apply_model(raw, stripped)
    zn = zipfile.ZipFile(BytesIO(out))
    out_xml = zn.read("word/document.xml")
    # txbxContent 与文本框文字必须原样保留
    assert out_xml.find(b"txbxContent") != -1
    assert out_xml.find("我是文本框".encode("utf-8")) != -1
    # 正常段落仍在
    assert out_xml.find("正常段落".encode("utf-8")) != -1


# --------------------------------------------------------------------------- #
# 7. 安全：docm/超大文件拦截
# --------------------------------------------------------------------------- #
def test_security_rejects_docm(blank_template_bytes, tmp_path):
    # 把 blank-template 包装成 docm：篡改 Content_Types 加宏标识
    z = zipfile.ZipFile(BytesIO(blank_template_bytes))
    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zo:
        for item in z.infolist():
            data = z.read(item.filename)
            if item.filename == "[Content_Types].xml":
                data = data.replace(b"wordprocessingml.document.main+xml",
                                    b"wordprocessingml.document.macroEnabled.main+xml")
            zo.writestr(item, data)
    docm_bytes = out.getvalue()
    with pytest.raises(SecurityError) as ei:
        validate_docx(docm_bytes)
    assert ei.value.status == 415


def test_security_rejects_oversize():
    import app.config as cfg
    big = b"PK\x03\x04" + b"\x00" * (cfg.MAX_TEMPLATE_BYTES + 10)
    with pytest.raises(SecurityError) as ei:
        validate_docx(big)
    assert ei.value.status == 413


def test_apply_endpoint_rejects_docm(business_template_bytes):
    """走 HTTP 路由：docm 上传 /apply 仍被拒。"""
    z = zipfile.ZipFile(BytesIO(business_template_bytes))
    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zo:
        for item in z.infolist():
            data = z.read(item.filename)
            if item.filename == "[Content_Types].xml":
                data = data.replace(b"wordprocessingml.document.main+xml",
                                    b"wordprocessingml.document.macroEnabled.main+xml")
            zo.writestr(item, data)
    resp = client.post(
        "/apply",
        files={"file": ("t.docm", out.getvalue(), DOCX_CT)},
        data={"model": json.dumps({"blocks": []})},
    )
    assert resp.status_code == 415
