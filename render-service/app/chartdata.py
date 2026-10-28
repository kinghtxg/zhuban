# -*- coding: utf-8 -*-
"""图表数据就地替换 —— 只改数据，一个字节都不动样式。

## 为什么必须"就地改"

docxtpl 改不了图表：图表数据不存在于 `document.xml` 的文本节点里，而是在
`word/charts/chartN.xml` 的 `c:numCache` / `c:strCache` 缓存节点里。
用 `InlineImage` 把整张图换成图片是最省事的做法，但那样**样式全丢**——
系列配色、线宽、字体、图例位置、网格线、数据标签全部变成一张死图，
用户后面想在 Word 里微调都不行了。本模块走的是另一条路：只重写缓存里的
`c:pt` 序列，其余节点原样保留。

## 能改 / 不能改（这是产品边界，不是实现缺陷）

| 维度 | 是否可改 | 原因 |
|---|---|---|
| 分类名（X 轴标签） | ✅ 可改，数量可变 | 只重写 `c:cat` 的缓存 |
| 数值 | ✅ 可改，数量可变 | 只重写 `c:val` 的缓存 |
| 系列名（图例文字） | ✅ 可改 | 只重写 `c:tx` 的缓存 |
| 系列个数 | ❌ 不可增减 | 增删 `c:ser` 会让 `c:dPt`（单点样式覆盖）索引错位，
|                |           | 且新系列没有 spPr，只能落到默认样式——这正是"样式被改" |
| 图表类型 | ❌ 不可改 | 换类型必须重建 plotArea |
| 任何样式 | ❌ 不可改 | 本模块从不触碰 spPr / txPr / dLbls / ax |

系列数不一致时明确报错，而不是静默画错——静默失败比报错更伤信任。

## 双写：缓存 + 内嵌工作簿

图表有两份数据来源：
  - `c:numCache` 缓存（Word 保存时写下的快照）
  - `word/embeddings/*.xlsx` 内嵌工作簿（真实的"数据源"）
只改缓存的话，用户在 Word 里点「编辑数据」会看到旧数字，Word 也可能在打开时
按 `c:autoUpdate` 重算把改动冲掉。所以两边都写。缓存是显示用的，工作簿是
"数据是真的"这件事的证据。
"""
from __future__ import annotations

import re
import zipfile
from io import BytesIO
from typing import Any, Dict, List, Optional, Sequence, Tuple

from lxml import etree

C_NS = "http://schemas.openxmlformats.org/drawingml/2006/chart"
A_NS = "http://schemas.openxmlformats.org/drawingml/2006/main"
R_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG_REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"
SS_NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

CHART_PART_RE = re.compile(r"^word/charts/chart(\d+)\.xml$")

# 图表类型 -> 中文名（给前端显示）
CHART_TYPE_CN = {
    "lineChart": "折线图",
    "line3DChart": "三维折线图",
    "barChart": "柱状图",
    "bar3DChart": "三维柱状图",
    "pieChart": "饼图",
    "pie3DChart": "三维饼图",
    "doughnutChart": "圆环图",
    "areaChart": "面积图",
    "area3DChart": "三维面积图",
    "radarChart": "雷达图",
    "scatterChart": "散点图",
    "bubbleChart": "气泡图",
    "stockChart": "股价图",
    "surface3DChart": "三维曲面图",
}

# 这些是"图表的形状"，不是数据。改写时绝不触碰。
_STYLE_TAGS = {
    "spPr", "txPr", "dLbls", "dLbl", "dPt", "marker", "trendline", "errBars",
    "catAx", "valAx", "serAx", "dateAx", "layout", "legend", "title",
    "firstSliceAng", "holeSize", "smooth", "explosion", "varyColors",
    "grouping", "barDir", "overlap", "gapWidth", "shape", "externalData",
    "plotVisOnly", "dispBlanksAs", "showDLblsOverMax", "roundedCorners",
    "autoTitleDeleted", "view3D", "floor", "sideWall", "backWall", "pivotSource",
}


class ChartError(ValueError):
    """图表数据处理失败。code 供上层转成契约错误码。"""

    def __init__(self, message: str, chart: str = "", code: str = "chart_invalid"):
        super().__init__(message)
        self.message = message
        self.chart = chart
        self.code = code


# 单个图表部件 / 内嵌工作簿的读取上限。
# docx 本体有 10MB 上限，但 zip 的解压比可以很高（压缩炸弹），单纯靠压缩包大小
# 拦不住。这里在"读某个部件"这一层加硬上限，超限就跳过而不是把内存吃干。
MAX_CHART_PART_BYTES = 8 * 1024 * 1024
MAX_WORKBOOK_BYTES = 24 * 1024 * 1024
MAX_SHEET_XML_BYTES = 32 * 1024 * 1024


def _safe_read(zf: zipfile.ZipFile, name: str, limit: int) -> Optional[bytes]:
    """带解压上限的读取；超过上限或读取失败返回 None。"""
    try:
        info = zf.getinfo(name)
    except KeyError:
        return None
    if info.file_size > limit:
        return None
    try:
        return zf.read(name)
    except (zipfile.BadZipFile, OSError, RuntimeError):
        return None


def _c(tag: str) -> str:
    return "{%s}%s" % (C_NS, tag)


def _ln(el) -> str:
    return etree.QName(el).localname if el is not None else ""


# --------------------------------------------------------------------------- #
# 读：缓存值
# --------------------------------------------------------------------------- #
def _ref_values(container) -> Optional[Dict[str, Any]]:
    """从 c:cat / c:val / c:tx 容器里读出 (f 引用, 缓存节点, 值列表, 是否数值)。"""
    if container is None:
        return None
    for ref_tag, is_num in (("strRef", False), ("numRef", True),
                            ("strLit", False), ("numLit", True)):
        ref = container.find(_c(ref_tag))
        if ref is None:
            continue
        cache_tag = "strCache" if ref_tag.startswith("str") else "numCache"
        cache = ref.find(_c(cache_tag))
        f_el = ref.find(_c("f"))
        values: List[str] = []
        if cache is not None:
            for pt in cache.findall(_c("pt")):
                v = pt.find(_c("v"))
                values.append(v.text if v is not None and v.text is not None else "")
        return {
            "f": f_el.text if f_el is not None else None,
            "cache": cache,
            "cache_tag": cache_tag,
            "is_num": is_num,
            "values": values,
            "container_tag": ref_tag,
        }
    return None


def _chart_type_element(plot_area):
    """plotArea 下第一个真正的图表类型元素。"""
    if plot_area is None:
        return None
    for child in plot_area:
        ln = _ln(child)
        if ln in CHART_TYPE_CN:
            return child
    return None


def _series_elements(chart_type_el) -> List[Any]:
    return chart_type_el.findall(_c("ser"))


def _series_name(ser) -> str:
    tx = ser.find(_c("tx"))
    if tx is None:
        return ""
    info = _ref_values(tx)
    if info and info["values"]:
        return str(info["values"][0])
    v = tx.find(_c("v"))
    if v is not None and v.text:
        return v.text
    return ""


def _series_cat_container(ser):
    """分类轴数据。散点图用 c:xVal。"""
    cat = ser.find(_c("cat"))
    if cat is not None:
        return cat, "cat"
    xval = ser.find(_c("xVal"))
    if xval is not None:
        return xval, "xVal"
    return None, None


def _series_val_container(ser):
    """数值。散点图用 c:yVal。"""
    val = ser.find(_c("val"))
    if val is not None:
        return val, "val"
    yval = ser.find(_c("yVal"))
    if yval is not None:
        return yval, "yVal"
    return None, None


def _chart_title(chart_el) -> str:
    title = chart_el.find(_c("title"))
    if title is None:
        return ""
    return "".join(t.text or "" for t in title.iter(_c("v"))).strip() or \
        "".join(t.text or "" for t in title.iter("{%s}t" % A_NS)).strip()


def _read_chart(chart_xml: bytes) -> Dict[str, Any]:
    root = etree.fromstring(chart_xml)
    plot = root.find("%s/%s" % (_c("chart"), _c("plotArea")))
    type_el = _chart_type_element(plot)
    if type_el is None:
        return {"kind": "", "kind_name": "", "title": "", "categories": [],
                "series": [], "root": root}
    kind = _ln(type_el)
    chart_el = root.find(_c("chart"))
    categories: List[str] = []
    series: List[Dict[str, Any]] = []
    for ser in _series_elements(type_el):
        cat_cont, _ = _series_cat_container(ser)
        val_cont, _ = _series_val_container(ser)
        cat_info = _ref_values(cat_cont)
        val_info = _ref_values(val_cont)
        if not categories and cat_info:
            categories = list(cat_info["values"])
        series.append({
            "name": _series_name(ser),
            "values": list(val_info["values"]) if val_info else [],
        })
    return {
        "kind": kind,
        "kind_name": CHART_TYPE_CN.get(kind, kind),
        "title": _chart_title(chart_el) if chart_el is not None else "",
        "categories": categories,
        "series": series,
        "root": root,
    }


# --------------------------------------------------------------------------- #
# 列表：枚举模板里的所有图表
# --------------------------------------------------------------------------- #
def list_charts(docx_bytes: bytes) -> List[Dict[str, Any]]:
    """返回 [{index, part, kind, kind_name, title, categories, series}]，按 chartN 排序。"""
    zf = zipfile.ZipFile(BytesIO(docx_bytes))
    out: List[Dict[str, Any]] = []
    names = [n for n in zf.namelist() if CHART_PART_RE.match(n)]
    names.sort(key=lambda n: int(CHART_PART_RE.match(n).group(1)))
    for name in names:
        raw = _safe_read(zf, name, MAX_CHART_PART_BYTES)
        if raw is None:
            continue
        try:
            info = _read_chart(raw)
        except etree.LxmlError:
            continue
        if not info["kind"]:
            continue
        out.append({
            "index": int(CHART_PART_RE.match(name).group(1)),
            "part": name,
            "kind": info["kind"],
            "kind_name": info["kind_name"],
            "title": info["title"],
            "categories": info["categories"],
            "series": info["series"],
            "series_names": [s["name"] for s in info["series"]],
            "series_count": len(info["series"]),
            "point_count": len(info["categories"]),
        })
    zf.close()
    return out


# --------------------------------------------------------------------------- #
# 写：重写缓存
# --------------------------------------------------------------------------- #
def _fmt_num(v: Any) -> str:
    if isinstance(v, bool):
        raise ChartError("图表数值不能是布尔值")
    if isinstance(v, (int,)):
        return str(v)
    if isinstance(v, float):
        if v != v or v in (float("inf"), float("-inf")):
            raise ChartError("图表数值不能是 NaN / Infinity")
        # 避免 1.0 被写成 "1.0" 之后的浮点噪声；最多保留 6 位小数
        s = ("%.6f" % v).rstrip("0").rstrip(".")
        return s if s not in ("", "-") else "0"
    s = str(v).strip()
    if s == "":
        return ""
    # 允许 "1,234.50" 这类人工输入
    try:
        float(s.replace(",", ""))
    except ValueError:
        raise ChartError("图表数值「%s」不是合法数字" % s[:20])
    return s.replace(",", "")


def _write_cache(cache_el, values: Sequence[str], is_num: bool) -> None:
    """只重写 c:ptCount 与 c:pt 序列，保留 c:formatCode 等其它子节点。"""
    if cache_el is None:
        raise ChartError("图表缺少数据缓存节点，无法写入")
    # 记住 formatCode 的位置，其它 pt/ptCount 全清掉
    format_el = cache_el.find(_c("formatCode"))
    for pt in cache_el.findall(_c("pt")):
        cache_el.remove(pt)
    for pc in cache_el.findall(_c("ptCount")):
        cache_el.remove(pc)

    pt_count = etree.SubElement(cache_el, _c("ptCount"))
    pt_count.set("val", str(len(values)))
    # ptCount 必须排在 formatCode 之后
    if format_el is not None:
        cache_el.remove(pt_count)
        cache_el.insert(list(cache_el).index(format_el) + 1, pt_count)

    for i, v in enumerate(values):
        pt = etree.SubElement(cache_el, _c("pt"))
        pt.set("idx", str(i))
        v_el = etree.SubElement(pt, _c("v"))
        v_el.text = _fmt_num(v) if is_num else str(v)


def _legend_name(value: str) -> str:
    """系列名清洗：控制字符会让 Word 判文档损坏。"""
    s = str(value).replace("\r", " ").replace("\n", " ").strip()
    return "".join(ch for ch in s if ch == "\t" or ord(ch) >= 0x20)


def _apply_to_chart(chart_xml: bytes, payload: Dict[str, Any],
                    chart_label: str) -> Tuple[bytes, Dict[str, Any]]:
    """把一份 payload 写进单个 chart XML。返回 (新 XML, 变更摘要)。"""
    info = _read_chart(chart_xml)
    if not info["kind"]:
        raise ChartError("图表结构无法识别（plotArea 里没有图表类型节点）", chart_label)
    type_el = _chart_type_element(info["root"].find("%s/%s" % (_c("chart"), _c("plotArea"))))
    sers = _series_elements(type_el)

    new_cats = payload.get("categories")
    new_sers = payload.get("series")
    if not isinstance(new_sers, list) or not new_sers:
        raise ChartError("图表数据缺少 series 数组", chart_label)

    # 系列数必须一致：增删系列只能落到默认样式，等价于"样式被改"
    if len(new_sers) != len(sers):
        raise ChartError(
            "系列数不匹配：模板有 %d 个系列（%s），传入 %d 个。"
            "只改数据不改样式的前提下不能增删系列"
            % (len(sers), "、".join(_series_name(s) or "系列%d" % (i + 1)
                                   for i, s in enumerate(sers)), len(new_sers)),
            chart_label, "chart_series_mismatch")

    changed_points = 0
    for ser, want in zip(sers, new_sers):
        if not isinstance(want, dict):
            raise ChartError("series 的每一项都必须是 {name, values} 对象", chart_label)

        # 1) 系列名
        if want.get("name") is not None:
            tx = ser.find(_c("tx"))
            if tx is not None:
                tx_info = _ref_values(tx)
                if tx_info and tx_info["cache"] is not None:
                    _write_cache(tx_info["cache"], [_legend_name(want["name"])],
                                 tx_info["is_num"])
                else:
                    # 没有缓存（纯字符串 c:v）时改 c:v
                    v = tx.find(_c("v"))
                    if v is not None:
                        v.text = _legend_name(want["name"])

        # 2) 分类
        cat_cont, _ = _series_cat_container(ser)
        if new_cats is not None:
            cat_info = _ref_values(cat_cont) if cat_cont is not None else None
            if cat_info and cat_info["cache"] is not None:
                if not isinstance(new_cats, (list, tuple)):
                    raise ChartError("categories 必须是数组", chart_label)
                _write_cache(cat_info["cache"], list(new_cats), cat_info["is_num"])
                changed_points += len(new_cats)

        # 3) 数值
        vals = want.get("values")
        if vals is None:
            continue
        if not isinstance(vals, (list, tuple)):
            raise ChartError("series[].values 必须是数组", chart_label)
        val_cont, _ = _series_val_container(ser)
        val_info = _ref_values(val_cont) if val_cont is not None else None
        if not val_info or val_info["cache"] is None:
            raise ChartError("系列「%s」没有数值缓存节点，无法写入" % _series_name(ser),
                             chart_label)
        _write_cache(val_info["cache"], list(vals), True)
        changed_points += len(vals)

    return (etree.tostring(info["root"], xml_declaration=True,
                           encoding="UTF-8", standalone=True),
            {"kind": info["kind"], "series": len(sers),
             "points": len(new_cats) if new_cats is not None
             else len(info["categories"]),
             "changed_values": changed_points})


# --------------------------------------------------------------------------- #
# 内嵌工作簿同步
# --------------------------------------------------------------------------- #
_RANGE_RE = re.compile(r"^(?:'([^']+)'|([^!]+))!\$?([A-Z]+)\$?(\d+)(?::\$?([A-Z]+)\$?(\d+))?$")


def _parse_ref(ref: str) -> Optional[Dict[str, Any]]:
    """'Sheet1!$A$2:$A$5' -> {sheet, col, row, col2, row2}。"""
    if not ref:
        return None
    m = _RANGE_RE.match(ref.strip())
    if not m:
        return None
    sheet = m.group(1) or m.group(2)
    return {
        "sheet": sheet,
        "col": m.group(3), "row": int(m.group(4)),
        "col2": m.group(5) or m.group(3), "row2": int(m.group(6) or m.group(4)),
    }


def _col_idx(letters: str) -> int:
    n = 0
    for ch in letters:
        n = n * 26 + (ord(ch) - 64)
    return n


def _col_letters(n: int) -> str:
    s = ""
    while n:
        n, rem = divmod(n - 1, 26)
        s = chr(65 + rem) + s
    return s


def _xlsx_sheet_path(zf: zipfile.ZipFile, want: str) -> Optional[str]:
    """按工作表名找 sheet part；找不到就退回第一个工作表。"""
    try:
        wb = etree.fromstring(zf.read("xl/workbook.xml"))
    except (KeyError, etree.LxmlError):
        return None
    rels = {}
    try:
        rel_root = etree.fromstring(zf.read("xl/_rels/workbook.xml.rels"))
        for rel in rel_root:
            rels[rel.get("Id")] = rel.get("Target")
    except (KeyError, etree.LxmlError):
        pass

    sheets = []
    for sh in wb.iter("{%s}sheet" % SS_NS):
        rid = sh.get("{%s}id" % R_NS)
        target = rels.get(rid, "")
        if target and not target.startswith("/"):
            target = "xl/" + target.lstrip("./")
        else:
            target = target.lstrip("/")
        sheets.append((sh.get("name") or "", target))

    for name, path in sheets:
        if name and want and name.strip().lower() == want.strip().lower():
            return path
    return sheets[0][1] if sheets else None


def _set_sheet_cells(sheet_xml: bytes, updates: Dict[Tuple[str, int], str]) -> bytes:
    """在 sheetData 里精确写单元格。updates: {(列字母, 行号): 值}。"""
    root = etree.fromstring(sheet_xml)
    sd = root.find("{%s}sheetData" % SS_NS)
    if sd is None:
        return sheet_xml

    rows: Dict[int, Any] = {}
    for row in sd.findall("{%s}row" % SS_NS):
        try:
            rows[int(row.get("r"))] = row
        except (TypeError, ValueError):
            continue

    for (col, rownum), value in updates.items():
        row = rows.get(rownum)
        if row is None:
            row = etree.SubElement(sd, "{%s}row" % SS_NS)
            row.set("r", str(rownum))
            rows[rownum] = row
            # 行要按 r 升序
            order = sorted(rows.items())
            for r, _ in order:
                sd.remove(rows[r])
            for r, _ in order:
                sd.append(rows[r])

        ref = "%s%d" % (col, rownum)
        cell = None
        for cc in row.findall("{%s}c" % SS_NS):
            if cc.get("r") == ref:
                cell = cc
                break
        if cell is None:
            cell = etree.SubElement(row, "{%s}c" % SS_NS)
            cell.set("r", ref)
            cells = [x for x in row.findall("{%s}c" % SS_NS)]
            cells.sort(key=lambda x: _col_idx(re.sub(r"\d", "", x.get("r") or "A")))
            for x in cells:
                row.remove(x)
            for x in cells:
                row.append(x)

        # 清掉旧值/类型
        for child in list(cell):
            cell.remove(child)
        is_num = False
        try:
            float(str(value))
            is_num = True
        except (TypeError, ValueError):
            pass
        if is_num:
            if "t" in cell.attrib:
                del cell.attrib["t"]
            v = etree.SubElement(cell, "{%s}v" % SS_NS)
            v.text = str(value)
        else:
            cell.set("t", "inlineStr")
            is_el = etree.SubElement(cell, "{%s}is" % SS_NS)
            t_el = etree.SubElement(is_el, "{%s}t" % SS_NS)
            t_el.text = str(value)

    return etree.tostring(root, xml_declaration=True, encoding="UTF-8", standalone=True)


def _sync_workbook(parts: Dict[str, bytes], chart_part: str,
                   payload: Dict[str, Any]) -> Optional[str]:
    """把新数据同步进图表的内嵌工作簿。返回写入的 xlsx 路径或 None。"""
    rels_part = "word/charts/_rels/%s.rels" % chart_part.split("/")[-1]
    raw_rels = parts.get(rels_part)
    if not raw_rels:
        return None
    try:
        rel_root = etree.fromstring(raw_rels)
    except etree.LxmlError:
        return None
    emb_path = None
    for rel in rel_root:
        if (rel.get("Type") or "").endswith("/package"):
            tgt = (rel.get("Target") or "").replace("../", "word/")
            emb_path = tgt if tgt.startswith("word/") else "word/" + tgt.lstrip("./")
            break
    if not emb_path or emb_path not in parts:
        return None
    if len(parts[emb_path]) > MAX_WORKBOOK_BYTES:
        return None

    try:
        xin = zipfile.ZipFile(BytesIO(parts[emb_path]))
    except zipfile.BadZipFile:
        return None

    # 从 chart XML 的 c:f 里拿到"该写哪个区域"，而不是假设固定版式
    info = _read_chart(parts[chart_part])
    type_el = _chart_type_element(info["root"].find("%s/%s" % (_c("chart"), _c("plotArea"))))
    updates: Dict[Tuple[str, int], str] = {}
    sheet_name = None

    def _push(cont, values: Sequence[Any], is_num: bool = False) -> None:
        """按 c:f 声明的区域把 values 线性写进去，支持竖向与横向两种排布。"""
        nonlocal sheet_name
        if cont is None or values is None:
            return
        ref_info = _ref_values(cont)
        if not ref_info or not ref_info["f"]:
            return
        rng = _parse_ref(ref_info["f"])
        if not rng:
            return
        sheet_name = sheet_name or rng["sheet"]
        horizontal = rng["col"] != rng["col2"]
        for i, v in enumerate(values):
            text = _fmt_num(v) if is_num else _legend_name(v)
            if horizontal:
                updates[(_col_letters(_col_idx(rng["col"]) + i), rng["row"])] = text
            else:
                updates[(rng["col"], rng["row"] + i)] = text

    new_cats = payload.get("categories")
    for ser, want in zip(_series_elements(type_el), payload.get("series") or []):
        if new_cats is not None:
            _push(_series_cat_container(ser)[0], list(new_cats))
        if want.get("values") is not None:
            _push(_series_val_container(ser)[0], list(want["values"]), is_num=True)
        if want.get("name") is not None:
            _push(ser.find(_c("tx")), [want["name"]])

    if not updates:
        xin.close()
        return None

    target_sheet = _xlsx_sheet_path(xin, sheet_name or "")
    if not target_sheet or target_sheet not in xin.namelist():
        xin.close()
        return None

    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in xin.infolist():
            if item.filename == target_sheet and item.file_size > MAX_SHEET_XML_BYTES:
                xin.close()
                return None
            data = xin.read(item.filename)
            if item.filename == target_sheet:
                data = _set_sheet_cells(data, updates)
            zout.writestr(item, data)
    xin.close()
    parts[emb_path] = out.getvalue()
    return emb_path


# --------------------------------------------------------------------------- #
# 对外：把 data 里的图表数据写回 docx
# --------------------------------------------------------------------------- #
def chart_var_name(chart: Dict[str, Any], used: Optional[set] = None) -> str:
    """图表变量名：chart_<标题>，标题不可用则用序号。"""
    used = used if used is not None else set()
    base = re.sub(r"[\s\u3000]+", "", chart.get("title") or "")
    base = re.sub(r"[^0-9A-Za-z_\u4e00-\u9fff]+", "", base)
    if not base:
        base = "图%d" % chart.get("index", 0)
    name = "chart_%s" % base
    if name not in used:
        used.add(name)
        return name
    i = 2
    while "%s_%d" % (name, i) in used:
        i += 1
    name = "%s_%d" % (name, i)
    used.add(name)
    return name


def apply_chart_data(docx_bytes: bytes, data: Dict[str, Any]) -> Tuple[bytes, List[Dict[str, Any]]]:
    """把 data 中形如 chart_* 的键写回对应图表。

    返回 (新 docx 字节, 变更明细列表)。没有图表或没有图表数据时原样返回，
    保证对不含图表的模板零影响。
    """
    charts = list_charts(docx_bytes)
    if not charts:
        return docx_bytes, []

    used: set = set()
    mapping: Dict[str, Dict[str, Any]] = {}
    for ch in charts:
        name = chart_var_name(ch, used)
        mapping[name] = ch

    plan = []
    for name, ch in mapping.items():
        if name in data and isinstance(data[name], dict):
            plan.append((name, ch, data[name]))
    if not plan:
        return docx_bytes, []

    zin = zipfile.ZipFile(BytesIO(docx_bytes))
    parts = {n: zin.read(n) for n in zin.namelist()}
    zin.close()

    changes: List[Dict[str, Any]] = []
    for name, ch, payload in plan:
        new_xml, summary = _apply_to_chart(parts[ch["part"]], payload, name)
        parts[ch["part"]] = new_xml
        changed_xlsx = _sync_workbook(parts, ch["part"], payload)
        changes.append({
            "variable": name,
            "part": ch["part"],
            "kind": ch["kind"],
            "kind_name": ch["kind_name"],
            "series": summary["series"],
            "points": summary["points"],
            "workbook": changed_xlsx,
        })

    out = BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for name, raw in parts.items():
            zout.writestr(name, raw)
    return out.getvalue(), changes


# --------------------------------------------------------------------------- #
# 契约：schema 与投递校验
# --------------------------------------------------------------------------- #
# 单个图表允许的上限。不是性能考虑，是防误用：一个 20 万点的数组写进
# chart XML 会让 Word 直接卡死，而且几乎一定是程序 bug 而不是真实需求。
MAX_POINTS = 2000
MAX_CATEGORY_LEN = 200


def payload_schema() -> Dict[str, Any]:
    """图表变量在 json_schema 里的形状。"""
    return {
        "type": "object",
        "required": ["series"],
        "properties": {
            "categories": {"type": "array", "items": {"type": "string"}},
            "series": {
                "type": "array",
                "items": {
                    "type": "object",
                    "required": ["values"],
                    "properties": {
                        "name": {"type": "string"},
                        "values": {"type": "array", "items": {"type": "number"}},
                    },
                },
            },
        },
    }


def validate_payload(chart_meta: Dict[str, Any], payload: Any,
                     path: str = "") -> List[Dict[str, str]]:
    """校验一份图表投递数据，返回 [{path, message}]。空列表表示合法。

    在"试渲染"阶段就报出来，比等到生成文档才发现数字写错要好得多。
    """
    name = path or chart_meta.get("title") or "chart"
    errs: List[Dict[str, str]] = []

    def bad(msg: str, sub: str = "") -> None:
        errs.append({"path": ("%s.%s" % (name, sub)) if sub else name, "message": msg})

    if not isinstance(payload, dict):
        bad("应为对象 {categories, series}")
        return errs

    series = payload.get("series")
    if series is None:
        bad("缺少 series 数组", "series")
        return errs
    if not isinstance(series, list):
        bad("应为数组", "series")
        return errs

    expect = chart_meta.get("series_count")
    if isinstance(expect, int) and expect > 0 and len(series) != expect:
        hint = "、".join(chart_meta.get("series_names") or [])
        bad("模板有 %d 个系列%s，传入 %d 个。只改数据不改样式，不能增删系列"
            % (expect, ("（%s）" % hint) if hint else "", len(series)), "series")

    cats = payload.get("categories")
    if cats is not None:
        if not isinstance(cats, list):
            bad("应为数组", "categories")
            cats = None
        elif len(cats) > MAX_POINTS:
            bad("分类数 %d 超过上限 %d" % (len(cats), MAX_POINTS), "categories")
        else:
            for i, c in enumerate(cats):
                if isinstance(c, (dict, list)):
                    bad("第 %d 项不能是对象/数组" % (i + 1), "categories")
                elif len(str(c)) > MAX_CATEGORY_LEN:
                    bad("第 %d 项超过 %d 字" % (i + 1, MAX_CATEGORY_LEN), "categories")

    for si, s in enumerate(series):
        if not isinstance(s, dict):
            bad("第 %d 个系列应为对象 {name, values}" % (si + 1), "series")
            continue
        if s.get("name") is not None:
            if isinstance(s["name"], (dict, list)):
                bad("系列名不能是对象/数组", "series")
            elif len(str(s["name"])) > MAX_CATEGORY_LEN:
                bad("系列名超过 %d 字" % MAX_CATEGORY_LEN, "series")
        vals = s.get("values")
        if vals is None:
            bad("第 %d 个系列缺少 values" % (si + 1), "series")
            continue
        if not isinstance(vals, list):
            bad("第 %d 个系列的 values 应为数组" % (si + 1), "series")
            continue
        if len(vals) > MAX_POINTS:
            bad("第 %d 个系列点数 %d 超过上限 %d" % (si + 1, len(vals), MAX_POINTS), "series")
        if cats is not None and len(vals) != len(cats):
            bad("第 %d 个系列有 %d 个数值，但 categories 有 %d 个，两者必须相等"
                % (si + 1, len(vals), len(cats)), "series")
        for vi, v in enumerate(vals):
            if isinstance(v, bool):
                bad("第 %d 个系列第 %d 个值是布尔值，应为数字" % (si + 1, vi + 1), "series")
                continue
            if isinstance(v, (dict, list)):
                bad("第 %d 个系列第 %d 个值是对象/数组，应为数字" % (si + 1, vi + 1), "series")
                continue
            try:
                f = float(str(v).replace(",", ""))
            except (TypeError, ValueError):
                bad("第 %d 个系列第 %d 个值「%s」不是数字" % (si + 1, vi + 1, str(v)[:20]),
                    "series")
                continue
            if f != f or f in (float("inf"), float("-inf")):
                bad("第 %d 个系列第 %d 个值是 NaN/Infinity" % (si + 1, vi + 1), "series")

    return errs


# --------------------------------------------------------------------------- #
# 样式不变性的机器可验证断言（测试与自检共用）
# --------------------------------------------------------------------------- #
def style_fingerprint(chart_xml: bytes) -> str:
    """把 chart XML 里所有"样式相关"的内容抽成指纹。

    做法：深拷贝整棵树，剥掉纯数据节点（cache / pt / ptCount / f），
    剩下的按规范化字符串输出。数据变了指纹必须不变 —— 这就是
    "只改数据不改样式"的可验证定义。
    """
    root = etree.fromstring(chart_xml)
    clone = etree.fromstring(etree.tostring(root))
    for tag in ("numCache", "strCache", "numLit", "strLit"):
        for el in clone.iter(_c(tag)):
            parent = el.getparent()
            if parent is None:
                continue
            # 保留容器本身（它带 cache 的 formatCode），只清数据点
            for child in list(el):
                ln = _ln(child)
                if ln in ("pt", "ptCount", "f") or ln.endswith("Cache"):
                    el.remove(child)
    for el in clone.iter(_c("f")):
        el.text = None
    return etree.tostring(clone, encoding="unicode")
