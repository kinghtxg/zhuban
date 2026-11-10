# -*- coding: utf-8 -*-
"""模板字体体检：这份 docx 用了哪些字体、本机能不能给出对应字形。

## 为什么需要它

模板里写「仿宋_GB2312」，普通 Linux 上根本不存在这个族名。缺了以后
**不报任何错**：接口 200、正文全对、PDF 能打开，只有字形被换掉
——要么换成 Noto Serif（宋体字形，看着像但不是仿宋），要么换成
DejaVu Sans（一个汉字都没有，逐字回退）。

用户判断不了这件事，因为他在自己的 Windows 上打开模板是好的。
所以必须在**录入模板的那一刻**就告诉他：这几处字体本机没有，导出会变成那样。

## 判定的三层

1. `fc-list` 里有这个族名 → **exact**（真的装了）
2. 没有，但 `fc-match` 落到别的中文字体 → **fallback**（会被替换，字形会变）
3. 没有，且落到不含汉字的字体 → **missing**（汉字要靠逐字回退，最坏）

纯拉丁字体（Arial / Times New Roman）单独标 **latin**：它们本来就会被
Liberation 之类的公制兼容字体替换，那是正常行为，不该混进「缺字体」的告警里。

## 两件事必须分开看（中文用哪支、西文用哪支）

中文模板的正文几乎都是**中英混排**：汉字走 `w:rFonts/@w:eastAsia`
（仿宋_GB2312），西文走 `@w:ascii` / `@w:hAnsi`（Times New Roman）。
OOOOXML 把这两种文字交给**不同槽位**，Word 渲染时也是按字符分别取字体的。

老实现把一个 `w:rFonts` 的四个槽位（ascii / hAnsi / eastAsia / cs）**压成一个
族名集合**，于是：

- 界面只能显示一串并列族名，用户看不出「哪个管中文、哪个管英文」；
- 纯英文 run 上挂着的仿宋也被当成"用到了仿宋"；
- 反过来，段落真正**继承**来的字体（run 没写 rFonts，靠样式链）根本看不出来。

所以本模块现在两条腿走路：

- `fonts` / `missing` / `severe` / `checked`：**面**上的统计，回答「这份模板总共
  用到哪些族名、本机有没有」。老字段，语义不变，前端和 Go 侧都在用。
- `roles` / `paragraphs`：**点**上的归属，回答「这一段的中文是什么字体、
  西文是什么字体」。按 run 级解析，并且真的去解样式链与主题槽位。

## 西文槽位的脏族名（真实踩过的坑）

WPS 存盘时会把主题槽位的**界面显示名**直接写进 `w:rFonts`，比如
`<w:rFonts w:cs="Calibri (西文正文)"/>`。老实现拿它去 fc-match，落空，
再因为「族名里有汉字」判成中文字体 —— 最后报「汉字可能显示不出来」。
一个西文字体被报警成"汉字显示不出来"，用户看到只会觉得体检在胡说。

所以判 CJK 之前必须先剥掉括号里的中文注解（见 `_base_family`），并且
`cs`（复杂文种）槽位在简体文档里根本不参与汉字判定。

## 为什么直接读 zip

不依赖 docmodel 的结构解析：那份解析是给在线结构编辑用的，遇到奇怪结构会抛异常
（并明确告诉用户"结构不可用但生成没问题"）。字体体检比它简单得多，也不该因为
一个奇怪的表格结构就整份体检失败——它要回答的问题只有「用了哪些族名」。
"""
from __future__ import annotations

import io
import re
import zipfile
from typing import Any, Dict, Iterable, List, Optional, Set, Tuple

from lxml import etree

from . import cjk_fonts

NS_W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
NS_A = "http://schemas.openxmlformats.org/drawingml/2006/main"

q = lambda t: "{%s}%s" % (NS_W, t)
qa = lambda t: "{%s}%s" % (NS_A, t)

# 这些属性存的是**主题槽位引用**（majorEastAsia / minorHAnsi …），不是族名。
# 把它们当族名去 fc-match 必然落到兜底字体，于是每一份模板都会报一堆假缺失。
_SLOT_ATTRS = ("asciiTheme", "hAnsiTheme", "eastAsiaTheme", "cstheme")

# 部件 -> 人读名称。没列到的按文件名兜底。
_PARTS = {
    "word/document.xml": "正文",
    "word/styles.xml": "样式",
    # Word 2010 的兼容副本，改样式时两边都要改，报「样式」会让人找不到地方
    "word/stylesWithEffects.xml": "样式(兼容)",
    "word/numbering.xml": "编号",
    "word/theme/theme1.xml": "主题",
    "word/footnotes.xml": "脚注",
    "word/endnotes.xml": "尾注",
}

# 角色 -> 人读名称。这里的「西文」= ascii + hAnsi 两个槽位。
ROLE_EA = "eastAsia"
ROLE_LATIN = "latin"
ROLE_CS = "cs"
ROLE_LABEL = {ROLE_EA: "中文", ROLE_LATIN: "西文", ROLE_CS: "复杂文种"}


def _part_name(entry: str) -> str:
    if entry in _PARTS:
        return _PARTS[entry]
    if entry.startswith("word/header"):
        return "页眉"
    if entry.startswith("word/footer"):
        return "页脚"
    if entry.startswith("word/comments"):
        return "批注"
    return entry.rsplit("/", 1)[-1]


def _para_part(p, fallback: str) -> str:
    """段落所在的位置标签：在表格里就标「表格」，否则沿用部件名。

    为什么要分：用户拿着报告去正文里找「序号 / 系统名称」这几行是找不到的，
    它们在表格里。位置说不准，这份报告就只能当情绪参考。
    """
    node = p.getparent() if hasattr(p, "getparent") else None
    while node is not None:
        if node.tag == q("tbl"):
            return "表格"
        if node.tag in (q("body"), q("sectPr")):
            break
        node = node.getparent() if hasattr(node, "getparent") else None
    return fallback


def _is_slot(value: str) -> bool:
    """主题槽位引用（"+mn-ea"、"majorEastAsia"）不是族名。"""
    v = (value or "").strip()
    if not v:
        return True
    if v.startswith("+"):
        return True
    return v in ("majorEastAsia", "minorEastAsia", "majorHAnsi", "minorHAnsi",
                 "majorAscii", "minorAscii", "majorBidi", "minorBidi")


# ---------------------------------------------------------------- 族名清洗

_NOTE_RE = re.compile(r"[\(（][^\)）]*[\)）]")


def _base_family(family: str) -> str:
    """剥掉族名里的括号注解：`Calibri (西文正文)` → `Calibri`。

    WPS 会把主题槽位的界面名写进 `w:rFonts`。这种串拿去 fc-match 一定落空，
    而它带着汉字注解，老实现就判成"中文字体缺失"——一个西文字体被报成
    「汉字可能显示不出来」。判断"是不是中文字体"必须看括号前的真名。
    """
    raw = (family or "").strip()
    if not raw:
        return ""
    stripped = _NOTE_RE.sub("", raw).strip()
    return stripped or raw


def _has_note(family: str) -> bool:
    """族名里带括号注解 —— 基本都是 WPS / 第三方工具写进去的脏数据。"""
    return bool(_NOTE_RE.search(family or ""))


def _families_in_rfonts(root) -> Iterable[str]:
    """从一个 XML 根里取所有 w:rFonts 的显式族名（四个槽位都算）。"""
    for el in root.iter(q("rFonts")):
        for attr in ("ascii", "hAnsi", "eastAsia", "cs"):
            v = el.get(q(attr))
            if v and not _is_slot(v):
                yield v.strip()


def _families_in_theme(root) -> Iterable[str]:
    """theme1.xml：只取**会作用到中文正文**的那几个 typeface。

    这里必须克制。Word 的 theme1.xml 会给世界上每一种文字都列一套字体
    （泰文 Angsana New、高棉文 DaunPenh、希伯来文……），一份普通中文模板
    能列三十多个。全量上报的结果是"体检出 40 种字体、缺 7 种"，
    用户看一眼就关掉了，真正的 `仿宋_GB2312` 反而被埋掉。

    所以只取：
      - `<a:latin> / <a:ea> / <a:cs>`：主题给拉丁/东亚/复杂文种的默认字体
      - `<a:font script="Hans">`：简体中文专用槽位——公文模板的中文默认字体就在这
    繁体（Hant）、日文（Jpan）、韩文（Kore）槽位不取：本产品处理简体文档，
    那些槽位上的字体（新細明體、ＭＳ 明朝）报了也处理不了，只是噪音。
    """
    for tag in ("latin", "ea", "cs"):
        for el in root.iter(qa(tag)):
            v = el.get("typeface")
            if v and not _is_slot(v):
                yield v.strip()
    for el in root.iter(qa("font")):
        if el.get("script") != "Hans":
            continue
        v = el.get("typeface")
        if v and not _is_slot(v):
            yield v.strip()


def _looks_cjk(family: str) -> bool:
    """这个族名是不是「应该给出汉字字形」的。

    判据两条，任一成立即算：
      1. 名字里有汉字（宋体 / 方正小标宋简体 / 华文中宋…）
      2. 在 cjk_fonts 的映射表里（SimSun / Microsoft YaHei / FangSong_GB2312…）

    **先剥掉括号注解**：`Calibri (西文正文)` 剥完是 `Calibri`，属于西文，
    不该按中文字体报"汉字显示不出来"。

    没能命中这两条的非拉丁字体（比如某个冷门厂商的英文族名）会被当成普通
    拉丁字体放过——宁可漏报，也不要让每份模板都堆一屏看不懂的告警。
    """
    fam = _base_family(family)
    if not fam:
        return False
    if any("\u4e00" <= ch <= "\u9fff" for ch in fam):
        return True
    # 族名是大小写不敏感匹配的（Word 里写 simsun 与 SimSun 是同一件事），
    # 查表也要忽略大小写，否则 `simsun` 会被当成普通拉丁字体放过。
    low = fam.lower()
    known = {name.lower() for name, _ in cjk_fonts.ALIASES}
    return low in known


def judge(family: str, have: Optional[set] = None) -> Tuple[str, str]:
    """返回 (status, resolved)。status ∈ exact / fallback / missing / latin。"""
    installed = have if have is not None else cjk_fonts.installed_families()
    family = (family or "").strip()
    if not family:
        return "latin", ""
    if family in installed:
        return "exact", family

    resolved = cjk_fonts.fc_match(family)
    names = {n.strip() for n in (resolved or "").split(",") if n.strip()}
    # fc-match 大小写不敏感，返回的常是规范名，精确比对要忽略大小写
    low = {n.lower() for n in names}
    if family.lower() in low:
        return "exact", resolved
    if not resolved:
        return ("missing" if _looks_cjk(family) else "latin"), ""

    if cjk_fonts.classify(resolved):
        # 落到了别的中文字体：比如 仿宋_GB2312 → Noto Serif CJK SC（宋体字形）。
        # 中文能显示，但字形不是模板要的那个。
        return ("fallback" if _looks_cjk(family) else "latin"), resolved
    return ("missing" if _looks_cjk(family) else "latin"), resolved


# ================================================================
# 以下为「按 run 识别中英文各用什么字体」的实现
# ================================================================

# run 可能被这些容器包着（超链接、修订插入、内容控件…），要递归进去取
_WRAPPERS = ("hyperlink", "ins", "moveTo", "smartTag", "fldSimple", "customXml",
             "sdt", "sdtContent", "dir", "bdo")

_CN_PUNCT = set("‘’“”…—－　、。，；：？！（）《》【】「」")


def _is_hans(ch: str) -> bool:
    o = ord(ch)
    if 0x3400 <= o <= 0x4DBF or 0x4E00 <= o <= 0x9FFF or 0xF900 <= o <= 0xFAFF:
        return True
    if 0x3000 <= o <= 0x303F or 0xFF00 <= o <= 0xFFEF:   # 全角标点 / 全角字符
        return True
    return ch in _CN_PUNCT


def _roles_of_text(text: str) -> Set[str]:
    """一个 run 的文字实际会用到哪些槽位。

    Word 是按字符选字体的：汉字走 eastAsia，ASCII 走 ascii，
    0x80–0xFF 的西文高位字符走 hAnsi。所以判断依据只能是**文字本身**，
    而不是 rFonts 写了几个属性（模板里常见"整段刷同一字体"，四个槽位都填满，
    但纯英文的那一段其实一个字都没用到 eastAsia 那支）。
    """
    roles: Set[str] = set()
    for ch in text or "":
        if _is_hans(ch):
            roles.add(ROLE_EA)
        elif ch.isascii() and ch.isalnum():
            roles.add(ROLE_LATIN)
        elif "\u0080" <= ch <= "\u00FF":
            roles.add(ROLE_LATIN)
    return roles


def _rfonts_dict(rf) -> Dict[str, str]:
    """把一个 w:rFonts 元素摊成 {槽位: 值}，主题槽位引用单独记成 xxxTheme。"""
    if rf is None:
        return {}
    out: Dict[str, str] = {}
    for attr in ("ascii", "hAnsi", "eastAsia", "cs"):
        v = rf.get(q(attr))
        if v and not _is_slot(v):
            out[attr] = v.strip()
    for attr in _SLOT_ATTRS:
        v = rf.get(q(attr))
        if v:
            out[attr] = v.strip()
    return out


def _theme_scheme(root) -> Dict[str, Dict[str, str]]:
    """theme1.xml → {"major": {latin/ea/cs/Hans}, "minor": {...}}。

    这里和 `_families_in_theme` 的口径一致：只认简体中文（Hans）槽位，
    泰文/日文/韩文那些取不到也不用取。
    """
    out: Dict[str, Dict[str, str]] = {"major": {}, "minor": {}}
    if root is None:
        return out
    for kind in ("major", "minor"):
        holder = None
        for el in root.iter(qa(kind + "Font")):
            holder = el
            break
        if holder is None:
            continue
        for slot in ("latin", "ea", "cs"):
            el = holder.find(qa(slot))
            if el is not None:
                v = (el.get("typeface") or "").strip()
                if v and not _is_slot(v):
                    out[kind][slot] = v
        for el in holder.findall(qa("font")):
            if el.get("script") == "Hans":
                v = (el.get("typeface") or "").strip()
                if v and not _is_slot(v):
                    out[kind]["Hans"] = v
    return out


def _resolve_slot_ref(ref: str, theme: Dict[str, Dict[str, str]]) -> str:
    """把 `minorEastAsia` / `majorHAnsi` 这类槽位引用解成真实族名。"""
    ref = (ref or "").strip()
    if not ref:
        return ""
    kind = "major" if ref.startswith("major") else "minor"
    if ref.startswith("+"):          # "+mn-ea" / "+mj-lt" 这种短写
        kind = "major" if ref.startswith("+mj") else "minor"
        tail = ref.split("-")[-1]
        table = {"lt": "latin", "ea": "ea", "cs": "cs", "hans": "Hans"}
        return theme.get(kind, {}).get(table.get(tail, "latin"), "")
    low = ref.lower()
    if "eastasia" in low:
        # 中文优先取 Hans 专用槽位；没有才退回 ea（ea 常留空）
        return theme.get(kind, {}).get("Hans") or theme.get(kind, {}).get("ea", "")
    if "bidi" in low:
        return theme.get(kind, {}).get("cs", "")
    return theme.get(kind, {}).get("latin", "")


def _merge_fonts(*layers: Dict[str, str]) -> Dict[str, str]:
    """按 Word 的优先级合并多层 rFonts：后面的覆盖前面的，空值不覆盖。"""
    out: Dict[str, str] = {}
    for layer in layers:
        for k, v in (layer or {}).items():
            if v:
                out[k] = v
    return out


def _styles_index(root):
    """styles.xml → (docDefaults 的 rFonts, {styleId: {basedOn, fonts}})。"""
    defaults: Dict[str, str] = {}
    styles: Dict[str, Dict[str, Any]] = {}
    if root is None:
        return defaults, styles

    dd = root.find(q("docDefaults"))
    if dd is not None:
        rpr = dd.find(q("rPrDefault"))
        if rpr is not None:
            inner = rpr.find(q("rPr"))
            if inner is not None:
                defaults = _rfonts_dict(inner.find(q("rFonts")))

    for st in root.findall(q("style")):
        sid = st.get(q("styleId"))
        if not sid:
            continue
        based = st.find(q("basedOn"))
        rpr = st.find(q("rPr"))
        styles[sid] = {
            "basedOn": based.get(q("val")) if based is not None else None,
            "fonts": _rfonts_dict(rpr.find(q("rFonts")) if rpr is not None else None),
        }
    return defaults, styles


def _style_chain(sid: Optional[str], styles: Dict[str, Dict[str, Any]]) -> List[str]:
    """styleId 的 basedOn 链，从根到自身。带环保护。"""
    chain: List[str] = []
    seen: Set[str] = set()
    cur = sid
    while cur and cur in styles and cur not in seen:
        seen.add(cur)
        chain.append(cur)
        cur = styles[cur].get("basedOn")
    return list(reversed(chain))


def _runs_of(p) -> List[Any]:
    """段落里的 run：递归进超链接/修订/内容控件，但**不进嵌套段落**（文本框）。"""
    out: List[Any] = []

    def walk(el):
        for c in el:
            tag = etree.QName(c).localname if isinstance(c.tag, str) else ""
            if tag == "r":
                out.append(c)
            elif tag == "p":
                continue                      # 嵌套段落单独统计
            elif tag in _WRAPPERS:
                walk(c)

    walk(p)
    return out


def _run_text(r) -> str:
    return "".join(t.text or "" for t in r.iter(q("t")))


def audit_runs(raw: bytes, have: Optional[set] = None,
               limit_paras: int = 40) -> Dict[str, Any]:
    """按 run 解析「这段的中文用什么、西文用什么」。

    返回：
      roles       {eastAsia|latin: [{family,status,resolved,segments}]}  按角色归的族名
      paragraphs  [{part,index,style,text,cjk:{...}|None,latin:{...}|None}]  段级示例
      suspects    [{family,reason}]  疑似脏族名（带括号注解、或像槽位名）

    为什么必须解析继承：中文模板里大量段落**根本没写 rFonts**（`1. 概述` 就是），
    字体来自 pStyle → basedOn 链 → docDefaults，或者来自主题槽位引用。
    只看 run 上的显式属性，这些段落的字体就全是"不知道"，等于没体检。
    """
    have = have if have is not None else cjk_fonts.installed_families()
    roles: Dict[str, Dict[str, Dict[str, Any]]] = {ROLE_EA: {}, ROLE_LATIN: {}}
    paragraphs: List[Dict[str, Any]] = []
    suspects: Dict[str, str] = {}

    try:
        zf = zipfile.ZipFile(io.BytesIO(raw))
    except Exception:  # noqa: BLE001
        return {"roles": {}, "paragraphs": [], "suspects": []}

    with zf:
        names = zf.namelist()

        def read(entry):
            try:
                return etree.fromstring(zf.read(entry))
            except Exception:  # noqa: BLE001 - 单部件坏掉不影响其它
                return None

        theme = _theme_scheme(read("word/theme/theme1.xml"))
        defaults, styles = _styles_index(read("word/styles.xml"))

        def _is_run_part(name: str) -> bool:
            """只有这些部件里才有真正的 run 文字（styles/numbering 里没有正文）。"""
            if name == "word/document.xml":
                return True
            if not (name.startswith("word/") and name.endswith(".xml")):
                return False
            return any(k in name for k in ("header", "footer", "footnote", "endnote"))

        targets = [e for e in names if _is_run_part(e)]
        # 正文排前面：示例段落优先展示正文，而不是页眉
        targets.sort(key=lambda e: (e != "word/document.xml", e))

        for entry in targets:
            root = read(entry)
            if root is None:
                continue
            part = _part_name(entry)
            idx = 0
            for p in root.iter(q("p")):
                runs = _runs_of(p)
                if not runs:
                    continue
                # 这一段的真实位置（表内段落标「表格」），下面记 parts 与示例都用它
                p_part = _para_part(p, part)
                pPr = p.find(q("pPr"))
                p_style = None
                p_direct: Dict[str, str] = {}
                if pPr is not None:
                    ps = pPr.find(q("pStyle"))
                    if ps is not None:
                        p_style = ps.get(q("val"))
                    inner = pPr.find(q("rPr"))
                    if inner is not None:
                        p_direct = _rfonts_dict(inner.find(q("rFonts")))

                chain_fonts = _merge_fonts(
                    defaults,
                    *[styles[s]["fonts"] for s in _style_chain(p_style, styles) if s in styles],
                    p_direct,
                )

                seg_cjk: List[str] = []
                seg_latin: List[str] = []
                texts: List[str] = []
                for r in runs:
                    text = _run_text(r)
                    if not text.strip():
                        continue
                    texts.append(text)
                    rPr = r.find(q("rPr"))
                    r_direct: Dict[str, str] = {}
                    r_style = None
                    if rPr is not None:
                        r_direct = _rfonts_dict(rPr.find(q("rFonts")))
                        rs = rPr.find(q("rStyle"))
                        if rs is not None:
                            r_style = rs.get(q("val"))
                    eff = _merge_fonts(
                        chain_fonts,
                        *[styles[s]["fonts"] for s in _style_chain(r_style, styles) if s in styles],
                        r_direct,
                    )
                    # 槽位引用（asciiTheme="minorHAnsi"）在这里才真的解成族名。
                    # **显式族名优先**：合并层里 ascii="黑体" 与 asciiTheme="minorHAnsi"
                    # 可能同时存在（下层 docDefaults 给槽位、上层 run 给族名），
                    # 无脑让槽位覆盖会让所有直接格式失效——那正是「中文全变成宋体」
                    # 的原因：docDefaults 的 eastAsiaTheme 把 run 上的仿宋顶掉了。
                    resolved: Dict[str, str] = {}
                    for slot in ("ascii", "hAnsi", "eastAsia", "cs"):
                        v = eff.get(slot)
                        if v:
                            resolved[slot] = v
                            continue
                        ref = eff.get(slot + "Theme")
                        if ref:
                            got = _resolve_slot_ref(ref, theme)
                            if got:
                                resolved[slot] = got

                    used = _roles_of_text(text)
                    if ROLE_EA in used and resolved.get("eastAsia"):
                        fam = resolved["eastAsia"]
                        if fam not in seg_cjk:
                            seg_cjk.append(fam)
                        roles[ROLE_EA].setdefault(fam, {"segments": 0, "parts": []})
                        if p_part not in roles[ROLE_EA][fam]["parts"]:
                            roles[ROLE_EA][fam]["parts"].append(p_part)
                        if _has_note(fam):
                            suspects.setdefault(fam, "族名里带括号注解，多半是 WPS 写进去的显示名")
                    if ROLE_LATIN in used:
                        fam = resolved.get("ascii") or resolved.get("hAnsi")
                        if fam:
                            if fam not in seg_latin:
                                seg_latin.append(fam)
                            roles[ROLE_LATIN].setdefault(fam, {"segments": 0, "parts": []})
                            if p_part not in roles[ROLE_LATIN][fam]["parts"]:
                                roles[ROLE_LATIN][fam]["parts"].append(p_part)
                            if _has_note(fam):
                                suspects.setdefault(fam, "族名里带括号注解，多半是 WPS 写进去的显示名")

                if not seg_cjk and not seg_latin:
                    continue
                # 每段每族只记一次：否则一个长段落会把计数刷成几十，
                # 「多少段用到这支字体」这个语义就没了
                for role, fams in ((ROLE_EA, seg_cjk), (ROLE_LATIN, seg_latin)):
                    for fam in fams:
                        roles[role][fam]["segments"] += 1

                if len(paragraphs) < limit_paras:
                    paragraphs.append({
                        "part": p_part,
                        "index": idx,
                        "style": p_style,
                        "text": "".join(texts)[:40],
                        "cjk": _entry(seg_cjk[0], have) if seg_cjk else None,
                        "latin": _entry(seg_latin[0], have) if seg_latin else None,
                    })
                idx += 1

    role_out: Dict[str, Any] = {}
    for role, bucket in roles.items():
        if not bucket:
            continue
        items = []
        for fam, meta in bucket.items():
            status, resolved = judge(fam, have)
            items.append({
                "family": fam,
                "status": status,
                "resolved": resolved,
                "segments": meta["segments"],
                "parts": meta["parts"],
            })
        # 缺的排前面：用户先要看到"哪个不能用"
        items.sort(key=lambda x: (x["status"] not in ("missing", "fallback"), -x["segments"], x["family"]))
        role_out[role] = {"label": ROLE_LABEL[role], "fonts": items}

    return {
        "roles": role_out,
        "paragraphs": paragraphs,
        "suspects": [{"family": k, "reason": v} for k, v in suspects.items()],
    }


def _entry(family: str, have: Optional[set]) -> Dict[str, Any]:
    status, resolved = judge(family, have)
    return {"family": family, "status": status, "resolved": resolved}


def audit_docx(raw: bytes) -> Dict[str, object]:
    """体检一份 docx。任何解析失败都降级成 ok=False + 说明，不抛异常。

    体检失败不该让模板上传失败——那是两件事。上传继续，只是这次没给出提醒。
    """
    used: Dict[str, List[str]] = {}
    try:
        zf = zipfile.ZipFile(io.BytesIO(raw))
    except Exception as e:  # noqa: BLE001
        return {"ok": False, "error": "不是有效的 docx（%s）" % str(e)[:120],
                "fonts": [], "missing": [], "checked": 0, "severe": [],
                "roles": {}, "paragraphs": [], "suspects": []}

    try:
        with zf:
            for entry in zf.namelist():
                if not entry.endswith(".xml"):
                    continue
                if not (entry.startswith("word/") and
                        ("document" in entry or "styles" in entry or "theme" in entry
                         or "header" in entry or "footer" in entry
                         or "numbering" in entry or "footnote" in entry
                         or "endnote" in entry)):
                    continue
                try:
                    root = etree.fromstring(zf.read(entry))
                except Exception:  # noqa: BLE001 - 单个部件坏掉不影响其它
                    continue
                names = (_families_in_theme(root) if "theme" in entry
                         else _families_in_rfonts(root))
                part = _part_name(entry)
                for fam in names:
                    parts = used.setdefault(fam, [])
                    if part not in parts:
                        parts.append(part)
    except Exception as e:  # noqa: BLE001
        return {"ok": False, "error": "读取 docx 失败（%s）" % str(e)[:120],
                "fonts": [], "missing": [], "checked": 0, "severe": [],
                "roles": {}, "paragraphs": [], "suspects": []}

    have = cjk_fonts.installed_families()
    fonts: List[Dict[str, object]] = []
    for fam in sorted(used):
        status, resolved = judge(fam, have)
        fonts.append({
            "family": fam,
            "status": status,
            "resolved": resolved,
            "parts": used[fam],
            "clean_name": _base_family(fam),
            "note": "族名里带括号注解，已按括号前的名字判断" if _has_note(fam) else "",
        })

    missing = [f["family"] for f in fonts if f["status"] in ("fallback", "missing")]
    severe = [f["family"] for f in fonts if f["status"] == "missing"]

    # 面上统计之外，再给点上归属：中文/西文各用哪支、这一段的实际分工
    try:
        detail = audit_runs(raw, have=have)
    except Exception as e:  # noqa: BLE001 - 段级解析失败不该拖垮整份体检
        detail = {"roles": {}, "paragraphs": [],
                  "suspects": [], "runs_error": str(e)[:120]}

    # 脏族名按「族名本身」判，不按它有没有真的被渲染到：
    # `w:cs="Calibri (西文正文)"` 这类脏数据在简体文档里一个字符都用不上，
    # 但它是模板该被清理的证据，仍然要提示。
    suspects: Dict[str, str] = {}
    for f in fonts:
        if _has_note(f["family"]):
            suspects[f["family"]] = "族名里带括号注解，多半是 WPS 写进去的主题槽位显示名"
    for s in detail.get("suspects") or []:
        suspects.setdefault(s.get("family", ""), s.get("reason", ""))

    return {
        "ok": True,
        "checked": len(fonts),
        "missing": missing,
        "severe": severe,
        "fonts": fonts,
        "roles": detail.get("roles") or {},
        "paragraphs": detail.get("paragraphs") or [],
        "suspects": [{"family": k, "reason": v} for k, v in suspects.items() if k],
        "runs_error": detail.get("runs_error", ""),
    }
