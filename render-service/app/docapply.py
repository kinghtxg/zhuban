# -*- coding: utf-8 -*-
"""块级模型 -> docx 原地回写（PRD V2 §7.1，/apply 核心）。

回写语义（与前端 diff 契约冻结，Go 工程师按此对接）：
  1. 同 id 的 paragraph/heading/placeholder：按模型更新文字/名称；
     **保留该段首个 run 的 rPr 与段落 pPr**（样式/字体/字号/对齐/缩进/eastAsia 字体），
     只重建 run 文本；jinja 标签（{{ }}、{% %}、{%tr %}）按原文写入。
     —— 仅当新文本与当前文本不同时才动 run；文本未变的段落 XML 一个字节都不改。
  2. 模型中消失的 id：删除对应段落/表格行；``new-`` 前缀块：按其在 blocks 数组中的
     位置（锚点 = 相邻同父级已存在 id）插入新段落，继承锚点样式。
  3. 表格：按模型 rows 与单元格内容 diff，支持行列增删、单元格文字改、循环行标记。
     loop_row=true 的数据行，在其前/后各包一个独立标记行：
       前 = ``{%tr for var in iterable %}``，后 = ``{%tr endfor %}``
     （docxtpl 的 patch_xml 会把含 {%tr %} 的整行替换成 jinja 块，故标记必须独立成行，
     不能塞进数据行的首/末单元格——经验证同行写法会触发 jinja 语法错误。）
  4. 独立 image/qrcode/rich 占位块：仅改标签引用 ``{{name}}``；图片二进制替换不在本服务做。
  5. unsupported 块及其余所有未在模型中命中的 XML：**原样保留，一个字节都不碰**；
     页眉页脚、样式表、编号、主题、字体设置全部不动（zip 里这些部件按字节原样复制）。

实现：zipfile 读原包，lxml 定点改 word/document.xml，其余部件字节复制另存。
"""
from __future__ import annotations

import copy
import re
import zipfile
from io import BytesIO
from typing import Any, Dict, List, Optional

from lxml import etree

from .docmodel import (
    W, qn, paragraph_text, extract_tokens,
    TR_FOR_RE, TR_END_RE, _is_unsupported_paragraph, _chart_rid, XML_SPACE,
)


# --------------------------------------------------------------------------- #
# 段落文本重建：保留 pPr + 首个 run 的 rPr，只换 w:t
# --------------------------------------------------------------------------- #
def _rewrite_paragraph_text(p, new_text: str) -> None:
    """把段落可视文本重建为 new_text。

    - 保留 pPr（段落属性：样式/对齐/缩进/编号等）；
    - 快照首个带 rPr 的 run 的 rPr（字体/字号/加粗/eastAsia），套到新 run 上；
    - 移除所有 w:r / w:hyperlink，追加一个新 run 承载 new_text（xml:space=preserve，
      保证 jinja 标签前后空格不被吞）。
    """
    first_rpr = None
    for r in p.findall(qn("r")):
        rpr = r.find(qn("rPr"))
        if rpr is not None:
            first_rpr = copy.deepcopy(rpr)
            break

    # 移除 run / hyperlink（保留 pPr、bookmarkStart/End 等其他节点）
    for child in list(p):
        ln = etree.QName(child).localname
        if ln in ("r", "hyperlink"):
            p.remove(child)

    new_r = etree.SubElement(p, qn("r"))
    if first_rpr is not None:
        new_r.append(first_rpr)
    t = etree.SubElement(new_r, qn("t"))
    t.set(XML_SPACE, "preserve")
    t.text = new_text


def _apply_paragraph_block(p, block: Dict[str, Any]) -> None:
    """按块模型更新一个段落元素；文本未变则完全不动该元素。"""
    btype = block.get("type")
    if btype == "unsupported":
        return  # 永不碰 unsupported

    # 图表段落里挂的是 <w:drawing>，任何文本回写都会把整张图删掉。
    # 正常路径上图表段落在 _walk_and_map 里就不会进 id2el（_is_unsupported_paragraph
    # 把它排除了），但这里再挡一道：结构解析与回写是两套独立代码，
    # 不能指望"另一处刚好挡住了"。
    if btype == "chart":
        return
    if _chart_rid(p) is not None:
        return

    if btype == "placeholder":
        name = block.get("name", "")
        desired = "{{" + name + "}}"
        if paragraph_text(p) != desired:
            _rewrite_paragraph_text(p, desired)
        return

    # paragraph / heading：text 是含 jinja 原文
    desired = block.get("text", "")
    if paragraph_text(p) != desired:
        _rewrite_paragraph_text(p, desired)


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
    # 清掉 keep_tc 里除 tcPr 外的所有段落
    for child in list(keep_tc):
        if etree.QName(child).localname != "tcPr":
            keep_tc.remove(child)
    p = etree.SubElement(keep_tc, qn("p"))
    r = etree.SubElement(p, qn("r"))
    t = etree.SubElement(r, qn("t"))
    t.set(XML_SPACE, "preserve")
    t.text = marker_text


def _update_cell(tc, model_cell: Dict[str, Any]) -> None:
    """按 model_cell.blocks 更新单元格内段落；文本未变不动。"""
    existing_ps = [ch for ch in tc if etree.QName(ch).localname == "p"]
    model_blocks = model_cell.get("blocks", [])

    for idx, mb in enumerate(model_blocks):
        if idx < len(existing_ps):
            p = existing_ps[idx]
        else:
            base = existing_ps[-1] if existing_ps else None
            newp = copy.deepcopy(base) if base is not None else etree.SubElement(tc, qn("p"))
            tc.append(newp)
            existing_ps.append(newp)
        _apply_paragraph_block(existing_ps[idx], mb)
    # 模型里没了的多余段落删掉
    for p in existing_ps[len(model_blocks):]:
        tc.remove(p)


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


def _reconcile_table(tbl, model_block: Dict[str, Any], id2tr: Dict[str, Any]) -> None:
    """按模型 rows 重建表格行：loop_row 行包 marker，增删行，改单元格文字。"""
    existing_trs = tbl.findall(qn("tr"))
    template_tr = existing_trs[0] if existing_trs else None

    # 构造 desired 行序列
    desired: List[tuple] = []
    for row in model_block.get("rows", []):
        if row.get("loop_row"):
            desired.append(("marker_for", _marker_row_text("for", row.get("loop"))))
        desired.append(("data", row))
        if row.get("loop_row"):
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
            for ci, cell_model in enumerate(payload.get("cells", [])):
                tcs = tr_el.findall(qn("tc"))
                if ci < len(tcs):
                    _update_cell(tcs[ci], cell_model)
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
    """新建一个段落元素（new- 前缀块）：尽量克隆锚点段落继承 pPr/rPr。"""
    if anchor_el is not None and etree.QName(anchor_el).localname == "p":
        newp = copy.deepcopy(anchor_el)
    else:
        newp = etree.Element(qn("p"))
    text = block.get("text", "")
    _rewrite_paragraph_text(newp, text)
    return newp


def apply_model(raw: bytes, model: Dict[str, Any]) -> bytes:
    """把块模型原地回写到 docx 字节，返回新 docx 字节。

    - 未在模型中命中的部件（样式/编号/页眉页脚/主题/字体）按字节原样复制；
    - unsupported 元素永不修改、永不删除。
    """
    zf = zipfile.ZipFile(BytesIO(raw))
    root = etree.fromstring(zf.read("word/document.xml"))
    body = root.find(qn("body"))

    walk = _walk_and_map(body)
    id2el = walk["id2el"]
    top_supported = walk["top_supported"]
    model_blocks = model.get("blocks", [])
    model_ids = {b["id"] for b in model_blocks if not str(b["id"]).startswith("new-")}

    # 1) 删除：模型中消失的已存在 id（unsupported 不在 top_supported 里，天然不删）
    for bid, el in top_supported:
        if bid not in model_ids:
            body.remove(el)

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
            _reconcile_table(el, mb, id2el)
        else:
            _apply_paragraph_block(el, mb)
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
