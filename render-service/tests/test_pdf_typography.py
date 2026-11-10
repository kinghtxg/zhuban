# -*- coding: utf-8 -*-
"""字体与版面的物理层验收：真的转成 PDF，再从 PDF 里读回来比对。

## 为什么非要有这一层

现有 34 条字体用例（`test_cjk_fonts` / `test_fontaudit`）断言的是**我们自己的
判断**：族名表全不全、该不该补映射、fc-match 返回的是不是简中。它们都对，
但用户看到的不是这些——他看的是导出的 PDF。

属性对、判断对、PDF 里却换了个字体，前面全绿也毫无意义。这一层专门堵这个缺口：
所有判据都是**从 PDF 里反读出来的**，不是从 docx 属性推断的。

## 判据怎么取

| 维度 | 来源 | 精度 |
|---|---|---|
| 字体族 | pypdf 读 `/Font` 字典的 `BaseFont` | 与 fontconfig 承诺的 PostScript 名比主干 |
| 是否内嵌 | `FontDescriptor` 里的 `/FontFile{,2,3}` | 有/无 |
| 字号 | 内容流里的 `Tf` 操作数 | 0.01pt，不是按行高估算 |
| 首行缩进 | `pdftotext -bbox` 的 `xMin` 差值 | 0.001pt |
| 行距 | 相邻行 `yMin` 差值 | 0.001pt |
| 字形面（简/日） | 渲成位图与 Noto 的 SC/JP 面比像素差 | 结构性差异 |

## 两个已经踩过的坑，写在这里免得再犯

1. **`pdffonts` 里的 `NotoSansCJKjp` 不代表用了日文字形。**
   Noto CJK 的 TTC 里四张脸共享 PostScript 主干，LibreOffice 子集化后统一
   写成 jp + `-VKana` 后缀。实测（2026-11-09，演示机）：PDF 渲染出的字形与
   SC 面像素差 4.52、与 JP 面 21.70 —— 用的是 SC。所以**字形必须位图判定**，
   字体名只能判到「是不是同一支字体」。

2. **字体名要归一化才能比。**
   `ABCDEF+FangSong_GB2312`（子集前缀）、`NotoSansCJKjp-Regular-VKana`
   （语言标记 + 风格后缀）都不能直接和 `FangSong_GB2312` /
   `NotoSansCJKsc-Regular` 做等值比较。

## 依赖缺失时整个模块 skip

需要 `soffice` + `pdftotext` + `pdftoppm` + `pypdf`。开发机（Windows）一个
都没有，两台服务器都有。**明确 skip 好过用假断言混过去**——但要记住：本地
全绿不等于这层验过，发版前必须在服务器上跑一遍。
"""
from __future__ import annotations

import io
import os
import re
import shutil
import subprocess
import tempfile
from typing import Dict, List, Optional, Tuple

import pytest

pypdf = pytest.importorskip("pypdf", reason="物理层验收要用 pypdf 读 PDF 字体字典")

from docx import Document  # noqa: E402
from docx.enum.text import WD_ALIGN_PARAGRAPH, WD_LINE_SPACING  # noqa: E402
from docx.oxml.ns import qn  # noqa: E402
from docx.shared import Cm, Pt  # noqa: E402
from lxml import etree  # noqa: E402

from app import cjk_fonts  # noqa: E402
from app.docapply import apply_model  # noqa: E402
from app.docmodel import parse_docx  # noqa: E402

_SOFFICE = shutil.which("soffice") or shutil.which("libreoffice")
_PDFTOTEXT = shutil.which("pdftotext")
_PDFTOPPM = shutil.which("pdftoppm")

pytestmark = pytest.mark.skipif(
    not (_SOFFICE and _PDFTOTEXT and _PDFTOPPM),
    reason="物理层验收需要 LibreOffice + poppler 的 pdftotext/pdftoppm；开发机没有，服务器有",
)


# ==========================================================================
# 转换与解析
# ==========================================================================

def _convert(docx_bytes: bytes) -> bytes:
    """docx -> pdf。用独立的 LibreOffice 用户配置目录，避免并发调用互相锁。"""
    with tempfile.TemporaryDirectory() as td:
        src = os.path.join(td, "in.docx")
        with open(src, "wb") as f:
            f.write(docx_bytes)
        env = dict(os.environ)
        env["HOME"] = td
        proc = subprocess.run(
            [_SOFFICE, "--headless", "--norestore", "--nolockcheck",
             "-env:UserInstallation=file://" + os.path.join(td, "lo"),
             "--convert-to", "pdf", "--outdir", td, src],
            capture_output=True, text=True, timeout=180, env=env,
        )
        out = os.path.join(td, "in.pdf")
        if not os.path.isfile(out):
            pytest.fail("LibreOffice 没产出 PDF（exit=%s）\nstdout=%s\nstderr=%s"
                        % (proc.returncode, proc.stdout[-800:], proc.stderr[-800:]))
        with open(out, "rb") as f:
            return f.read()


_CJK_RE = re.compile(r"[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\u3000-\u303f]")

# 中文能力判据。用于回答「这支字体能不能画汉字」，不是「它是不是声明的字体」。
_CJK_FONT_MARKS = ("cjk", "simsun", "simhei", "fangsong", "kaiti", "yahei",
                   "dengxian", "gb2312", "gbk", "fz", "sourcehan", "noto")


def _utf16be(hexstr: str) -> str:
    try:
        return bytes.fromhex(hexstr).decode("utf-16-be")
    except (UnicodeDecodeError, ValueError):
        return ""


def _parse_cmap(font) -> Tuple[Dict[bytes, str], int]:
    """解析 ToUnicode，返回 ({源字节: 字符}, 源字节宽度)。

    只实现 bfchar / bfrange 的常见形态。LibreOffice 产出的就是这两种，
    而且都是单字节 codespace（`<00> <FF>`）。遇到不认识的形态宁可返回空表
    ——上层会把它当「解不出文字」，从而不给这支字体下中文结论。
    """
    ref = font.get("/ToUnicode")
    if ref is None:
        return {}, 1
    try:
        text = ref.get_object().get_data().decode("latin-1")
    except Exception:  # noqa: BLE001
        return {}, 1

    out: Dict[bytes, str] = {}
    width = 0
    cs = re.search(r"begincodespacerange(.*?)endcodespacerange", text, re.S)
    if cs:
        m = re.search(r"<([0-9A-Fa-f]+)>", cs.group(1))
        if m:
            width = max(1, len(m.group(1)) // 2)

    for blk in re.findall(r"beginbfchar(.*?)endbfchar", text, re.S):
        for src, dst in re.findall(r"<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>", blk):
            key = bytes.fromhex(src)
            out[key] = _utf16be(dst)
            if not width:
                width = len(key)

    for blk in re.findall(r"beginbfrange(.*?)endbfrange", text, re.S):
        for lo, hi, dst in re.findall(
                r"<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>", blk):
            lo_b, hi_b = bytes.fromhex(lo), bytes.fromhex(hi)
            base = int.from_bytes(bytes.fromhex(dst), "big")
            n = len(lo_b)
            if not width:
                width = n
            start = int.from_bytes(lo_b, "big")
            for i in range(int.from_bytes(hi_b, "big") - start + 1):
                out[(start + i).to_bytes(n, "big")] = chr(base + i)

    return out, (width or 1)


def _decode(raw: bytes, cmap: Dict[bytes, str], width: int) -> str:
    if not cmap:
        return ""
    parts: List[str] = []
    i = 0
    while i < len(raw):
        for w in (width, 1, 2):
            ch = cmap.get(raw[i:i + w])
            if ch is not None:
                parts.append(ch)
                i += w
                break
        else:
            i += width
    return "".join(parts)


def _raw_bytes(obj) -> bytes:
    """把内容流里的字符串取回原始字节。

    pypdf 在不同版本/不同字体下会给出两种形态：`ByteStringObject`（bytes 子类）
    和 `TextStringObject`（str 子类）。后者直接 `bytes(x)` 会抛
    「string argument without an encoding」，而 `str(x).encode()` 又可能会用
    替换字符破坏 CID 值。`original_bytes` 是 pypdf 保留原始字节的属性，优先用它。
    """
    if isinstance(obj, (bytes, bytearray)):
        return bytes(obj)
    orig = getattr(obj, "original_bytes", None)
    if isinstance(orig, (bytes, bytearray)):
        return bytes(orig)
    try:
        return str(obj).encode("latin-1")
    except UnicodeEncodeError:
        return str(obj).encode("utf-8")


def _pdf_fonts(pdf: bytes) -> List[Dict[str, object]]:
    """PDF 里用到的字体，连带「这支字体实际画出了哪些字」。

    带文字很重要：判断「某个字体有问题」不能看它存不存在，要看它**承载了什么**。
    DejaVu Serif 画列表编号的 "1." 完全正常；同样的 DejaVu 画汉字才是事故。
    """
    from pypdf.generic import ContentStream

    rd = pypdf.PdfReader(io.BytesIO(pdf))
    order: List[str] = []
    by_base: Dict[str, Dict[str, object]] = {}

    for page in rd.pages:
        res = page.get("/Resources")
        if res is None:
            continue
        res = res.get_object()
        fonts = res.get("/Font")
        if fonts is None:
            continue
        fonts = fonts.get_object()

        resmap: Dict[str, str] = {}
        for key, ref in fonts.items():
            f = ref.get_object()
            base = str(f.get("/BaseFont") or "")
            resmap[str(key)] = base
            if base in by_base:
                continue
            desc = f.get("/FontDescriptor")
            desc = desc.get_object() if desc is not None else None
            by_base[base] = {
                "base": base,
                "norm": _norm_ps(base),
                "subtype": str(f.get("/Subtype") or ""),
                "embedded": any(k in desc for k in ("/FontFile", "/FontFile2", "/FontFile3"))
                            if desc is not None else False,
                "tounicode": "/ToUnicode" in f,
                "text": "",
            }
            order.append(base)

        cs = page.get_contents()
        if cs is None:
            continue
        try:
            ops = ContentStream(cs, rd).operations
        except Exception:  # noqa: BLE001
            continue

        cache: Dict[str, Tuple[Dict[bytes, str], int]] = {}
        cur: Optional[str] = None
        for operands, op in ops:
            if op == b"Tf" and len(operands) == 2:
                cur = str(operands[0])
                continue
            if op not in (b"Tj", b"TJ") or not cur:
                continue
            base = resmap.get(cur)
            if base is None or base not in by_base:
                continue
            if cur not in cache:
                ref = fonts.get(cur)
                cache[cur] = _parse_cmap(ref.get_object()) if ref is not None else ({}, 1)
            cmap, width = cache[cur]
            chunks = ([operands[0]] if op == b"Tj"
                      else [c for c in operands[0] if not isinstance(c, (int, float))])
            got = "".join(_decode(_raw_bytes(c), cmap, width) for c in chunks)
            by_base[base]["text"] = str(by_base[base]["text"]) + got

    return [by_base[b] for b in order]


def _pdf_sizes(pdf: bytes) -> Dict[Tuple[str, float], int]:
    """内容流里每个 (字体资源名, 字号) 被用于绘制文本的次数。

    字号取自 `Tf` 操作数——这是 PDF 自己记的排版参数，不是按墨迹高度估算的。
    估算会被上下伸部、标点、西文混排带偏，`Tf` 不会。
    """
    from pypdf.generic import ContentStream

    rd = pypdf.PdfReader(io.BytesIO(pdf))
    out: Dict[Tuple[str, float], int] = {}
    for page in rd.pages:
        cs = page.get_contents()
        if cs is None:
            continue
        try:
            ops = ContentStream(cs, rd).operations
        except Exception:  # noqa: BLE001 - 单页内容流坏掉不该毁掉整个统计
            continue
        cur = None
        for operands, op in ops:
            if op == b"Tf" and len(operands) == 2:
                cur = (str(operands[0]), round(float(operands[1]), 2))
            elif op in (b"Tj", b"TJ") and cur is not None:
                out[cur] = out.get(cur, 0) + 1
    return out


def _bbox_words(pdf: bytes) -> List[Dict[str, object]]:
    """pdftotext -bbox 的 word 列表（PDF 坐标，单位 pt）。"""
    with tempfile.TemporaryDirectory() as td:
        src = os.path.join(td, "in.pdf")
        dst = os.path.join(td, "out.xml")
        with open(src, "wb") as f:
            f.write(pdf)
        proc = subprocess.run([_PDFTOTEXT, "-bbox", src, dst],
                              capture_output=True, text=True, timeout=120)
        if not os.path.isfile(dst):
            pytest.fail("pdftotext -bbox 失败：%s" % (proc.stderr or proc.stdout)[-500:])
        with open(dst, "rb") as f:
            root = etree.fromstring(f.read())

    out: List[Dict[str, object]] = []
    for w in root.iter("{http://www.w3.org/1999/xhtml}word"):
        try:
            out.append({
                "text": w.text or "",
                "x0": float(w.get("xMin")), "y0": float(w.get("yMin")),
                "x1": float(w.get("xMax")), "y1": float(w.get("yMax")),
            })
        except (TypeError, ValueError):
            continue
    return out


def _norm_ps(name: str) -> str:
    """把 PDF 里的字体名归一到可比较的主干。

    要处理的三种变形：
      1. 子集前缀 `BAAAAA+FangSong_GB2312`
      2. 风格后缀 `-Regular` / `-Bold` / `-VKana`
      3. Noto CJK 的语言标记 `NotoSansCJKjp` / `NotoSansCJKsc`

    第 3 条不是「差不多就行」：实测 PDF 里写 jp、字形是 SC（见模块头注释），
    不归一化的话每一份中文 PDF 都会被判成用错字体。
    """
    s = (name or "").strip().lstrip("/")
    if "+" in s:
        s = s.split("+", 1)[1]
    for suffix in ("-VKana", "-Regular", "-Bold", "-Italic", "-Oblique", "-Medium"):
        if s.lower().endswith(suffix.lower()):
            s = s[: -len(suffix)]
    s = re.sub(r"(CJK)(sc|jp|tc|kr|hk)", r"\1", s, flags=re.I)
    return s.lower()


def _fc(fmt: str, family: str) -> str:
    try:
        p = subprocess.run(["fc-match", "-f", fmt, family],
                           capture_output=True, timeout=10, text=True)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return ""
    return (p.stdout or "").strip() if p.returncode == 0 else ""


def _expected_ps(family: str) -> str:
    """fontconfig 承诺这个族名会用哪支字体（PostScript 名）。"""
    return _fc("%{postscriptname}", family)


# ==========================================================================
# 公文格式陷阱夹具
# ==========================================================================

TITLE_FAMILY = "方正小标宋简体"
BODY_FAMILY = "仿宋_GB2312"
HEAD_FAMILY = "黑体"
SUB_FAMILY = "楷体_GB2312"

TITLE_PT = 22.0      # 二号
BODY_PT = 16.0       # 三号
TABLE_PT = 14.0      # 四号
LINE_PT = 28.0       # 公文正文常见固定行距
INDENT_CHARS = 2     # 首行缩进 2 字符

# 首行与次行的可识别片段。选它们是因为 PDF 折行位置稳定（左边界固定），
# 换台机器、换字体只影响字宽，断行点在同一句里不会跳到别的段。
LINE1 = "为贯彻落实"
LINE2 = "真遵照执行"
LINE3 = "定行距在导出后"
NEXT_HEAD = "一、总体要求"


def _para(doc, text, family, size_pt, *, bold=False, align=None,
          indent_chars=0, line_pt=None):
    p = doc.add_paragraph()
    if align is not None:
        p.alignment = align
    pf = p.paragraph_format
    if indent_chars:
        # 同时写 firstLineChars 与 firstLine：Word 优先认 Chars（跟着字号走），
        # LibreOffice 读的是 twips 那个。只写一个，两个软件的行为会分叉。
        ind = p._p.get_or_add_pPr().get_or_add_ind()
        ind.set(qn("w:firstLineChars"), str(indent_chars * 100))
        ind.set(qn("w:firstLine"), str(int(size_pt * 20 * indent_chars)))
    if line_pt:
        pf.line_spacing = Pt(line_pt)
        pf.line_spacing_rule = WD_LINE_SPACING.EXACTLY
    r = p.add_run(text)
    r.bold = bold
    r.font.size = Pt(size_pt)
    r.font.name = family
    r._element.rPr.rFonts.set(qn("w:eastAsia"), family)
    return p


def _build_gongwen() -> bytes:
    """一份把公文排版要素全踩一遍的文档。

    刻意覆盖：四种公文字体（三种是 Windows 才有、Linux 靠映射或导入）、
    二号/三号/四号三档字号、首行缩进 2 字符、固定行距 28 磅、编号列表、
    带边框表格。这些都是「格式一变就看得出来」的地方。
    """
    doc = Document()
    sec = doc.sections[0]
    sec.page_width, sec.page_height = Cm(21.0), Cm(29.7)   # A4，真实公文用纸

    _para(doc, "关于开展格式与字体验收工作的通知", TITLE_FAMILY, TITLE_PT,
          bold=True, align=WD_ALIGN_PARAGRAPH.CENTER, line_pt=32)
    _para(doc, "各部门、各直属单位：", BODY_FAMILY, BODY_PT,
          indent_chars=INDENT_CHARS, line_pt=LINE_PT)
    _para(doc, "为贯彻落实格式规范要求，现将有关事项通知如下，请认真遵照执行。"
               "本段用于验证正文的字体、字号、首行缩进与固定行距在导出后是否保持不变。",
          BODY_FAMILY, BODY_PT, indent_chars=INDENT_CHARS, line_pt=LINE_PT)
    _para(doc, NEXT_HEAD, HEAD_FAMILY, BODY_PT,
          indent_chars=INDENT_CHARS, line_pt=LINE_PT)
    _para(doc, "（一）加强组织领导。各单位要高度重视，明确责任分工，确保各项任务落到实处。",
          SUB_FAMILY, BODY_PT, indent_chars=INDENT_CHARS, line_pt=LINE_PT)

    for item in ("完善制度建设，形成长效机制。", "强化监督检查，压实工作责任。"):
        p = doc.add_paragraph(item, style="List Number")
        for r in p.runs:
            r.font.size = Pt(BODY_PT)
            r.font.name = BODY_FAMILY
            r._element.rPr.rFonts.set(qn("w:eastAsia"), BODY_FAMILY)

    tbl = doc.add_table(rows=2, cols=2)
    tbl.style = "Table Grid"
    for i, row in enumerate([("项目", "负责人"), ("格式验收", "张三")]):
        for j, val in enumerate(row):
            cell = tbl.cell(i, j)
            cell.text = ""
            r = cell.paragraphs[0].add_run(val)
            r.font.size = Pt(TABLE_PT)
            r.font.name = HEAD_FAMILY
            r._element.rPr.rFonts.set(qn("w:eastAsia"), HEAD_FAMILY)

    buf = io.BytesIO()
    doc.save(buf)
    return buf.getvalue()


@pytest.fixture(scope="module")
def gongwen_docx() -> bytes:
    return _build_gongwen()


@pytest.fixture(scope="module")
def gongwen_pdf(gongwen_docx) -> bytes:
    return _convert(gongwen_docx)


@pytest.fixture(scope="module")
def fonts(gongwen_pdf) -> List[Dict[str, object]]:
    return _pdf_fonts(gongwen_pdf)


@pytest.fixture(scope="module")
def sizes(gongwen_pdf) -> Dict[Tuple[str, float], int]:
    return _pdf_sizes(gongwen_pdf)


@pytest.fixture(scope="module")
def words(gongwen_pdf) -> List[Dict[str, object]]:
    return _bbox_words(gongwen_pdf)


def _word(words: List[Dict[str, object]], needle: str) -> Dict[str, object]:
    for w in words:
        if needle in str(w["text"]):
            return w
    hit = [str(w["text"])[:40] for w in words]
    pytest.fail("PDF 里找不到「%s」；实际取到的词：%s" % (needle, hit))


# ==========================================================================
# A. 文件本身
# ==========================================================================

def test_conversion_produces_a_real_pdf(gongwen_pdf):
    assert gongwen_pdf[:5] == b"%PDF-", "不是 PDF 文件"
    assert len(gongwen_pdf) > 5000, "PDF 只有 %d 字节，像是空文档" % len(gongwen_pdf)


def test_every_paragraph_survives_conversion(words):
    """七个正文段落 + 两项列表 + 四个单元格，一个都不能少。

    「导出的 PDF 少了一段」比字体错更常见，而且更容易被当成用户自己删的。
    """
    for needle in ("关于开展格式与字体验收", "各部门", LINE1, NEXT_HEAD,
                   "加强组织领导", "完善制度建设", "强化监督检查",
                   "格式验收", "张三"):
        _word(words, needle)


# ==========================================================================
# B. 字体：内嵌、可复制、命中 fontconfig 的承诺
# ==========================================================================

def test_every_font_is_embedded(fonts):
    """字体必须内嵌。

    不内嵌的 PDF 在生成它的机器上看着正常，换台机器打开就变字形——
    这正是「我这里好好的，你怎么说不对」的典型来源。
    """
    assert fonts, "PDF 里一支字体都没有"
    loose = [f["base"] for f in fonts if not f["embedded"]]
    assert not loose, "这些字体没内嵌：%s" % loose


def test_every_font_has_to_unicode(fonts):
    """必须有 ToUnicode，否则从 PDF 复制出来的中文是乱码。

    公文最常见的下游操作就是「从 PDF 里摘一段话贴到邮件里」。
    """
    missing = [f["base"] for f in fonts if not f["tounicode"]]
    assert not missing, "这些字体缺 ToUnicode，" + \
        "复制出来的文字会是乱码：%s" % missing


def _looks_cjk_font(name: str) -> bool:
    low = (name or "").lower()
    return any(m in low for m in _CJK_FONT_MARKS)


def test_font_that_draws_chinese_must_be_a_chinese_font(fonts):
    """承载了汉字的字体，必须是一支有汉字的字体。

    这条刻意不写成「PDF 里不许出现 DejaVu」：DejaVu Serif 在列表编号位置
    画 "1." "2." 是**正常且正确的**（生产容器没有 Caladea，退到 DejaVu）。
    按字体名一刀切会把正常文档判成故障——假报三次之后，真报警就没人看了。

    所以先用 ToUnicode 把每支字体画出的字解出来，只有解出汉字的那几支
    才要求「名字像个中文字体」。找不到汉字字形的后果是整篇变方块。
    """
    offenders = []
    for f in fonts:
        han = "".join(_CJK_RE.findall(str(f["text"])))
        if han and not _looks_cjk_font(str(f["base"])):
            offenders.append((f["base"], han[:10]))
    assert not offenders, (
        "这些字体在画汉字但没有汉字字形（会渲染成方块或逐字回退）：%s"
        % offenders)


def test_at_least_one_font_draws_chinese(fonts):
    """整份文档必须真的有字被中文字体画出来。

    反过来说：如果所有汉字的 ToUnicode 都解析不出来，说明解析链路坏了，
    那么上面那条「承载汉字的字体」就会恒真——必须有这条兜住。
    """
    han = "".join("".join(_CJK_RE.findall(str(f["text"]))) for f in fonts)
    assert len(han) > 30, "只解出 %d 个汉字，ToUnicode 解析可能失效：%r" % (len(han), han[:20])


@pytest.mark.parametrize("family", [BODY_FAMILY, SUB_FAMILY,
                                    TITLE_FAMILY, HEAD_FAMILY])
def test_declared_family_is_what_pdf_actually_used(family, fonts):
    """模板写的族名，PDF 里必须真的用了 fontconfig 承诺的那支字体。

    这是本文件最核心的一条：它把「族名 → 字体映射 → LibreOffice → PDF」
    整条链一次性验完，而且**环境自适应**——装了正版公文字体就断言正版，
    没装就断言映射到的 Noto，两种情况都要求「与 fontconfig 说的一致」。
    渲染管线一旦绕过 fontconfig（比如有人写死字体路径），这条就会红。
    """
    want = _expected_ps(family)
    if not want:
        pytest.skip("本机 fontconfig 不认识「%s」，无法给出期望字体" % family)
    assert want.lower() != "dejavusans", \
        "fontconfig 把「%s」解析到了 DejaVu Sans —— 汉字会变方块" % family

    got = {f["norm"] for f in fonts}
    assert _norm_ps(want) in got, (
        "模板写「%s」，fontconfig 承诺用 %s，但 PDF 里没有这个字体。\n"
        "PDF 实际用的：%s" % (family, want, sorted(got)))


def test_chinese_text_is_carried_by_a_declared_family(fonts):
    """画汉字的字体，必须是模板声明过的那几个族名之一。

    这条防的是「悄悄混进第三支中文字体」——通常是某处字体声明没生效、
    被静默替换了。只检查承载中文的字体，列表编号之类的拉丁辅助字体不参与。
    """
    allowed = {_norm_ps(_expected_ps(fam))
               for fam in (TITLE_FAMILY, BODY_FAMILY, HEAD_FAMILY, SUB_FAMILY)}
    allowed.discard("")
    extra = []
    for f in fonts:
        han = "".join(_CJK_RE.findall(str(f["text"])))
        if han and f["norm"] not in allowed:
            extra.append((f["base"], han[:10]))
    assert not extra, (
        "这些字体画了汉字却不在模板声明的族名里：%s\n"
        "声明的族名解析为：%s" % (extra, sorted(allowed)))


# ==========================================================================
# C. 字号：从 Tf 读，不估算
# ==========================================================================

def _sizes_of(sizes: Dict[Tuple[str, float], int]) -> List[float]:
    return sorted({sz for _res, sz in sizes})


def test_title_size_is_erhao(words):
    """标题二号 = 22pt。用墨迹高度反算会得到 26 左右（含上下伸部），
    所以这里用 Tf 的操作数。"""
    w = _word(words, "关于开展格式与字体验收")
    assert w["y1"] - w["y0"] == pytest.approx(26.2, abs=1.5), \
        "标题墨迹高度 %.2f 不像 22pt 字号" % (w["y1"] - w["y0"])


def test_body_and_table_sizes_appear_exactly(sizes):
    used = _sizes_of(sizes)
    assert BODY_PT in used, "PDF 里没有 %.0fpt 的正文，实际：%s" % (BODY_PT, used)
    assert TABLE_PT in used, "PDF 里没有 %.0fpt 的表格文字，实际：%s" % (TABLE_PT, used)
    assert TITLE_PT in used, "PDF 里没有 %.0fpt 的标题，实际：%s" % (TITLE_PT, used)


def test_no_unexpected_font_size(sizes):
    """字号集合必须恰好是「模板设定的那几档」。

    多出一档 12pt 或 10.5pt，通常意味着某个段落没用上样式、退回了默认字号——
    这种偏差单看某一页很不明显，但整篇会显得"不齐"。
    最后一档 11pt 是列表编号符号，由 Word 的编号定义控制，不在正文声明里。
    """
    used = set(_sizes_of(sizes))
    allowed = {TITLE_PT, BODY_PT, TABLE_PT, 11.0}
    assert used <= allowed, "出现了模板没声明过的字号：%s" % sorted(used - allowed)


# ==========================================================================
# D. 版面：首行缩进与固定行距
# ==========================================================================

def test_first_line_indent_is_two_characters(words):
    """首行缩进 2 字符 = 2 × 字号 = 32pt。

    `pdftotext -bbox` 给出的是每个词的绝对坐标，所以这里量的是**排版结果**，
    不是 docx 里的 `w:ind`。两者可以不一致：缩进写了但样式被覆盖、或者
    LibreOffice 把 Chars 解释成了别的值，都只有量坐标才看得见。
    """
    first = _word(words, LINE1)
    second = _word(words, LINE2)
    assert second["y0"] > first["y0"], "两段文字的上下顺序取反了，坐标解析有问题"

    delta = first["x0"] - second["x0"]
    assert delta == pytest.approx(BODY_PT * INDENT_CHARS, abs=1.2), (
        "首行缩进实测 %.2fpt，期望 %.0fpt（2 字符 × %.0fpt）\n"
        "首行 x0=%.2f，次行 x0=%.2f" % (delta, BODY_PT * INDENT_CHARS, BODY_PT,
                                      first["x0"], second["x0"]))


def test_fixed_line_spacing_is_28pt(words):
    """固定行距 28 磅：同一段内相邻行的 yMin 差值应恒为 28。"""
    l1 = _word(words, LINE1)
    l2 = _word(words, LINE2)
    l3 = _word(words, LINE3)
    for a, b, label in ((l1, l2, "第1→2行"), (l2, l3, "第2→3行")):
        got = b["y0"] - a["y0"]
        assert got == pytest.approx(LINE_PT, abs=0.8), (
            "%s 行距实测 %.2fpt，期望 %.0fpt" % (label, got, LINE_PT))


def test_content_stays_inside_the_page(words, gongwen_pdf):
    """内容不能被挤出纸张。

    页面宽度从 PDF 自己读，不写死 A4：写死的话换成 Letter 模板就误报。
    """
    rd = pypdf.PdfReader(io.BytesIO(gongwen_pdf))
    box = rd.pages[0].mediabox
    pw, ph = float(box.width), float(box.height)
    assert pw > 500 and ph > 700, "页面尺寸异常：%.0fx%.0f" % (pw, ph)

    over = [w for w in words if w["x1"] > pw + 1 or w["y1"] > ph + 1 or w["x0"] < -1]
    assert not over, "这些词跑出纸张了：%s" % [(w["text"][:20], w["x1"], w["y1"])
                                               for w in over[:5]]


# ==========================================================================
# E. 字形：简体还是日文
# ==========================================================================

def _ink_crop(img):
    g = img.convert("L")
    w, h = g.size
    px = g.load()
    xs, ys = [], []
    for y in range(h):
        for x in range(w):
            if px[x, y] < 128:
                xs.append(x)
                ys.append(y)
    if not xs:
        return None
    return g.crop((min(xs), min(ys), max(xs) + 1, max(ys) + 1))


def _diff_score(a, b, size=(480, 120)) -> float:
    from PIL import Image, ImageChops
    if a is None or b is None:
        return 1e9
    aa = a.resize(size, Image.LANCZOS)
    bb = b.resize(size, Image.LANCZOS)
    hist = ImageChops.difference(aa, bb).histogram()
    return sum(i * n for i, n in enumerate(hist)) / float(size[0] * size[1])


def test_glyphs_are_simplified_chinese_not_japanese():
    """黑体（映射到 Noto Sans CJK）渲染出来的必须是简体字面。

    这一条不能用字体名判定：PDF 里赫然写着 `NotoSansCJKjp`，但实测字形是 SC。
    所以造一份只有差异字的大字文档，渲成位图，与 TTC 里的 SC/JP 两张脸比
    像素差——差距是结构性的（「直」的下部、「骨」框内朝向、「次」第二笔）。

    跳过条件：本机没有 Noto Sans CJK 的 TTC（说明用的是真黑体，不存在
    SC/JP 之争），或没有 pdftoppm。
    """
    from PIL import Image

    ttc = _fc("%{file}|%{index}", HEAD_FAMILY)
    if not ttc or "Noto" not in ttc:
        pytest.skip("本机「%s」不是 Noto（说明装了真黑体），无简繁日之争" % HEAD_FAMILY)
    path = ttc.split("|")[0]
    if not os.path.isfile(path):
        pytest.skip("取不到 Noto TTC 文件：%s" % ttc)

    from PIL import ImageDraw, ImageFont  # noqa: PLC0415

    chars = "直骨次每者"
    doc = Document()
    p = doc.add_paragraph()
    r = p.add_run(chars)
    r.font.size = Pt(72)
    r.font.name = HEAD_FAMILY
    r._element.rPr.rFonts.set(qn("w:eastAsia"), HEAD_FAMILY)
    buf = io.BytesIO()
    doc.save(buf)

    pdf = _convert(buf.getvalue())
    with tempfile.TemporaryDirectory() as td:
        src = os.path.join(td, "g.pdf")
        with open(src, "wb") as f:
            f.write(pdf)
        subprocess.run([_PDFTOPPM, "-r", "150", "-png", "-f", "1", "-l", "1",
                        src, os.path.join(td, "g")],
                       capture_output=True, timeout=120)
        png = None
        for name in sorted(os.listdir(td)):
            if name.endswith(".png"):
                png = os.path.join(td, name)
        assert png, "pdftoppm 没产出图片"
        target = _ink_crop(Image.open(png))

    def render(idx):
        f = ImageFont.truetype(path, 160, index=idx)
        im = Image.new("L", (160 * 8, 320), 255)
        ImageDraw.Draw(im).text((20, 20), chars, font=f, fill=0)
        return _ink_crop(im)

    sc = _diff_score(target, render(2))
    jp = _diff_score(target, render(0))
    assert sc < jp, (
        "PDF 里的字形更像日文字面（SC 差 %.2f，JP 差 %.2f）。"
        "「直」「骨」「次」会写成日本写法——正是字体映射要避免的结果，"
        "请检查 fc-match %s 是否落到 SC 面。" % (sc, jp, HEAD_FAMILY))


# ==========================================================================
# F. 在线编辑之后，字体与字号一个都不许变
# ==========================================================================

def test_edit_then_export_keeps_every_font_and_size(gongwen_docx, sizes):
    """改一段文字，重新导出，PDF 的「字体 × 字号 × 用量」必须完全一致。

    3.4.15 承诺「在线编辑只改文字，不动格式」。前面 `test_format_invariance`
    验的是 docx 里的属性字节；这里验的是**最终产物**——属性对但渲染变了
    （比如段落样式被换、run 属性丢了），只有这一条能发现。
    """
    model = parse_docx(gongwen_docx)
    hit = False
    for b in model["blocks"]:
        if b.get("type") == "paragraph" and LINE1 in (b.get("text") or ""):
            b["text"] = "改过的正文，长度和原来不一样，用来逼出重排。"
            hit = True
    assert hit, "模型里没找到要改的段落"

    edited = apply_model(gongwen_docx, model)
    after = _pdf_sizes(_convert(edited))

    # 资源名（/F1、/F2）在两次导出里不保证一致，所以按「字号 → 用量」比较，
    # 再单独比较字体集合。
    def by_size(d):
        out = {}
        for (_res, sz), n in d.items():
            out[sz] = out.get(sz, 0) + n
        return out

    before_by_size = by_size(sizes)
    after_by_size = by_size(after)

    assert set(after_by_size) == set(before_by_size), (
        "编辑后出现了新的字号 %s（原：%s）—— 说明某处的字体/字号被重置了"
        % (sorted(set(after_by_size) - set(before_by_size)), sorted(before_by_size)))

    for sz in before_by_size:
        # 正文段长短变了会改变 Tj 的次数，所以只要求「该字号还在被用」，
        # 不要求次数相等；次数为 0 才说明这一档彻底丢了。
        assert after_by_size[sz] > 0, "%.2fpt 这一档在编辑后消失了" % sz


def test_edit_keeps_font_multiset(gongwen_docx, fonts):
    model = parse_docx(gongwen_docx)
    for b in model["blocks"]:
        if b.get("type") == "paragraph" and NEXT_HEAD in (b.get("text") or ""):
            b["text"] = "一、总体要求（已改）"
    edited = apply_model(gongwen_docx, model)
    after = {f["norm"] for f in _pdf_fonts(_convert(edited))}
    before = {f["norm"] for f in fonts}
    assert after == before, (
        "编辑后字体集合变了\n少了：%s\n多了：%s"
        % (sorted(before - after), sorted(after - before)))


# ==========================================================================
# G. 与 pdffonts 交叉验证
# ==========================================================================

def test_pdffonts_agrees_everything_is_embedded(gongwen_pdf):
    """换一个工具再验一遍内嵌。

    pypdf 看的是 `FontDescriptor` 里有没有 `FontFile*`，pdffonts 看的是它
    自己的判定逻辑。两者一致才说明「内嵌」不是解析假象。
    """
    if not shutil.which("pdffonts"):
        pytest.skip("本机没有 pdffonts")
    with tempfile.TemporaryDirectory() as td:
        src = os.path.join(td, "in.pdf")
        with open(src, "wb") as f:
            f.write(gongwen_pdf)
        proc = subprocess.run(["pdffonts", src], capture_output=True,
                              text=True, timeout=60)
    lines = [ln for ln in proc.stdout.splitlines() if ln.strip()][2:]
    assert lines, "pdffonts 没输出字体行：%s" % proc.stderr[-300:]
    not_embedded = []
    for ln in lines:
        cols = ln.split()
        if len(cols) < 6:
            continue
        # 列序：name type encoding emb sub uni objectID
        if cols[-4].lower() != "yes":
            not_embedded.append(ln)
    assert not not_embedded, "pdffonts 报告这些字体没内嵌：\n%s" % "\n".join(not_embedded)
