# -*- coding: utf-8 -*-
"""块级模型 -> docx 原地回写（PRD V2 §7.1，/apply 核心）。

回写语义（与前端 diff 契约冻结，Go 工程师按此对接）：
  1. 同 id 的 paragraph/heading/placeholder：按模型更新文字/名称；
     **保留该段 pPr 与每个 run 的 rPr**（缩进/行距/对齐/字体/字号/eastAsia），
     只替换改动区间内的字符；jinja 标签（{{ }}、{% %}、{%tr %}）按原文写入。
     —— 文本未变的段落 XML 一个字节都不改。
  2. 模型中消失的 id：删除对应段落/表格行；``new-`` 前缀块：按其在 blocks 数组中的
     位置（锚点 = 相邻同父级已存在 id）插入新段落，**只继承锚点的格式，不继承内容**。
  3. 表格：按模型 rows 与单元格内容 diff，支持行列增删、单元格文字改、循环行标记。
  4. 独立 image/qrcode/rich 占位块：仅改标签引用 ``{{name}}``；图片二进制替换不在本服务做。
  5. unsupported 块及其余所有未在模型中命中的 XML：**原样保留，一个字节都不碰**；
     页眉页脚、样式表、编号、主题、字体设置全部不动（zip 里这些部件按字节原样复制）。

格式不变是硬约束，四条"宁可不改也不许改坏"的红线：
  - 域（fldSimple / fldChar 域代码与域结果）区间只读，改动与之相交则整段拒改；
  - 承载分节符（pPr/sectPr）的段落不删，只清空文字——删了页面方向/页边距就跟着没；
  - 拿不到可靠 run 切分（文本框等嵌套内容）时拒改，绝不"整体重建"；
  - 新建段落只复制 pPr 与一个 rPr，**不复制书签/图片/域/超链接**（复制会重名重图）。

实现：zipfile 读原包，lxml 定点改 word/document.xml，其余部件字节复制另存。
"""
from __future__ import annotations

import copy
import difflib
import re
import zipfile
from io import BytesIO
from typing import Any, Dict, List, Optional

from lxml import etree

from .docmodel import (
    W, qn, paragraph_text, extract_tokens,
    TR_FOR_RE, TR_END_RE, TR_SAME_ROW_RE,
    _is_unsupported_paragraph, _chart_rid, XML_SPACE,
)


# --------------------------------------------------------------------------- #
# 段落文本重写：字符级差异回填，格式一个字节都不动
#
# 这一节最容易"看起来成功、实际毁文档"，规则写死在这里：
#
#   1. pPr（缩进/行距/段前段后/对齐/编号/分节）永不修改、永不删除；
#   2. 每个 run 的 rPr（字体/字号/加粗/颜色/字符间距）永不修改；
#   3. 只有落在改动区间内的字符会被换掉，区间外的 run 连 w:t 都不重建；
#   4. 域（fldSimple / fldChar 域代码与域结果）里的字符只读，与之相交就整段拒改；
#   5. 拿不到可靠 run 切分时拒改，绝不"整体重建"。
#
# 老实现是「删掉所有 run，用第一个 run 的 rPr 重建一个」。单 run 段落下看不出
# 问题，但公文里「**一、总体情况**：其余文字」这种混排段落会被压平成一种字形；
# 含域的段落会把域结果搬到段首、正文重复一遍；含修订痕迹的段落会多出一段
# 没有字体没有字号的文字。三种都要求"打开 Word 才看得出来"，接口全是 200。
# --------------------------------------------------------------------------- #

# 这些容器里的 run 属于**另一段内容**（文本框里的段落、VML 图形里的文字、
# 已被修订删除的文字），既不能算进本段文本，也不能被本段改写。
_SKIP_RUN_PARENTS = ("txbxContent", "pict", "del")

# 段落标记本身可能带字符格式；pPr 里这些子元素是"修订记录/分节"，不能跟着搬
_PPR_DROP = ("sectPr", "sectPrChange", "pPrChange")

# 段落被删除时需要保住的"身份标记"：书签、批注锚点、拼写标记
_KEEP_ON_DELETE = ("bookmarkStart", "bookmarkEnd",
                   "commentRangeStart", "commentRangeEnd", "proofErr")


def _ancestor_has(el, boundary, names) -> bool:
    """el 的祖先（到 boundary 为止，不含）里是否有 names 中的标签。"""
    anc = el.getparent()
    while anc is not None and anc is not boundary:
        if etree.QName(anc).localname in names:
            return True
        anc = anc.getparent()
    return False


def _all_runs(p) -> list:
    """段落内所有 run，文档顺序。含 hyperlink/ins/smartTag/sdt 里的，排除嵌套内容。"""
    return [r for r in p.iter(qn("r")) if not _ancestor_has(r, p, _SKIP_RUN_PARENTS)]


def _run_text(r) -> str:
    """run 自己的文字。只取**直属** w:t：run 里可能挂着文本框，那属于另一段内容，
    混进来会让字符偏移全部错位。"""
    return "".join(t.text or "" for t in r.findall(qn("t")))


def _has_non_text(r) -> bool:
    """run 里除 rPr/w:t 外还有别的东西（制表符、换行、图片、域代码）。

    这类 run 即使文字被删空也不能删——删掉就把制表位或那张图一起删了。
    """
    for child in r:
        if etree.QName(child).localname not in ("rPr", "t"):
            return True
    return False


def _set_run_text(r, text: str) -> None:
    """把 run 的可见文字换成 text，只动直属 w:t，rPr 与其它节点原样保留。"""
    ts = r.findall(qn("t"))
    if not ts:
        t = etree.SubElement(r, qn("t"))
        t.set(XML_SPACE, "preserve")
        t.text = text
        return
    ts[0].set(XML_SPACE, "preserve")
    ts[0].text = text
    for extra in ts[1:]:
        extra.getparent().remove(extra)


def _field_spans(runs) -> List[tuple]:
    """只读字符区间：域代码 + 域结果（fldSimple 与 fldChar 复合域）。

    域里的文字是 Word 生成的，改它不生效（下次更新域就回来），但把结构清掉
    会立刻生效——域结果残缺、目录变白、页码变「第 页 共 页」都从这里来。
    """
    spans: List[tuple] = []
    pos = 0
    depth = 0
    for r in runs:
        t = _run_text(r)
        s, e = pos, pos + len(t)
        pos = e
        if _ancestor_has(r, None, ("fldSimple",)):
            if t:
                spans.append((s, e))
            continue
        types = {fc.get(qn("fldCharType")) for fc in r.findall(qn("fldChar"))}
        if "begin" in types:
            depth += 1
        if depth > 0 and t:
            spans.append((s, e))
        if "end" in types:
            depth = max(0, depth - 1)
    return spans


def _in_spans(s: int, e: int, spans) -> bool:
    """[s,e) 是否与任一区间有真实重叠（退化区间/贴边界不算）。"""
    for (fs, fe) in spans:
        if s < fe and e > fs:
            return True
    return False


def _prune_empty_wrappers(p) -> None:
    """文字被删空的 ins/hyperlink/smartTag 容器跟着删掉。

    空的 ``<w:hyperlink/>`` 是非法结构，空的 ``<w:ins>`` 会在 Word 里显示成
    一个空的修订标记。都被删空说明这一段本来就没内容，连壳一起清掉。
    """
    for _ in range(8):
        removed = False
        for w in list(p.iter()):
            if w is p:
                continue
            if etree.QName(w).localname in ("ins", "hyperlink", "smartTag"):
                if not w.findall(".//" + qn("r")):
                    parent = w.getparent()
                    if parent is not None:
                        parent.remove(w)
                        removed = True
        if not removed:
            break


def _pick_target(spans, lo: int) -> Optional[int]:
    """改动起点该写进哪个 run：优先"起点落在它内部"的，其次"结束于起点"的，
    再次"开始于起点"的。

    取"结束于起点"这一档，是为了让「段尾追加」「两点之间插入」落在**前一个字**
    所在的 run 上——Word 自己也是按前一个字符的格式继续输入的。这样边界插入
    不会跑进后面那个域或超链接里。
    """
    for i, (s, e, _r, _t) in enumerate(spans):
        if s < lo < e:
            return i
    for i, (s, e, _r, _t) in enumerate(spans):
        if e == lo:
            return i
    for i, (s, e, _r, _t) in enumerate(spans):
        if s == lo:
            return i
    return None


def _splice_ok(runs, lo: int, hi: int) -> bool:
    """这条改动能不能安全落地（不碰域、不把字写进域里）。"""
    spans = []
    pos = 0
    for r in runs:
        t = _run_text(r)
        spans.append((pos, pos + len(t), r, t))
        pos += len(t)
    if not spans:
        return False
    ro = _field_spans(runs)
    if not ro:
        return True
    if _in_spans(lo, hi, ro):
        return False
    target = _pick_target(spans, lo)
    if target is not None and _in_spans(spans[target][0], spans[target][1], ro):
        return False
    return True


def _splice(p, lo: int, hi: int, mid: str) -> None:
    """把段落里 [lo,hi) 这段字符换成 mid，只重写被覆盖的 run。

    要求 lo/hi/mid 是**针对当前段落文本**算好的，且已经过 ``_splice_ok`` 判定；
    调用方按从右到左的顺序调用，这样左侧的位置不会被前一次改写挪动。
    """
    runs = _all_runs(p)
    spans = []
    pos = 0
    for r in runs:
        t = _run_text(r)
        spans.append((pos, pos + len(t), r, t))
        pos += len(t)
    if not spans:
        return

    target = _pick_target(spans, lo)
    for idx, (s, e, r, t) in enumerate(spans):
        keep_head = t[: min(len(t), lo - s)] if lo > s else ""
        keep_tail = t[max(0, hi - s):] if hi < e else ""
        put_mid = mid if idx == target else ""
        new_t = keep_head + put_mid + keep_tail
        if new_t == t:
            continue
        if new_t == "" and not _has_non_text(r):
            parent = r.getparent()
            if parent is not None:
                parent.remove(r)
            continue
        _set_run_text(r, new_t)


def _rewrite_paragraph_text(p, new_text: str) -> bool:
    """把段落可视文本改成 new_text，返回是否真的改成功。

    做法：先算新旧文本的差异清单（difflib 的 opcode），**先整体判定能不能改**，
    再从右往左逐条应用。
      - equal 之外的操作才动 XML，没覆盖到的 run 连 w:t 都不重建；
      - 落在域区间上的操作让整段拒改（域结果是 Word 生成的，改了下次更新域就
        回滚，但清掉结构会立刻生效：目录变白、页码变「第 页 共 页」）；
      - **先判定后动手**：只要有任意一条操作过不了判定，整段一个字节都不改，
        绝不出现"改了一半"的段落；
      - 拿不到可靠 run 切分（文本框等嵌套内容）→ 整段不改，返回 False。

    为什么不用"公共前缀 + 公共后缀"：那个模型只认"中间一整段被换掉"，一次插入
    会被算成"从插入点删到段尾"，于是只要段尾有域，整段就都改不了。
    """
    old = paragraph_text(p)
    if old == new_text:
        return True

    runs = _all_runs(p)
    if not runs:
        if not new_text:
            return True
        # 本来是空段落（<w:p><w:pPr/></w:p>）：补一个 run，字符格式取段落标记的
        r = etree.SubElement(p, qn("r"))
        pPr = p.find(qn("pPr"))
        rpr = pPr.find(qn("rPr")) if pPr is not None else None
        if rpr is not None:
            r.append(copy.deepcopy(rpr))
        t = etree.SubElement(r, qn("t"))
        t.set(XML_SPACE, "preserve")
        t.text = new_text
        return True

    if "".join(_run_text(r) for r in runs) != old:
        return False

    ops = [o for o in difflib.SequenceMatcher(None, old, new_text, autojunk=False)
           .get_opcodes() if o[0] != "equal"]
    if not ops:
        return True
    if not all(_splice_ok(runs, i1, i2) for _tag, i1, i2, _j1, _j2 in ops):
        return False

    # 从右往左应用：右边的改写不会挪动左边还没处理的位置
    for _tag, i1, i2, j1, j2 in reversed(ops):
        _splice(p, i1, i2, new_text[j1:j2])

    _prune_empty_wrappers(p)
    return True




def _clone_shell(anchor) -> Any:
    """按锚点造一个新段落：只继承**格式**，不继承内容与身份。

    深拷贝整个段落曾是实现方式，但它把书签 id、图片、域、超链接、分节符一起
    复制过去：书签重名（目录跳转错乱）、图多一张、分节符多一个（版式被劈开）。
    全都不报错，只在打开 Word 时才看得见。
    """
    newp = etree.Element(qn("p"))
    if anchor is not None and etree.QName(anchor).localname == "p":
        pPr = anchor.find(qn("pPr"))
        if pPr is not None:
            new_pPr = copy.deepcopy(pPr)
            for ch in list(new_pPr):
                if etree.QName(ch).localname in _PPR_DROP:
                    new_pPr.remove(ch)
            newp.append(new_pPr)
        # 空 run 带上锚点最后一个 run 的字符格式：新段落敲字时字体字号就对了
        rpr = None
        for r in _all_runs(anchor):
            got = r.find(qn("rPr"))
            if got is not None:
                rpr = got
        if rpr is not None:
            etree.SubElement(newp, qn("r")).append(copy.deepcopy(rpr))
    return newp


def _delete_block(body, el, warnings: List[str]) -> None:
    """删除一个段落/表格，但保住"删了会改版式或断引用"的东西。

    - 段落 pPr 里挂着 sectPr（分节符）→ 只清空文字，段落本身保留。
      删掉它页面方向、页边距、页眉页脚归属会一起消失，整篇排版塌掉。
    - 书签 / 批注锚点 → 迁到下一个同级元素，否则目录与交叉引用指向的位置消失。
    """
    if etree.QName(el).localname == "p":
        pPr = el.find(qn("pPr"))
        if pPr is not None and pPr.find(qn("sectPr")) is not None:
            for r in _all_runs(el):
                parent = r.getparent()
                if parent is not None:
                    parent.remove(r)
            warnings.append("有一段承载分节符（页面方向/页边距），已保留为空段落以免版式改变")
            return

        nxt = el.getnext()
        for ch in list(el):
            if etree.QName(ch).localname not in _KEEP_ON_DELETE:
                continue
            el.remove(ch)
            if nxt is not None:
                nxt.addprevious(ch)
            else:
                sectPr = body.find(qn("sectPr"))
                if sectPr is not None:
                    sectPr.addprevious(ch)
                else:
                    body.append(ch)

    body.remove(el)


def _apply_paragraph_block(p, block: Dict[str, Any], warnings: List[str]) -> None:
    """按块模型更新一个段落元素；文本未变则完全不动该元素。"""
    btype = block.get("type")
    if btype in ("unsupported", "list"):
        return  # unsupported 永不碰；list 由 _apply_list_block 逐项处理

    # 图表段落里挂的是 <w:drawing>，任何文本回写都会把整张图删掉。
    # 正常路径上图表段落在 _walk_and_map 里就不会进 id2el（_is_unsupported_paragraph
    # 把它排除了），但这里再挡一道：结构解析与回写是两套独立代码，
    # 不能指望"另一处刚好挡住了"。
    if btype == "chart":
        return
    if _chart_rid(p) is not None:
        return

    if btype == "placeholder":
        desired = "{{" + block.get("name", "") + "}}"
    else:
        # paragraph / heading：text 是含 jinja 原文
        desired = block.get("text", "")

    if paragraph_text(p) == desired:
        return
    if not _rewrite_paragraph_text(p, desired):
        snippet = (block.get("text") or desired or "")[:24]
        warnings.append(
            "「%s…」这一段含域或嵌套内容，为保证格式与结构不被破坏，本次未改动该段文字"
            % snippet
        )


def _list_item_ids(block: Dict[str, Any]) -> List[str]:
    """列表块每一项对应的段落 id。

    新版 parse 会给每个 item 下发自己的 id；老客户端（缓存里的前端版本）不带，
    此时按「块 id 起始序号连续递增」兜底——文档里连续的 numPr 段落本来就是
    p{n}, p{n+1}…，这个假设与 parse 的聚合规则一致。
    """
    items = block.get("items", [])
    ids = [it.get("id") for it in items]
    if ids and all(ids):
        return [str(i) for i in ids]
    m = re.match(r"^p(\d+)$", str(block.get("id", "")))
    if not m:
        return [str(i) for i in ids if i]
    base = int(m.group(1))
    return ["p%d" % (base + i) for i in range(len(items))]


def _apply_list_block(block: Dict[str, Any], id2el: Dict[str, Any],
                      warnings: List[str]):
    """列表块：按 items 的 id 逐段回写。

    parse 会把连续的 numPr 段落聚合成一个 list 块，**每个 item 仍对应 document.xml
    里独立的一段**。老实现按顶层 block id 做删除判断，只有第一项有 id，其余项
    （以及它们承载的编号格式）会被当成"模型里没有的段落"整段删掉——段落一存就没了。
    """
    last = None
    for pid, it in zip(_list_item_ids(block), block.get("items", [])):
        pel = id2el.get(pid)
        if pel is None:
            continue
        last = pel
        desired = it.get("text", "")
        if paragraph_text(pel) == desired:
            continue
        if not _rewrite_paragraph_text(pel, desired):
            warnings.append("列表项「%s…」含域或嵌套内容，本次未改动"
                            % (desired or "")[:24])
    return last


# --------------------------------------------------------------------------- #
# 表格行 diff
# --------------------------------------------------------------------------- #
def _marker_row_text(kind: str, loop: Optional[Dict[str, str]]) -> str:
    if kind == "for":
        loop = loop or {}
        var = loop.get("var", "item")
        it = loop.get("iterable", "items")
        return "{%tr for " + var + " in " + it + " %}"
    return "{%tr endfor %}"


def _set_marker_row(tr, marker_text: str) -> None:
    """把一行改造为标记行：保留 trPr 与首个 tc 的 tcPr，清空其余内容，单格写标记文本。"""
    tcs = tr.findall(qn("tc"))
    # 保留第一个 tc 作承载，删掉多余 tc
    if not tcs:
        return
    keep_tc = tcs[0]
    for extra in tcs[1:]:
        tr.remove(extra)
    # 清掉 keep_tc 里除 tcPr 外的所有段落，顺手留下第一个段落的 pPr（格式兜底）
    pPr = None
    for child in list(keep_tc):
        ln = etree.QName(child).localname
        if ln == "p" and pPr is None:
            got = child.find(qn("pPr"))
            if got is not None:
                pPr = copy.deepcopy(got)
                for bad in list(pPr):
                    if etree.QName(bad).localname in _PPR_DROP:
                        pPr.remove(bad)
        if ln != "tcPr":
            keep_tc.remove(child)
    p = etree.SubElement(keep_tc, qn("p"))
    if pPr is not None:
        p.append(pPr)
    r = etree.SubElement(p, qn("r"))
    t = etree.SubElement(r, qn("t"))
    t.set(XML_SPACE, "preserve")
    t.text = marker_text


def _update_cell(tc, model_cell: Dict[str, Any], warnings: List[str]) -> None:
    """按 model_cell.blocks 更新单元格内段落；文本未变不动。"""
    existing_ps = [ch for ch in tc if etree.QName(ch).localname == "p"]
    model_blocks = model_cell.get("blocks", [])

    for idx, mb in enumerate(model_blocks):
        if idx < len(existing_ps):
            p = existing_ps[idx]
        else:
            base = existing_ps[-1] if existing_ps else None
            newp = _clone_shell(base) if base is not None else etree.SubElement(tc, qn("p"))
            tc.append(newp)
            existing_ps.append(newp)
        _apply_paragraph_block(existing_ps[idx], mb, warnings)
    # 模型里没了的多余段落删掉（单元格里的段落同样不能带走 sectPr / 书签）
    for p in existing_ps[len(model_blocks):]:
        _delete_block(tc, p, warnings)


def _row_cell_texts(tr) -> List[str]:
    out = []
    for tc in tr.findall(qn("tc")):
        out.append("".join(t.text or "" for t in tc.iter(qn("t"))))
    return out


def _block_display_text(block: Dict[str, Any]) -> str:
    """块在 docx 里的可见文本：placeholder 按 {{name}} 计（与 _apply_paragraph_block 一致）。"""
    if block.get("type") == "placeholder":
        return "{{" + block.get("name", "") + "}}"
    return block.get("text", "")


def _model_cell_text(cell: Dict[str, Any]) -> str:
    return "".join(_block_display_text(b) for b in cell.get("blocks", []))


def _reconcile_table(tbl, model_block: Dict[str, Any], id2tr: Dict[str, Any],
                     warnings: List[str]) -> None:
    """按模型 rows 重建表格行：loop_row 行包 marker，增删行，改单元格文字。"""
    existing_trs = tbl.findall(qn("tr"))
    template_tr = existing_trs[0] if existing_trs else None

    # 构造 desired 行序列
    #
    # 两种行循环写法要区别对待：
    #   ① 标签各占一整行（for 一行、endfor 一行）—— 数据行本身不带标签，
    #      所以回写时要替它把两行 marker 补回来；
    #   ② 手册 §4 的同行写法（for 在首格、endfor 在末格）—— 这一行自己就带着
    #      标签，再补 marker 就变成 marker + 带标签的数据行 + marker，渲染时
    #      多出两行空表行，甚至直接报 unknown tag 'endfor'。
    def _self_marked(row: Dict[str, Any]) -> bool:
        txt = "".join(_model_cell_text(c) for c in row.get("cells", []))
        return TR_SAME_ROW_RE.match(txt) is not None

    desired: List[tuple] = []
    for row in model_block.get("rows", []):
        needs_marker = bool(row.get("loop_row")) and not _self_marked(row)
        if needs_marker:
            desired.append(("marker_for", _marker_row_text("for", row.get("loop"))))
        desired.append(("data", row))
        if needs_marker:
            desired.append(("marker_end", _marker_row_text("end", None)))

    # 先判断是否真的需要动表（未变则整表保留）
    def existing_signature() -> List[tuple]:
        sig = []
        data_i = 0
        for tr in existing_trs:
            txt = "".join(t.text or "" for t in tr.iter(qn("t")))
            if TR_FOR_RE.match(txt.strip()):
                sig.append(("marker_for", txt.strip()))
            elif TR_END_RE.match(txt.strip()):
                sig.append(("marker_end", txt.strip()))
            else:
                sig.append(("data", data_i))
                data_i += 1
        return sig

    desired_sig = []
    data_ids = []
    for kind, payload in desired:
        if kind == "data":
            desired_sig.append(("data", len(data_ids)))
            data_ids.append(payload["id"])
        else:
            desired_sig.append((kind, payload))

    if len(desired_sig) == len(existing_signature()) and desired_sig == existing_signature():
        # 行数/标记结构一致；再逐格比对文字
        data_iter = iter([s for s in existing_trs if not (
            TR_FOR_RE.match("".join(t.text or "" for t in s.iter(qn("t"))).strip())
            or TR_END_RE.match("".join(t.text or "" for t in s.iter(qn("t"))).strip()))])
        same = True
        for row in model_block.get("rows", []):
            try:
                tr = next(data_iter)
            except StopIteration:
                same = False
                break
            if _row_cell_texts(tr) != [_model_cell_text(c)
                                       for c in row.get("cells", [])]:
                same = False
                break
        if same:
            return  # 整表未变，一个字节都不动

    # 需要动表： detach 所有 tr，按 desired 重新挂回
    for tr in existing_trs:
        tbl.remove(tr)

    prev_data_tr = None
    for kind, payload in desired:
        if kind == "data":
            row_id = payload["id"]
            if row_id in id2tr:
                tr_el = id2tr[row_id]
            else:
                anchor = prev_data_tr or template_tr
                tr_el = copy.deepcopy(anchor) if anchor is not None else etree.SubElement(tbl, qn("tr"))
                # 新增行：清掉锚点行的内容，只留段落格式。
                # 不清就会把锚点行的文字、图片、域一起复制一份；整段删光又会连
                # 单元格的对齐/缩进一起丢，所以留第一个段落的 pPr。
                if anchor is not None:
                    for tc in tr_el.findall(qn("tc")):
                        ps = [ch for ch in tc if etree.QName(ch).localname == "p"]
                        for extra in ps[1:]:
                            tc.remove(extra)
                        if ps:
                            for ch in list(ps[0]):
                                if etree.QName(ch).localname != "pPr":
                                    ps[0].remove(ch)
            for ci, cell_model in enumerate(payload.get("cells", [])):
                tcs = tr_el.findall(qn("tc"))
                if ci < len(tcs):
                    _update_cell(tcs[ci], cell_model, warnings)
            tbl.append(tr_el)
            prev_data_tr = tr_el
        else:
            tr_el = copy.deepcopy(template_tr) if template_tr is not None else etree.SubElement(tbl, qn("tr"))
            _set_marker_row(tr_el, payload)
            tbl.append(tr_el)


# --------------------------------------------------------------------------- #
# 主回写入口
# --------------------------------------------------------------------------- #
def _walk_and_map(body) -> Dict[str, Any]:
    """与 docmodel.parse 同一套 id 规则，建立 id -> element 映射 + 顺序清单。

    返回 {
      "id2el": {id: element},
      "top_supported": [(id, element)] 顺序（不含 unsupported、不含 sectPr）,
      "unsupported": [element],
    }
    """
    id2el: Dict[str, Any] = {}
    top_supported: List[tuple] = []
    unsupported: List[Any] = []

    p_counter = 0
    tbl_counter = 0
    for child in body:
        ln = etree.QName(child).localname
        if ln == "p":
            pid = f"p{p_counter}"
            p_counter += 1
            if _is_unsupported_paragraph(child):
                unsupported.append(child)
                continue
            id2el[pid] = child
            top_supported.append((pid, child))
        elif ln == "tbl":
            tid = f"t{tbl_counter}"
            tbl_counter += 1
            id2el[tid] = child
            top_supported.append((tid, child))
            # 映射行 / 单元格 / 单元格段落
            for ri, tr in enumerate(child.findall(qn("tr"))):
                rid = f"{tid}r{ri}"
                id2el[rid] = tr
                for ci, tc in enumerate(tr.findall(qn("tc"))):
                    cid = f"{rid}c{ci}"
                    id2el[cid] = tc
                    pidx = 0
                    for sub in tc:
                        if etree.QName(sub).localname == "p":
                            id2el[f"{cid}p{pidx}"] = sub
                            pidx += 1
        # sectPr / 其他：忽略

    return {"id2el": id2el, "top_supported": top_supported, "unsupported": unsupported}


def _create_paragraph_element(block: Dict[str, Any], anchor_el) -> Any:
    """新建段落：继承锚点的 pPr 与一个 rPr，然后写入文本。"""
    newp = _clone_shell(anchor_el)
    _rewrite_paragraph_text(newp, block.get("text", ""))
    return newp


def apply_model(raw: bytes, model: Dict[str, Any],
                warnings: Optional[List[str]] = None) -> bytes:
    """把块模型原地回写到 docx 字节，返回新 docx 字节。

    - 未在模型中命中的部件（样式/编号/页眉页脚/主题/字体）按字节原样复制；
    - unsupported 元素永不修改、永不删除；
    - ``warnings`` 传入列表时，把"为保证格式没敢改"的地方追加进去。
    """
    warns: List[str] = warnings if warnings is not None else []
    zf = zipfile.ZipFile(BytesIO(raw))
    root = etree.fromstring(zf.read("word/document.xml"))
    body = root.find(qn("body"))

    walk = _walk_and_map(body)
    id2el = walk["id2el"]
    top_supported = walk["top_supported"]
    model_blocks = model.get("blocks", [])

    model_ids = set()
    for b in model_blocks:
        bid = str(b.get("id"))
        if not bid.startswith("new-"):
            model_ids.add(b["id"])
        # 列表块把多个段落聚合成一个块，逐项 id 也必须算作"模型里还有"
        if b.get("type") == "list":
            for pid in _list_item_ids(b):
                model_ids.add(pid)

    # 1) 删除：模型中消失的已存在 id（unsupported 不在 top_supported 里，天然不删）
    for bid, el in top_supported:
        if bid not in model_ids:
            _delete_block(body, el, warns)

    # 2) 更新已有 + 插入 new-
    anchor = None
    sectPr = body.find(qn("sectPr"))
    for mb in model_blocks:
        bid = mb["id"]
        btype = mb.get("type")

        if str(bid).startswith("new-"):
            if btype in ("paragraph", "heading", "placeholder"):
                # 新增图片/二维码/签名/印章占位：docxtpl 里就是普通段落的纯文本
                # ``{{name}}``（渲染上下文把 {type:image,src,...} 转 InlineImage）。
                # 故构造一个 text="{{name}}" 的段落即可，kind(image/qrcode/signature/seal) 统一按 image 走。
                if btype == "placeholder":
                    mb = {**mb, "text": "{{" + mb.get("name", "") + "}}"}
                newp = _create_paragraph_element(mb, anchor)
                if anchor is not None:
                    anchor.addnext(newp)
                else:
                    # 插到 sectPr 之前
                    if sectPr is not None:
                        sectPr.addprevious(newp)
                    else:
                        body.append(newp)
                anchor = newp
            # new- 表格本期不自动构造（前端暂不产生），忽略
            continue

        el = id2el.get(bid)
        if el is None:
            continue  # 模型里有但文档里没有（陈旧 id），跳过

        if btype == "table":
            _reconcile_table(el, mb, id2el, warns)
            anchor = el
        elif btype == "list":
            last = _apply_list_block(mb, id2el, warns)
            anchor = last if last is not None else el
        else:
            _apply_paragraph_block(el, mb, warns)
            anchor = el

    new_document_xml = etree.tostring(
        root, xml_declaration=True, encoding="UTF-8", standalone=True
    )

    # 3) 重新打包：仅替换 word/document.xml，其余部件字节原样复制
    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zf.infolist():
            data = zf.read(item.filename)
            if item.filename == "word/document.xml":
                data = new_document_xml
            zout.writestr(item, data)
    zf.close()
    return out.getvalue()
