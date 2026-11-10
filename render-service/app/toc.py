"""目录（TOC）重建：让目录跟着正文变，格式逐字节沿用模板自己那一条。

## 为什么非做不可

TOC 是 Word 域，它的「结果」是**静态缓存**——标题文字 + 页码的快照。
docxtpl 渲染时 `{%p for %}` 会展开出 N 个标题，但缓存一条都不会变：

- 「漏洞数量是循环内容，可能填写一个或多个」的模板，生成 5 条漏洞，
  目录里永远还是模板作者当时那 4 条，页码也停在旧版；
- 用户把标题改了（比如把「未授权登录管理员」改成别的），目录还是旧名。

实测（生产 docforge-render 容器，LibreOffice 7.x）：

    soffice --headless --convert-to pdf
      · 目录文字照抄缓存（把缓存改成「目的MUT」，PDF 里就印「目的MUT」）
      · PAGEREF 的缓存页码也照抄（改成 99，PDF 里就印 99）

也就是说 **LibreOffice 既不重建目录、也不重算页码**。所以只能自己重写缓存。

## 怎么做到「格式一点都不变」

不重新"画"目录，而是**克隆**：模板里每个层级（toc 1 / toc 2 / toc 3）各取
第一条现存条目当原型，深拷贝它的整段 XML——包括

    <w:pPr><w:pStyle w:val="7"/><w:tabs><w:tab w:val="right"
            w:leader="dot" w:pos="8312"/></w:tabs></w:pPr>
    <w:r><w:rPr><w:rFonts w:eastAsia="方正楷体简体" .../><w:szCs .../></w:rPr>

然后**只改三样东西**：条目的标题文字、PAGEREF 指向的书签名、缓存的页码数字。
字体、字号、缩进、右制表位位置、点线前导符、段前段后，全部来自原型那一份，
不是我们另造一套。所以「字体格式 / 目录格式 / 页码格式」三样都不会变。

## 页码怎么定

页码只有在排版之后才知道，而排版要读页码、页码又写在文档里，是循环依赖。
解法：先按占位页码排一次 → 从 PDF 里量出每个标题实际落在第几页 →
改写缓存 → 再排一次。条目数固定后版面高度不变（右对齐制表位吃掉位数差异），
一次回填即收敛；万一分页数变了，最多再来一轮（`MAX_PASSES`）。
"""

from __future__ import annotations

import copy
import io
import re
import unicodedata
import zipfile
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Sequence, Tuple

from lxml import etree

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
NS = {"w": W}


def _w(tag: str) -> str:
    return "{%s}%s" % (W, tag)


# ---------------------------------------------------------------- 小工具


def _attr(el, tag: str, default: Optional[str] = None) -> Optional[str]:
    if el is None:
        return default
    v = el.get(_w(tag))
    return default if v is None else v


def _child(el, path: str):
    if el is None:
        return None
    return el.find("/".join(_w(p) for p in path.split("/")))


def _ptext(p) -> str:
    """段落可见文字（不含 instrText）。"""
    out = []
    for t in p.iter(_w("t")):
        out.append(t.text or "")
    return "".join(out)


def _instr(p) -> str:
    return "".join((it.text or "") for it in p.iter(_w("instrText")))


def _norm(s: str) -> str:
    """比较用的归一化：去空白、全角转半角、去掉零宽字符。

    PDF 抽出来的行里汉字之间常被插入空格（两端对齐 + 中西文混排），
    目录条目里却没有。不去掉就对不上。
    """
    s = unicodedata.normalize("NFKC", s or "")
    s = s.replace("\u200b", "").replace("\ufeff", "")
    return re.sub(r"\s+", "", s)


# ---------------------------------------------------------------- 编号


_CN_DIGITS = "零一二三四五六七八九"


def _cn_counting(n: int) -> str:
    """1 -> 一，10 -> 十，21 -> 二十一（chapterCounting 的常用形态）。"""
    if n <= 0:
        return str(n)
    if n < 10:
        return _CN_DIGITS[n]
    if n < 20:
        return "十" + (_CN_DIGITS[n % 10] if n % 10 else "")
    if n < 100:
        return _CN_DIGITS[n // 10] + "十" + (_CN_DIGITS[n % 10] if n % 10 else "")
    return str(n)


def _roman(n: int) -> str:
    table = [(1000, "M"), (900, "CM"), (500, "D"), (400, "CD"), (100, "C"), (90, "XC"),
             (50, "L"), (40, "XL"), (10, "X"), (9, "IX"), (5, "V"), (4, "IV"), (1, "I")]
    out = []
    for v, s in table:
        while n >= v:
            out.append(s)
            n -= v
    return "".join(out)


def _letters(n: int) -> str:
    if n <= 0:
        return str(n)
    return chr(ord("a") + (n - 1) % 26) * ((n - 1) // 26 + 1)


def _fmt_num(n: int, fmt: str) -> str:
    fmt = (fmt or "decimal").lower()
    if fmt in ("none", "bullet"):
        return ""
    if fmt == "decimalzero":
        return "%02d" % n
    if fmt == "lowerletter":
        return _letters(n)
    if fmt == "upperletter":
        return _letters(n).upper()
    if fmt == "lowerroman":
        return _roman(n).lower()
    if fmt == "upperroman":
        return _roman(n)
    if fmt in ("chinesecounting", "chinesecountingthousand", "japanesecounting",
               "taiwancounting", "koreancounting"):
        return _cn_counting(n)
    if fmt == "ideographtraditional":
        marks = "甲乙丙丁戊己庚辛壬癸"
        return marks[(n - 1) % 10] if n >= 1 else str(n)
    if fmt == "decimalfullwidth":
        return "".join(chr(ord("０") + int(c)) if c.isdigit() else c for c in str(n))
    return str(n)


@dataclass
class _Lvl:
    fmt: str = "decimal"
    text: str = "%1."
    start: int = 1


def _numbering(zf: zipfile.ZipFile) -> Dict[str, Dict[int, _Lvl]]:
    """numId -> {ilvl: (numFmt, lvlText, start)}。"""
    try:
        root = etree.fromstring(zf.read("word/numbering.xml"))
    except KeyError:
        return {}
    abstracts: Dict[str, Dict[int, _Lvl]] = {}
    for an in root.iter(_w("abstractNum")):
        aid = _attr(an, "abstractNumId")
        lvls: Dict[int, _Lvl] = {}
        for lvl in an.findall(_w("lvl")):
            try:
                ilvl = int(_attr(lvl, "ilvl", "0"))
            except ValueError:
                ilvl = 0
            lvls[ilvl] = _Lvl(
                fmt=_attr(_child(lvl, "numFmt"), "val", "decimal") or "decimal",
                text=_attr(_child(lvl, "lvlText"), "val", "") or "",
                start=int(_attr(_child(lvl, "start"), "val", "1") or 1),
            )
        abstracts[aid] = lvls
    out: Dict[str, Dict[int, _Lvl]] = {}
    for num in root.iter(_w("num")):
        nid = _attr(num, "numId")
        aid = _attr(_child(num, "abstractNumId"), "val")
        if nid is None or aid is None:
            continue
        lvls = dict(abstracts.get(aid, {}))
        # w:lvlOverride 会改起点（「从 5 开始编号」）
        for ov in num.findall(_w("lvlOverride")):
            try:
                ilvl = int(_attr(ov, "ilvl", "0"))
            except ValueError:
                continue
            so = _child(ov, "startOverride")
            if so is not None and ilvl in lvls:
                lvls[ilvl] = _Lvl(lvls[ilvl].fmt, lvls[ilvl].text,
                                  int(_attr(so, "val", "1") or 1))
        out[nid] = lvls
    return out


def _style_levels(zf: zipfile.ZipFile) -> Tuple[Dict[str, int], Dict[str, Tuple[str, int]]]:
    """styleId -> 标题级别；styleId -> (numId, ilvl)（样式自带的自动编号）。"""
    try:
        root = etree.fromstring(zf.read("word/styles.xml"))
    except KeyError:
        return {}, {}
    levels: Dict[str, int] = {}
    nums: Dict[str, Tuple[str, int]] = {}
    for st in root.iter(_w("style")):
        sid = _attr(st, "styleId")
        if not sid:
            continue
        nm = (_attr(_child(st, "name"), "val", "") or "").strip().lower()
        m = re.match(r"^(?:heading|标题)\s*([1-9])$", nm)
        if m:
            levels[sid] = int(m.group(1))
        ppr = st.find(_w("pPr"))
        if ppr is not None:
            ol = _child(ppr, "outlineLvl")
            if ol is not None and sid not in levels:
                try:
                    lv = int(_attr(ol, "val", "8"))
                except ValueError:
                    lv = 8
                if lv < 9:
                    levels[sid] = lv + 1
            npr = ppr.find(_w("numPr"))
            if npr is not None:
                nid = _attr(_child(npr, "numId"), "val")
                try:
                    ilvl = int(_attr(_child(npr, "ilvl"), "val", "0"))
                except ValueError:
                    ilvl = 0
                if nid:
                    nums[sid] = (nid, ilvl)
    return levels, nums


# ---------------------------------------------------------------- 数据


@dataclass
class Entry:
    """目录里的一条。text 已含编号（"3.1. 未授权登录管理员（高危）"）。"""

    level: int
    text: str
    anchor: str = ""
    page: int = 0
    title: str = ""


@dataclass
class RebuildResult:
    docx: bytes
    entries: List[Entry] = field(default_factory=list)
    changed: bool = False
    reason: str = ""


# ---------------------------------------------------------------- 定位 TOC


def _style_names(zf: zipfile.ZipFile) -> Dict[str, str]:
    """styleId -> 样式名。

    必须走这一层：Word 的 styleId 经常是 "6" / "7" 这种数字（导出工具重排过），
    真正说明「这是目录第几级」的是样式名 "toc 1" / "toc 2"。
    拿 styleId 直接正则匹配 toc<数字> 会一条都匹配不上，然后静默「文档里没有目录」。
    """
    out: Dict[str, str] = {}
    try:
        root = etree.fromstring(zf.read("word/styles.xml"))
    except KeyError:
        return out
    for st in root.iter(_w("style")):
        sid = _attr(st, "styleId")
        if sid:
            out[sid] = (_attr(_child(st, "name"), "val", "") or "")
    return out


def _toc_level(style_id: str, style_names: Dict[str, str]) -> Optional[int]:
    for cand in ((style_names.get(style_id or "") or ""), (style_id or "")):
        m = re.match(r"^\s*toc\s*([1-9])\s*$", cand.strip().lower())
        if m:
            return int(m.group(1))
    return None


def _find_toc_range(body, style_names: Dict[str, str]):
    """返回 (全部段落, 开始段下标, 结束段下标, 层级->原型段落)。找不到 TOC 返回 None。

    结构（Word / LibreOffice 生成的都长这样）：

        段A: [fld begin][instr "TOC \\o ..."][fld separate] + 第一条目录项
        段B..: 目录项（每条自己是一对 HYPERLINK + PAGEREF 域）
        段Z: [fld end]
    """
    paras = [p for p in body.iter(_w("p"))]
    start = None
    for i, p in enumerate(paras):
        ins = _instr(p)
        if re.match(r"^\s*TOC\b", ins):
            start = i
            break
    if start is None:
        return None

    # 从 TOC 的 begin 开始数 fldChar 深度，深度归零处就是 TOC 结束段
    depth = 0
    end = None
    for i in range(start, len(paras)):
        for r in paras[i].findall(_w("r")):
            fc = r.find(_w("fldChar"))
            if fc is None:
                continue
            t = _attr(fc, "fldCharType")
            if t == "begin":
                depth += 1
            elif t == "end":
                depth -= 1
                if depth == 0:
                    end = i
                    break
        if end is not None:
            break
    if end is None:
        return None

    # 每个层级保留**全部**旧条目（按文档顺序），供后面「按位继承」用。
    # 只留第一条是不够的：模板里章节条目是黑的、漏洞块条目是红的，
    # 一律克隆第一条会把漏洞条目刷成黑色。
    protos: Dict[int, List] = {}
    order: List[str] = []
    for p in paras[start:end]:
        sid = _attr(_child(p.find(_w("pPr")), "pStyle"), "val", "")
        if not _ptext(p).strip():
            continue
        lv = _toc_level(sid or "", style_names)
        if lv is None:
            # 兜底：样式名被本地化或自造时，按「首次出现的顺序」当层级。
            # 目录天然是按级排的，第一组是一级、第二组是二级，比放弃更接近真相。
            if sid and sid not in order:
                order.append(sid)
            lv = order.index(sid) + 1 if sid in order else 1
        protos.setdefault(lv, []).append(p)
    if not protos:
        return None
    return paras, start, end, protos


# ---------------------------------------------------------------- 标题


_JINJA_TAG = re.compile(r"^\s*\{%")


def _collect_headings(paras, skip_from: int, skip_to: int,
                      levels: Dict[str, int], style_nums: Dict[str, Tuple[str, int]],
                      numbering: Dict[str, Dict[int, _Lvl]]) -> List[Entry]:
    """按文档顺序收标题，并复算出与 Word 自动编号一致的编号文字。"""
    out: List[Entry] = []
    counters: Dict[int, int] = {}
    for idx, p in enumerate(paras):
        if skip_from <= idx <= skip_to:
            continue
        ppr = p.find(_w("pPr"))
        sid = _attr(_child(ppr, "pStyle"), "val", "")
        level = levels.get(sid or "")
        if level is None:
            ol = _child(ppr, "outlineLvl") if ppr is not None else None
            if ol is not None:
                try:
                    lv = int(_attr(ol, "val", "8"))
                except ValueError:
                    lv = 8
                if lv < 9:
                    level = lv + 1
        if level is None:
            continue
        title = _ptext(p).strip()
        if not title:
            continue
        # 块级指令（{%p for %} / {%p endfor %}）经常被作者挂在标题样式的段落上。
        # 它们在 docxtpl 里是指令、不是标题，收进目录就变成一行乱码。
        # 注意只挡 {% %}，不挡 {{ }}：整段就是一个占位符的标题是合法用法，
        # 而且重建发生在 docxtpl 之后，那里早就换成真实文字了。
        if _JINJA_TAG.match(title):
            continue

        num_text = ""
        nid, ilvl = style_nums.get(sid or "", (None, level - 1))
        npr = ppr.find(_w("numPr")) if ppr is not None else None
        if npr is not None:
            nid = _attr(_child(npr, "numId"), "val", nid)
            ilvl = int(_attr(_child(npr, "ilvl"), "val", str(ilvl)) or ilvl)
        if nid and nid != "0":
            lvls = numbering.get(nid) or {}
            lvl = lvls.get(ilvl)
            if lvl and lvl.text:
                counters[ilvl] = counters.get(ilvl, lvl.start - 1) + 1
                for deeper in [k for k in counters if k > ilvl]:
                    del counters[deeper]
                def repl(m, _lvls=lvls, _c=counters):
                    k = int(m.group(1)) - 1
                    sub = _lvls.get(k)
                    return _fmt_num(_c.get(k, 1), sub.fmt if sub else "decimal")
                num_text = re.sub(r"%([1-9])", repl, lvl.text)
        text = (num_text + " " + title).strip() if num_text else title
        out.append(Entry(level=level, text=text, title=title))
    return out


# ---------------------------------------------------------------- 条目改写


def _entry_runs(par):
    """定位 (标题 run 列表, 页码 run 列表)。

    为什么返回**列表**而不是单个 run：Word 存目录缓存时会按格式片段把标题拆成
    好几个 run。真模板里 `3.1. 未授权登录管理员（高危）` 就是 "3.1. 未授权" +
    "登录管理员（高危）" 两段。只改第一个 run 的话，后面几段旧文字原样留着，
    生成出来就是「3.1. 未授权登录管理员（高危）登录管理员（高危）」。
    """
    titles, pages = [], []
    after_pageref = False
    for r in par.findall(_w("r")):
        ins = "".join((it.text or "") for it in r.iter(_w("instrText")))
        if "PAGEREF" in ins.upper():
            after_pageref = True
        if r.find(_w("t")) is None:
            continue
        (pages if after_pageref else titles).append(r)
    return titles, pages


def _clear_run_text(run):
    """抹掉一个 run 的可见文字，**保留 run 本身**。

    不整只删掉：run 上可能挂着 rPr（字体/字号/颜色），也可能是域字符的宿主，
    删 run 有连带风险；留一个只有 rPr 的空 run 对 Word 完全无害。
    """
    for t in run.findall(_w("t")):
        t.text = ""


def _set_run_text(run, text: str):
    if run is None:
        return
    ts = run.findall(_w("t"))
    if not ts:
        el = etree.SubElement(run, _w("t"))
        el.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
        el.text = text
        return
    ts[0].text = text
    ts[0].set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
    for extra in ts[1:]:
        extra.getparent().remove(extra)


def _retarget(par, anchor: str):
    """把 HYPERLINK / PAGEREF 指向新的书签名，其余字节不动。"""
    for it in par.iter(_w("instrText")):
        s = it.text or ""
        if re.search(r"HYPERLINK\s+\\l\s+\S+", s):
            it.text = re.sub(r"(HYPERLINK\s+\\l\s+)\S+", lambda m: m.group(1) + anchor, s)
        elif re.search(r"PAGEREF\s+\S+", s):
            it.text = re.sub(r"(PAGEREF\s+)\S+", lambda m: m.group(1) + anchor, s)


_BM_START = "_Toc"


def _clear_toc_bookmarks(body):
    """删掉所有 _Toc* 书签。

    它们是上一次排版留下的，docxtpl 展开循环时还会被原样复制成重名书签
    （Word 遇到重名书签会跳到第一个，目录点哪都跳同一处）。全部删掉重建最稳。
    """
    ids = set()
    for bm in list(body.iter(_w("bookmarkStart"))):
        name = _attr(bm, "name", "") or ""
        if name.startswith(_BM_START):
            ids.add(_attr(bm, "id", ""))
            bm.getparent().remove(bm)
    for bm in list(body.iter(_w("bookmarkEnd"))):
        if _attr(bm, "id", "") in ids:
            bm.getparent().remove(bm)


def _add_bookmark(par, bm_id: str, name: str):
    start = etree.Element(_w("bookmarkStart"))
    start.set(_w("id"), bm_id)
    start.set(_w("name"), name)
    end = etree.Element(_w("bookmarkEnd"))
    end.set(_w("id"), bm_id)
    # 插在 pPr 之后（bookmarkStart 必须是段落的合法子元素，位置在 pPr 后最稳）
    ppr = par.find(_w("pPr"))
    pos = 0 if ppr is None else list(par).index(ppr) + 1
    par.insert(pos, start)
    par.append(end)


def _heading_paragraphs(paras, skip_from: int, skip_to: int,
                        levels: Dict[str, int]) -> List[object]:
    out = []
    for idx, p in enumerate(paras):
        if skip_from <= idx <= skip_to:
            continue
        ppr = p.find(_w("pPr"))
        sid = _attr(_child(ppr, "pStyle"), "val", "")
        lv = levels.get(sid or "")
        if lv is None and ppr is not None:
            ol = ppr.find(_w("outlineLvl"))
            if ol is not None:
                try:
                    v = int(_attr(ol, "val", "8"))
                except ValueError:
                    v = 8
                if v < 9:
                    lv = v + 1
        if lv is not None and _ptext(p).strip():
            out.append(p)
    return out


def _toc_head_bounds(par):
    """TOC 域头三个 run 的下标区间：[begin] .. [separate]。

    这一段是「域的开关」，不含目录内容。它能和目录项共处一段（Word 就是这么
    生成的），所以改写必须精确到 run，不能按段切。
    """
    runs = par.findall(_w("r"))
    idx = None
    for i, r in enumerate(runs):
        ins = "".join((it.text or "") for it in r.iter(_w("instrText")))
        if re.match(r"^\s*TOC\b", ins):
            idx = i
            break
    if idx is None:
        return None
    begin = idx
    for j in range(idx - 1, -1, -1):
        fc = runs[j].find(_w("fldChar"))
        if fc is not None and _attr(fc, "fldCharType") == "begin":
            begin = j
            break
    sep = idx
    for j in range(idx + 1, len(runs)):
        fc = runs[j].find(_w("fldChar"))
        if fc is not None and _attr(fc, "fldCharType") == "separate":
            sep = j
            break
    return runs, begin, sep


def _toc_head_runs(par):
    b = _toc_head_bounds(par)
    if not b:
        return []
    runs, begin, sep = b
    return [copy.deepcopy(r) for r in runs[begin:sep + 1]]


def _strip_toc_head(par):
    b = _toc_head_bounds(par)
    if not b:
        return
    runs, begin, sep = b
    for r in runs[begin:sep + 1]:
        par.remove(r)


def rebuild(raw: bytes, pages: Optional[Sequence[Optional[int]]] = None) -> RebuildResult:
    """重写 TOC 域的结果部分。文档里没有 TOC 就原样返回。

    pages：与 entries 等长的页码（由 measure() 量得）。None 时按「不倒退」的
    保守规则填（第一条 1，之后沿用上一条），Word 打开时会自行更新。
    """
    try:
        zin = zipfile.ZipFile(io.BytesIO(raw))
    except zipfile.BadZipFile:
        return RebuildResult(docx=raw, reason="not_a_zip")
    names = zin.namelist()
    if "word/document.xml" not in names:
        return RebuildResult(docx=raw, reason="no_document")

    root = etree.fromstring(zin.read("word/document.xml"))
    body = root.find(_w("body"))
    if body is None:
        return RebuildResult(docx=raw, reason="no_body")

    found = _find_toc_range(body, _style_names(zin))
    if not found:
        return RebuildResult(docx=raw, reason="no_toc")
    paras, start, end, protos = found

    levels, style_nums = _style_levels(zin)
    numbering = _numbering(zin)
    entries = _collect_headings(paras, start, end, levels, style_nums, numbering)
    if not entries:
        return RebuildResult(docx=raw, reason="no_heading")

    # 页码：有实测值就用实测，没有就保守沿用（不倒退）
    if pages is not None:
        for i, e in enumerate(entries):
            e.page = int(pages[i]) if i < len(pages) and pages[i] else 0
    else:
        last = 0
        for e in entries:
            e.page = last or 1
            last = e.page
    prev_page = 1
    for i, e in enumerate(entries, start=1):
        e.anchor = "%s9000%02d" % (_BM_START, i)
        if not e.page:
            # 量不到（标题在扫描件里、或被拆成两行）就沿用前一条：
            # 页码只可能不倒退，填 1 会把后面所有条目都拉回首页。
            e.page = prev_page
        prev_page = e.page

    # 每个标题段落挂一个（新的）唯一书签
    heads = _heading_paragraphs(paras, start, end, levels)
    _clear_toc_bookmarks(body)
    for i, (hp, e) in enumerate(zip(heads, entries), start=1):
        _add_bookmark(hp, str(900000 + i), "%s9000%02d" % (_BM_START, i))

    # 造新条目：按位继承原型 -> 只改文字 / 书签 / 页码
    seen: Dict[int, int] = {}

    def _make(e: Entry):
        lst = protos.get(e.level) or protos[min(protos)]
        i = seen.get(e.level, 0)
        seen[e.level] = i + 1
        # **按位继承**：第 i 条新条目沿用模板里第 i 条同级旧条目的格式。
        # 为什么不能「一律用第一条」：本模板里章节（1./2./3.）是黑色，而漏洞块
        # （3.1~3.4）被作者标成了红色，正文里对应的 3.x 标题同样是红色。一律克隆
        # 第一条会把漏洞条目刷成黑色，出现「正文红、目录黑」的不一致。
        # 超出模板条目数时沿用最后一条 —— 循环多长出来几条漏洞，就该长得和前一条
        # 漏洞一模一样，而不是回退到章节的黑色。
        proto = lst[i] if i < len(lst) else lst[-1]
        par = copy.deepcopy(proto)
        # 一级原型往往就是 TOC 起始段——它同时装着「TOC 域头」和「第一条目录项」。
        # 先精确摘掉域头这三个 run，剩下的才是干净的目录项；域头稍后统一补回，
        # 否则新文档里会长出两套 begin/separate，Word 打开直接报域错误。
        _strip_toc_head(par)
        title_runs, page_runs = _entry_runs(par)
        # 文字只写进第一个 run，其余 run 必须清空——否则旧标题的残余片段会
        # 拼在新文字后面（真模板的 3.1 就是两段 run，实测会变成
        # 「3.1. 未授权登录管理员（高危）登录管理员（高危）」）。
        _set_run_text(title_runs[0] if title_runs else None, e.text)
        for extra in title_runs[1:]:
            _clear_run_text(extra)
        _set_run_text(page_runs[0] if page_runs else None, str(e.page))
        for extra in page_runs[1:]:
            _clear_run_text(extra)
        _retarget(par, e.anchor)
        return par

    new_paras = [_make(e) for e in entries]

    # TOC 域头（begin + instr + separate）从原起始段原样复制，塞进第一条新条目里。
    # 复制而不是新造：域开关里的 \o "1-3" \h \z \u 是模板作者的设置，改一个字
    # 目录的收录层级就变了。
    head_runs = _toc_head_runs(paras[start])
    first = new_paras[0]
    for i, r in enumerate(head_runs):
        first.insert(i, r)

    # 替换：起始段位置换成新条目序列，原条目段与结束段之间清空（保留结束段）
    parent = paras[start].getparent()
    anchor_idx = list(parent).index(paras[start])
    for el in parent[anchor_idx:anchor_idx + (end - start)]:
        parent.remove(el)
    for i, np_ in enumerate(new_paras):
        parent.insert(anchor_idx + i, np_)

    data = {n: zin.read(n) for n in names}
    data["word/document.xml"] = etree.tostring(root, xml_declaration=True,
                                              encoding="UTF-8", standalone=True)
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zo:
        for n, b in data.items():
            zo.writestr(n, b)
    return RebuildResult(docx=buf.getvalue(), entries=entries, changed=True)


# ---------------------------------------------------------------- 量页码


_DOT_LEADER = re.compile(r"\.{4,}|·{4,}|…{2,}")

# 两遍排版的上限。条目数固定后版面高度不变（右对齐制表位吃掉位数差异），
# 正常一次回填就收敛；留一点余量是因为极端情况下页码从 1 位变 2 位会把
# 最后一行挤到下一页，那就还要再量一次。
MAX_PASSES = 3


def measure_text(text: str, entries: Sequence[Entry]) -> List[Optional[int]]:
    """从 `pdftotext -layout` 的全文里找出每条目录项落在第几页。

    两个必须的过滤：
    1. **跳过目录页自己**。目录行也以标题文字开头（"1. 概述......1"），
       不排除就会把每条都定到目录页。目录行的特征是点线前导符，正文标题没有。
    2. **单调递增**。同名标题可能出现多次（比如附录里重复章节名），
       目录顺序本身就是递增的，只向后找就不会错配。
    """
    pages = text.split("\f")
    out: List[Optional[int]] = []
    cur = 0
    for e in entries:
        key = _norm(e.text)
        short = _norm(e.title)[:8] or key[:8]
        hit = None
        for i in range(cur, len(pages)):
            for line in pages[i].splitlines():
                if not line.strip() or _DOT_LEADER.search(line):
                    continue
                ln = _norm(line)
                if not ln:
                    continue
                if ln.startswith(key) or (short and short in ln[:max(len(short) + 6, 12)]):
                    hit = i + 1
                    break
            if hit:
                break
        if hit:
            cur = hit - 1
        out.append(hit)
    return out


def needs_two_pass(entries: Sequence[Entry]) -> bool:
    """只有真需要页码时才值当多排一次版。"""
    return bool(entries)
