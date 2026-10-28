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
            for m in TOKEN_RE.finditer(text):
                tok = m.group(0)
                if tok.startswith("{{"):
                    tokens.append(("var", tok[2:-2].strip()))
                elif tok.startswith("{%"):
                    tokens.append(("block", tok[2:-2].strip()))
                elif tok.startswith("{#"):
                    tokens.append(("comment", tok[2:-2].strip()))
    return tokens, diags


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

    loop_re = re.compile(rf"^for\s+({IDENT})\s+in\s+(.+)$")
    # 点引用用**前缀**匹配，不能整串锚定。
    # `{{ it.备注 or "—" }}`、`{{ it.完成 if ok else "" }}`、`{{ g.条目 | length }}`
    # 都是合法模板写法，锚定 `$` 会让这些字段一个都扫不出来：用户看不到、
    # 也就填不了，生成出来的文档里留着空白，而校验全绿。
    dot_re = re.compile(rf"({IDENT})\s*\.\s*([{CN}]+)")
    name_re = re.compile(rf"([{CN}]+)")
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
                # 先剔除 loop.xxx / self.xxx 等内建点引用，再抽取裸变量名。
                #
                # 还必须先剥掉字符串字面量：`{% if 风险等级 == "高" %}` 里的
                # "高" 是**比较值**，不是变量。不剥的话它会被当成一个叫「高」的
                # 条件变量塞进变量清单，界面里多出一个永远填不了的字段，
                # 示例 JSON 里也跟着多一条。
                cleaned = _STRING_LIT_RE.sub(" ", _BUILTIN_DOTTED_RE.sub(" ", expr))
                stack_vars = {s[0] for s in stack}
                for nm in name_re.findall(cleaned):
                    if nm in JINJA_KEYWORDS or nm in JINJA_BUILTINS or nm in _LOOP_ATTRS:
                        continue
                    if nm not in stack_vars:
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
        # 字符串字面量先剥掉：`{{ "已完成" if it.完成 else "进行中" }}` 里的
        # 「已完成」「进行中」是常量，不是变量。
        cleaned = _STRING_LIT_RE.sub(" ", expr)
        refs = dot_re.findall(cleaned)
        if refs:
            stack_vars = {s[0] for s in stack}
            for base, field in refs:
                if base in JINJA_BUILTINS:
                    continue  # loop.xxx / self.xxx 内建引用，不产出变量
                # 归属到**最内层**同名循环变量所属的那个循环
                owner = None
                for lv, it in reversed(stack):
                    if lv == base:
                        owner = it
                        break
                if owner is not None:
                    loops[owner]["fields"].setdefault(field, None)
                elif base not in _LOOP_ATTRS:
                    # 非循环对象的点引用（user.name 之类）：按基名登记为标量，
                    # 与旧行为一致，免得既有模板的变量凭空消失。
                    scalars.setdefault(base, None)
            continue
        nm = re.match(rf"^([{CN}]+)", cleaned)
        if nm:
            name = nm.group(1)
            if name in JINJA_BUILTINS or name in _LOOP_ATTRS:
                continue
            if name not in {s[0] for s in stack}:
                scalars.setdefault(name, None)
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
        m = re.match(rf"^({IDENT})\.([{CN}]+)$", it)
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
