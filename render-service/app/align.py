# -*- coding: utf-8 -*-
"""自由文本 → 模板槽位的语义对齐（align）。

## 这个模块要解决的问题

用户不想按字段一条条填。他想像写文档一样，把整篇内容（含标题、正文、图片）
一次写（或粘贴）进来，由系统判断「这一段该落在模板的哪个位置」。

**关键约束：对齐只决定"哪个值填进哪个变量"，绝不碰模板格式。**
所以这条路是安全的——拼错只是填错位置，不会让版式变形。格式仍然由
docapply 的字符级回填保证（pPr 逐字节不变）。

## 两边都是文档顺序，所以用「单调序列对齐」

用户写的段落有先后，模板的槽位也有先后。任意乱序匹配会把「漏洞描述」
填进「测试结论」——分数可能还挺高。所以约束匹配是单调的（i 和 j 同时
只能前进），允许跳过（用户多写的段落 / 模板没填的槽位）。

这等价于 Needleman-Wunsch 的加权版，用 DP 一次求全局最优，
比"逐段贪心找最高分"稳：贪心会让前面一个高分段抢走后面唯一的槽。

## 信号从哪来（按可信度排序）

1. **变量名与标签文字**（`{{ 漏洞.名称 }}` 的「名称」）。中文变量名本身就是
   语义标签，这是最强的信号。
2. **表头文字**。表格循环的列槽位，同列表头就是它的名字：
   「序号 / 系统名称 / 地址 / 备注」。这是用户说的"区域名称"。
3. **字体指纹**。模板里封面标题是黑体 36pt、正文是仿宋 16pt、表头是宋体——
   用户从 Word 粘过来的内容也带着字体，两边字体一致说明"同一类内容"。
   这条是弱信号，只做加分/减分，不单独定生死。
4. **位置先验**。单调性本身就在做这件事。
"""

from __future__ import annotations

import difflib
import re
from typing import Any, Dict, Iterable, List, Optional, Tuple

try:  # lxml 是既有依赖（docx 解析都靠它），这里只借用它的 HTML 解析
    from lxml import html as lxml_html
except Exception:  # pragma: no cover - 兜底，环境异常时退化成纯文本
    lxml_html = None

# HTML 里算"一个段落"的标签。td/th 单列是因为表格单元格里的内容也是段落。
_BLOCK_TAGS = {
    "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6",
    "blockquote", "section", "article", "td", "th", "pre",
}
_HEADING_TAGS = {"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}
# 行内格式标签 → 格式位
_INLINE_BOLD = {"b", "strong"}
_INLINE_ITALIC = {"i", "em"}
_INLINE_UNDERLINE = {"u", "ins"}
_INLINE_STRIKE = {"s", "strike", "del"}

_PUNCT = set(" \t\r\n　、。，．；：？！“”‘’（）《》【】〈〉「」『』·—…-,.;:?!()[]{}<>/\\|*#`~_+=\"'")

# 段落序号前缀：「1.」「1.1.」「（一）」「第一章」「一、」——用来判断"这行像标题"。
# 「一、二、三、」这种中文数字加顿号是国内公文最常见的编号，原来只认「（一）」
# 和阿拉伯数字，整类模板的小标题都识别不出来（正文全被当成一个巨型段落）。
_SEQ_RE = re.compile(
    r"^\s*(第[一二三四五六七八九十百]+[章节条]"
    r"|[0-9]+(\.[0-9]+)*[.、．]"
    r"|[（(][一二三四五六七八九十]+[）)]"
    r"|[一二三四五六七八九十]+[、．])"
    r"\s*")

# 「标签：值」写在同一行。中文报告的信息区几乎都是这个形态
# （`项目名称：某医院`），从 Word 或表格里复制出来都长这样。
# 标签限 12 字以内：再长就该是正文里的一句带冒号的话，不是字段名。
_INLINE_LABEL_RE = re.compile(r"^\s*([^：:\n]{1,12}?)\s*[：:]\s*(\S.*)$", re.S)
# URL 的协议前缀长得跟「标签:值」一模一样（`https://…`），必须排除，
# 否则每个地址都会被拆成「https」+「//…」两段。
_URL_SCHEMES = {"http", "https", "ftp", "ftps", "sftp", "mailto", "tel", "file",
                "ws", "wss", "ssh", "git", "smb", "ldap", "jdbc", "redis"}

# 对齐阈值：低于这个分数就不认。宁可少填，不要填错位置——
# 填错了用户要一段段改回来，比空着更费劲。
MIN_SCORE = 0.42
# 第二遍（放宽顺序约束）的阈值要更高：既然已经不要求顺序一致，
# 就只能靠分数本身立住，否则「填错位置」的概率会上去。
FREE_PASS = 0.62
# 但「名字一字不差」是另一码事：用户写的「漏洞描述」和模板里的
# 「描述段.段落」（段前行名就叫「漏洞描述」）对上了，这时候位置顺序无关紧要，
# 0.55 左右的分就该认。用 0.5 当底，是因为低于 0.5 基本只剩字体这类弱信号，
# 那就不该越过顺序约束硬填。
FREE_NAME_PASS = 0.5
# 一个槽位最多吃 3 段：模板里「漏洞描述」是一个段落占位符，但用户写了两三段
# 是很常见的。再多就说明匹配错了，不是内容长。
MAX_MERGE = 3


# ---------------------------------------------------------------- 文本归一

def _norm(s: str) -> str:
    """归一化：去空白标点、全角转半角意义上的统一、小写。

    只用于比较，不改任何输出内容。
    """
    if not s:
        return ""
    out = []
    for ch in s:
        if ch in _PUNCT:
            continue
        code = ord(ch)
        if 0xFF01 <= code <= 0xFF5E:      # 全角 ASCII → 半角
            ch = chr(code - 0xFEE0)
        out.append(ch.lower())
    return "".join(out)


def _text_score(text: str, target: str) -> float:
    """text（用户这一段）里有多少 target（变量名/标签）。

    先用包含判断（最信得过），再用 difflib 兜住「漏字/多字"。
    """
    t, g = _norm(text), _norm(target)
    if not t or not g:
        return 0.0
    if g == t:
        return 1.0
    if g in t:
        # 标签越短越可能是巧合（「名称」出现在任何一句里都不奇怪），按长度打折
        return 0.92 if len(g) >= 3 else 0.78
    head = t[: len(g) + 2]
    if head.startswith(g) or g.startswith(head):
        return 0.7
    r = difflib.SequenceMatcher(None, t[: max(len(g) * 2, 8)], g).ratio()
    return max(0.0, r - 0.15)


def _name_cores(name: str) -> List[str]:
    """变量名可以比的几种写法。

    模板作者写 `{{ 描述段.段落 }}`、`{{ 漏洞.影响范围 }}` 这种名字，
    里面的 `段`/`段落`/`描述` 是**约定后缀**，不是语义。用户嘴上说的是
    「漏洞详情」「修复建议」。所以既要拿全名比，也要拿剥掉后缀的核心比——
    只比全名的话，「漏洞详情」和「详情段.段落」一个字都对不上。
    """
    if not name:
        return []
    # 先按点号拆开：`详情段.段落` 的两半各自都是有意义的名字候选。
    # （早先只把"点号后的那半"当候选，于是 `描述段.段落` 只产出"段落"，
    #   而真正有语义的是"描述段"——差一个字，整套匹配就全落空。）
    parts = [p for p in re.split(r"[.．]", name) if p]
    cores: List[str] = [name] + parts
    for c in list(cores):
        for suf in ("段落", "段", "内容", "正文", "文本", "文字", "描述", "说明"):
            if len(c) > len(suf) + 1 and c.endswith(suf):
                cores.append(c[: -len(suf)])
                break
    seen, uniq = set(), []
    for c in cores:
        if c and c not in seen:
            seen.add(c)
            uniq.append(c)
    return uniq


_TOKEN_RE = re.compile(r"\{\{.*?\}\}|\{%.*?%\}|\{#.*?#\}", re.S)


def _strip_tokens(text: str) -> str:
    """把残留的 `{{ x }}` / `{% %}` 从标签里剔掉。

    真踩到：`{{ 漏洞.名称 }}（{{ 漏洞.等级 }}）` 这种一行两个占位符，
    "token 前那截"取到的是空串，回退取"token 后那截"就拿到了 `（{{ 漏洞`——
    一个半截的模板标记被当成标签去比，跟谁都能"对上一半"。
    """
    return _TOKEN_RE.sub(" ", text or "").strip()


def _clean_label(text: str) -> str:
    """标签 / 章节名一律不许带模板标记。

    `_strip_tokens` 只认得**成对**的 `{{ … }}`；一行里挤着两个占位符时，
    切出来的那半截（`{{ 漏洞`、`名称 }}`）不匹配正则，会原样留下来。于是
    「段前标签」成了 `名称 }}`、「所在章节」成了 `{{ 漏洞.名称 }}（{{ 漏洞.等级 }}）`，
    界面上直接显示成一串花括号，识别分数也被这堆噪声带偏。

    判据很简单：花括号在中文正文里没有语义，标签里只要还剩就整段丢掉——
    宁可这个槽位没有标签（还有名字/表头/字体可用），也不要用一串假文字去比。
    """
    s = _strip_tokens(text or "")
    if "{" in s or "}" in s:
        return ""
    s = re.sub(r"\s+", " ", s).strip(" 　()（）【】[]：:，,、-—")
    return s[:16]


def _is_seq(text: str) -> bool:
    return bool(_SEQ_RE.match(text or ""))


def _split_inline_label(text: str) -> Optional[Tuple[str, str]]:
    """一段文字里「名字 + 内容」写在一起的拆法。拆不出来返回 None。

    两种真实形态都要认：

        `项目名称：某市第一医院挂号系统`   ← 信息区一行一个字段，最常见
        `项目名称\\n某市第一医院挂号系统`   ← 纯文本粘贴（没有样式时只剩这个结构）

    为什么必须在解析阶段拆：不拆的话整段就是一个"没有标题的正文"，分组时
    被并进上一节，用户写的六个字段最后只认出一个。而这两种形态连字体都用不上
    （纯文本压根没有字体信息），唯一的线索就是冒号与换行。

    拆错的代价要控制住：标签限 12 字、且不能带句末标点。`注意：本次测试只覆盖
    生产环境` 这种会被拆成 标签「注意」+ 值 —— 值仍然完整保留，最坏是多一个
    匹配不上任何字段的组，不会把内容弄丢。
    """
    s = (text or "").strip()
    if not s:
        return None
    m = _INLINE_LABEL_RE.match(s)
    if m:
        label, value = m.group(1).strip(), m.group(2).strip()
        # `https://example.com/a?id=1` 在字面上就是「标签:值」——标签是 https、
        # 值是 //example.com/…。不挡掉的话每个 URL 都会被拆成两段，
        # 而 URL 恰恰是最常见的字段内容（漏洞地址、回调地址、仓库地址）。
        if value.startswith("//") or label.lower() in _URL_SCHEMES:
            return None
        if label and value and not _SENT_END_RE.search(label):
            return label, value
    lines = [ln.strip() for ln in s.splitlines() if ln.strip()]
    if len(lines) >= 2:
        label, value = lines[0], "\n".join(lines[1:])
        if len(label) <= 16 and not _SENT_END_RE.search(label) and "。" not in label:
            return label, value
    return None


def _inline_fields(text: str) -> Dict[str, str]:
    il = _split_inline_label(text)
    return {"inline_label": il[0], "inline_value": il[1]} if il else {}


def _is_row_label_cell(el) -> bool:
    """这个元素是不是表格某行的**第一格**。

    表格行「项目名称 | 某医院」解析出来是两段独立的文字，第一格天然就是
    这一行的名字。判据只认 `tr` 下的第一个 `td/th`：合并单元格、嵌套表格
    这些边界情况一律当普通单元格处理——宁可少认一个行名，也不要把内容当标题吞掉。
    """
    tag = (el.tag or "").lower() if isinstance(el.tag, str) else ""
    if tag not in ("td", "th"):
        return False
    parent = el.getparent()
    if parent is None or (parent.tag or "").lower() != "tr":
        return False
    try:
        return len(parent) > 1 and parent.index(el) == 0
    except ValueError:  # pragma: no cover - lxml 里不该发生
        return False


# 句末标点：带这些的段落是在"说话"，不是在"起名"。
_SENT_END_RE = re.compile(r"[。；！？;!?]$")


def _looks_like_heading(p: Dict[str, Any], body_size: float, body_font: str = "") -> bool:
    """这段像不像标题/小标题。

    四个依据：语义标签（h1~h6）、序号前缀、"整段加粗且字号不小于正文"，
    以及**中文字体差**。

    最后那条是必须的。中文报告最普遍的分层方式不是加粗也不是放大，而是
    「小标题用黑体、正文用仿宋/宋体」，字号一模一样。只认字号与加粗，
    用户从 Word 粘过来的「影响范围 / 漏洞描述 / 修复建议」全部会被当成正文，
    整篇塌成一个巨型段落——随后一个字段都对不上，界面上只剩"一条都没识别到"。
    这不是理论问题，实测就是这么挂的。

    字体差必须配"短、且不像一句话"才作数：正文里偶尔换个字体强调某几个字
    很常见，把「需要整改」当成小标题会比漏判更糟——它会把后面的正文全部
    吞进自己的值里。
    """
    if p.get("heading_level"):
        return True
    if _is_seq(p.get("text", "")):
        return True
    size = p.get("size_pt") or 0
    if p.get("bold") and size and body_size and size >= body_size * 1.15:
        return True

    font = (p.get("font") or "").strip()
    text = (p.get("text") or "").strip()
    if not font or not body_font or not text:
        return False
    if _same_family(font, body_font):
        return False
    # 全文只有一种字体时 body_font 就是它自己，走不到这里；
    # 能走到这里说明这份内容确实分了两种字体，短行才可能是行名。
    return len(text) <= 16 and not _SENT_END_RE.search(text) and "。" not in text


# ---------------------------------------------------------------- 用户侧解析

def _decls(el) -> Dict[str, str]:
    """取元素内联 style。

    注意**不能把整串小写**：`font-family: 仿宋_GB2312` 小写之后族名就变成
    `仿宋_gb2312`，虽不影响比较（比较前会归一化），但会在界面上显示成
    一个不存在的字体名——这种"看着像个小 bug"的东西最容易被当成真的。
    只把属性名小写。
    """
    raw = el.get("style") or ""
    out: Dict[str, str] = {}
    for piece in raw.split(";"):
        if ":" not in piece:
            continue
        k, v = piece.split(":", 1)
        out[k.strip().lower()] = v.strip()
    return out


def _pt_from_css(v: str) -> Optional[float]:
    """`12pt` / `16px` / `1.5em`(忽略) → 磅值。px 按 96dpi 折算。"""
    if not v:
        return None
    m = re.match(r"^([0-9.]+)\s*(pt|px|cm|mm)?$", v.strip())
    if not m:
        return None
    n = float(m.group(1))
    unit = m.group(2) or "pt"
    if unit == "px":
        return round(n * 0.75, 2)
    if unit == "cm":
        return round(n * 28.35, 2)
    if unit == "mm":
        return round(n * 2.835, 2)
    return round(n, 2)


def _first_family(v: str) -> str:
    """`"仿宋_GB2312", 宋体, serif` → 仿宋_GB2312。"""
    if not v:
        return ""
    for part in v.split(","):
        name = part.strip().strip('"').strip("'").strip()
        if name and name.lower() not in ("serif", "sans-serif", "monospace", "inherit", "initial"):
            return name
    return ""


class _Para:
    """收集一个段落：文字、行内 run、图片、继承来的字体字号。"""

    __slots__ = ("text", "runs", "images", "bold", "italic", "underline",
                 "font", "size_pt", "heading_level", "align")

    def __init__(self):
        self.text = ""
        self.runs: List[Dict[str, Any]] = []
        self.images: List[Dict[str, Any]] = []
        self.bold = False
        self.italic = False
        self.underline = False
        self.font = ""
        self.size_pt: Optional[float] = None
        self.heading_level = 0
        self.align = ""

    def add_run(self, text: str, fmt: Dict[str, Any]) -> None:
        if not text:
            return
        self.text += text
        last = self.runs[-1] if self.runs else None
        same = last is not None and all(last.get(k) == fmt.get(k)
                                       for k in ("bold", "italic", "underline", "font", "size_pt"))
        if same:
            last["text"] += text           # 相邻同格式合并，run 数别虚高
        else:
            run = dict(fmt)
            run["text"] = text
            self.runs.append(run)


def _walk_block(el, para: _Para, fmt: Dict[str, Any], body_size: Optional[float]) -> None:
    """把 el 里的行内内容填进 para；遇到嵌套块级标签就跳过（它自己会被单独收）。"""
    tag = (el.tag or "").lower() if isinstance(el.tag, str) else ""
    f = dict(fmt)

    # 内联 style 对所有标签都生效，不只 span/div——Word 粘出来的
    # <h1 style="font-family:黑体"> 和 <p style="font-size:16pt"> 都很常见。
    d = _decls(el)
    fam = _first_family(d.get("font-family", ""))
    if fam:
        f["font"] = fam
    sz = _pt_from_css(d.get("font-size", ""))
    if sz:
        f["size_pt"] = sz
    if (d.get("font-weight") or "").lower() in ("bold", "600", "700", "800", "900"):
        f["bold"] = True
    if (d.get("font-style") or "").lower() == "italic":
        f["italic"] = True
    if (d.get("text-decoration") or "").lower().find("underline") >= 0:
        f["underline"] = True

    if tag in _INLINE_BOLD:
        f["bold"] = True
    if tag in _INLINE_ITALIC:
        f["italic"] = True
    if tag in _INLINE_UNDERLINE:
        f["underline"] = True
    if tag in _INLINE_STRIKE:
        f["strike"] = True
    if tag == "font" and el.get("face"):
        # 老式 <font face= size=>：Word/网页粘贴的降级形态
        f["font"] = _first_family(el.get("face") or "")
    if tag == "img":
        src = el.get("src") or ""
        if src:
            para.images.append({"src": src, "alt": el.get("alt") or ""})
        return

    if el.text:
        para.add_run(el.text, f)
    for child in el:
        ctag = (child.tag or "").lower() if isinstance(child.tag, str) else ""
        if ctag in _BLOCK_TAGS:
            continue                        # 嵌套块：交给上层遍历单独处理
        _walk_block(child, para, f, body_size)
        if child.tail:
            para.add_run(child.tail, f)


def parse_freeform(content: str, mime: str = "html") -> List[Dict[str, Any]]:
    """把用户写/粘贴的内容解析成段落序列。

    mime="html" 走 DOM 解析（粘贴自 Word 时会带 font-family / font-size，
    这正是我们要的识别信号）；"text" 走按行切段。

    返回的每一项：
        {index, text, runs[], images[], heading_level, font, size_pt, bold,
         italic, underline, align, is_heading}
    """
    if not (content or "").strip():
        return []

    if mime != "html" or lxml_html is None:
        return _parse_plain(content)

    try:
        root = lxml_html.fragment_fromstring(content, create_parent="div")
    except Exception:
        return _parse_plain(content)

    paras: List[_Para] = []
    row_labels: List[bool] = []
    for el in root.iter():
        if el is root:
            # create_parent 造出来的那层 div 是容器不是段落。把它当段落会让
            # 首段变成一个「空文字、只挂着图片」的怪段落，后面所有匹配跟着错位。
            continue
        tag = (el.tag or "").lower() if isinstance(el.tag, str) else ""
        if tag not in _BLOCK_TAGS:
            continue
        p = _Para()
        p.heading_level = _HEADING_TAGS.get(tag, 0)
        d = _decls(el)
        p.align = (d.get("text-align") or "").strip()
        _walk_block(el, p, {}, None)
        if not p.text.strip() and not p.images:
            continue
        paras.append(p)
        row_labels.append(_is_row_label_cell(el))

    # 裸在容器顶层的内联内容：粘贴「一段文字 + 一张图」时，图片常常不在
    # 任何 <p> 里，上面的块遍历收不到它——图片就这么无声无息地丢了。
    # （顺序上它排到最后，图通常在文末，够用。）
    stray = _Para()
    if root.text:
        stray.add_run(root.text, {})
    for child in root:
        ctag = (child.tag or "").lower() if isinstance(child.tag, str) else ""
        if ctag in _BLOCK_TAGS:
            continue
        _walk_block(child, stray, {}, None)
        if child.tail:
            stray.add_run(child.tail, {})
    if stray.text.strip() or stray.images:
        paras.append(stray)
        row_labels.append(False)

    # 正文基准字号：取所有段里的众数，用来判断"这段是不是大字标题"
    sizes = [p.size_pt for p in paras if p.size_pt]
    body_size = max(set(sizes), key=sizes.count) if sizes else 0.0
    # 正文基准字体同理。它是"小标题用黑体、正文用仿宋"这条判据的参照系：
    # 跟它不同的短行才可能是行名。字体众数按字符数加权，避免正文里零星的
    # 几段特殊字体把基准带偏。
    font_chars: Dict[str, int] = {}
    for p in paras:
        fonts = [r.get("font") for r in p.runs if r.get("font")]
        if not fonts:
            continue
        f = max(set(fonts), key=fonts.count)
        font_chars[f] = font_chars.get(f, 0) + max(len(p.text.strip()), 1)
    body_font = max(font_chars, key=font_chars.get) if font_chars else ""

    out: List[Dict[str, Any]] = []
    for i, p in enumerate(paras):
        fonts = [r.get("font") for r in p.runs if r.get("font")]
        p.font = max(set(fonts), key=fonts.count) if fonts else ""
        if not p.size_pt:
            rs = [r.get("size_pt") for r in p.runs if r.get("size_pt")]
            p.size_pt = max(rs) if rs else None
        # 「整段都加粗」才算标题特征：段里只有几个字加粗是强调，不是标题
        p.bold = bool(p.runs) and all(r.get("bold") for r in p.runs if r["text"].strip())
        p.italic = bool(p.runs) and all(r.get("italic") for r in p.runs if r["text"].strip())
        out.append({
            "index": i,
            "text": p.text.strip(),
            "runs": p.runs,
            "images": p.images,
            "heading_level": p.heading_level,
            "font": p.font,
            "size_pt": p.size_pt,
            "bold": p.bold,
            "italic": p.italic,
            "align": p.align,
            # 表格首列是这一行的名字。从 Word 复制一个「项目 | 内容」两列表格
            # 是常态，单元格解析出来是一段段没有样式的文字——没有字体差、没有
            # 序号、也不加粗，整张表会被当成一大段正文，一个字段都认不出来。
            # 结构上的事实（它是这一行的第一格）比任何样式信号都可靠。
            "is_heading": row_labels[i] or _looks_like_heading(
                {"text": p.text, "heading_level": p.heading_level,
                 "size_pt": p.size_pt, "bold": p.bold, "font": p.font},
                body_size, body_font),
            "row_label": row_labels[i],
            **_inline_fields(p.text.strip()),
        })
    return out


def _plain_label_flags(chunks: List[str]) -> List[bool]:
    """纯文本里「哪一行是名字」。

    纯文本没有任何样式可依靠，只剩排列方式。中文报告从记事本里写出来最常见
    的就是一行名字、一行内容交替往下：

        建设单位
        某市卫生健康委员会

    判据只能成对看，不能逐行看——「某市卫生健康委员会」单看也是个短行、
    也没有句末标点，跟名字长得一模一样。所以用扫描式配对：这一行短、不带
    句末标点，且下一行不比它短（或下一行是个完整句子），就认成名字，并
    **跳过下一行**，避免把内容又当一次名字。

    「一、二、」这类序号由 `_is_seq` 另行判定，不参与配对。
    """
    flags = [False] * len(chunks)
    i = 0
    n = len(chunks)
    while i < n:
        cur = chunks[i]
        if _is_seq(cur):
            flags[i] = True
            i += 1
            continue
        if i + 1 < n and _looks_like_plain_label(cur):
            nxt = chunks[i + 1]
            if len(nxt) >= len(cur) or _SENT_END_RE.search(nxt):
                flags[i] = True
                i += 2
                continue
        i += 1
    return flags


def _looks_like_plain_label(s: str) -> bool:
    """短、不带句末标点、不像一整句话——纯文本里才可能是名字。"""
    s = (s or "").strip()
    if not s or len(s) > 12:
        return False
    if _SENT_END_RE.search(s):
        return False
    # 带逗号顿号的是在叙述，不是在起名
    return not re.search(r"[，、,]", s)


def _parse_plain(content: str) -> List[Dict[str, Any]]:
    """纯文本兜底：空行分段，单行不换行也算一段。"""
    chunks = [c.strip() for c in re.split(r"\n\s*\n", content) if c.strip()]
    if len(chunks) == 1 and "\n" in chunks[0]:
        # 没有空行但有多行：按行切（用户常从记事本粘一堆行）
        chunks = [c.strip() for c in content.splitlines() if c.strip()]
    labels = _plain_label_flags(chunks)
    out = []
    for i, c in enumerate(chunks):
        t = c.strip()
        inline = _split_inline_label(t)
        out.append({
            "index": i, "text": t, "runs": [{"text": t}], "images": [],
            "heading_level": 0, "font": "", "size_pt": None, "bold": False,
            "italic": False, "align": "", "is_heading": labels[i] or _is_seq(t),
            **({"inline_label": inline[0], "inline_value": inline[1]} if inline else {}),
        })
    return out


# ---------------------------------------------------------------- 模板侧目录

def _label_for_token(block: Dict[str, Any], tk: Dict[str, Any]) -> str:
    """这个占位符**紧邻的标签文字**是什么。

    为什么不能直接用「整段静态文字」：`{{ 系统数量 }}` 那种行内占位符，
    整段静态文字是一整句「渗透测试结果如下，本次渗透测试共…个系统」——
    拿它当标签去比，比什么都像、也什么都不像。

    真正有用的是紧挨着它的那一截：token 前面截到最近的标点为止，
    前面没有就看后面。这才是用户嘴上说的「段落名称」。
    """
    text = block.get("text") or ""
    try:
        s, e = int(tk.get("start", 0)), int(tk.get("end", 0))
    except (TypeError, ValueError):
        return ""
    before = text[:s]
    after = text[e:]
    seg = re.split(r"[，。；：、,.;:!?\n]", before)[-1]
    seg = _clean_label(seg)
    if len(seg) < 2:
        seg = _clean_label(re.split(r"[，。；：、,.;:!?\n]", after)[0])
    return seg


def _block_text_without_tokens(block: Dict[str, Any]) -> str:
    """段落的静态文字：把 token（{{ x }} / {% % }）挖掉，剩下的是「标签」。"""
    text = block.get("text") or ""
    for tk in block.get("tokens") or []:
        try:
            s, e = int(tk.get("start", 0)), int(tk.get("end", 0))
        except (TypeError, ValueError):
            continue
        if 0 <= s < e <= len(text):
            text = text[:s] + " " + text[e:]
    return re.sub(r"\s+", " ", text).strip(" 　:：")


def _dominant_font(block: Dict[str, Any]) -> str:
    fonts = [r.get("font") for r in (block.get("runs") or []) if r.get("font")]
    return max(set(fonts), key=fonts.count) if fonts else ""


def _first_size(block: Dict[str, Any]) -> Optional[float]:
    for r in block.get("runs") or []:
        if r.get("size_pt"):
            return float(r["size_pt"])
    return None


def _docx_index(variables: Optional[List[Dict[str, Any]]]) -> Dict[str, Dict[str, Any]]:
    """建立「docx 里怎么写」→「数据对象里的路径与类型」的映射。

    这两套名字不一样，而且是**必须**转的：
      docx 里写 `{{ 漏洞.名称 }}`（循环内的相对引用），
      数据对象里是 `漏洞列表[].名称`（schema 路径）。
    只按 docx 名字返回结果，前端拿到的就是一条填不进去的"死名"。

    规则来自 scanner：循环项带 `loop_var`（如 漏洞列表 → 漏洞），
    循环体内的引用是**相对最内层循环**的，所以内层只拼内层的 loop_var。
    """
    out: Dict[str, Dict[str, Any]] = {}

    def walk(vs: List[Dict[str, Any]], loop_var: Optional[str]) -> None:
        for v in vs:
            name = v.get("name") or ""
            if v.get("kind") == "loop":
                lv = v.get("loop_var") or name
                # 两个名字都要能索引：模板里可能直接用循环名引用（`{{ 系统清单 }}`
                # 那种整体替换），也可能用循环变量。少索引一个，那个变量就会被
                # 当成普通标量混进对齐，白占一个槽位。
                out[lv] = {"path": v.get("path") or name, "kind": "loop", "name": name}
                out[name] = {"path": v.get("path") or name, "kind": "loop", "name": name}
                walk(v.get("fields") or [], lv)
                continue
            key = "%s.%s" % (loop_var, name) if loop_var else name
            out[key] = {"path": v.get("path") or key, "kind": v.get("kind") or "scalar",
                        "name": name}

    walk(variables or [], None)
    return out


# 表达式里能当变量用的标识符（允许中文与点号路径）。
_EXPR_IDENT_RE = re.compile(
    r"[A-Za-z_\u4e00-\u9fff][0-9A-Za-z_\u4e00-\u9fff]*"
    r"(?:\.[A-Za-z_\u4e00-\u9fff][0-9A-Za-z_\u4e00-\u9fff]*)*")
_STRING_LIT = re.compile(r'"[^"]*"|\'[^\']*\'')
_EXPR_KEYWORDS = {"and", "or", "not", "in", "is", "if", "else", "none", "true",
                  "false", "length", "default", "loop"}


def _resolve_token(expr: str, dx: Dict[str, Dict[str, Any]]) -> Optional[str]:
    """把 `{{ ... }}` 里的表达式解析成**一个真实字段名**（docx 侧的写法）。

    占位符里写的常常不是裸名字：

        {{ 副标题 or "—— 能力演示" }}        真正的字段是「副标题」
        {{ "已完成" if it.完成 else "进行中" }} 真正的字段是「it.完成」
        {{ it.备注 or "—" }}                 真正的字段是「it.备注」

    早先直接拿整段表达式当字段名，后果是一整条假字段链：界面出现
    「编制人 or "未指派"」这种用户填不进去的字段、对齐时永远匹配不上、
    前端按路径写库也找不到。内置演示模板里就有五处这种写法。

    解析不出来（`loop.index`、纯字面量、算术式）就返回 None——那不是可填字段，
    应当整个跳过，而不是造一个假槽位。
    """
    expr = (expr or "").strip()
    if not expr:
        return None
    # 裸名字（绝大多数情况）原样返回：即使 scan 没把它报上来（variables 传空、
    # 或扫描器跳过了某个位置），也不该把槽位丢掉。
    # 但 `loop.index` 这种**伪名字**要排除：它字面上也是个合法标识符，
    # 却根本不是数据字段，认下来会在界面上造一个永远填不进去的槽位。
    if (_EXPR_IDENT_RE.fullmatch(expr)
            and expr.split(".")[0].lower() not in _EXPR_KEYWORDS):
        return expr
    masked = _STRING_LIT.sub(lambda m: " " * len(m.group(0)), expr)
    idents = [c for c in _EXPR_IDENT_RE.findall(masked)
              if c.lower() not in _EXPR_KEYWORDS
              and c.split(".")[0].lower() not in _EXPR_KEYWORDS]
    for cand in idents:
        if cand in dx:
            return cand
    # 表达式里只有一个名字（`{{ 编制人 or "" }}` 的 编制人）就直接用它；
    # 有两个以上又都不在 dx 里，说明无法判断主语，宁可跳过。
    if len(idents) == 1:
        return idents[0]
    # 循环里的 `漏洞.名称` 在 dx 里索引成 `漏洞.名称`，但 tidy 之后可能是
    # `it.名称` 之类，再兜一次：取带点号且 dx 里后缀能对上的。
    for cand in idents:
        if "." in cand and cand.split(".", 1)[1] in dx:
            return cand
    return None


def build_slots(model: Dict[str, Any],
                variables: Optional[List[Dict[str, Any]]] = None) -> List[Dict[str, Any]]:
    """从文档模型（docmodel.parse_docx 的结果）建"槽位目录"。

    一个变量可能在多处出现，这里**每个变量只留第一处**：
    对齐是"值 → 变量"的关系，同一变量填两次没有意义，
    留多处反而会让 DP 把两段内容分给同一个变量。

    variables 是 scan 出的变量清单，用来带上 kind（image/richtext/loop/chart），
    没有也能跑，只是 kind 一律当 scalar。
    """
    dx = _docx_index(variables)
    sys_resolved = {v.get("name") for v in (variables or []) if v.get("system_resolved")}

    slots: List[Dict[str, Any]] = []
    seen: Dict[str, int] = {}

    # 最近一行"短而独立"的文字，用来给下面那个独占一行的占位符当标签。
    # 渗透报告模板的实际写法是：
    #     影响范围                ← 单独一行，是这一行的名字
    #     {{ 漏洞.影响范围 }}      ← 下一段，自己一个字都没有
    # 只从占位符所在段落里取标签，这里永远取到空——而用户嘴上说的
    # 「段落名称」指的恰恰是上一行那四个字。
    last_title = ""

    def want(name: str, is_image: bool, kind: Optional[str]) -> bool:
        """哪些变量不参与"内容对齐"。

        - 签章/印章：由系统注入，用户写不出来，也不该写。
        - 图表：数据在 chartN.xml，不是文字。
        - 二维码：由文本派生。
        图片变量参与（用户写的图片要能落到 image_ 字段）。
        """
        if name in sys_resolved:
            return False
        # 循环本身不参与：一个段落填不满一个数组，硬匹配只会让人以为
        # "这条漏洞已经填好了"，实际项目列表是空的。
        if kind in ("chart", "qrcode", "loop"):
            return False
        if name.startswith(("sig_", "seal_")):
            return False
        return True

    def visit(block: Dict[str, Any], part: str, section: str,
              table: Optional[Dict[str, Any]] = None, col: Optional[int] = None) -> None:
        btype = block.get("type") or "paragraph"
        if btype == "list":
            # 列表项里的占位符必须能进槽位。模板里「一、二、三」这种编号段落
            # 装的是正文，用户照样要在里面填内容；不下探 items 的话，这些
            # 字段会凭空消失——扫描器说有这么个变量，对齐却永远填不到。
            for it in block.get("items") or []:
                visit(it, part, section, table=table, col=col)
            return
        if btype == "table":
            rows = block.get("rows") or []
            header: List[str] = []
            if rows:
                for cell in (rows[0].get("cells") or []):
                    # 表头那一格常常**本身就是占位符**（`{{ 报告编号 }}`），
                    # 直接取文字的话列名会变成一整段花括号表达式，界面上
                    # 三四个字段都显示成 `{{ 报告编号 }}`，用户根本认不出哪个是哪个。
                    # 把 token 挖掉：挖完是空就说明这格没有列名，宁可留空。
                    txt = " ".join(_block_text_without_tokens(b)
                                   for b in (cell.get("blocks") or [])).strip()
                    header.append(txt)
            for row in rows:
                for ci, cell in enumerate(row.get("cells") or []):
                    for cb in cell.get("blocks") or []:
                        visit(cb, "表格", section, table=block, col=ci)
            # 表头文字挂到本表所有槽位上（后面按列号取）
            if header:
                for s in slots:
                    if s.get("table_id") == block.get("id") and s.get("col") is not None:
                        if 0 <= s["col"] < len(header):
                            s["column_label"] = header[s["col"]]
            return

        for tk in block.get("tokens") or []:
            if tk.get("kind") != "var":
                continue
            raw = (tk.get("text") or "").strip()
            m = re.match(r"^\{\{\s*([\s\S]+?)\s*\}\}$", raw)
            if not m:
                continue
            # 占位符里可能是表达式（`{{ 编制人 or "未指派" }}`），要落到真正的
            # 字段名上；解析不出真实字段就整个跳过，别造一个用户填不进去的假字段。
            name = _resolve_token(m.group(1), dx)
            if not name:
                continue
            meta = dx.get(name) or {}
            kind = meta.get("kind") or "scalar"
            path = meta.get("path") or name
            if not want(name, (kind == "image"), kind):
                continue
            fmt = block.get("fmt") or {}
            slot = {
                "name": name,
                "path": path,
                "kind": kind or "scalar",
                "block_id": block.get("id"),
                "block_type": btype,
                "part": part,
                "section": section,
                "label": (_clean_label(_label_for_token(block, tk)
                                       or _block_text_without_tokens(block))
                          # 标题段不再借用上一行的文字当标签。标题本身是章节名，
                          # 它前面那一行十有八九是「表 1-3 漏洞结果列表」这类图表
                          # 题注——拿它当字段标签，等于给「漏洞.名称」贴了个假名字，
                          # 之后所有靠名字的匹配都被这个假名字带偏。
                          # 标签留空反而更准：让字段名自己去比。
                          or ("" if (table is not None or btype == "heading"
                                     # 上一行就是章节标题时也不借：那不是字段名。
                                     # 借了的话「经营概况/原因分析/措施建议」三个字段
                                     # 在界面上会一起显示成「月度经营分析报告」，
                                     # 用户看不出这一条到底填到哪儿去了。
                                     or _clean_label(last_title) == _clean_label(section))
                              else last_title)),
                "column_label": "",
                "font": _dominant_font(block),
                "size_pt": _first_size(block),
                "bold": bool((block.get("runs") or [{}])[0].get("bold")),
                "align": fmt.get("align") or "",
                "line_height": fmt.get("line_height"),
                "indent_first_mm": fmt.get("indent_first_mm"),
                "table_id": (table or {}).get("id"),
                "col": col,
                "is_heading_conf": btype == "heading",
            }
            if name in seen:
                # 同一个变量可能出现在多处（表里一处、章节标题一处）。
                # 原来的实现「留第一处」，而这份模板里 `漏洞.名称` 第一次出现在
                # 表格单元格里——代表项就成了一个既没标签、又没有标题性的格子，
                # 用户写的「漏洞名称」小标题反而匹配不上。改成留**上下文最全**
                # 的那一处：标题段 > 有列名 > 有标签 > 有字号。
                idx = seen[name]
                if _context_score(slot) > _context_score(slots[idx]):
                    slots[idx] = slot
                continue
            seen[name] = len(slots)
            slots.append(slot)

    # 章节名要按文档顺序推进：遍历是顺序的，「当前章节」就是最近一个标题。
    # 一开始我把它当参数在 visit 里更新，结果循环每次都用初始值传进去，
    # 所有槽位的 section 恒为空——这种"看着像传参、其实是闭包快照"的错
    # 不报错，只会让一个信号悄悄失效。
    section = ""
    for blk in model.get("blocks") or []:
        btype = blk.get("type") or "paragraph"
        if btype == "heading":
            # 标题里可能整行都是占位符（`{{ 漏洞.名称 }}（{{ 漏洞.等级 }}）`
            # 这种在渗透报告模板里就是一级标题样式）。清洗后为空说明它不是
            # 章节名、是数据行，就沿用上一节，别把花括号塞进 section。
            cand = _clean_label(blk.get("text") or "")
            if cand:
                section = cand
        visit(blk, "正文", section)
        # 记下"短而独立"的一行，给下一个独占一行的占位符当标签。
        # 单行编号段落也算：模板里「影响范围」这类行名就是用编号样式排的。
        #
        # 规则要分开关/闭两种，不能一律清零：
        #   遇到 `{%p endfor %}` / `{% endif %}` → 这一层结束了，上面那个行名
        #     不能再传给下面的占位符。不清零的话「图.image_截图」会认领
        #     「修复建议」这个名字——图上不该有名字，硬贴一个只会让匹配跑偏。
        #   遇到 `{%p for X in Y %}` → 结构刚打开，紧挨在它上面的行名**正是**
        #     这一段的名字（「修复建议」→ 建议列表第一段）。一律清零会把这里
        #     唯一可用的信号丢掉，三个段落字段全变成无标签。
        for tk in blk.get("tokens") or []:
            if (tk.get("kind") or "") == "var":
                continue
            low = (tk.get("text") or "").strip().lower()
            if "endfor" in low or "endif" in low or low.startswith("{% else"):
                last_title = ""
                break
        title = _clean_label(blk.get("text") or "")
        if not title and btype == "list":
            items = blk.get("items") or []
            if len(items) == 1:
                title = _clean_label(items[0].get("text") or "")
        # 标题是"章节名"，已经有 section 兜着了；再当行名用就会把
        # 「渗透测试过程」贴到「漏洞名称」头上，反而干扰打分。
        if btype != "heading" and title and not (blk.get("tokens") or []) and len(title) <= 16:
            last_title = title
    _assign_fingerprint_weights(slots)
    return slots


def _context_score(s: Dict[str, Any]) -> float:
    """一处出现作为"代表项"的合适程度（只比上下文，不比内容）。"""
    return (
        (2.0 if s.get("is_heading_conf") else 0.0)
        + (1.0 if s.get("column_label") else 0.0)
        + (1.0 if s.get("label") else 0.0)
        + (0.5 if s.get("size_pt") else 0.0)
    )


def _assign_fingerprint_weights(slots: List[Dict[str, Any]]) -> None:
    """给每个槽位算「字体 + 字号」的独特性权重（0~1）。

    为什么需要权重：正文里十几个槽位都是「仿宋 16pt」，这个组合本身说明不了
    任何事；而封面标题「黑体 36pt」全篇只出现一次——用户粘进来的 36pt 黑体
    段落几乎一定就是它。同样是"字体对上了"，独特性越高越值钱。
    这就是用户说的"根据中文什么字体英文什么字体，直接识别到对应的位置"。
    """
    bucket: Dict[Any, int] = {}
    for s in slots:
        key = (_norm(s.get("font") or ""), s.get("size_pt"))
        bucket[key] = bucket.get(key, 0) + 1
    for s in slots:
        key = (_norm(s.get("font") or ""), s.get("size_pt"))
        n = bucket.get(key, 1)
        if not (s.get("font") or s.get("size_pt")):
            s["_fp"] = 0.0
        else:
            s["_fp"] = round(1.0 / (n ** 0.5), 3)


# ---------------------------------------------------------------- 打分与对齐

def _same_family(a: str, b: str) -> bool:
    """两个族名是不是同一个字体。

    去掉 `_GB2312` 这种版本后缀再比：用户粘贴过来的常常是「仿宋」，
    模板里写的是「仿宋_GB2312」，其实是同一支字体的两个叫法。
    只做前缀包含判断，不做编辑距离——`宋体` 和 `仿宋` 编辑距离很近，
    但它们是两种完全不同的字体，模糊匹配会直接判错。
    """
    na, nb = _norm(a).replace("_gb2312", ""), _norm(b).replace("_gb2312", "")
    if not na or not nb:
        return False
    return na == nb or na.startswith(nb) or nb.startswith(na)


def _pair_score(p: Dict[str, Any], s: Dict[str, Any]) -> Tuple[float, List[str], bool]:
    """一段（一组）vs 一个槽位的匹配分 + 人话理由 + 名字是否对上。

    第三个返回值 `name_hit` 决定的不是分数，而是**取哪个当值**：
    名字对得上说明"这一节就是这个字段"，值取下面的正文；
    只是靠字体指纹对上的（封面大标题），值取标题自己。

    打分权重是拍的，但排序逻辑是明确的：名字/标签 > 表头 > 结构 > 字体。
    字体只做 0.1 量级的加减，因为它会被"整段刷同一字体"的模板污染。
    """
    why: List[str] = []
    score = 0.0

    head = p.get("text") or ""
    # 变量名有几写法（全名 / 点号后的尾段 / 剥掉「段」「段落」等约定后缀）：
    # 「漏洞详情」要对上 `{{ 详情段.段落 }}`，只比全名一个字都对不上。
    #
    # 但两个字的"核心"不能当主证据。`漏洞.名称` 的核心是「漏洞」和「名称」，
    # 而「漏洞」出现在「漏洞名称」里、「名称」出现在「项目名称」里，两个都
    # 给 0.78——于是「项目名称」那段被认成了「漏洞.名称」，真正的「漏洞名称」
    # 反而被挤到「漏洞.等级」上，整篇错位一格。打分是按全局总分选的，
    # 这种"两项都像"的假匹配总分更高，会稳定赢过正确答案。
    def _core_score(core: str) -> float:
        sc = _text_score(head, core)
        # 只罚"从长名字里拆出来的短片段"。字段本来就叫两个字（`{{ 备注 }}`）
        # 时它自己就是全名，不该被罚——那不是片段，那就是它的名字。
        #
        # 还有一类不能罚：`{{ it.建议 }}` 的核心是 `it`/`建议`，两个都短，
        # 用户的小标题恰恰就写「建议」。罚下去的话 0.55 落进弱相似那一档，
        # 一个名字一字不差、本该最确定的字段反而谁也认不出来。
        # 判据用 `sc < 1.0`：1.0 意味着**整段就叫这个名字**（`t == g`），
        # 而「项目名称」里含有「名称」只是 0.78，那才是要罚的巧合。
        if len(_norm(core)) < 3 and _norm(core) != _norm(full_name) and sc < 1.0:
            sc *= 0.55
        return sc

    full_name = s.get("name") or ""
    name_s = max([_core_score(c) for c in _name_cores(full_name)] + [0.0])
    label_s = _text_score(head, s.get("label") or "")
    col_s = _text_score(head, s.get("column_label") or "")

    best = max(name_s, label_s, col_s)
    if best >= 0.6:
        score += 0.55 * best
        if label_s >= best and label_s > 0.3:
            why.append("段前标签「%s」对得上" % (s.get("label") or ""))
        elif col_s >= best and col_s > 0.3:
            why.append("与表格列名「%s」一致" % (s.get("column_label") or ""))
        else:
            why.append("文字与字段名「%s」对得上" % (s.get("name") or ""))
    else:
        score += 0.25 * best          # 弱相似也给一点，靠其它信号拉起来

    # 区域名（所在章节标题）也参与：用户写小标题时常常照抄章节名
    sec_s = _text_score(head, s.get("section") or "")
    if sec_s > 0.5:
        score += 0.08 * sec_s
        why.append("落在「%s」这一节" % (s.get("section") or ""))

    # 标题性只在「两边都像标题」时加分，**不一致不扣分**。
    # 用户拿「漏洞详情」当小标题、模板那边却是一个普通正文段落槽位——
    # 这是最常见的正常情况，原来的 -0.12 会把这类正确匹配直接压到阈值以下。
    if p.get("is_heading") and (s.get("is_heading_conf")
                                or (s.get("bold") and (s.get("size_pt") or 0) >= 14)):
        score += 0.08

    # 字体指纹。这不是"顺带一提"的弱信号——用户从 Word 粘过来的内容带着
    # 字体与字号，而模板里每个区域都有自己的字体字号组合。组合越独特，
    # 这一条越能定生死（封面标题黑体 36pt 全篇唯一）。
    pf, sf = (p.get("font") or ""), (s.get("font") or "")
    ps, ss = p.get("size_pt"), s.get("size_pt")
    if _same_family(pf, sf):
        fp = float(s.get("_fp") or 0.0)
        size_ok = bool(ps and ss and abs(ps - ss) / max(ss, 1) <= 0.1)
        # fp=1 表示这个「字体+字号」组合全篇唯一（封面标题黑体 36pt 就是），
        # 这种指纹自己就够定案；fp 小的（正文一堆仿宋 16pt）只能当佐证。
        score += 0.06 + (0.46 * fp if size_ok else 0.0)
        why.append("字体%s%s与模板这一处一致"
                   % (sf, ("、字号 %gpt " % ss) if size_ok else ""))
    elif ps and ss:
        # 字体对不上但字号差很多：多半不是同一类内容
        if abs(ps - ss) / max(ss, 1) >= 0.6:
            score -= 0.06

    # 图片槽位只接受带图的段落；纯文字段落到图片槽位要重罚
    if s.get("kind") == "image":
        if p.get("images"):
            # 一张图没有任何文字可供比对，所以只能靠"这一段是不是以图为主"来判。
            # 「以纯图开头的组」几乎必然属于图片槽位——哪怕后面还跟了一句说明，
            # 那句话是图注，不是另一个字段的正文。
            if p.get("lead_picture"):
                score += 0.7
            elif p.get("text"):
                score += 0.25
            else:
                score += 0.7
            why.append("这段带图")
        else:
            score -= 0.35
    elif p.get("images") and not head:
        score -= 0.2

    # 表格列槽位 vs 用户写在正文里的行：不是硬性冲突，轻罚
    if s.get("part") == "表格" and not (p.get("align") == "center"):
        score -= 0.03

    return max(0.0, min(1.0, score)), why, name_s >= 0.6 or label_s >= 0.6 or col_s >= 0.6



# ---------------------------------------------------------------- 分组与对齐

def make_groups(paragraphs: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """把平铺的段落按「小标题 + 内容」分组。

    ## 为什么必须分组

    这是这个功能真正的语义模型。用户写的是：

        漏洞详情               ← 段落名称
        在挂号查询接口的 id…    ← 内容
        进一步可以联合查询…     ← 内容（还是同一节的内容）

    我第一版把段落拍平逐个去比槽位，于是「漏洞详情」这个小标题自己去找
    槽位、下面的正文又各自去找槽位——结果是标题占了「详情段.段落」这个槽，
    正文全被丢进"未匹配"。用户看到的还是一堆红字。

    分组之后：**标题决定落在哪一节，内容是那一节的值**。
    这才是人写文档、也是 Word 里"标题 + 正文"的实际结构。

    没有标题的段落（封面、连续正文）各自成组，标题字段为 None。
    """
    groups: List[Dict[str, Any]] = []
    cur: Dict[str, Any] = {"heading": None, "body": [], "paras": [], "lead_picture": False}
    for p in paragraphs:
        # 纯图段落自己成一组，不要被上一节的正文吸走：
        # 「修复建议」后面跟一张图，那张图属于图片字段，不属于建议正文。
        # 这一条踩过——图被当成建议的内容吞掉，图片字段永远是空的。
        picture_only = bool(p.get("images")) and not (p.get("text") or "").strip()
        # 「标签：值」写在一行里：这一行本身就是完整的一节（名字 + 内容），
        # 既不需要上一节的标题，也不该把后面的正文吸进来。让它独立成组。
        inline = bool(p.get("inline_label"))
        if p.get("is_heading") or picture_only or inline:
            if cur["heading"] is not None or cur["body"]:
                groups.append(cur)
            standalone = p.get("is_heading") or inline
            cur = {"heading": p if standalone else None,
                   "body": [] if standalone else [p],
                   "paras": [p["index"]],
                   "lead_picture": bool(picture_only) and not inline}
        else:
            cur["body"].append(p)
            cur["paras"].append(p["index"])
    groups.append(cur)

    out: List[Dict[str, Any]] = []
    for g in groups:
        head = g["heading"]
        body = g["body"]
        body_text = "\n".join(b["text"] for b in body if b.get("text"))
        src = head or (body[0] if body else None)
        images: List[Dict[str, Any]] = []
        for b in body:
            images.extend(b.get("images") or [])
        if head and not images:
            images = list(head.get("images") or [])
        inline_label = (head or {}).get("inline_label")
        out.append({
            "paras": g["paras"],
            "heading": head,
            "body_text": body_text,
            # 有正文就用正文：小标题自己不是值。「漏洞名称」这个标题本身
            # 不该被填进名称字段，它的内容「SQL 注入」才是。
            # 行内「标签：值」是个例外：值就在那一行里，是冒号后面那截。
            "value_text": (((head or {}).get("inline_value") or "") if inline_label
                           else (body_text or ((head or {}).get("text") or ""))),
            # 行内形态拿标签去比，不拿整行「标签：值」——后者会把值里的字
            # 也算进相似度，让两条不相干的字段互相像。
            "text": inline_label or (src or {}).get("text") or "",
            "font": (src or {}).get("font") or "",
            "size_pt": (src or {}).get("size_pt"),
            "bold": bool((src or {}).get("bold")),
            "align": (src or {}).get("align") or "",
            "is_heading": head is not None,
            "images": images,
            # 这一组的开头就是一张纯图（后面可能还跟着一句说明）。
            # 图片槽位靠它判定"这段就是截图"，而不是"正文里顺手插了张图"。
            "lead_picture": bool(g.get("lead_picture")),
        })
    return out


def align(paragraphs: List[Dict[str, Any]], slots: List[Dict[str, Any]]) -> Dict[str, Any]:
    """单调序列对齐（允许跳过的加权最长公共子序列）。

    返回的是**建议**，不是既成事实：前端要让用户看一眼再确认。
    填错位置比空着更费劲——空着用户自己会填，填错了他得先发现。
    """
    groups = make_groups(paragraphs)
    n, m = len(groups), len(slots)
    if not n or not m:
        return {"ok": True, "matches": [], "groups": groups,
                "unmatched_paragraphs": list(range(len(paragraphs))),
                "unmatched_slots": [s["name"] for s in slots], "score": 0.0}

    pair: List[List[Tuple[float, List[str], bool]]] = [[(0.0, [], False) for _ in range(m)] for _ in range(n)]
    for i in range(n):
        for j in range(m):
            pair[i][j] = _pair_score(groups[i], slots[j])

    NEG = -1e9
    dp = [[NEG] * (m + 1) for _ in range(n + 1)]
    back: List[List[Optional[Tuple[int, int, str]]]] = [[None] * (m + 1) for _ in range(n + 1)]
    dp[0][0] = 0.0
    for i in range(n + 1):
        for j in range(m + 1):
            cur = dp[i][j]
            if cur <= NEG:
                continue
            if i < n and cur > dp[i + 1][j]:              # 这一组不用
                dp[i + 1][j] = cur
                back[i + 1][j] = (i, j, "skip_para")
            if j < m and cur > dp[i][j + 1]:              # 这个槽位不填
                dp[i][j + 1] = cur
                back[i][j + 1] = (i, j, "skip_slot")
            if i < n and j < m:
                sc = pair[i][j][0]
                # `- 1e-6 * i`：分数完全相同时（用户把同一个字段写了两遍），
                # 取**先出现**的那一段。没有这个微弱偏好时，两条路径的总分相等，
                # 回溯会按后写入的指针走——用户明明先写了对的内容，结果被后面
                # 那句盖掉。1e-6 远小于任何实际分差，只在并列时起作用。
                if sc >= MIN_SCORE and cur + sc - 1e-6 * i > dp[i + 1][j + 1]:
                    dp[i + 1][j + 1] = cur + sc - 1e-6 * i
                    back[i + 1][j + 1] = (i, j, "one")
            # 多组并进一个槽位：用户可能把一节内容写成几段小标题 + 正文
            acc = 0.0
            for k in range(1, MAX_MERGE + 1):
                if i + k > n or j >= m:
                    break
                acc += pair[i + k - 1][j][0]
                if k == 1:
                    continue
                # 合并不给红利，反而扣一点：把封面两行、章节标题和正文
                # 一起塞进同一个槽位是最容易发生的错，代价得让它不划算。
                if min(pair[i + q][j][0] for q in range(k)) < MIN_SCORE:
                    continue
                merged = acc / k - 0.02 * (k - 1)
                if merged >= MIN_SCORE and cur + merged > dp[i + k][j + 1]:
                    dp[i + k][j + 1] = cur + merged
                    back[i + k][j + 1] = (i, j, "merge%d" % k)

    raw: List[Dict[str, Any]] = []
    i, j = n, m
    while i > 0 or j > 0:
        step = back[i][j]
        if step is None:
            break
        pi, pj, act = step
        if act == "one":
            raw.append({"slot": pj, "groups": [pi]})
        elif act.startswith("merge"):
            k = int(act[5:])
            raw.append({"slot": pj, "groups": list(range(pi, pi + k))})
        i, j = pi, pj
    raw.reverse()

    used_g = {gi for mt in raw for gi in mt["groups"]}
    used_s = {mt["slot"] for mt in raw}

    # 第二遍：单调约束会漏掉「顺序写反、但标签很强」的情况——用户把
    # 「本次共 12 个系统」写在文末，模板里它却在表格前面。顺序只是先验，
    # 标签才是证据，所以对剩下的组做一次**高置信度**回填（阈值比第一遍高）。
    free: List[Tuple[float, int, int]] = []
    for gi in range(n):
        if gi in used_g:
            continue
        best_j, best_sc = -1, 0.0
        for j in range(m):
            if j in used_s:
                continue
            sc = pair[gi][j][0]
            if sc > best_sc:
                best_j, best_sc = j, sc
        # 门槛分两档。光看分数会漏掉一类**最该填**的情况：用户把「漏洞描述」
        # 写在了「漏洞地址」后面，而模板里描述段在地址前面——单调约束过不去，
        # 分数 0.55 又够不到 FREE_PASS，于是明明标签一字不差的一节被丢进
        # 「未匹配」。这种时候真正作数的是「名字对上了」这个事实，不是分数高低。
        if best_j >= 0 and (best_sc >= FREE_PASS
                            or (pair[gi][best_j][2] and best_sc >= FREE_NAME_PASS)):
            free.append((best_sc, gi, best_j))
    free.sort(reverse=True)
    out_of_order: set = set()
    for _, gi, j in free:
        if gi in used_g or j in used_s:
            continue
        raw.append({"slot": j, "groups": [gi]})
        used_g.add(gi)
        used_s.add(j)
        if pair[gi][j][0] < FREE_PASS:
            out_of_order.add(gi)

    # 第三遍：图跟着它上面那一节走。
    # 用户在富文本里写完一段再粘一张截图时，编辑器会把图单独放成一个段落
    # （contenteditable 的默认行为，从 Word 粘过来也一样）。上面分组特意让
    # 纯图段落独立成组，是为了「修复建议 + 图片字段」这种模板能分开填；可当
    # 模板里**只有富文本槽位、没有图片槽位**时，那张图就成了没人要的孤儿，
    # 被静默丢掉——文字报告看起来完整，缺的偏偏是证据截图。
    # 规则：紧挨在富文本匹配后面的纯图组，并进这一处。
    extra: set = set()
    for mt in raw:
        if slots[mt["slot"]]["kind"] not in ("richtext", "rich"):
            continue
        last = mt["groups"][-1]
        gi = last + 1
        while gi < n and gi not in used_g and _is_picture_only(groups[gi]):
            if groups[gi]["paras"][0] != groups[last]["paras"][-1] + 1:
                break          # 中间隔着别的段落，那就不是"跟着这一节"的图
            mt["groups"].append(gi)
            used_g.add(gi)
            extra.add(gi)
            last = gi
            gi += 1

    # 第四遍：循环里的第二条、第三条记录。
    # 「漏洞数量是循环内容，可能填写一个或多个」是这类模板最核心的用法，可每个
    # 字段在槽位表里只有一个位置、DP 又是 1:1 的：用户粘两条漏洞时，第二套
    # 小标题（漏洞名称/影响范围/修复建议）占不到任何槽位，被静默丢掉，界面还
    # 报「已填入 3 项」——用户以为两条都进去了。
    # 只允许**数组项里的字段**重复（path 带 `[]`）：标量重复一定会覆盖原值，
    # 那才是真正的错。门槛也只用「名字对得上」这一条证据，宁可少认一条，
    # 也不要把一段无处安放的正文塞成第二条记录。
    cyc = [j for j in {mt["slot"] for mt in raw}
           if "[]" in (slots[j].get("path") or "")]
    if cyc:
        cyc.sort()
        repeats: List[int] = []
        for gi in range(n):
            if gi in used_g:
                continue
            best_j, best_sc = -1, 0.0
            for j in cyc:
                sc = pair[gi][j][0]
                if sc > best_sc:
                    best_j, best_sc = j, sc
            if best_j >= 0 and pair[gi][best_j][2] and best_sc >= FREE_NAME_PASS:
                raw.append({"slot": best_j, "groups": [gi], "repeat": True})
                used_g.add(gi)
                repeats.append(gi)

    # 按用户文档顺序输出：用户在界面上是从上往下核对的，
    # 按模板顺序输出会让他来回跳。
    raw.sort(key=lambda mt: mt["groups"][0])

    matches: List[Dict[str, Any]] = []
    for mt in raw:
        s = slots[mt["slot"]]
        gs = [groups[k] for k in mt["groups"]]
        # 纯图组是按"跟着上一节"补进来的，本身没有可比对的信息，
        # 算进平均分会把一处好好的匹配拖成低置信度，反而让用户不敢确认。
        scored = [k for k in mt["groups"] if k not in extra]
        score = sum(pair[k][mt["slot"]][0] for k in scored) / max(len(scored), 1)
        why = list(pair[mt["groups"][0]][mt["slot"]][1])
        if len(scored) > 1:
            why.append("%d 节内容合并到这一处" % len(scored))
        if extra & set(mt["groups"]):
            why.append("图跟着这一节走")
        if out_of_order & set(mt["groups"]):
            why.append("位置和模板不一致，但标签对得上")
        if mt.get("repeat"):
            why.append("同一循环里的又一条记录")
        matches.append({
            "slot": s["name"],
            "slot_path": s.get("path") or s["name"],
            "slot_kind": s["kind"],
            "slot_label": s.get("label") or s.get("column_label") or s.get("name"),
            "section": s.get("section") or "",
            "part": s.get("part") or "",
            "paragraphs": [pi for g in gs for pi in g["paras"]],
            "score": round(score, 3),
            # 「这一段的标题就是那个字段名」——和分数是两回事。
            # 前端默认只勾它 + 高分的项：这份模板上靠标签命中的匹配一律 0.55，
            # 而字体指纹能到 0.7，只按分数划线会把名字完全对得上的项全排除在外，
            # 用户每次都得手动勾七下——那等于把判断责任又推回给他。
            "name_hit": bool(pair[mt["groups"][0]][mt["slot"]][2]),
            "why": why,
            "value": _value_of(gs, s, name_hit=bool(pair[mt["groups"][0]][mt["slot"]][2])),
        })

    total = sum(mt["score"] for mt in matches)
    return {
        "ok": True,
        "matches": matches,
        "unmatched_paragraphs": [pi for gi, g in enumerate(groups)
                                 if gi not in used_g for pi in g["paras"]],
        "unmatched_slots": [slots[k]["name"] for k in range(m) if k not in used_s],
        "score": round(total / max(len(matches), 1), 3),
    }


def _value_of(groups: List[Dict[str, Any]], slot: Dict[str, Any],
              name_hit: bool = True) -> Any:
    """把匹配到的内容转成可写入表单的值。

    - 图片槽位：取第一张图
    - 富文本槽位：`{type:html, content}`，段间用 `<br/>`（**同段落内换行**）
    - 其它：纯文本

    为什么富文本只用 `<br/>` 不切段：段落格式（居中/缩进/行距/编号）属于模板，
    新增段落就得替用户决定这些属性，而"格式一个字节都不改"是硬要求。
    宁可让多段变成段内换行，也不替他生成新段落。
    """
    if slot.get("kind") == "image":
        for g in groups:
            if g.get("images"):
                return g["images"][0]["src"]
        return ""
    # 名字对上了：这一节就是那个字段，值取标题下面的正文。
    # 名字没对上（只靠字体指纹认出来的封面大标题）：值取标题自己，
    # 否则「某某市第一人民医院」下面跟着一行「渗透测试报告」，
    # 会把第二行当成公司名填进去。
    if slot.get("kind") in ("richtext", "rich"):
        return {"type": "html", "content": _rich_html(groups, name_hit)}
    if not name_hit:
        return "\n".join((g.get("heading") or {}).get("text") or g.get("value_text") or ""
                         for g in groups)
    return "\n".join(g.get("value_text") or "" for g in groups if g.get("value_text"))


def _is_picture_only(group: Dict[str, Any]) -> bool:
    return bool(group.get("images")) and not (group.get("text") or "").strip()


def _rich_html(groups: List[Dict[str, Any]], name_hit: bool) -> str:
    """把匹配到的几组写成富文本 html，**图不能丢**。

    用户的原话是"里面随便写"，而安全报告里"写"天然包含截图。原先这里只拼
    文字，粘进去的图被静默丢掉——接口 200、文字一字不差，只是那张证明漏洞
    存在的截图没了。这种丢失恰恰是最要命的：报告看起来是完整的。

    顺序上仍是「先文字、后图」：段内的图通常就是紧随其后的佐证。做到逐字
    还原图在文字中的插入位置需要记录流水号，收益不抵复杂度——图放段末读者
    照样看得懂，放错段落才叫错。
    """
    parts: List[str] = []
    for g in groups:
        if name_hit:
            txt = g.get("value_text") or ""
        else:
            txt = (g.get("heading") or {}).get("text") or g.get("value_text") or ""
        if txt:
            parts.append(_esc_html(txt).replace("\n", "<br/>"))
        for img in g.get("images") or []:
            src = (img or {}).get("src") or ""
            if src:
                parts.append('<img src="%s"/>' % _esc_attr(src))
    return "<br/>".join(parts)


def _esc_html(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def _esc_attr(s: str) -> str:
    return _esc_html(s).replace('"', "&quot;")
