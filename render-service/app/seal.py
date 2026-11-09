# -*- coding: utf-8 -*-
"""电子印章图生成（PRD V3 §6.1，POST /seal/render）。

红色 #C8102E 公章：外圈圆环 + 环形单位名（沿弧排布，中文正立、底部不倒字）
+ 中央五角星 + 五角星下方横排印章名；透明底 PNG。
椭圆章：水平半径 = radius_mm*1.18，文字沿椭圆弧排布。
"""
from __future__ import annotations

import io
import math
from typing import List, Tuple

from PIL import Image, ImageDraw, ImageFont

from .fonts import load_font, resolve_font_path

PPI = 200
SEAL_RED = (200, 16, 46, 255)  # #C8102E

# 环形文字所用字体（中文单位名用毛笔楷书，观感接近真章宋体/楷体）
RING_FONT_ID = "ma-shan-zheng"
SEAL_NAME_FONT_ID = "ma-shan-zheng"


def _mm_to_px(mm: float) -> float:
    return mm / 25.4 * PPI


def _star_points(cx: float, cy: float, r_outer: float, r_inner: float) -> List[Tuple[float, float]]:
    """五角星顶点（10 个点，outer/inner 交替），顶点朝上。"""
    pts = []
    # 顶点从 -90°（正上）开始，每 36° 一个外点/内点
    for i in range(10):
        ang = math.radians(-90 + i * 36)
        r = r_outer if i % 2 == 0 else r_inner
        pts.append((cx + r * math.cos(ang), cy + r * math.sin(ang)))
    return pts


def _draw_ring_text(
    img: Image.Image,
    text: str,
    cx: float,
    cy: float,
    rx: float,
    ry: float,
    font: ImageFont.FreeTypeFont,
) -> None:
    """把 text 的每个字沿椭圆上弧均匀排布，字底朝圆心（顶部正立、底部不倒字）。

    文字沿上半圆展开：从左下经正上到右下；字符朝向按该处外法线旋转。
    """
    draw = ImageDraw.Draw(img)
    n = len(text)
    if n == 0:
        return
    # 弧跨度：居中于正上方(-90°)。短名字收窄弧，长名字放宽但底部留 90° 缺口
    # 每字角距：依字号与环半径估算，避免字重叠或拉太开
    # 字高约 char_px；对应圆心角 ≈ char_px / mean_radius
    char_px = font.size
    mean_r = (rx + ry) / 2
    pitch = math.radians(char_px * 1.15 / mean_r)  # 每字角距（弧度）
    span = pitch * n
    max_span = math.radians(270)   # 底部留 90°
    min_span = math.radians(120)
    span = max(min_span, min(max_span, span))
    start = -math.pi / 2 - span / 2  # 从左端开始

    for i, ch in enumerate(text):
        ang = start + pitch * (i + 0.5)
        # 重新按等距在 span 内分配（避免 pitch 与 span 不一致导致末尾偏差）
        ang = start + span * (i + 0.5) / n
        x = cx + rx * math.cos(ang)
        y = cy + ry * math.sin(ang)
        # 外法线方向角（椭圆近似）
        normal = math.atan2(ry * math.sin(ang), rx * math.cos(ang))
        # 字符需要旋转的角度（度）：让字底朝圆心。顶部 ang=-90 时旋转 0。
        rot_deg = math.degrees(normal) + 90
        # 单字渲染到小画布
        bbox = font.getbbox(ch)
        w = bbox[2] - bbox[0]
        h = bbox[3] - bbox[1]
        tile = Image.new("RGBA", (w + 8, h + 8), (0, 0, 0, 0))
        td = ImageDraw.Draw(tile)
        td.text((4 - bbox[0], 4 - bbox[1]), ch, font=font, fill=SEAL_RED)
        # PIL rotate 逆时针为正；我们要顺时针 rot_deg
        tile = tile.rotate(-rot_deg, expand=True, resample=Image.BICUBIC)
        img.paste(tile, (int(x - tile.width / 2), int(y - tile.height / 2)), tile)


def render_seal_png(
    unit_name: str,
    seal_name: str,
    shape: str = "circle",
    radius_mm: float = 20.0,
    star: bool = True,
) -> bytes:
    """渲染印章 PNG（透明底 RGBA），返回 PNG 字节。"""
    unit_name = (unit_name or "").strip()
    seal_name = (seal_name or "").strip()
    if not unit_name and not seal_name:
        raise ValueError("unit_name 与 seal_name 不能同时为空")

    is_ellipse = shape == "ellipse"
    base_r = _mm_to_px(radius_mm)
    if is_ellipse:
        rx = base_r * 1.18
        ry = base_r
    else:
        rx = ry = base_r

    # 画布：半径 + 描边 + 留白
    stroke = max(3, int(base_r * 0.06))
    margin = stroke + int(base_r * 0.08)
    cx = int(rx + margin)
    cy = int(ry + margin)
    W = int(cx + rx + margin)
    H = int(cy + ry + margin)

    img = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)

    # 外圈
    draw.ellipse(
        [cx - rx, cy - ry, cx + rx, cy + ry],
        outline=SEAL_RED, width=stroke,
    )

    # 环形单位名：放在环内侧约 65% 半径处
    ring_font_size = max(14, int(base_r * (0.22 if len(unit_name) <= 12 else 0.18)))
    ring_font = load_font(RING_FONT_ID, ring_font_size)
    ring_rx = rx * 0.78
    ring_ry = ry * 0.78
    _draw_ring_text(img, unit_name, cx, cy, ring_rx, ring_ry, ring_font)

    # 中央五角星
    if star:
        star_outer = base_r * 0.42
        star_inner = star_outer * 0.382
        pts = _star_points(cx, cy, star_outer, star_inner)
        draw.polygon(pts, fill=SEAL_RED)

    # 五角星下方横排印章名
    if seal_name:
        seal_font_size = max(14, int(base_r * 0.26))
        seal_font = load_font(SEAL_NAME_FONT_ID, seal_font_size)
        sb = seal_font.getbbox(seal_name)
        sw = sb[2] - sb[0]
        sh = sb[3] - sb[1]
        # 放在中心偏下（五角星下方）
        tx = cx - sw // 2 - sb[0]
        ty = cy + base_r * 0.28 - sb[1]
        draw.text((tx, ty), seal_name, font=seal_font, fill=SEAL_RED)

    # 裁掉四周全透明边
    bbox = img.getbbox()
    if bbox:
        m = int(PPI * 0.05)
        img = img.crop((max(0, bbox[0] - m), max(0, bbox[1] - m),
                       min(img.width, bbox[2] + m), min(img.height, bbox[3] + m)))

    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()
