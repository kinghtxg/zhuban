"""特殊数据类型转换（CONTRACT §3.6）：图片 / 二维码 / 富文本 -> docxtpl 对象。"""
from __future__ import annotations

import io
from typing import Any

import markdown as md_lib
from bs4 import BeautifulSoup, NavigableString, Tag
from docx.shared import Mm
from docxtpl import InlineImage, RichText

from . import config, security

_ALLOWED_INLINE = {"b", "strong", "i", "em", "u", "s", "strike", "br", "p", "span", "font", "li"}


def _hex_color(val: str | None) -> str | None:
    if not val:
        return None
    val = val.strip().lower()
    named = {
        "red": "FF0000", "blue": "0000FF", "green": "008000", "black": "000000",
        "white": "FFFFFF", "gray": "808080", "grey": "808080",
    }
    if val.startswith("#"):
        val = val[1:]
    if len(val) == 6 and all(c in "0123456789abcdef" for c in val):
        return val
    return named.get(val)


def _extract_color(tag: Tag) -> str | None:
    color = tag.get("color")
    style = tag.get("style", "")
    m = None
    import re
    mm = re.search(r"color\s*:\s*([^;]+)", style)
    if mm:
        m = mm.group(1)
    return _hex_color(color or m)


def _html_to_segments(node, fmt: dict, out: list, list_ctx: dict):
    for child in getattr(node, "children", []):
        if isinstance(child, NavigableString):
            txt = str(child)
            if txt:
                out.append((txt, dict(fmt)))
            continue
        if not isinstance(child, Tag):
            continue
        name = child.name.lower()
        new_fmt = dict(fmt)
        if name in ("b", "strong"):
            new_fmt["bold"] = True
        elif name in ("i", "em"):
            new_fmt["italic"] = True
        elif name == "u":
            new_fmt["underline"] = True
        elif name in ("s", "strike"):
            new_fmt["strike"] = True
        elif name == "br":
            out.append(("\n", dict(fmt)))
            continue
        elif name in ("span", "font"):
            c = _extract_color(child)
            if c:
                new_fmt["color"] = c
        elif name == "p":
            out.append(("\n", dict(fmt)))
        elif name in ("ul", "ol"):
            list_ctx = dict(list_ctx)
            list_ctx["type"] = "ol" if name == "ol" else "ul"
            list_ctx["counter"] = 0
        elif name == "li":
            if list_ctx.get("type") == "ol":
                list_ctx["counter"] += 1
                out.append((f"{list_ctx['counter']}. ", dict(fmt)))
            else:
                out.append(("• ", dict(fmt)))
        else:
            # 未白名单标签：剥离标签但保留其子内容
            pass
        _html_to_segments(child, new_fmt, out, list_ctx)
        if name == "li":
            out.append(("\n", dict(fmt)))


def html_to_richtext(content: str, fmt_type: str) -> RichText:
    """markdown/html 内容 -> docxtpl RichText（白名单，去脚本/事件）。"""
    if fmt_type == "markdown":
        html = md_lib.markdown(content, extensions=["extra"])
    else:
        html = content
    soup = BeautifulSoup(html, "html.parser")
    # 剥离危险标签
    for bad in soup(["script", "style", "object", "iframe", "link", "meta"]):
        bad.decompose()
    # 清除事件属性与 javascript:
    for tag in soup.find_all(True):
        for attr in list(tag.attrs):
            av = tag.attrs[attr]
            if isinstance(av, str) and ("javascript:" in av.lower() or attr.startswith("on")):
                del tag.attrs[attr]
            elif attr not in ("color", "style"):
                # 仅保留 color 相关，其它属性丢弃
                pass
    segments: list = []
    _html_to_segments(soup, {}, segments, {"type": "ul", "counter": 0})
    rt = RichText()
    for text, fmt in segments:
        rt.add(
            text,
            bold=fmt.get("bold", False),
            italic=fmt.get("italic", False),
            underline=fmt.get("underline", False),
            color=fmt.get("color"),
        )
    return rt


def make_qrcode(text: str, size_mm: float) -> InlineImage:
    import qrcode
    qr = qrcode.QRCode(box_size=10, border=2)
    qr.add_data(text)
    qr.make(fit=True)
    img = qr.make_image(fill_color="black", back_color="white")
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    buf.seek(0)
    return buf  # type: ignore


def _image_pixels(raw: bytes) -> tuple[int, int, float | None]:
    """读原图的像素尺寸与 DPI。读不到就返回 (0,0,None)，调用方按无比例处理。"""
    try:
        from PIL import Image
        with Image.open(io.BytesIO(raw)) as im:
            dpi = None
            info_dpi = im.info.get("dpi")
            if isinstance(info_dpi, tuple) and info_dpi[0]:
                dpi = float(info_dpi[0])
            return int(im.width), int(im.height), dpi
    except Exception:  # noqa: BLE001 - 读不到尺寸不该让整次渲染失败
        return 0, 0, None


def fit_image_size(raw: bytes, want_w_mm: float | None,
                   want_h_mm: float | None) -> tuple[float | None, float | None]:
    """算出一张内联图片该摆多大，**保持原图宽高比并夹在版心内**。

    为什么必须有这一步（这是「插签名/印章把模板格式搞乱」的直接原因）：
      - 老实现把记录里的 width_mm 和 height_mm 同时写进 wp:extent。两者比例
        一旦和原图不一致，图片就被压扁/拉长，看起来"变形"；
      - 记录的尺寸是用户建资源时填的，跟这张模板的版面毫无关系。60mm 宽的
        签名塞进 13mm 宽的表格格里，Word 只能把行撑开 —— 版面直接崩。
    这里的做法是：有比例信息就以宽度为准反算高度（或反过来），最后等比
    缩放到 MAX_INLINE_IMAGE_* 之内。宁可图小一点，也不许撑坏版面。
    """
    px_w, px_h, dpi = _image_pixels(raw)
    ratio = (px_w / px_h) if px_w and px_h else None

    w = float(want_w_mm) if want_w_mm else None
    h = float(want_h_mm) if want_h_mm else None

    if w and h and ratio:
        # 记录给的框和原图比例不一致：以宽度为准，高度按原图反算。
        # 反过来（以高度为准）也行，但签名/印章都是"横向占地"更常见，
        # 宽度更能反映使用者的意图。
        if abs((w / h) - ratio) > 0.03 * ratio:
            h = round(w / ratio, 2)
    elif w and ratio:
        h = round(w / ratio, 2)
    elif h and ratio:
        w = round(h * ratio, 2)
    elif not w and not h and px_w and px_h:
        # 一个尺寸都没给：按图片自身 DPI 折算物理尺寸，再交给下面的钳制
        use_dpi = dpi or config.DEFAULT_IMAGE_DPI
        w = round(px_w / use_dpi * 25.4, 2)
        h = round(px_h / use_dpi * 25.4, 2)

    if w and h:
        k = min(1.0, config.MAX_INLINE_IMAGE_WIDTH_MM / w,
                config.MAX_INLINE_IMAGE_HEIGHT_MM / h)
        if k < 1.0:
            w, h = round(w * k, 2), round(h * k, 2)
    elif w:
        w = min(w, float(config.MAX_INLINE_IMAGE_WIDTH_MM))
    elif h:
        h = min(h, float(config.MAX_INLINE_IMAGE_HEIGHT_MM))
    return w, h


def resolve_context(data: Any, doc) -> Any:
    """递归遍历数据，把 {type: image|qrcode|html|markdown} dict 换成 docxtpl 对象。"""
    if isinstance(data, dict):
        t = data.get("type")
        if t == "image":
            raw = security.download_image(data["src"])
            w, h = fit_image_size(raw, data.get("width"), data.get("height"))
            kw = {}
            if w:
                kw["width"] = Mm(float(w))
            if h:
                kw["height"] = Mm(float(h))
            return InlineImage(doc, io.BytesIO(raw), **kw)
        if t == "qrcode":
            buf = make_qrcode(str(data.get("text", "")), float(data.get("size", 20)))
            size = float(data.get("size", 20))
            return InlineImage(doc, buf, width=Mm(size))
        if t in ("html", "markdown"):
            return html_to_richtext(str(data.get("content", "")), t)
        return {k: resolve_context(v, doc) for k, v in data.items()}
    if isinstance(data, list):
        return [resolve_context(v, doc) for v in data]
    return data
