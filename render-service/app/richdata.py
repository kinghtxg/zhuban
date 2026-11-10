"""特殊数据类型转换（CONTRACT §3.6）：图片 / 二维码 / 富文本 -> docxtpl 对象。"""
from __future__ import annotations

import io
import re
from typing import Any

import markdown as md_lib
from bs4 import BeautifulSoup, NavigableString, Tag
from docx.oxml.ns import nsdecls, qn
from docx.shared import Mm
from docxtpl import InlineImage, RichText
from lxml import etree

from . import config, security

_ALLOWED_INLINE = {"b", "strong", "i", "em", "u", "s", "strike", "br", "p", "span", "font", "li"}

# 注入 run 的识别标记（字符样式 id）。渲染完成后由 layout.inherit_inline_format
# 读走并删除，成品里不会留下它。
MARK_STYLE = "ZBInlineRich"


def _mark_runs(runs_xml: str) -> str:
    """给每个注入的 run 打上识别标记。

    为什么必须打标记：docxtpl 的 ``RichText`` 只产出 ``<w:r>``，它不知道自己
    落在哪个段落里，也就拿不到该段落的 ``rFonts``/``sz``。缺这两项时 Word 会
    退回**文档默认值或主题字体**——模板里明明写着「仿宋_GB2312 三号」，生成
    出来却是「宋体 小四」。

    这个失败极难发现：接口 200、文字一字不差、`document.xml` 里也找得到那句
    话，只有肉眼比对 PDF 才看得出字形变了；而混排的正文里"哪一段字号不对"
    更是没人会逐段核。

    标记用的是字符样式引用，而不是自定义属性：``w:rStyle`` 是合法 OOXML，
    Word/LibreOffice 遇到不存在的样式 id 只是当作没有样式；即便后处理环节
    意外没跑，也不会让文件打不开。
    """
    if not runs_xml:
        return runs_xml
    try:
        root = etree.fromstring(
            ("<w:rroot %s>%s</w:rroot>" % (nsdecls("w"), runs_xml)).encode("utf-8")
        )
    except etree.XMLSyntaxError:  # 生成物不合法时保持原样，交给 render 的 try 兜住
        return runs_xml
    for r in root.findall(qn("w:r")):
        rpr = r.find(qn("w:rPr"))
        if rpr is None:
            rpr = etree.SubElement(r, qn("w:rPr"))
            r.remove(rpr)
            r.insert(0, rpr)
        style = etree.Element(qn("w:rStyle"))
        style.set(qn("w:val"), MARK_STYLE)
        rpr.insert(0, style)  # CT_RPr 的序列里 rStyle 必须排第一
    return "".join(
        etree.tostring(c, encoding="unicode", with_tail=False) for c in root
    )


def _hex_color(val: str | None) -> str | None:
    """把常见写法归一成 OOXML 要的 ``RRGGBB``（不带 `#`）。

    支持三种：``#c00`` / ``#cc0000`` / ``rgb(204,0,0)``，外加十来个小写色名。
    必须支持 3 位短写：HTML 里 `#c00` 极常见，只认 6 位会让颜色**静默消失**
    ——文字照旧生成，只是黑的，而且没有任何提示。
    """
    if not val:
        return None
    val = val.strip().lower()
    named = {
        "red": "FF0000", "blue": "0000FF", "green": "008000", "black": "000000",
        "white": "FFFFFF", "gray": "808080", "grey": "808080", "orange": "FFA500",
        "purple": "800080", "brown": "A52A2A", "navy": "000080",
    }
    # rgb(204, 0, 0) / rgba(204,0,0,.8) —— 透明度 Word 的 w:color 表达不了，忽略
    m = re.match(r"rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})", val)
    if m:
        try:
            r, g, b = (int(x) for x in m.groups())
        except ValueError:
            return None
        if all(0 <= x <= 255 for x in (r, g, b)):
            return "%02X%02X%02X" % (r, g, b)
        return None
    if val.startswith("#"):
        val = val[1:]
    if len(val) == 3 and all(c in "0123456789abcdef" for c in val):
        val = "".join(c * 2 for c in val)
    if len(val) == 6 and all(c in "0123456789abcdef" for c in val):
        return val.upper()
    return named.get(val)


def _extract_color(tag: Tag) -> str | None:
    color = tag.get("color")
    style = tag.get("style", "")
    m = re.search(r"color\s*:\s*([^;]+)", style)
    return _hex_color(color or (m.group(1) if m else None))


def _px_to_mm(val: str | None) -> float | None:
    """把 HTML 里的图片尺寸写法换成毫米。认 px / pt / cm / mm，其余（%、auto、em）不认。

    为什么必须换算而不是原样用：HTML 的 ``width="600"`` 是**像素**，直接当毫米
    塞进 ``wp:extent`` 会让图片放大 3.78 倍（96dpi→25.4mm/inch），一页 A4 版心
    也就 159mm 宽，600 的图直接撑出版面。宁可当"没给尺寸"。
    """
    if not val:
        return None
    s = str(val).strip().lower()
    m = re.match(r"^([0-9]+(?:\.[0-9]+)?)\s*(px|pt|cm|mm)?$", s)
    if not m:
        return None
    try:
        n = float(m.group(1))
    except ValueError:
        return None
    unit = m.group(2) or "px"
    if n <= 0:
        return None
    if unit == "px":
        return round(n * 25.4 / 96.0, 2)
    if unit == "pt":
        return round(n * 25.4 / 72.0, 2)
    if unit == "cm":
        return round(n * 10.0, 2)
    return round(n, 2)


def _img_size_attrs(tag: Tag) -> tuple[float | None, float | None]:
    """取 ``<img>`` 声明的宽高（属性优先，其次 style）。"""
    w = _px_to_mm(tag.get("width"))
    h = _px_to_mm(tag.get("height"))
    style = tag.get("style") or ""
    m = re.search(r"(?:^|;)\s*width\s*:\s*([0-9.]+)\s*(px|pt|cm|mm)", style)
    if w is None and m:
        w = _px_to_mm(m.group(1) + m.group(2))
    m = re.search(r"(?:^|;)\s*height\s*:\s*([0-9.]+)\s*(px|pt|cm|mm)", style)
    if h is None and m:
        h = _px_to_mm(m.group(1) + m.group(2))
    return w, h


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
        if name == "img":
            # 图片在富文本里必须能落地：安全报告的主体就是截图，只能打字
            # 不能贴图的编辑器等于逼用户"先导出再手工插图"，那格式一定乱。
            # 这里只把"哪张图、多大"记下来，真正的 run 要等到渲染时才生成
            # ——docxtpl 的 InlineImage 依赖 current_rendering_part，此刻还是
            # None（见 _image_run 的注释）。
            src = (child.get("src") or "").strip()
            if src:
                w_mm, h_mm = _img_size_attrs(child)
                out.append(("", {"image": {"src": src, "w": w_mm, "h": h_mm}}))
            continue
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


def _break_lines(runs_xml: str) -> str:
    """把 ``<w:t>`` 里的裸换行变成真正的 ``<w:br/>``。

    为什么必须换：docxtpl 的 RichText.add 只是把 ``\\n`` 原样写进 ``<w:t>``，
    而 ``<w:t>`` 里的换行**不表示换行**——Word 会把它当空白吃掉，
    LibreOffice 也一样。于是用户敲的每一处回车，在成稿里全变成了空格，
    多段内容挤成一坨。接口 200、文字一个不少，所以只断言"文本包含 XXX"
    的测试一条都不会红（和 3.4.9 那次空白段是同一类毛病）。

    做法是在同一个 ``<w:r>`` 里把 ``<w:t>`` 拆开、中间插 ``<w:br/>``：
    run 的 rPr 在外面，字体字号因此一点不受影响。
    """
    def repl(m: "re.Match") -> str:
        inner = m.group(2)
        if "\n" not in inner:
            return m.group(0)
        pieces = inner.split("\n")
        buf = []
        for i, piece in enumerate(pieces):
            if i:
                buf.append("<w:br/>")
            if piece:
                buf.append('<w:t xml:space="preserve">%s</w:t>' % piece)
        return "".join(buf)

    return re.sub(r"(<w:t[^>]*>)(.*?)(</w:t>)", repl, runs_xml or "", flags=re.S)


def _image_run(img: dict, doc) -> str:
    """把一张 ``<img>`` 变成 ``<w:r><w:drawing>…</w:drawing></w:r>``。

    两个必须讲清楚的点：

    **① 为什么不能在这里立刻生成，而要拖到 ``ParagraphLevelRichText.__str__``**：
    docxtpl 的 ``InlineImage`` 是把图片塞进*当前正在渲染的那个 part*
    （``current_rendering_part.new_pic_inline``）。而 ``resolve_context`` 跑在
    ``doc.render()`` **之前**，那时 ``current_rendering_part`` 还是 None，直接
    ``str(InlineImage(...))`` 会 AttributeError。调用栈里唯一"part 已就绪"的时刻
    就是 jinja 求值 ``{{ 变量 }}`` 的那一刻，也就是 ``__str__``。所以这里被设计成
    由 ``__str__`` 回调。

    **② 为什么要从 InlineImage 的字符串里抠出 ``<w:drawing>``**：
    ``str(InlineImage)`` 返回的是

        </w:t></w:r><w:r><w:drawing>…</w:drawing></w:r><w:r><w:t …>

    ——它假定调用方此刻正躺在 ``<w:t>`` 里面（那是 ``{{r 图}}`` 写法的场景）。
    而我们是在自己拼 run 序列，前面没有任何未闭合的 ``<w:t>``，原样拼进去就是
    一对孤立的闭合标签，Word 直接判文件损坏。所以只取中间的 drawing 元素、自己
    补一个 ``<w:r>`` 外壳 —— 这样图片 run 和文字 run 是同一个层级，段落属性
    （``w:jc`` 居中、``w:ind`` 缩进）一丝不动地罩在它身上。

    实测依据：``InlineImage`` 产出的 ``wp:inline`` 自带 ``xmlns:wp``/``xmlns:a``
    声明，抠出来单独放进文档也合法。
    """
    if doc is None:  # 单测里没有 docxtpl 实例时静默跳过，不影响文字
        return ""
    raw = security.download_image(img["src"])
    w_mm, h_mm = fit_image_size(raw, img.get("w"), img.get("h"))
    kw = {}
    if w_mm:
        kw["width"] = Mm(float(w_mm))
    if h_mm:
        kw["height"] = Mm(float(h_mm))
    xml = str(InlineImage(doc, io.BytesIO(raw), **kw))
    m = re.search(r"<w:drawing>.*</w:drawing>", xml, re.S)
    if not m:  # 版式异常时不吞掉整段文字，退化成"图没插上"
        return ""
    return "<w:r>%s</w:r>" % m.group(0)


def _runs_from_segments(segments: list, doc) -> str:
    """段落片段 -> 连续的 ``<w:r>`` 序列（文字与图片混排）。"""
    parts: list[str] = []
    buf: list[tuple[str, dict]] = []

    def flush() -> None:
        if not buf:
            return
        rt = RichText()
        for text, fmt in buf:
            rt.add(
                text,
                bold=fmt.get("bold", False),
                italic=fmt.get("italic", False),
                underline=fmt.get("underline", False),
                strike=fmt.get("strike", False),
                color=fmt.get("color"),
            )
        parts.append(rt.xml)
        buf.clear()

    for text, fmt in segments:
        img = fmt.get("image")
        if img:
            flush()
            run = _image_run(img, doc)
            if run:
                parts.append(run)
            continue
        if text:
            buf.append((text, fmt))
    flush()
    return "".join(parts)


def _escape_bare_ampersand(html: str) -> str:
    """把「不属于合法实体的裸 ``&``」补成 ``&amp;``。

    BeautifulSoup 的 html.parser 会把 ``&D`` 这种畸形实体**整个吞掉**：
    实测 ``<b>x</b> 与 A&D`` 解析出来是 ``x 与 AD``，``&`` 没了，也不报错。
    用户写「研发&测试部门」「P&G」就静默少一个字符——接口 200、字数差不出来、
    只有逐字比对原文才会发现。

    只补裸的：``&amp;`` ``&lt;`` ``&#39;`` ``&#x27;`` 这些合法实体原样保留，
    否则会把用户本来想要的 ``&amp;`` 变成 ``&amp;amp;``（多印两个字 ``amp``）。
    """
    return re.sub(r"&(?!(?:[a-zA-Z][a-zA-Z0-9]{1,31}|#\d{1,7}|#[xX][0-9a-fA-F]{1,6});)", "&amp;", html)


def html_to_richtext(content: str, fmt_type: str, doc=None) -> ParagraphLevelRichText:
    """markdown/html 内容 -> 段落级富文本片段（白名单，去脚本/事件）。

    ``doc`` 是 docxtpl 的 ``DocxTemplate`` 实例，插图时必需；不给就只保留文字。
    """
    if fmt_type == "markdown":
        html = md_lib.markdown(content, extensions=["extra"])
    else:
        html = content
    soup = BeautifulSoup(_escape_bare_ampersand(html), "html.parser")
    # 剥离危险标签
    for bad in soup(["script", "style", "object", "iframe", "link", "meta"]):
        bad.decompose()
    # 清除事件属性与 javascript:（src 指到 javascript: 的图也要摘掉）
    for tag in soup.find_all(True):
        for attr in list(tag.attrs):
            av = tag.attrs[attr]
            if isinstance(av, str) and ("javascript:" in av.lower() or attr.startswith("on")):
                del tag.attrs[attr]
    segments: list = []
    _html_to_segments(soup, {}, segments, {"type": "ul", "counter": 0})
    return ParagraphLevelRichText(builder=lambda: _runs_from_segments(segments, doc))


class ParagraphLevelRichText:
    """把 docxtpl 的 RichText 包成"先闭合外层 run、末尾再开回一对"的片段。

    docxtpl 的 ``RichText.__str__`` 只产出 ``<w:r>…</w:r>``，它假定调用方写的是
    ``{{r 变量 }}``——那种写法会先被 patch_xml 换成
    ``</w:t></w:r><w:r><w:t>{{r …}}</w:t></w:r><w:r><w:t>``，前后各垫一段。

    可本产品手册教的是普通 ``{{ 变量 }}``。于是这些 ``<w:r>`` 被原样塞进了
    ``<w:t>`` 里，而 ``<w:t>`` 只允许纯文本：

        <w:r><w:t>本月营业收入 <w:r><w:t>…</w:t></w:r>…</w:t></w:r>
                                 ↑ 非法的嵌套

    Word 打开这种文件会报"内容有问题"，LibreOffice 更干脆：整段当空，转出来的
    PDF 里就是一片空白。**接口 200、`document.xml` 里文字原封不动还在**，所以
    只断言"文本包含 XXX"的测试一条都不会红——这份 docx 是 3.4.9 演示站缩略图
    里先看见空白、再倒查 XML 才发现的。

    修法就是自己补上那对垫片，让注入的 run 落在段落层而不是 ``<w:t>`` 里面。
    末尾那个空 ``<w:r><w:t>`` 会被模板原有的 ``</w:t></w:r>`` 收掉，与 docxtpl
    自己的 ``{{r }}`` 写法产出完全一致。

    **第二个问题（3.4.14 修）**：那对垫片让注入 run 和"重开的尾 run"都脱离了
    原 run 的 ``rPr``，字体字号一起丢。两类 run 都打上 ``MARK_STYLE`` 标记，
    渲染后由 ``layout.inherit_inline_format`` 从同一段落补回来。

    **第三个问题（3.4.17 修）**：富文本里要能插图。插图必须发生在"当前有 part
    在渲染"的时刻，而本对象构造时还没有（见 ``_image_run``）。所以整段 XML
    改成**惰性拼装**：构造只存 builder，``__str__``（jinja 求值那一刻）才真正
    生成。副作用是同一个对象被 jinja 取两次也只会插一次图（有缓存）。
    """

    __slots__ = ("_builder", "_runs", "_xml")

    def __init__(self, runs_xml: str = "", builder=None) -> None:
        self._builder = builder
        self._runs = runs_xml or ""
        self._xml: str | None = None

    @property
    def xml(self) -> str:
        if self._xml is None:
            runs = self._builder() if self._builder is not None else self._runs
            self._xml = (
                "</w:t></w:r>"
                + _mark_runs(_break_lines(runs))
                + '<w:r><w:rPr><w:rStyle w:val="%s"/></w:rPr>'
                '<w:t xml:space="preserve">' % MARK_STYLE
            )
        return self._xml

    def __str__(self) -> str:  # jinja 里 {{ x }} 取的就是 str()
        return self.xml

    def __html__(self) -> str:
        return self.xml


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
            return html_to_richtext(str(data.get("content", "")), t, doc)
        return {k: resolve_context(v, doc) for k, v in data.items()}
    if isinstance(data, list):
        return [resolve_context(v, doc) for v in data]
    return data
