"""模板扫描器（CONTRACT §3.1）：从预处理后的 docx 提取变量清单、示例 JSON、JSON Schema。

标签提取后剥离 docxtpl 的 p/tr 段落指令，用 jinja2 校验语法；按 for/if 嵌套
推导 loop/condition，按命名前缀约定识别 image/qrcode/richtext。
"""
from __future__ import annotations

import re
import zipfile
from collections import OrderedDict
from io import BytesIO
from typing import List, Tuple

import jinja2
from lxml import etree

from . import chartdata
from . import preprocess as pp
from .security import SecurityError, validate_docx

TOKEN_RE = re.compile(r"\{\{.*?\}\}|\{%.*?%\}|\{#.*?#\}", re.DOTALL)
IDENT = r"[A-Za-z_][A-Za-z0-9_]*"
CN = r"0-9A-Za-z_一-鿿"
# 变量名（含中文）。Python 3 的标识符规则本来就允许中文，Jinja2 也认；
# 只用 IDENT 会把中文名整条漏掉。凡「这里可能出现用户起的变量名」的地方都用 VAR。
VAR = rf"[A-Za-z_一-鿿][{CN}]*"

JINJA_KEYWORDS = {"not", "and", "or", "in", "is", "True", "False", "None",
                  "if", "else", "endif", "for", "endfor", "loop", "as", "by"}

# Jinja/循环内建伪变量与属性：绝不产出变量（根 Name=loop 时整段引用都忽略）
JINJA_BUILTINS = {
    "self", "cycler", "joiner", "namespace", "range", "dict", "lipsum",
    "loop",  # 及其属性
}
_LOOP_ATTRS = {
    "last", "first", "index", "index0", "length", "revindex", "revindex0",
    "even", "odd", "depth", "depth0", "previtem", "nextitem",
}
# 形如 loop.xxx / self.xxx 的点引用整体剔除
_BUILTIN_DOTTED_RE = re.compile(r"\b(loop|self|cycler|joiner|namespace)\s*\.\s*[A-Za-z_]\w*")
# 字符串字面量：单双引号都要认（Jinja 两种都合法）。
# 中文字面量不会含引号，所以不需要处理转义。
_STRING_LIT_RE = re.compile(r"'[^']*'|\"[^\"]*\"")

# 字面量下标访问：`it["备注"]` / `数据['客户名']`。
# Jinja 里 obj["中文键"] 和 obj.中文键 完全等价，而中文键做不了合法标识符，
# 很多模板作者（尤其是从别的系统迁过来的）天生更习惯写方括号。
# 只认**字面量**键：`it[循环变量]` 这种动态下标取不到确定的字段名，跳过。
_SUBSCRIPT_RE = re.compile(rf"({VAR})\s*\[\s*(['\"])(.*?)\2\s*\]")

# 点引用链：`it.备注`、`a.b.c`、`数据.备注`。
# 用一个正则吃下整条链再拆，避免 `a.b` 被拆成两个裸名字。
_CHAIN_RE = re.compile(rf"[{CN}]+(?:\s*\.\s*[{CN}]+)+")

# 过滤器链：`{{ 附件 | default("—") }}` 里 `|` 之后全是变换，不影响
# "哪个字段要填"。整段切掉，免得 default / length / upper 被当成变量。
_FILTER_TAIL_RE = re.compile(r"\|.*$", re.DOTALL)

# 函数调用名（`default(` 的 default）。过滤器参数里的表达式会跟着被切掉，
# 这是已知取舍：宁可少扫一个兜底变量，也不能凭空造出 `default` 这种假变量。
_CALL_RE = re.compile(r"\b[A-Za-z_]\w*\s*\(")

_DIGITS_RE = re.compile(r"^\d+$")

_ASCII_IDENT_RE = re.compile(rf"^{IDENT}$")

# 裸名字（可含中文），用于比较式左右侧与行内三目里的开关
_NAME_RE = re.compile(rf"[{CN}]+")


def qn(tag: str) -> str:
    return f"{{{pp.W}}}{tag}"


def _normalize_block(inner: str) -> str:
    """剥离 docxtpl 段落/行指令：{%p for ... %} -> {% for ... %}。"""
    s = inner.strip()
    # endp/endtr -> end
    if s.startswith("tr "):
        s = s[3:]
    elif s.startswith("p "):
        s = s[2:]
    elif s in ("tr", "p"):
        s = ""
    elif s.startswith("tr"):
        s = s[2:]
    elif s.startswith("p"):
        s = s[1:]
    return s.strip()


def _extract_tokens(repaired_bytes: bytes) -> Tuple[List[Tuple[str, str]], List[dict]]:
    """返回 [(kind, inner)]，kind in var/block/comment。

    ⚠️ 必须扫全部"可能被 docxtpl 替换"的部件，不只是 document.xml。
    历史缺陷：这里原先只读 word/document.xml，导致**页眉/页脚里的占位符完全不
    被识别**。后果比缺一个变量严重得多 —— 用户看不到、也就没法填，生成出来的
    文档会在页眉里原样留着 `{{报告编号}}`。而中文公文/报告模板把「报告编号」
    「受控标识」「单位名称」放页眉是常态，这批模板会整体失效。
    preprocess.py 早就在修 header/footer 的跨 run 拆分，扫描侧必须与之同口径。
    """
    diags: List[dict] = []
    zf = zipfile.ZipFile(BytesIO(repaired_bytes))
    tokens: List[Tuple[str, str]] = []

    parts = [
        n for n in zf.namelist()
        if n == "word/document.xml"
        or n.startswith("word/header")
        or n.startswith("word/footer")
        or n in ("word/footnotes.xml", "word/endnotes.xml")
    ]
    # document.xml 放最前，保证变量在清单里的顺序以正文为主（页眉变量排后面）
    parts.sort(key=lambda n: (n != "word/document.xml", n))
    _seen_img_warn = set()   # 同一张图在多个部件/段落重复出现只报一次

    for part in parts:
        try:
            root = etree.fromstring(zf.read(part))
        except etree.LxmlError as e:  # 单个部件坏了不该让整份扫描失败
            diags.append({"severity": "warning",
                          "message": "部件 %s 无法解析，其中的占位符未被识别：%s"
                                     % (part, e),
                          "location": part})
            continue
        for p in root.iter(qn("p")):
            text = "".join(t.text or "" for t in p.iter(qn("t")))
            para_vars: List[str] = []
            for m in TOKEN_RE.finditer(text):
                tok = m.group(0)
                if tok.startswith("{{"):
                    nm = tok[2:-2].strip()
                    tokens.append(("var", nm))
                    para_vars.append(nm)
                elif tok.startswith("{%"):
                    tokens.append(("block", tok[2:-2].strip()))
                elif tok.startswith("{#"):
                    tokens.append(("comment", tok[2:-2].strip()))
            # 图片 + 固定行距 = 图片被裁成一行高。
            #
            # 公文正文按 GB/T 9704 用「固定值 28.8 磅」行距，而固定行距会
            # **按行高把超出的内容裁掉**：一张 30mm 高的印章图放进 28.8 磅的段落，
            # 纸面上只剩顶部一条弧。这不是渲染错，是行距规则，所以生成成功、
            # 接口 200、字数字句全对，只有肉眼看才发现「图片没了」。
            # 实测：本轮 10 式里 ① 的印章、⑦ 的插图各只剩一条。
            # 扫描期就把这个事实说出来，比生成完再让用户去数图片强。
            for nm in para_vars:
                if _kind_by_name(nm, "scalar") != "image":
                    continue
                pt = _exact_line_pt(p)
                if pt is None:
                    continue
                key = (nm, pt)
                if key in _seen_img_warn:
                    continue
                _seen_img_warn.add(key)
                diags.append({
                    "severity": "warning",
                    "message": "图片变量「%s」所在段落用了固定行距 %g 磅：固定行距会把超出"
                               "行高的内容裁掉，图片只会显示 %g 磅高的一条。请把该段行距改成"
                               "「单倍」或「最小值」，或把图片单独放在行距不受限的段落里。"
                               % (nm, pt, pt),
                    "location": part,
                })
    return tokens, diags


def _exact_line_pt(p) -> "float | None":
    """段落若用固定行距，返回行高（磅）；否则 None。

    只认**段落自己**的 `w:spacing@w:lineRule="exact"`。从样式继承来的固定行距
    （styles.xml 里的 docDefaults）暂且不管：那属于全局设定，改起来是另一回事，
    而这里要拦的是「用户给图片段落单独设了固定行距」这个高频坑。
    """
    pPr = p.find(qn("pPr"))
    if pPr is None:
        return None
    sp = pPr.find(qn("spacing"))
    if sp is None:
        return None
    if (sp.get(qn("lineRule")) or "") != "exact":
        return None
    try:
        v = int(sp.get(qn("line")) or 0)
    except (TypeError, ValueError):
        return None
    # >60 磅的固定行距基本容得下常见小图，不再打扰
    if v <= 0 or v > 1200:
        return None
    return v / 20.0


def _kind_by_name(name: str, default: str) -> str:
    if name.startswith("qr_"):
        return "qrcode"
    if name.startswith("sig_") or name.startswith("seal_"):
        # 服务端 sigauth 解析注入的签名/印章占位：按图片对象处理，但不进 sample/required
        return "image"
    if name.startswith("image_") or name.startswith("img_"):
        return "image"
    if name.startswith("rich_") or name.startswith("html_") or name.startswith("md_"):
        return "richtext"
    return default


def is_system_sig_var(name: str) -> bool:
    """sig_/seal_ 前缀的变量由服务端 sigauth 注入，不属于用户投递数据。"""
    return name.startswith("sig_") or name.startswith("seal_")


def _as_number(v):
    """图表缓存里取出来的是字符串，示例值给回真数字（用户编辑时更好操作）。"""
    try:
        f = float(str(v).replace(",", ""))
    except (TypeError, ValueError):
        return 0
    if f != f or f in (float("inf"), float("-inf")):
        return 0
    return int(f) if f == int(f) else round(f, 6)


# ============================================================
# 示例值：只做结构占位，不做语义推测
#
# 设计原则（产品约束，勿改）：模板里显式写了占位符的地方才是变量，值由使用者提供；
# 模板里没写占位符的地方就是固定文本，系统不去推断、不去补全。
#
# 历史教训：曾按变量名里的中文关键词猜示例值（含「时间」→ 2026-01-01、含「人员」→
# 张三、含「编号」→ AP-2026-001）。看似聪明，实则不稳定——中文命名的模板猜得像模像样，
# 换成 t1 / deadline / 交付期限 就全部退化成通用文本；猜错时用户会以为系统理解了语义，
# 反而降低信任。故一律给同一中性占位。
# ============================================================
PLACEHOLDER_TEXT = "（待填）"

# 1x1 透明 PNG：保证示例 JSON 可直接点「试渲染」跑通，又不表达任何内容。
# 不用 https://example.com/... 是因为内网/政务网部署下该域名不可达，
# 会导致「示例数据试渲染」因图片下载失败而报错。
PLACEHOLDER_IMAGE_SRC = (
    "data:image/png;base64,"
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
)


def _split_ternary(expr: str):
    """把行内三目拆成 (取值, 条件, 否则取值)。不是三目就返回 None。

    `{{ 项目名称 if 已交付 else "" }}` 里三样东西的身份完全不同：
      项目名称 —— 要人工填的文本（标量）；
      已交付   —— 开关（布尔）；
      ""       —— 常量。
    一锅端会把 项目名称 也变成开关：界面上只给一个 True/False，用户没地方填
    项目名；示例值 True 渲染出来正文里就印着 `True`。

    切分在**等长遮罩**（字面量换成同长度空格）的副本上做，因为用原文字面量
    可能含 " if "；但切片下标必须与原文对齐，所以不能把字面量替换成一个空格。
    """
    masked = _STRING_LIT_RE.sub(lambda m: " " * len(m.group(0)), expr)
    m = re.search(r"\sif\s", masked)
    if not m:
        return None
    head = expr[:m.start()]
    rest = m.end()
    m2 = re.search(r"\selse\s", masked[rest:])
    if m2:
        cond = expr[rest:rest + m2.start()]
        tail = expr[rest + m2.end():]
    else:
        cond = expr[rest:]
        tail = ""
    return head, cond, tail


def _refs(expr: str, stack: List[Tuple[str, str]]):
    """从一条 Jinja 表达式里抽出「引用了什么」。

    返回 (loop_fields, names)：
      loop_fields  [(所属循环的 iterable, 字段名)]  —— 归给最内层同名循环变量
      names        [裸名字]                        —— 标量/开关候选

    为什么要有这么一层：模板作者写变量的方式远不止 `{{ it.备注 }}` 一种。
    `it["备注"]`、`it.备注 or "—"`、`it.完成 if 已交付 else ""`、`x | default(兜底)`
    都是合法写法，而它们此前各漏一部分，症状完全一样 —— **字段扫不出来，
    用户就填不了，生成出来的文档那一格是空的，而扫描全绿、零诊断**。
    这类错不会在演示里出现，只会出现在发给客户的报告上。

    归属必须按「作用域栈 + 循环变量名」查，不能按名字记账：中文模板里两个
    不相干的循环都写成 `it` 是常态（见 _build_variables 里 stack 的注释）。
    """
    loop_fields: List[Tuple[str, str]] = []
    names: List[str] = []

    stack_vars = {lv for lv, _ in stack}

    def owner_of(base: str):
        for lv, it in reversed(stack):
            if lv == base:
                return it
        return None

    work = expr

    # 1) 字面量下标（必须在剥字符串之前——键本身就是字符串字面量）
    for base, _q, key in _SUBSCRIPT_RE.findall(work):
        loop_fields.append((base, key))
    work = _SUBSCRIPT_RE.sub(" ", work)

    # 2) 字符串字面量：`{{ "已完成" if it.完成 else "进行中" }}` 里的是常量
    work = _STRING_LIT_RE.sub(" ", work)

    # 3) loop.index / self.xxx 之类内建点引用，整条剔除
    work = _BUILTIN_DOTTED_RE.sub(" ", work)

    # 4) 过滤器链与调用括号
    work = _FILTER_TAIL_RE.sub(" ", work)
    work = _CALL_RE.sub(" ", work)

    # 5) 点引用链：拆成 (基名, 第一跳字段)
    for chain in _CHAIN_RE.findall(work):
        parts = re.split(r"\s*\.\s*", chain)
        if len(parts) < 2:
            continue
        base, field = parts[0], parts[1]
        if _ASCII_IDENT_RE.match(base) or base in stack_vars:
            # stack_vars 这一支是为中文循环变量加的：`{% for 条 in 条款 %}` 里的
            # `条` 不是 ASCII 标识符，若只按 ASCII 判断，`条.编号` 会掉进下面的
            # else 分支，被登记成一个名叫「条」的标量字段——正好是「循环消失、
            # 多出一个莫名其妙的输入框」那个症状。
            loop_fields.append((base, field))
        else:
            # 中文基名的点引用（`数据.备注`，且 数据 不在任何循环栈里）历史上
            # 按「基名登记为标量」处理，这里保持原样，免得既有模板的变量
            # 凭空消失或改名。
            names.append(base)
    work = _CHAIN_RE.sub(" ", work)

    # 6) 剩下的裸名字
    for nm in _NAME_RE.findall(work):
        if _DIGITS_RE.match(nm):
            continue  # 算术里的数字字面量
        if nm in JINJA_KEYWORDS or nm in JINJA_BUILTINS or nm in _LOOP_ATTRS:
            continue
        if nm in stack_vars:
            continue  # 循环变量本身不是字段
        names.append(nm)

    # 归属：能对上循环变量的点引用/下标归该循环，其余按基名登记
    resolved_fields: List[Tuple[str, str]] = []
    for base, field in loop_fields:
        if base in JINJA_BUILTINS:
            continue
        it = owner_of(base)
        if it is not None:
            resolved_fields.append((it, field))
        elif base not in _LOOP_ATTRS:
            names.append(base)

    return resolved_fields, list(dict.fromkeys(names))


def _build_variables(tokens: List[Tuple[str, str]]):
    loops: "OrderedDict[str, dict]" = OrderedDict()
    scalars: "OrderedDict[str, None]" = OrderedDict()
    conditions: "OrderedDict[str, None]" = OrderedDict()
    enums: "OrderedDict[str, list]" = OrderedDict()
    tags_seen = set()
    # 栈里存 (循环变量, 可迭代对象) 二元组，**不能只存变量名**。
    # 中文模板里两个不相关的循环用同一个变量名（都很自然地写成 it）是常态，
    # 只按名字归属会把内层循环的字段写到外层循环头上：演示模板的交付清单
    # 因此被塞进了内层「问题/等级/建议」三个字段，而它自己的「完成/备注」
    # 反倒没了 —— 界面让用户填一堆不存在的字段，校验还会报「字段缺失」。
    stack: List[Tuple[str, str]] = []
    syntax_errors: List[dict] = []

    # 先拼虚拟 jinja 源做语法校验
    virt_parts = []
    for kind, inner in tokens:
        if kind == "block":
            virt_parts.append("{% " + _normalize_block(inner) + " %}")
        elif kind == "var":
            virt_parts.append("{{ " + inner + " }}")
    virt = "".join(virt_parts)
    env = jinja2.Environment()
    try:
        env.parse(virt)
    except jinja2.TemplateSyntaxError as e:
        syntax_errors.append({
            "severity": "error",
            "message": f"模板语法错误：{e.message}，请检查标签是否配对",
            "location": f"行{e.lineno}",
        })

    # 循环变量名允许中文。
    #
    # 原先用 IDENT（纯 ASCII）匹配，理由是「Jinja 的循环变量必须是合法 Python
    # 标识符」。但 Python 3 的标识符规则本来就允许中文，实测 Jinja2 3.1.6 对
    # `{% for 条 in 条款 %}{{ 条.编号 }}{% endfor %}` 编译与渲染都正常。
    #
    # 只认 ASCII 的后果是**整个循环凭空消失**：loops 里没有它、迭代对象也不登记，
    # 循环变量反过来被当成一个标量字段（界面让用户填「条」这个字符串），
    # 而 {{ 条.编号 }} 在渲染时因 undefined 输出空串。中文模板作者几乎必然
    # 写成中文循环变量，这类模板会整体失效，而扫描全绿、零诊断。
    #
    # 首字符不能是数字（Python 标识符规则），其余按 CN 字符集放宽。
    LOOP_VAR = r"[A-Za-z_一-鿿][0-9A-Za-z_一-鿿]*"
    loop_re = re.compile(rf"^for\s+({LOOP_VAR})\s+in\s+(.+)$")
    # 比较式里的**左侧字段名允许中文**。
    #
    # IDENT 只认 ASCII，那是给 for 循环变量用的（Jinja 的循环变量必须是
    # 合法 Python 标识符）。拿它去匹配比较式左侧，等于让「风险等级」这类
    # 中文字段的枚举永远提取不到 —— 而中文公文/审计报告模板里几乎不会有
    # 英文变量名，手册 §4.5 承诺的「比较值会被系统提取为该字段的枚举校验、
    # 提交别的值会被拦下」就变成了一句空话。
    #
    # 单双引号都要认（Jinja 两种都合法）；用反向引用配对，避免
    # `== "高'` 这种不配对的串被吞进来。
    cmp_re = re.compile(rf'([{CN}]+)\s*==\s*(["\'])(.*?)\2')

    for kind, inner in tokens:
        if kind == "comment":
            continue
        if kind == "block":
            nb = _normalize_block(inner)
            if nb.startswith("for "):
                tags_seen.add("for")
                m = loop_re.match(nb)
                if m:
                    loopvar, iterable = m.group(1), m.group(2).strip()
                    loops.setdefault(iterable, {"loop_var": loopvar, "fields": OrderedDict()})
                    stack.append((loopvar, iterable))
                continue
            if nb.startswith("endfor"):
                if stack:
                    stack.pop()
                continue
            if nb.startswith("if "):
                tags_seen.add("if")
                expr = nb[3:].strip()
                # 归属规则与正文变量**共用同一个提取器**。
                #
                # 这里原先只抽裸名字（顺带剥了字面量和 loop.xxx），后果是
                # `{% if it.完成 %}` 里那个循环字段被当成顶层开关：变量清单里
                # 循环内一个字段都没有（示例 JSON 的循环项是空对象 {}），
                # 顶上却多出一个没意义的布尔「完成」。同一份模板，正文写
                # `{{ it.完成 }}` 是对的、写在 if 里就错——用户根本猜不到规律。
                cond_fields, cond_names = _refs(expr, stack)
                for it, field in cond_fields:
                    loops[it]["fields"].setdefault(field, None)
                for nm in cond_names:
                    conditions.setdefault(nm, None)
                for lhs, _quote, lit in cmp_re.findall(expr):
                    if lhs in JINJA_BUILTINS or lhs in _LOOP_ATTRS:
                        continue
                    enums.setdefault(lhs, [])
                    if lit not in enums[lhs]:
                        enums[lhs].append(lit)
                continue
            if nb.startswith("endif"):
                continue
            if nb.startswith("tr "):
                tags_seen.add("tr")
            continue
        # var
        expr = inner.strip()
        tri = _split_ternary(expr)
        if tri is not None:
            # 取值部分（then 与 else 两支）按数据字段登记，条件部分按开关登记。
            head, cond, tail = tri
            var_fields, var_names = _refs(head + " " + tail, stack)
            cond_fields, cond_names = _refs(cond, stack)
            for it, field in var_fields + cond_fields:
                loops[it]["fields"].setdefault(field, None)
            for nm in var_names:
                scalars.setdefault(nm, None)
            for nm in cond_names:
                conditions.setdefault(nm, None)
            continue
        var_fields, var_names = _refs(expr, stack)
        for it, field in var_fields:
            loops[it]["fields"].setdefault(field, None)
        for nm in var_names:
            scalars.setdefault(nm, None)

    # 常量与数组同名时以数组为准。
    #
    # `{{ 清单 | length }}`（「共 N 项」写在循环外）会被当成引用了裸名 清单，
    # 于是同一个名字既有一条标量、又有一个循环，界面上出现两行：一行要填字符串、
    # 一行要填数组，用户不知道该动哪个；schema.required 里也会重复一次。
    for name in list(scalars):
        if name in loops:
            scalars.pop(name)
    return loops, scalars, conditions, enums, tags_seen, syntax_errors


def scan_docx(raw: bytes, repair: bool = True) -> dict:
    validate_docx(raw)
    pre_diags: List[dict] = []
    repaired_bytes = raw
    repaired_flag = False
    if repair:
        repaired_bytes, pre_diags, changed = pp.preprocess_docx(raw)
        repaired_flag = changed

    tokens, part_diags = _extract_tokens(repaired_bytes)
    loops, scalars, conditions, enums, tags_seen, syntax_errors = _build_variables(tokens)

    # ------------------------------------------------------------------ #
    # 归一化「比较式左侧」的字段
    #
    # `{% if 风险等级 == "高" %}` 里的 风险等级 是**取值受限的字符串**，
    # 不是开关。不归一化会有两个后果：
    #   1. 它同时落进 scalars 和 conditions，界面上出现两行同名变量
    #      （一个是文本框、一个是布尔开关），用户不知道该填哪个；
    #   2. 只有 `{% if 状态 == "通过" %}` 而没有 `{{ 状态 }}` 的字段，
    #      只在 conditions 里，一旦按枚举处理就整条消失，条件块永远走 else。
    # ------------------------------------------------------------------ #
    loop_field_names = {f for info in loops.values() for f in info["fields"]}
    for name in enums:
        if name not in scalars and name not in loop_field_names:
            scalars[name] = None
    for name in list(conditions):
        if name in enums:
            conditions.pop(name)

    variables = []
    sample: dict = {}
    schema_props: dict = {}
    schema_required: list = []

    # scalars
    for name in scalars:
        kind = _kind_by_name(name, "scalar")
        entry = {"name": name, "kind": kind, "type": "object" if kind != "scalar" else "string",
                 "path": name, "required": True}
        if kind == "scalar":
            # 枚举字段的示例值必须取**枚举里的值**，不能填占位文本。
            # 否则用户点「下载示例 JSON」拿到的样例本身就通不过枚举校验，
            # 第一次投递就被自己的模板拦下 —— 这比不给示例更糟。
            entry["sample"] = enums[name][0] if name in enums else PLACEHOLDER_TEXT
            sample[name] = entry["sample"]
            schema_props[name] = {"type": "string"}
            if name in enums:
                entry["enum"] = list(enums[name])
                schema_props[name]["enum"] = list(enums[name])
        elif kind == "image" and is_system_sig_var(name):
            # 服务端 sigauth 注入的签名/印章：保留变量条目，但不给示例值、
            # 不进 schema_required/props（用户不投递此 key，Go 渲染前强制注入图片对象）。
            entry["system_resolved"] = True
            entry["required"] = False
            variables.append(entry)
            continue
        elif kind == "image":
            sample[name] = {"type": "image", "src": PLACEHOLDER_IMAGE_SRC, "width": 30, "height": 30}
            schema_props[name] = {"type": "object", "required": ["type", "src"]}
        elif kind == "qrcode":
            sample[name] = {"type": "qrcode", "text": PLACEHOLDER_TEXT, "size": 20}
            schema_props[name] = {"type": "object", "required": ["type", "text"]}
        elif kind == "richtext":
            sample[name] = {"type": "markdown", "content": "**加粗** 说明"}
            schema_props[name] = {"type": "object", "required": ["type", "content"]}
        schema_required.append(name)
        variables.append(entry)

    # loops
    #
    # 嵌套循环（`{% for g in 问题分组 %}` 里再 `{% for e in g.条目 %}`）**不能**
    # 当顶层变量。数据要挂在父项内部（问题分组[i].条目[...]），而扫描器原先按
    # iterable 字符串记账，会生出一个叫 `g.条目` 的顶层变量：示例 JSON 里它被
    # 平铺在最外层，用户照着填，父项里没有 条目，Jinja 对缺失属性抛
    # UndefinedError，整单渲染直接失败；就算侥幸不抛，生成的表格也是空表。
    # 所以这里先把"子循环"识别出来，随后作为父循环的一个数组字段挂进去。
    loopvar_to_iterable: dict = {}
    for it, info in loops.items():
        loopvar_to_iterable.setdefault(info["loop_var"], it)  # 同名变量取首次出现，结果才稳定
    nested_of: "OrderedDict[str, Tuple[str, str]]" = OrderedDict()  # 子 iterable -> (父 iterable, 字段名)
    for it in loops:
        # 基名必须按 VAR（含中文）匹配。
        #
        # 这里原先写 `({IDENT})`，只认 ASCII 循环变量。中文模板里的嵌套循环
        # 几乎必然写成 `{% for 章 in 章节 %}{% for 条 in 章.条款 %}`——`章.条款`
        # 匹配不上，就不会被认成子循环，于是整个子循环被当成**顶层变量**
        # 挂到最外层，名字就叫「章.条款」。后果是致命的：
        #   · schema.required 里多出一个顶层 key「章.条款」，而正确数据是
        #     章节[i].条款 → 校验直接报「字段缺失」，**嵌套循环模板一次都投不进去**；
        #   · 变量面板出现一个用户看不懂的「章.条款」输入项，真正要填的
        #     章节项里反而没有条款。
        # 中文循环变量与中文字段名是公文/报告模板的默认写法，这条路必须通。
        m = re.match(rf"^({VAR})\.([{CN}]+)$", it)
        if not m:
            continue
        parent_it = loopvar_to_iterable.get(m.group(1))
        # parent_it == it 说明形如 `for g in g.条目`（自引用畸形写法），不认
        if parent_it and parent_it != it:
            nested_of[it] = (parent_it, m.group(2))

    def build_fields(field_names, base_path: str):
        """构造一层循环的字段元数据。base_path 用于生成可读的 path，如 `指标[].名称`。"""
        f_meta, f_props, f_req, f_sample = [], {}, [], {}
        for f in field_names:
            fkind = _kind_by_name(f, "scalar")
            ftype = "object" if fkind != "scalar" else "string"
            fm = {"name": f, "kind": fkind, "type": ftype, "path": f"{base_path}.{f}"}
            if fkind == "scalar":
                # 枚举字段的示例值取枚举里的第一个值，别填占位文本：否则用户
                # 下载的示例 JSON 本身就通不过枚举校验，第一次投递就被自己的模板拦住。
                fm["sample"] = enums[f][0] if f in enums else PLACEHOLDER_TEXT
                f_sample[f] = fm["sample"]
                f_props[f] = {"type": "string"}
                if f in enums:
                    fm["enum"] = list(enums[f])
                    f_props[f]["enum"] = list(enums[f])
            elif fkind == "richtext":
                f_sample[f] = {"type": "markdown", "content": "修复建议"}
                f_props[f] = {"type": "object", "required": ["type", "content"]}
            elif fkind == "image":
                f_sample[f] = {"type": "image", "src": PLACEHOLDER_IMAGE_SRC, "width": 30, "height": 30}
                f_props[f] = {"type": "object", "required": ["type", "src"]}
            elif fkind == "qrcode":
                f_sample[f] = {"type": "qrcode", "text": PLACEHOLDER_TEXT, "size": 20}
                f_props[f] = {"type": "object", "required": ["type", "text"]}
            f_req.append(f)
            f_meta.append(fm)
        return f_meta, f_sample, f_props, f_req

    for iterable, info in loops.items():
        if iterable in nested_of:
            continue  # 由父循环的字段携带，不单独成为顶层变量
        loopvar = info["loop_var"]
        base_path = f"{iterable}[]"
        fields_meta, field_sample, field_props, field_req = build_fields(info["fields"], base_path)

        for child_it, (parent_it, field_name) in nested_of.items():
            if parent_it != iterable:
                continue  # 只处理父子两层；更深的层级文档已声明不支持
            child = loops[child_it]
            c_meta, c_sample, c_props, c_req = build_fields(child["fields"],
                                                           f"{base_path}.{field_name}[]")
            # 父循环正文里常写着 `{{ g.条目 | length }}`，那一遍已经把 条目
            # 当标量字段登记过了。这里以「数组字段」为准，把标量那条摘掉，
            # 否则界面上同一个名字出现两次（一次要填字符串、一次要填数组）。
            fields_meta = [x for x in fields_meta if x["name"] != field_name]
            field_req = [x for x in field_req if x != field_name]
            field_sample.pop(field_name, None)
            fields_meta.append({
                "name": field_name, "kind": "loop", "type": "array",
                "path": f"{base_path}.{field_name}", "sample_size": 2,
                "required": True, "nested": True, "loop_var": child["loop_var"],
                "fields": c_meta,
            })
            field_sample[field_name] = [dict(c_sample), dict(c_sample)]
            field_props[field_name] = {"type": "array",
                                       "items": {"type": "object", "properties": c_props, "required": c_req}}
            field_req.append(field_name)

        entry = {"name": iterable, "kind": "loop", "iterable": iterable,
                 "loop_var": loopvar, "sample_size": 2, "fields": fields_meta,
                 "path": iterable, "required": True}
        sample[iterable] = [dict(field_sample), dict(field_sample)]
        schema_props[iterable] = {"type": "array",
                                  "items": {"type": "object", "properties": field_props, "required": field_req}}
        schema_required.append(iterable)
        variables.append(entry)

    # conditions
    for name in conditions:
        variables.append({"name": name, "kind": "condition", "expression": name,
                          "path": name, "required": False})
        # 不覆盖已有示例值：同名变量若已按标量登记（枚举/正文都用到它），
        # 这里再写 True 会把示例 JSON 写成 `"风险等级": true`，投递必挂。
        sample.setdefault(name, True)

    # ------------------------------------------------------------------ #
    # 图表：数据不在文本节点里，按 chart 部件单独生成变量
    #
    # 图表没有 `{{ }}` 可以放，所以变量名只能由系统派生（chart_<图表标题>）。
    # required=False 是刻意的：不传就用模板原有数据，这在业务上完全合理
    # （"这张图我今天不想改"），与文本变量的"必填"语义不同。真要让用户必须
    # 传，用户自己会在交付前检查。
    # ------------------------------------------------------------------ #
    charts_meta: List[dict] = []
    try:
        charts = chartdata.list_charts(repaired_bytes)
    except Exception as e:  # noqa: BLE001 - 图表损坏不该让整份模板扫不了
        diags_chart = [{"severity": "warning",
                        "message": "图表部件解析失败，图表变量未生成：%s" % str(e)[:120],
                        "location": "word/charts"}]
        diagnostics_extra = diags_chart
        charts = []
    else:
        diagnostics_extra = []

    used_names = {v["name"] for v in variables}
    for ch in charts:
        cname = chartdata.chart_var_name(ch, used_names)
        sample_chart = {
            "categories": list(ch["categories"]),
            "series": [{"name": s["name"],
                        "values": [_as_number(x) for x in s["values"]]}
                       for s in ch["series"]],
        }
        variables.append({
            "name": cname,
            "kind": "chart",
            "type": "object",
            "path": cname,
            "required": False,
            "chart_kind": ch["kind"],
            "chart_kind_name": ch["kind_name"],
            "title": ch["title"],
            "series_count": ch["series_count"],
            "point_count": ch["point_count"],
            "sample": sample_chart,
        })
        sample[cname] = sample_chart
        schema_props[cname] = chartdata.payload_schema()
        charts_meta.append({
            "variable": cname,
            "kind": ch["kind"],
            "kind_name": ch["kind_name"],
            "title": ch["title"],
            "part": ch["part"],
            "series_names": ch.get("series_names") or [],
            "series_count": ch["series_count"],
            "point_count": ch["point_count"],
        })

    json_schema = {"type": "object", "properties": schema_props, "required": schema_required}

    diagnostics = pre_diags + part_diags + syntax_errors + diagnostics_extra
    return {
        "variables": variables,
        "tags": sorted(tags_seen),
        "sample_json": sample,
        "json_schema": json_schema,
        "charts": charts_meta,
        "diagnostics": diagnostics,
        "repaired": repaired_flag,
        "_repaired_bytes": repaired_bytes,
    }
