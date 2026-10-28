# -*- coding: utf-8 -*-
"""内联图片尺寸自适应（V4）。

用户反馈「插入签名和印章图片的时候，自适应大小，不要破坏模板的格式」。
这一组用例锁住三条硬要求：

  1. 不拉伸 —— 输出宽高比必须等于原图宽高比（旧实现把记录里的
     width/height 直接写进 wp:extent，比例不对就把图压扁）；
  2. 不撑破版面 —— 超过版心宽度必须等比缩小（窄格子里塞 60mm 签名的
     后果是表格行高被顶开、正文整体下移）；
  3. 只给一维时反算另一维，而不是原样透传。
"""
from __future__ import annotations

import io

import pytest
from PIL import Image

from app import config, richdata


def png_bytes(w: int, h: int, dpi: int | None = None) -> bytes:
    im = Image.new("RGB", (w, h), (255, 255, 255))
    buf = io.BytesIO()
    kw = {}
    if dpi:
        kw["dpi"] = (dpi, dpi)
    im.save(buf, format="PNG", **kw)
    return buf.getvalue()


def ratio(w: float, h: float) -> float:
    return w / h


def test_keeps_recorded_size_when_ratio_matches():
    """记录宽高比与原图一致：按记录尺寸摆放，不做无谓改动。"""
    raw = png_bytes(400, 200)  # 2:1
    w, h = richdata.fit_image_size(raw, 40, 20)
    assert (w, h) == (40, 20)


def test_does_not_squash_when_recorded_ratio_differs():
    """记录是 40x40（正方形）但原图是 2:1：高度必须按原图反算成 20。"""
    raw = png_bytes(400, 200)
    w, h = richdata.fit_image_size(raw, 40, 40)
    assert w == 40
    assert h == 20
    assert ratio(w, h) == pytest.approx(2.0, rel=1e-6)


def test_clamps_to_text_area_width():
    """超过版心宽度：等比缩小到上限，比例不变。"""
    raw = png_bytes(1200, 600)  # 2:1
    w, h = richdata.fit_image_size(raw, 300, 150)
    assert w <= config.MAX_INLINE_IMAGE_WIDTH_MM
    assert ratio(w, h) == pytest.approx(2.0, abs=0.02)


def test_clamps_height():
    """高度超限同样要收，否则行内图片会把整行撑得极高。"""
    raw = png_bytes(200, 400)  # 1:2，竖图
    w, h = richdata.fit_image_size(raw, 40, 80)
    assert h <= config.MAX_INLINE_IMAGE_HEIGHT_MM
    assert ratio(w, h) == pytest.approx(0.5, abs=0.01)


def test_only_width_given_derives_height():
    raw = png_bytes(300, 100)  # 3:1
    w, h = richdata.fit_image_size(raw, 30, None)
    assert w == 30
    assert h == pytest.approx(10, abs=0.05)


def test_only_height_given_derives_width():
    raw = png_bytes(300, 100)  # 3:1
    w, h = richdata.fit_image_size(raw, None, 10)
    assert h == 10
    assert w == pytest.approx(30, abs=0.15)


def test_no_size_falls_back_to_dpi_and_is_clamped():
    """一个尺寸都没给：按 DPI 折算物理尺寸，并且仍受版心钳制。"""
    raw = png_bytes(4000, 2000, dpi=96)  # 96dpi 下宽约 1058mm
    w, h = richdata.fit_image_size(raw, None, None)
    assert w is not None and h is not None
    assert w <= config.MAX_INLINE_IMAGE_WIDTH_MM
    assert h <= config.MAX_INLINE_IMAGE_HEIGHT_MM
    assert ratio(w, h) == pytest.approx(2.0, abs=0.02)


def test_undecodable_image_passes_through():
    """不是图片（或截断的字节）：不猜尺寸，交给上层原有行为处理。"""
    w, h = richdata.fit_image_size(b"not-an-image", 10, 10)
    assert (w, h) == (10, 10)


# --------------------------------------------------------------------------- #
# 端到端：签章标签留在原段落里，图片尺寸受版心约束
#
# 这一组是「插入签名/印章不要破坏模板格式」在产物层面的证据：
# 光看函数返回值不够 —— 真正决定版面的是生成出来的 docx 里 wp:extent 的值，
# 以及那个 drawing 到底落在哪个 w:p 里。
# --------------------------------------------------------------------------- #
import base64  # noqa: E402

from docx import Document  # noqa: E402
from lxml import etree  # noqa: E402

from app.render import render_template  # noqa: E402

WP_NS = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
EMU_PER_MM = 36000


def template_with_tag(text: str = "签字：{{sig}}") -> bytes:
    doc = Document()
    doc.add_paragraph(text)
    buf = io.BytesIO()
    doc.save(buf)
    return buf.getvalue()


def extent_mm(docx_bytes: bytes) -> tuple[float, float]:
    import zipfile
    with zipfile.ZipFile(io.BytesIO(docx_bytes)) as z:
        root = etree.fromstring(z.read("word/document.xml"))
    ext = root.find(".//{%s}extent" % WP_NS)
    assert ext is not None, "产物里没有内联图片"
    return (round(int(ext.get("cx")) / EMU_PER_MM, 2),
            round(int(ext.get("cy")) / EMU_PER_MM, 2))


def drawing_paragraph_text(docx_bytes: bytes) -> list[str]:
    """哪些段落里含 drawing，返回这些段落的文字。"""
    import zipfile
    with zipfile.ZipFile(io.BytesIO(docx_bytes)) as z:
        root = etree.fromstring(z.read("word/document.xml"))
    out = []
    for p in root.iter("{http://schemas.openxmlformats.org/wordprocessingml/2006/main}p"):
        if p.find(".//{%s}inline" % WP_NS) is not None:
            out.append("".join(t.text or "" for t in p.iter(
                "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}t")))
    return out


def image_data(w: int, h: int, want_w=None, want_h=None) -> dict:
    raw = png_bytes(w, h)
    return {
        "type": "image",
        "src": "data:image/png;base64," + base64.b64encode(raw).decode(),
        "width": want_w,
        "height": want_h,
    }


def test_signature_stays_in_its_paragraph():
    """图片必须落在「签字：」那一段里，而不是新起一段。

    新起一段会把正文整体往下推一行，这是用户看到的"格式被搞乱"。
    """
    tpl = template_with_tag("签字：{{sig}}")
    out = render_template(tpl, {"sig": image_data(400, 200, 40, 20)})
    paras = drawing_paragraph_text(out)
    assert len(paras) == 1
    assert "签字：" in paras[0]


def test_signature_size_follows_source_ratio():
    """记录尺寸与原图比例一致：按记录摆，不变形。"""
    tpl = template_with_tag()
    out = render_template(tpl, {"sig": image_data(400, 200, 40, 20)})
    w, h = extent_mm(out)
    assert (w, h) == (40, 20)


def test_signature_is_scaled_down_not_squashed():
    """记录是 60x20，但原图是 2:1 —— 生成结果必须是 60x30，而不是被压扁的 60x20。"""
    tpl = template_with_tag()
    out = render_template(tpl, {"sig": image_data(600, 300, 60, 20)})
    w, h = extent_mm(out)
    assert w == 60
    assert h == pytest.approx(30, abs=0.5)


def test_oversized_signature_is_clamped_to_text_area():
    """超宽签名会被等比缩到版心内，不会把整行撑开。"""
    tpl = template_with_tag()
    out = render_template(tpl, {"sig": image_data(800, 400, 400, 200)})
    w, h = extent_mm(out)
    assert w <= config.MAX_INLINE_IMAGE_WIDTH_MM
    assert h <= config.MAX_INLINE_IMAGE_HEIGHT_MM
    assert w / h == pytest.approx(2.0, abs=0.05)

