"""跨 run 占位符合并预处理（CONTRACT §3.7）。

用 lxml 操作 word/document.xml 及 header/footer：逐段落把被 Word 拆到多个
run 里的 `{{ }}` `{% %}` `{# #}` 标记合并进第一个含文本的 run（保留其 rPr），
其余 run 的 w:t 清空但保留 run 与 rPr。幂等：对已规范模板再跑不改变结果。
"""
from __future__ import annotations

import re
import zipfile
from io import BytesIO
from typing import List, Tuple

from lxml import etree

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
XML_SPACE = "{http://www.w3.org/XML/1998/namespace}space"


def qn(tag: str) -> str:
    return f"{{{W}}}{tag}"


# 完整模板标签（非贪婪）
TAG_RE = re.compile(r"\{\{.*?\}\}|\{%.*?%\}|\{#.*?#\}", re.DOTALL)
MARKERS = ("{{", "}}", "{%", "%}", "{#", "#}")


def _paragraph_runs(p) -> List[Tuple[object, object, str]]:
    """返回 [(run_element, t_element, text)]，按文档顺序。"""
    out = []
    for r in p.findall(f".//{qn('r')}"):
        for t in r.findall(qn("t")):
            out.append((r, t, t.text or ""))
    return out


def _process_paragraph(p) -> Tuple[List[dict], bool]:
    """处理单个段落。返回 (诊断列表, 是否真正合并了 run)。"""
    items = _paragraph_runs(p)
    diags: List[dict] = []
    modified = False
    if not items:
        return diags, modified

    full = "".join(it[2] for it in items)
    if not any(m in full for m in MARKERS):
        return diags, modified

    # 构建 offset -> run index 映射
    offsets: List[Tuple[int, int, int]] = []
    cur = 0
    for idx, (_, _, text) in enumerate(items):
        offsets.append((cur, cur + len(text), idx))
        cur += len(text)

    def run_at(off: int) -> int:
        for s, e, ridx in offsets:
            if s <= off < e:
                return ridx
        return offsets[-1][2]

    # 匹配完整标签，判断是否跨 run
    split_spans: List[Tuple[int, int]] = []
    split_tags: List[str] = []
    for m in TAG_RE.finditer(full):
        s, e = m.start(), m.end()
        r0 = run_at(s)
        if any(run_at(o) != r0 for o in range(s, e)):
            split_spans.append((s, e))
            split_tags.append(m.group(0))

    # 未配对标记：移除完整标签后剩余的标记
    leftover = TAG_RE.sub("", full)
    unbalanced = [mk for mk in ("{{", "{%", "{#") if mk in leftover]

    if split_tags:
        first = None
        for it in items:
            if it[2]:
                first = it
                break
        if first is None:
            first = items[0]
        first_t = first[1]
        first_t.text = full
        first_t.set(XML_SPACE, "preserve")
        for r, t, _ in items:
            if t is not first_t:
                t.text = ""
        nruns = len({run_at(o) for s, e in split_spans for o in range(s, e)})
        diags.append({
            "severity": "warning",
            "message": f"占位符 {split_tags[0]} 被 Word 拆到 {nruns} 个 run，已自动合并修复",
            "location": "正文/表格段落",
        })
        modified = True

    if unbalanced:
        diags.append({
            "severity": "error",
            "message": f"存在未配对的占位符标记 {unbalanced[0]}，请在 Word 中把占位符重新输入为纯文本",
            "location": "正文/表格段落",
        })
    return diags, modified


def _process_xml(xml_bytes: bytes) -> Tuple[bytes, List[dict], bool]:
    parser = etree.XMLParser(remove_blank_text=False)
    root = etree.fromstring(xml_bytes, parser)
    diags: List[dict] = []
    modified = False
    for p in root.iter(qn("p")):
        d, m = _process_paragraph(p)
        diags.extend(d)
        modified = modified or m
    return etree.tostring(root, xml_declaration=True, encoding="UTF-8", standalone=True), diags, modified


# ---------------------------------------------------------------------- #
# 表格行级标签 {%tr ... %}
#
# docxtpl 自带的 {%tr %} 处理是**坏的**，而且坏得很隐蔽。它的正则长这样：
#
#   <w:tr[ >](?:(?!<w:tr[ >]).)*({%|{{)tr ([^}%]*(?:%}|}})).*?</w:tr>
#
# 前缀 `(?:(?!<w:tr[ >]).)*` 是贪婪的，会一路吃到这一行结束再回溯找
# `{%tr `，于是回溯取到的是**这一行里最后一个** `{%tr` 标签。手册 §4 教的
# 写法恰好在同一行里同时有 `{%tr for %}`（首格）和 `{%tr endfor %}`（末格），
# 结果整行被替换成 `{% endfor %}`，`for` 直接消失 —— 报错是
# 「Encountered unknown tag 'endfor'」，看不出跟表格有关。
# 已用原生 docxtpl 0.20.2 复现，不是我们这条链路引入的。
#
# 所以这里自己把行级标签搬到行外：
#   `{%tr for X in Y %}`   → 该 <w:tr> 之前 `{% for X in Y %}`（整行成为循环体）
#   `{%tr endfor %}`        → 该 <w:tr> 之后 `{% endfor %}`
# 搬完之后 XML 里再无 {%tr %}，docxtpl 那条正则没有机会误伤。
# 单元格级的 {%tr vm %} / {%tr hm %} / {%tr colspan %} 不在此列，原样留着。
# ---------------------------------------------------------------------- #
_ROW_TAG_RE = re.compile(r"\{%\s*tr\s+(.+?)\s*%\}", re.DOTALL)
_OPEN_KINDS = ("for ", "if ", "else", "elif")


def _nearest_row(el):
    p = el.getparent()
    while p is not None and p.tag != qn("tr"):
        p = p.getparent()
    return p


def _append_tail(el, text: str) -> None:
    """把文本接到 el 之后（作为 tail 序列化到元素外面）。"""
    if el is None or text == "":
        return
    el.tail = (el.tail or "") + text


def _row_is_empty(row) -> bool:
    """去掉标签之后这一行还有没有可见内容（文字/图片/嵌套表）。"""
    for tag in ("tbl", "drawing", "pict", "object"):
        if row.find(".//" + qn(tag)) is not None:
            return False
    for st in ("sectPr", "fldSimple", "instrText"):
        if row.find(".//" + qn(st)) is not None:
            return False
    for t in row.iter(qn("t")):
        if (t.text or "").strip():
            return False
    return True


def _normalize_row_tags(root):
    """把行级 {%tr ... %} 标签搬到 <w:tr> 外面。返回 (诊断, 是否改动)。"""
    diags: List[dict] = []
    modified = False

    for row in list(root.iter(qn("tr"))):
        before: List[str] = []
        after: List[str] = []
        for t in list(row.iter(qn("t"))):
            if _nearest_row(t) is not row:
                continue  # 嵌套表格自己的单元格，交给它自己那一次循环处理
            text = t.text or ""
            if "tr " not in text:
                continue
            for m in list(_ROW_TAG_RE.finditer(text)):
                inner = m.group(1).strip()
                if inner.startswith(_OPEN_KINDS):
                    before.append("{%% %s %%}" % inner)
                elif inner.startswith(("endfor", "endif")):
                    after.append("{%% %s %%}" % inner)
                else:
                    # vm/hm/colspan 等单元格指令：留在原处给 docxtpl 处理
                    continue
                text = text.replace(m.group(0), "", 1)
            if text != (t.text or ""):
                t.text = text
                t.set(XML_SPACE, "preserve")

        if not before and not after:
            continue

        prev = row.getprevious()
        if _row_is_empty(row):
            # 整行只有标签（"for 单独一行 / endfor 单独一行" 的写法）：
            # 把标签挪到前一个兄弟后面，然后**删掉这一行**，否则成品里会
            # 多出一条空行。
            _append_tail(prev, "".join(before + after))
            row.getparent().remove(row)
        else:
            _append_tail(prev, "".join(before))
            _append_tail(row, "".join(after))
        modified = True

    return diags, modified


def normalize_row_tags_docx(raw: bytes) -> Tuple[bytes, List[dict]]:
    """只做行级 {%tr %} 标签外移，不动跨 run 合并。

    **必须单独一步、只在渲染前调用**，不能塞进 preprocess_docx 里。
    原因：标签被搬到 <w:tr> 外面之后，它就不再是任何 <w:t> 的文字了，
    而扫描器只读 <w:t> —— 先搬再扫，扫描器会看不见 `{%tr for %}`，
    于是把循环变量 m/it 当成普通标量塞进必填字段，示例 JSON 里凭空多出
    两个填不了的键，投递直接被自己的校验拒掉（报「字段 m 缺失」）。
    扫描看原结构、渲染看搬完的结构，两边各用各的。
    """
    zin = zipfile.ZipFile(BytesIO(raw))
    out_buf = BytesIO()
    diags: List[dict] = []
    with zipfile.ZipFile(out_buf, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            data = zin.read(item.filename)
            if item.filename == "word/document.xml":
                try:
                    root = etree.fromstring(data, etree.XMLParser(remove_blank_text=False))
                    d, _ = _normalize_row_tags(root)
                    diags.extend(d)
                    data = etree.tostring(root, xml_declaration=True,
                                          encoding="UTF-8", standalone=True)
                except etree.XMLSyntaxError:
                    pass
            zout.writestr(item, data)
    zin.close()
    return out_buf.getvalue(), diags


def preprocess_docx(raw: bytes) -> Tuple[bytes, List[dict], bool]:
    """对 docx 字节做跨 run 合并。返回 (新字节, 诊断列表, 是否发生了合并)。"""
    zin = zipfile.ZipFile(BytesIO(raw))
    out_buf = BytesIO()
    changed = False
    diags: List[dict] = []
    with zipfile.ZipFile(out_buf, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            data = zin.read(item.filename)
            fname = item.filename
            if fname.endswith(".xml") and (
                fname == "word/document.xml"
                or fname.startswith("word/header")
                or fname.startswith("word/footer")
            ):
                try:
                    data, part_diags, part_mod = _process_xml(data)
                    changed = changed or part_mod
                    diags.extend(part_diags)
                except etree.XMLSyntaxError:
                    pass
            zout.writestr(item, data)
    zin.close()
    return out_buf.getvalue(), diags, changed
