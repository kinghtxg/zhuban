# -*- coding: utf-8 -*-
"""V3 §6.1：GET /fonts、POST /signature/render 契约与出图质量。"""
import io

import pytest
from PIL import Image
from fastapi.testclient import TestClient

from app.main import app

client = TestClient(app)

PNG_MAGIC = b"\x89PNG\r\n\x1a\n"


def _png(resp):
    assert resp.status_code == 200, resp.text
    assert resp.headers["content-type"] == "image/png"
    assert resp.content.startswith(PNG_MAGIC)
    im = Image.open(io.BytesIO(resp.content))
    assert im.mode == "RGBA", f"应透明底 RGBA，实际 {im.mode}"
    return im


def test_list_fonts():
    r = client.get("/fonts")
    assert r.status_code == 200
    body = r.json()
    assert "fonts" in body and len(body["fonts"]) >= 5
    ids = {f["id"] for f in body["fonts"]}
    assert {"ma-shan-zheng", "liu-jian-mao-cao", "zhi-mang-xing", "long-cang",
            "dancing-script"} <= ids
    for f in body["fonts"]:
        assert {"id", "name", "family", "style"} <= set(f)


def test_signature_basic_png():
    im = _png(client.post("/signature/render", data={"signer_name": "张三", "font_id": "ma-shan-zheng"}))
    # 透明底：四角应完全透明
    px = im.load()
    for corner in ((0, 0), (im.width - 1, 0), (0, im.height - 1), (im.width - 1, im.height - 1)):
        assert px[corner][3] == 0
    # 尺寸合理：默认 width_mm=60，PPI=200 -> 约 472px 宽
    assert 300 < im.width < 700
    assert im.height > 40


def test_signature_width_scales():
    im_small = _png(client.post("/signature/render", data={"signer_name": "张三", "font_id": "long-cang", "width_mm": "30"}))
    im_big = _png(client.post("/signature/render", data={"signer_name": "张三", "font_id": "long-cang", "width_mm": "90"}))
    # 大 width 出图应明显更宽
    assert im_big.width > im_small.width * 1.5


def test_signature_422_on_missing_or_bad_font():
    # 缺 signer_name
    r = client.post("/signature/render", data={"font_id": "ma-shan-zheng"})
    assert r.status_code in (400, 422)
    # 名字太短（<2）
    r = client.post("/signature/render", data={"signer_name": "张", "font_id": "ma-shan-zheng"})
    assert r.status_code == 422
    # 非法 font_id
    r = client.post("/signature/render", data={"signer_name": "张三", "font_id": "no-such-font"})
    assert r.status_code == 422


def test_signature_timestamp_toggle_differs():
    im_no = _png(client.post("/signature/render", data={
        "signer_name": "欧阳建国", "font_id": "zhi-mang-xing",
        "with_timestamp": "false", "timestamp": "2026-10-19T10:30:00+08:00"}))
    im_yes = _png(client.post("/signature/render", data={
        "signer_name": "欧阳建国", "font_id": "zhi-mang-xing",
        "with_timestamp": "true", "timestamp": "2026-10-19T10:30:00+08:00"}))
    # 带时间戳的图应更高
    assert im_yes.height > im_no.height * 1.1


# ---------------------------------------------------------------- #
# 日期表达式：出图那一刻现场求值（而不是调用方先算好传进来）
# ---------------------------------------------------------------- #
BASE = {"signer_name": "欧阳建国", "font_id": "zhi-mang-xing"}


def test_timestamp_expr_is_rendered_without_with_timestamp_flag():
    """给了表达式就等于要印时间——不该逼调用方重复声明 with_timestamp。"""
    im_no = _png(client.post("/signature/render", data=BASE))
    im_ex = _png(client.post("/signature/render", data={
        **BASE, "timestamp_expr": '{"$date":"2026-10-25","format":"%Y-%m-%d"}'}))
    assert im_ex.height > im_no.height * 1.1


def test_timestamp_expr_actually_changes_pixels():
    """不同日期必须画出不同的像素——否则等于表达式没生效。"""
    a = _png(client.post("/signature/render", data={
        **BASE, "timestamp_expr": '{"$date":"2026-10-25","format":"%Y-%m-%d"}'}))
    b = _png(client.post("/signature/render", data={
        **BASE, "timestamp_expr": '{"$date":"2020-01-02","format":"%Y-%m-%d"}'}))
    assert a.tobytes() != b.tobytes(), "两个不同日期画出了完全一样的图"


def test_timestamp_prefix_override_changes_pixels():
    """默认前缀「服务器时间：」能改掉（公文上通常不要这几个字）。"""
    d = {"signer_name": "欧阳建国", "font_id": "zhi-mang-xing",
         "timestamp_expr": '{"$date":"2026-10-25","format":"%Y-%m-%d"}'}
    default_im = _png(client.post("/signature/render", data=d))
    signed_im = _png(client.post("/signature/render", data={**d, "timestamp_prefix": "签署日期："}))
    assert default_im.tobytes() != signed_im.tobytes()


def test_timestamp_expr_now_uses_shanghai_time():
    """`now` 取的是服务端这一刻的北京时间，与容器 tz 无关。"""
    from datetime import datetime, timedelta, timezone

    from app.signature import resolve_timestamp_text

    bj = timezone(timedelta(hours=8))
    before = datetime.now(bj).strftime("%Y-%m-%d %H:%M")
    got = resolve_timestamp_text(timestamp_expr='{"$date":"now","format":"%Y-%m-%d %H:%M"}')
    after = datetime.now(bj).strftime("%Y-%m-%d %H:%M")
    assert got in (before, after), f"应为北京时间当下，实际 {got}"


def test_timestamp_text_normalization_rules():
    from app.signature import resolve_timestamp_text

    # 字面 ISO 时间 → 默认格式
    assert resolve_timestamp_text("2026-10-25T09:05:00+08:00") == "2026-10-25 09:05:00"
    # 纯日期也接受
    assert resolve_timestamp_text("2026-10-25") == "2026-10-25 00:00:00"
    # 表达式原样使用它自己的 format
    assert resolve_timestamp_text(
        timestamp_expr='{"$date":"2026-10-25","format":"%Y年%m月%d日"}') == "2026年10月25日"
    # 没给 → None（交给容器当下时间兜底）
    assert resolve_timestamp_text() is None
    # 给了但解析不了 → 报错，绝不静默退回「现在」
    for bad in ("2026-13-45", "昨天"):
        with pytest.raises(ValueError):
            resolve_timestamp_text(bad)


@pytest.mark.parametrize("payload", [
    {"timestamp_expr": "not-json"},
    {"timestamp_expr": '{"$date":"todya"}'},                 # 拼错 base
    {"timestamp_expr": '{"$date":"now","offset_day":3}'},    # 拼错字段
    {"timestamp_expr": '{"date":"now"}'},                    # 少 $
    {"timestamp": "2026-10-25T09:00:00+08:00",
     "timestamp_expr": '{"$date":"now"}'},                   # 两种时间打架
    {"timestamp": "2026-13-45"},                             # 非法字面时间
])
def test_timestamp_bad_input_is_422(payload):
    r = client.post("/signature/render", data={**BASE, **payload})
    assert r.status_code == 422, r.text
    assert r.json()["error"]["code"] in ("invalid_timestamp", "invalid_param")

