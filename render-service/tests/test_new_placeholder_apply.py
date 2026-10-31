# -*- coding: utf-8 -*-
"""V3 修复：apply 支持新增 image/qrcode/signature/seal 占位块（new- placeholder）。

前端编辑器插入签名/印章/图片时新增的是 {id:"new-...",type:"placeholder",kind,
name,width_mm,height_mm}；修复前这类块在 apply 时被忽略，导致预览永远看不到。
修法：new- placeholder 与 paragraph 一样构造段落，text="{{name}}"，由 docxtpl
在渲染时把 {type:image,...} 上下文转成 InlineImage。
"""
import io
import json
import zipfile

import pytest
from fastapi.testclient import TestClient
from lxml import etree
from PIL import Image as PILImage
from docxtpl import DocxTemplate, InlineImage
from docx.shared import Mm

from app.main import app
from app.docmodel import parse_docx
from app.docapply import apply_model
from app.preprocess import qn

client = TestClient(app)
DOCX_CT = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


def _make_test_png() -> bytes:
    """造一张红/透明底测试 PNG 字节。"""
    im = PILImage.new("RGBA", (80, 40), (200, 16, 46, 255))
    buf = io.BytesIO()
    im.save(buf, format="PNG")
    return buf.getvalue()


def test_new_placeholder_appears_as_tag(migrated_template_bytes):
    """apply 后 document.xml 应含 {{sig_test1}}，且 /scan 能扫到该占位。"""
    model = parse_docx(migrated_template_bytes)
    new_block = {
        "id": "new-sig1", "type": "placeholder", "kind": "image",
        "name": "sig_test1", "width_mm": 40, "height_mm": 20,
    }
    model["blocks"].append(new_block)
    out = apply_model(migrated_template_bytes, model)

    z = zipfile.ZipFile(io.BytesIO(out))
    doc_xml = z.read("word/document.xml").decode("utf-8")
    assert "{{sig_test1}}" in doc_xml, "新增占位块未写入 {{sig_test1}}"

    # /scan 能扫到 sig_test1 变量
    r = client.post("/scan", files={"file": ("t.docx", out, DOCX_CT)}, data={"repair": "true"})
    assert r.status_code == 200, r.text
    names = {v["name"] for v in r.json()["variables"]}
    assert "sig_test1" in names


def test_new_placeholder_renders_inline_image(migrated_template_bytes, sample_data):
    """用图片上下文渲染后：{{sig_test1}} 被替换，媒体目录新增图片，无残留标签。"""
    model = parse_docx(migrated_template_bytes)
    model["blocks"].append({
        "id": "new-sig1", "type": "placeholder", "kind": "image",
        "name": "sig_test1", "width_mm": 40, "height_mm": 20,
    })
    out = apply_model(migrated_template_bytes, model)

    # 渲染上下文：sig_test1 -> InlineImage（等价 richdata 对 {type:image} 的处理）
    data = dict(sample_data)
    tpl = DocxTemplate(io.BytesIO(out))
    data["sig_test1"] = InlineImage(
        tpl, io.BytesIO(_make_test_png()), width=Mm(40), height=Mm(20))
    tpl.render(data)
    buf = io.BytesIO()
    tpl.save(buf)

    z = zipfile.ZipFile(io.BytesIO(buf.getvalue()))
    doc_xml = z.read("word/document.xml").decode("utf-8")
    # 渲染后无残留标签
    assert "{{sig_test1}}" not in doc_xml
    # 媒体目录新增图片
    media = [n for n in z.namelist() if n.startswith("word/media/")]
    assert media, "渲染后 media 目录应含内嵌图片"
    # document.xml 含 drawing 引用图片
    assert "<w:drawing" in doc_xml or "drawing:" in doc_xml


def test_new_placeholder_in_table_cell(migrated_template_bytes):
    """表格单元格内新增占位块：经 _update_cell 写成 {{sig_cell}} 段落。"""
    model = parse_docx(migrated_template_bytes)
    t0 = next(b for b in model["blocks"] if b["type"] == "table")
    # 往第一行最后一格追加一个新占位块
    first_row = t0["rows"][0]
    cell = first_row["cells"][-1]
    cell["blocks"].append({
        "id": "new-cellph", "type": "placeholder", "kind": "image",
        "name": "sig_cell",
    })
    out = apply_model(migrated_template_bytes, model)

    z = zipfile.ZipFile(io.BytesIO(out))
    doc_xml = z.read("word/document.xml").decode("utf-8")
    assert "{{sig_cell}}" in doc_xml
