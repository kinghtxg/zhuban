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

## 为什么直接读 zip

不依赖 docmodel 的结构解析：那份解析是给在线结构编辑用的，遇到奇怪结构会抛异常
（并明确告诉用户"结构不可用但生成没问题"）。字体体检比它简单得多，也不该因为
一个奇怪的表格结构就整份体检失败——它要回答的问题只有「用了哪些族名」。
"""
from __future__ import annotations

import io
import zipfile
from typing import Dict, Iterable, List, Optional, Tuple

from lxml import etree

from . import cjk_fonts

NS_W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
NS_A = "http://schemas.openxmlformats.org/drawingml/2006/main"

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


def _is_slot(value: str) -> bool:
    """主题槽位引用（"+mn-ea"、"majorEastAsia"）不是族名。"""
    v = (value or "").strip()
    if not v:
        return True
    if v.startswith("+"):
        return True
    return v in ("majorEastAsia", "minorEastAsia", "majorHAnsi", "minorHAnsi",
                 "majorAscii", "minorAscii", "majorBidi", "minorBidi")


def _families_in_rfonts(root) -> Iterable[str]:
    """从一个 XML 根里取所有 w:rFonts 的显式族名（四个属性都算）。"""
    for el in root.iter("{%s}rFonts" % NS_W):
        for attr in ("ascii", "hAnsi", "eastAsia", "cs"):
            v = el.get("{%s}%s" % (NS_W, attr))
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
        for el in root.iter("{%s}%s" % (NS_A, tag)):
            v = el.get("typeface")
            if v and not _is_slot(v):
                yield v.strip()
    for el in root.iter("{%s}font" % NS_A):
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

    没能命中这两条的非拉丁字体（比如某个冷门厂商的英文族名）会被当成普通
    拉丁字体放过——宁可漏报，也不要让每份模板都堆一屏看不懂的告警。
    """
    if any("\u4e00" <= ch <= "\u9fff" for ch in family):
        return True
    # 族名是大小写不敏感匹配的（Word 里写 simsun 与 SimSun 是同一件事），
    # 查表也要忽略大小写，否则 `simsun` 会被当成普通拉丁字体放过。
    low = family.lower()
    known = {name.lower() for name, _ in cjk_fonts.ALIASES}
    return low in known


def judge(family: str, have: Optional[set] = None) -> Tuple[str, str]:
    """返回 (status, resolved)。status ∈ exact / fallback / missing / latin。"""
    installed = have if have is not None else cjk_fonts.installed_families()
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


def audit_docx(raw: bytes) -> Dict[str, object]:
    """体检一份 docx。任何解析失败都降级成 ok=False + 说明，不抛异常。

    体检失败不该让模板上传失败——那是两件事。上传继续，只是这次没给出提醒。
    """
    used: Dict[str, List[str]] = {}
    try:
        zf = zipfile.ZipFile(io.BytesIO(raw))
    except Exception as e:  # noqa: BLE001
        return {"ok": False, "error": "不是有效的 docx（%s）" % str(e)[:120],
                "fonts": [], "missing": [], "checked": 0, "severe": []}

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
                "fonts": [], "missing": [], "checked": 0, "severe": []}

    have = cjk_fonts.installed_families()
    fonts: List[Dict[str, object]] = []
    for fam in sorted(used):
        status, resolved = judge(fam, have)
        fonts.append({
            "family": fam,
            "status": status,
            "resolved": resolved,
            "parts": used[fam],
        })

    missing = [f["family"] for f in fonts if f["status"] in ("fallback", "missing")]
    severe = [f["family"] for f in fonts if f["status"] == "missing"]
    return {
        "ok": True,
        "checked": len(fonts),
        "missing": missing,
        "severe": severe,
        "fonts": fonts,
    }

