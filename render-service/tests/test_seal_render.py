# -*- coding: utf-8 -*-
"""V3 §6.1：POST /seal/render 出图契约与几何正确性。"""
import io

from PIL import Image
from fastapi.testclient import TestClient

from app.main import app

client = TestClient(app)

PNG_MAGIC = b"\x89PNG\r\n\x1a\n"
SEAL_RED = (200, 16, 46, 255)


def _png(resp):
    assert resp.status_code == 200, resp.text
    assert resp.headers["content-type"] == "image/png"
    assert resp.content.startswith(PNG_MAGIC)
    im = Image.open(io.BytesIO(resp.content))
    assert im.mode == "RGBA"
    return im


def _red_fraction(im):
    px = im.load()
    w, h = im.size
    red = 0
    total = 0
    for y in range(0, h, 2):
        for x in range(0, w, 2):
            total += 1
            r, g, b, a = px[x, y]
            if a > 128 and r > 150 and g < 90 and b < 90:
                red += 1
    return red / total if total else 0.0


def _center_red(im):
    px = im.load()
    cx, cy = im.width // 2, im.height // 2
    # 中心附近是否有红像素（五角星或印章中心）
    for dy in range(-8, 9, 2):
        for dx in range(-8, 9, 2):
            r, g, b, a = px[cx + dx, cy + dy]
            if a > 128 and r > 150 and g < 90 and b < 90:
                return True
    return False


def test_seal_circle_basic():
    im = _png(client.post("/seal/render", data={
        "unit_name": "示例信息技术有限公司", "seal_name": "合同专用章"}))
    # 中心有红（五角星）
    assert _center_red(im)
    # 红色像素占比合理（印章红，不能几乎为 0 也不能满版）
    frac = _red_fraction(im)
    assert 0.02 < frac < 0.6, f"红色占比异常 {frac}"
    # 圆形：宽高接近
    ratio = im.width / im.height
    assert 0.9 < ratio < 1.1


def test_seal_ellipse_ratio():
    im = _png(client.post("/seal/render", data={
        "unit_name": "示例科技安全实验室研发中心", "seal_name": "财务专用章",
        "shape": "ellipse", "radius_mm": "18"}))
    # 椭圆更宽：rx=radius*1.18 -> 宽高比应 >1.1
    ratio = im.width / im.height
    assert ratio > 1.1, f"椭圆宽高比 {ratio} 不对"


def test_seal_no_star():
    im = _png(client.post("/seal/render", data={
        "unit_name": "示例信息技术有限公司", "seal_name": "发票专用章", "star": "false"}))
    # 无星章中心附近应无大红色块（印章名在下方，中心空心）
    assert not _center_red(im)
    frac = _red_fraction(im)
    assert 0.01 < frac < 0.5


def test_seal_422_bad_shape():
    r = client.post("/seal/render", data={"unit_name": "某单位", "seal_name": "章", "shape": "square"})
    assert r.status_code == 422


def test_seal_422_empty():
    r = client.post("/seal/render", data={"unit_name": "", "seal_name": ""})
    assert r.status_code == 422
