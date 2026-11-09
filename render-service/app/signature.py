# -*- coding: utf-8 -*-
"""电子签名图生成（PRD V3 §6.1，POST /signature/render）。

用内置手写风字体把姓名渲染成透明底 PNG；可选下方一行灰色服务器时间小字。

时间为什么要分两种形态
----------------------
一次性预生成的签名图，时间就被烤进像素里了——同一张图用在三份文件上，
三份都印着同一个「签署时间」，而且都是生成那一刻的时间，不是出具那一刻。
所以除了字面时间（ISO 8601），还接受**日期表达式**（`{"$date":"now",...}`）：
把「怎么算」传进来，由本模块在**出图那一刻**现场求值。语义与 `dateexpr`
完全一致（默认时区 Asia/Shanghai、支持 offset_*/format）。

三种时间来源的优先级：timestamp_expr（表达式）> timestamp（字面）> 容器当下时间。
两者同时给直接报错——静默挑一个等于让用户以为生效的是另一个。
"""
from __future__ import annotations

import io
import json
from datetime import datetime, timezone
from typing import Optional

from PIL import Image, ImageDraw, ImageFont

from . import dateexpr
from .fonts import load_font, resolve_font_path

# 出图分辨率（DPI）：Word 按 width_mm 插入，200 DPI 保证打印清晰
PPI = 200
# 透明边距（像素，按 PPI）
PAD_X_RATIO = 0.06
MAIN_COLOR = (20, 20, 20, 255)          # 近黑签名墨色
TS_COLOR = (102, 102, 102, 255)         # #666 时间小字

# 字面时间（精确到秒）的默认呈现；表达式形态由表达式自己的 format 决定
DEFAULT_TS_FORMAT = "%Y-%m-%d %H:%M:%S"
DEFAULT_TS_PREFIX = "服务器时间："


def _mm_to_px(mm: float) -> int:
    return int(round(mm / 25.4 * PPI))


def _parse_timestamp(ts: Optional[str]) -> datetime:
    """解析 ISO 8601 时间；缺失返回本机当前时间，**给了但解析不了直接报错**。

    早期实现解析失败会静默退回「现在」——那会把「2026-13-45」印刷成今天，
    校验还是绿的。这里改成抛 ValueError，由调用方转 422。
    """
    if not ts or not str(ts).strip():
        return datetime.now().astimezone()
    s = str(ts).strip()
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(s)
    except ValueError:
        raise ValueError("timestamp 无法解析为 ISO 8601 时间：%r" % ts)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


def resolve_timestamp_text(timestamp: str = "", timestamp_expr: str = "") -> Optional[str]:
    """把时间参数归一成「要印在图上的文本」（不含前缀）。

    返回 None 表示「没指定」——由调用方用容器当下时间兜底。
    表达式在**调用这一刻**求值，所以每份产物拿到的都是自己出具时的时间。
    """
    ts = (timestamp or "").strip()
    ex = (timestamp_expr or "").strip()
    if ts and ex:
        raise ValueError("timestamp 与 timestamp_expr 不能同时提供，二选一")
    if ex:
        try:
            node = json.loads(ex)
        except Exception as e:
            raise ValueError("timestamp_expr 不是合法 JSON：%s" % e)
        if not dateexpr.is_date_expr(node):
            raise ValueError(
                'timestamp_expr 需形如 {"$date":"now","format":"%Y-%m-%d %H:%M"}')
        try:
            return dateexpr.parse(node, "timestamp_expr").evaluate()
        except dateexpr.DateExprError as e:
            raise ValueError("timestamp_expr 有误：%s" % e.message)
    if ts:
        return _parse_timestamp(ts).strftime(DEFAULT_TS_FORMAT)
    return None


def render_signature_png(
    signer_name: str,
    font_id: str,
    width_mm: float = 60.0,
    with_timestamp: bool = False,
    timestamp: Optional[str] = None,
    timestamp_text: Optional[str] = None,
    timestamp_prefix: str = DEFAULT_TS_PREFIX,
) -> bytes:
    """渲染签名 PNG，返回 PNG 字节（透明底 RGBA）。

    高度按文字比例自适应；时间小字字号约为主签名的 20%。

    timestamp_text 是已经算好的时间文本（表达式或字面时间都走这条），
    给了就直接印；不给就走 timestamp / 容器当下时间。
    """
    if resolve_font_path(font_id) is None:
        raise KeyError(f"未知字体 id: {font_id}")

    name = (signer_name or "").strip()
    target_w = _mm_to_px(width_mm)
    pad_x = int(target_w * PAD_X_RATIO)
    avail_w = target_w - 2 * pad_x

    # 1) 以探测字号量字宽，反推主字号，使姓名宽度占满 avail_w
    probe = 100
    probe_font = load_font(font_id, probe)
    bbox = probe_font.getbbox(name)
    text_w = bbox[2] - bbox[0]
    if text_w <= 0:
        text_w = 1
    scale = avail_w / text_w
    main_size = max(24, int(probe * scale))
    main_font = load_font(font_id, main_size)

    # 主文本尺寸
    mb = main_font.getbbox(name)
    main_w = mb[2] - mb[0]
    main_h = mb[3] - mb[1]

    ts_size = max(16, int(main_size * 0.20))
    ts_font = load_font(font_id, ts_size)

    ts_text = ""
    ts_h = 0
    gap = 0
    if with_timestamp:
        body = timestamp_text
        if body is None:
            body = _parse_timestamp(timestamp).strftime(DEFAULT_TS_FORMAT)
        # 前缀刻意留空也要尊重用户（合同上「服务器时间」这几字往往要去掉）
        ts_text = (timestamp_prefix if timestamp_prefix is not None else DEFAULT_TS_PREFIX) + body
        tb = ts_font.getbbox(ts_text)
        ts_h = tb[3] - tb[1]
        gap = int(main_size * 0.18)

    pad_top = int(main_size * 0.15)
    pad_bottom = int(main_size * 0.20)

    canvas_w = target_w
    canvas_h = pad_top + main_h + gap + ts_h + pad_bottom

    img = Image.new("RGBA", (canvas_w, canvas_h), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)

    # 主签名：水平居中，基线附近垂直排布
    x_main = (canvas_w - main_w) // 2 - mb[0]
    y_main = pad_top - mb[1]
    draw.text((x_main, y_main), name, font=main_font, fill=MAIN_COLOR)

    if with_timestamp:
        tb = ts_font.getbbox(ts_text)
        ts_w = tb[2] - tb[0]
        x_ts = (canvas_w - ts_w) // 2 - tb[0]
        y_ts = pad_top + main_h + gap - tb[1]
        draw.text((x_ts, y_ts), ts_text, font=ts_font, fill=TS_COLOR)

    # 2) 按内容包围盒裁剪 + 四周透明边距（避免贴边）
    bbox = img.getbbox()
    if bbox:
        m = int(PPI * 0.08)  # ~0.08mm 透明边距
        l = max(0, bbox[0] - m)
        t = max(0, bbox[1] - m)
        r = min(img.width, bbox[2] + m)
        b = min(img.height, bbox[3] + m)
        img = img.crop((l, t, r, b))

    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()
