# -*- coding: utf-8 -*-
"""「引用字体编号 + 时间，输出时现场生成带时间的签字版本」端到端证据。

链路：/signature/render（现场出图）→ data:URI 图片对象 → /render 真出 docx。
这一组不看函数返回值，只量产物里的 wp:extent 与 drawing 落在哪个段落——
「格式有没有被撑坏」「时间有没有真的印上去」只有产物能证明。
"""
import base64
import io

import pytest
from fastapi.testclient import TestClient
from PIL import Image

from app import config
from app.main import app
from app.render import render_template
from tests.test_image_fit import drawing_paragraph_text, extent_mm, template_with_tag

client = TestClient(app)

FONT = "zhi-mang-xing"
NAME = "欧阳建国"

# 不带时间 / 带时间（表达式）两种出图
EXPR = '{"$date":"2026-10-25","format":"%Y-%m-%d"}'


def render_png(**extra) -> bytes:
    r = client.post("/signature/render",
                    data={"signer_name": NAME, "font_id": FONT, "width_mm": "60", **extra})
    assert r.status_code == 200, r.text
    return r.content


def as_context(png: bytes, width_mm=60) -> dict:
    """Go 侧 sigauth 注入的形态：给了宽度、高度留 0（由下游按原图比例反算）。"""
    return {"type": "image",
            "src": "data:image/png;base64," + base64.b64encode(png).decode(),
            "width": width_mm, "height": 0}


def plain_size(png: bytes) -> tuple[int, int]:
    im = Image.open(io.BytesIO(png))
    return im.width, im.height


def test_generated_png_gets_taller_with_timestamp():
    """第一层证据：出图本身——带时间的那张更高。"""
    h0 = plain_size(render_png())[1]
    h1 = plain_size(render_png(timestamp_expr=EXPR))[1]
    assert h1 > h0 * 1.1


def test_docx_keeps_signature_in_its_own_paragraph():
    """带时间的签章仍然落在「签字：」那一段里，不新起段落。"""
    out = render_template(template_with_tag("签字：{{sig}}"),
                          {"sig": as_context(render_png(timestamp_expr=EXPR))})
    paras = drawing_paragraph_text(out)
    assert len(paras) == 1
    assert "签字：" in paras[0]


def test_docx_height_grows_exactly_because_of_timestamp():
    """同一份模板、同一个宽度，带时间的产物在 Word 里更高——版式只被「时间那一行」撑高。"""
    tpl = template_with_tag("签字：{{sig}}")
    w0, h0 = extent_mm(render_template(tpl, {"sig": as_context(render_png())}))
    w1, h1 = extent_mm(render_template(tpl, {"sig": as_context(render_png(timestamp_expr=EXPR))}))
    assert w0 == pytest.approx(60, abs=0.5)
    assert w1 == pytest.approx(60, abs=0.5)
    assert h1 > h0


def test_docx_size_stays_inside_text_area():
    """再高的签章也不会把版面撑开：仍被夹在版心内。"""
    for extra in ({}, {"timestamp_expr": EXPR},
                  {"timestamp_expr": '{"$date":"2026-10-25","format":"%Y-%m-%d %H:%M:%S"}'}):
        w, h = extent_mm(render_template(template_with_tag("签字：{{sig}}"),
                                        {"sig": as_context(render_png(**extra))}))
        assert w <= config.MAX_INLINE_IMAGE_WIDTH_MM
        assert h <= config.MAX_INLINE_IMAGE_HEIGHT_MM


def test_different_dates_produce_different_documents():
    """两份产物里嵌的签名图字节不同——证明「时间表达式」真的进了图，而不是被忽略。"""
    import zipfile

    def media_bytes(expr: str) -> bytes:
        out = render_template(template_with_tag("签字：{{sig}}"),
                              {"sig": as_context(render_png(timestamp_expr=expr))})
        with zipfile.ZipFile(io.BytesIO(out)) as z:
            names = [n for n in z.namelist() if n.startswith("word/media/")]
            assert names, "产物里没有图片部件"
            return z.read(names[0])

    a = media_bytes('{"$date":"2026-10-25","format":"%Y-%m-%d"}')
    b = media_bytes('{"$date":"2020-01-02","format":"%Y-%m-%d"}')
    assert a != b


def test_width_only_request_keeps_aspect_ratio():
    """Go 传 width=60 / height=0 时高度必须按原图比例反算，不能被压扁。"""
    png = render_png(timestamp_expr=EXPR)
    px_w, px_h = plain_size(png)
    w, h = extent_mm(render_template(template_with_tag("签字：{{sig}}"),
                                    {"sig": as_context(png)}))
    assert w == pytest.approx(60, abs=0.5)
    assert h == pytest.approx(60 * px_h / px_w, abs=1.0)
