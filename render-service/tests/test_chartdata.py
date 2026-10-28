# -*- coding: utf-8 -*-
"""图表数据替换：单元级测试矩阵。

被测对象：app/chartdata.py（只改数据、不改样式的核心约束）

判据的核心是 C11/C12 两条：
  - C11 样式指纹：把 chart XML 里除数据缓存外的内容抽成指纹，改数据前后必须逐字节一致；
  - C12 其它部件：document.xml / styles.xml 的字节必须完全不变。
只要这两条成立，"样式没被改"就是机器可以验证的事实，不是"我觉得没改"。

可直接 `python tests/test_chartdata.py` 跑，也兼容 pytest。
"""
import copy
import json
import os
import sys
import zipfile
from io import BytesIO

RENDER_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
for p in (RENDER_DIR, os.path.join(RENDER_DIR, "tests")):
    if p not in sys.path:
        sys.path.insert(0, p)

from app import chartdata  # noqa: E402
from app.chartdata import (  # noqa: E402
    ChartError, apply_chart_data, chart_var_name, list_charts,
    style_fingerprint, validate_payload,
)
from chart_fixtures import (  # noqa: E402
    build_chart_docx, sample_bar_chart, sample_line_chart, sample_pie_chart,
)

LINE = "word/charts/chart1.xml"
PIE = "word/charts/chart2.xml"


def _parts(raw: bytes) -> dict:
    z = zipfile.ZipFile(BytesIO(raw))
    return {n: z.read(n) for n in z.namelist()}


def _cache_values(chart_xml: bytes) -> dict:
    """抽出一个图表里所有系列的 (名称, 分类, 数值)，用于断言数据真的变了。"""
    info = chartdata._read_chart(chart_xml)
    return {
        "categories": info["categories"],
        "series": [{"name": s["name"], "values": s["values"]} for s in info["series"]],
    }


def _line_payload(**over):
    p = {
        "categories": ["1月", "2月", "3月", "4月"],
        "series": [
            {"name": "去年", "values": [100, 200, 300, 400]},
            {"name": "今年", "values": [150, 250, 350, 450]},
        ],
    }
    p.update(over)
    return p


def _pie_payload(**over):
    p = {"categories": ["软件", "服务", "订阅"],
         "series": [{"name": "占比", "values": [60, 25, 15]}]}
    p.update(over)
    return p


# --------------------------------------------------------------------------- #
# 功能：C01–C10
# --------------------------------------------------------------------------- #
def test_C01_line_values_changed():
    raw = build_chart_docx([sample_line_chart()])
    out, changes = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    assert len(changes) == 1 and changes[0]["kind"] == "lineChart"
    got = _cache_values(_parts(out)[LINE])
    assert got["series"][0]["values"] == ["100", "200", "300", "400"]
    assert got["series"][1]["values"] == ["150", "250", "350", "450"]


def test_C02_categories_changed():
    raw = build_chart_docx([sample_line_chart()])
    out, _ = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    assert _cache_values(_parts(out)[LINE])["categories"] == ["1月", "2月", "3月", "4月"]


def test_C03_multi_series_both_changed():
    raw = build_chart_docx([sample_line_chart()])
    out, _ = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    s = _cache_values(_parts(out)[LINE])["series"]
    assert s[0]["values"] == ["100", "200", "300", "400"]
    assert s[1]["values"] == ["150", "250", "350", "450"]


def test_C04_pie_values_changed():
    raw = build_chart_docx([sample_pie_chart()])
    out, changes = apply_chart_data(raw, {"chart_收入构成": _pie_payload()})
    assert changes[0]["kind"] == "pieChart"
    # 这份文档里只有一个图表，所以它是 chart1 而不是 chart2
    assert _cache_values(_parts(out)[LINE])["series"][0]["values"] == ["60", "25", "15"]


def test_C05_bar_values_changed():
    raw = build_chart_docx([sample_bar_chart()])
    out, changes = apply_chart_data(
        raw, {"chart_漏洞分布": {"categories": ["高", "中", "低"],
                                 "series": [{"name": "数量", "values": [1, 2, 3]}]}})
    assert changes[0]["kind"] == "barChart"
    assert _cache_values(_parts(out)[LINE])["series"][0]["values"] == ["1", "2", "3"]


def test_C06_points_increased():
    """4 点 -> 6 点：点数可变是产品承诺的一部分（新季度、新月份）。"""
    raw = build_chart_docx([sample_line_chart()])
    cats = ["1月", "2月", "3月", "4月", "5月", "6月"]
    payload = {"categories": cats,
               "series": [{"name": "去年", "values": [10, 20, 30, 40, 50, 60]},
                          {"name": "今年", "values": [11, 21, 31, 41, 51, 61]}]}
    out, changes = apply_chart_data(raw, {"chart_季度销售额": payload})
    got = _cache_values(_parts(out)[LINE])
    assert len(got["categories"]) == 6
    assert got["series"][0]["values"] == ["10", "20", "30", "40", "50", "60"]
    assert changes[0]["points"] == 6


def test_C07_points_decreased():
    raw = build_chart_docx([sample_line_chart()])
    payload = {"categories": ["上半年", "下半年"],
               "series": [{"name": "去年", "values": [300, 500]},
                          {"name": "今年", "values": [400, 600]}]}
    out, _ = apply_chart_data(raw, {"chart_季度销售额": payload})
    got = _cache_values(_parts(out)[LINE])
    assert got["categories"] == ["上半年", "下半年"]
    assert got["series"][1]["values"] == ["400", "600"]


def test_C08_series_name_changed():
    raw = build_chart_docx([sample_line_chart()])
    out, _ = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    names = [s["name"] for s in _cache_values(_parts(out)[LINE])["series"]]
    assert names == ["去年", "今年"]


def test_C09_two_charts_at_once():
    raw = build_chart_docx([sample_line_chart(), sample_pie_chart()])
    out, changes = apply_chart_data(raw, {
        "chart_季度销售额": _line_payload(),
        "chart_收入构成": _pie_payload(),
    })
    assert len(changes) == 2
    p = _parts(out)
    assert _cache_values(p[LINE])["categories"] == ["1月", "2月", "3月", "4月"]
    assert _cache_values(p[PIE])["series"][0]["values"] == ["60", "25", "15"]


def test_C10_only_one_chart_touched():
    """只传一个图表时，另一个图表必须一个字节都不动。"""
    raw = build_chart_docx([sample_line_chart(), sample_pie_chart()])
    before = _parts(raw)
    out, changes = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    after = _parts(out)
    assert len(changes) == 1
    assert changes[0]["variable"] == "chart_季度销售额"
    assert before[PIE] == after[PIE], "未传入的饼图被改动了"


# --------------------------------------------------------------------------- #
# 样式不变：C11–C13（这是本能力的存在理由）
# --------------------------------------------------------------------------- #
def test_C11_style_fingerprint_unchanged():
    raw = build_chart_docx([sample_line_chart(), sample_pie_chart()])
    before = _parts(raw)
    fp_before = {p: style_fingerprint(before[p]) for p in (LINE, PIE)}
    out, _ = apply_chart_data(raw, {"chart_季度销售额": _line_payload(),
                                    "chart_收入构成": _pie_payload()})
    after = _parts(out)
    for p in (LINE, PIE):
        assert style_fingerprint(after[p]) == fp_before[p], \
            "%s 的样式相关内容被改动了" % p


def test_C12_other_parts_byte_identical():
    raw = build_chart_docx([sample_line_chart()])
    before = _parts(raw)
    out, _ = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    after = _parts(out)
    changed = [n for n in before if before[n] != after.get(n)]
    # 只有图表 XML 与内嵌工作簿允许变
    for n in changed:
        assert n == LINE or "embeddings" in n, "不该改动的部件被改了：%s" % n
    assert sorted(after) == sorted(before), "部件清单发生变化"


def test_C13_workbook_structure_preserved():
    """内嵌工作簿里只有单元格值可以变，包结构（其它 part）必须原样。"""
    raw = build_chart_docx([sample_line_chart()])
    out, changes = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    assert changes[0]["workbook"] == "word/embeddings/data1.xlsx"
    x1 = zipfile.ZipFile(BytesIO(_parts(raw)["word/embeddings/data1.xlsx"]))
    x2 = zipfile.ZipFile(BytesIO(_parts(out)["word/embeddings/data1.xlsx"]))
    assert sorted(x1.namelist()) == sorted(x2.namelist())
    for n in x1.namelist():
        if n == "xl/worksheets/sheet1.xml":
            continue
        assert x1.read(n) == x2.read(n), "工作簿里 %s 被改动了" % n
    sheet = x2.read("xl/worksheets/sheet1.xml").decode()
    assert "<v>100</v>" in sheet and "<v>450</v>" in sheet


# --------------------------------------------------------------------------- #
# 边界：C14–C22
# --------------------------------------------------------------------------- #
def test_C14_series_count_mismatch_rejected():
    raw = build_chart_docx([sample_line_chart()])
    payload = {"categories": ["a", "b"], "series": [{"name": "x", "values": [1, 2]}]}
    try:
        apply_chart_data(raw, {"chart_季度销售额": payload})
    except ChartError as e:
        assert e.code == "chart_series_mismatch"
        assert "不能增删系列" in e.message
        return
    raise AssertionError("系列数不匹配时应当报错，而不是静默画错")


def test_C15_values_not_array():
    raw = build_chart_docx([sample_line_chart()])
    payload = {"series": [{"name": "a", "values": 1}, {"name": "b", "values": 2}]}
    try:
        apply_chart_data(raw, {"chart_季度销售额": payload})
    except ChartError as e:
        assert "必须是数组" in e.message
        return
    raise AssertionError("values 不是数组时应当报错")


def test_C16_non_numeric_value():
    raw = build_chart_docx([sample_line_chart()])
    payload = {"categories": ["a", "b"],
               "series": [{"name": "a", "values": [1, "abc"]},
                          {"name": "b", "values": [1, 2]}]}
    try:
        apply_chart_data(raw, {"chart_季度销售额": payload})
    except ChartError as e:
        assert "不是合法数字" in e.message
        return
    raise AssertionError("非数字值时应当报错")


def test_C17_nan_inf_bool_rejected():
    meta = {"title": "t", "series_count": 2}
    for bad in (float("nan"), float("inf"), True):
        errs = validate_payload(meta, {"categories": ["a"], "series": [
            {"name": "x", "values": [bad]}, {"name": "y", "values": [1]}]})
        assert errs, "%r 应当被拒绝" % (bad,)


def test_C18_length_mismatch_reported():
    meta = {"title": "t", "series_count": 2}
    errs = validate_payload(meta, {"categories": ["a", "b", "c"], "series": [
        {"name": "x", "values": [1, 2]}, {"name": "y", "values": [1, 2]}]})
    assert any("必须相等" in e["message"] for e in errs), errs


def test_C19_negative_decimal_large():
    raw = build_chart_docx([sample_line_chart()])
    payload = {"categories": ["a", "b", "c", "d"],
               "series": [{"name": "x", "values": [-12.5, 0, 1234567890, 0.001]},
                          {"name": "y", "values": [1, 2, 3, 4]}]}
    out, _ = apply_chart_data(raw, {"chart_季度销售额": payload})
    vals = _cache_values(_parts(out)[LINE])["series"][0]["values"]
    assert vals == ["-12.5", "0", "1234567890", "0.001"], vals


def test_C20_empty_categories():
    """空数组不应崩，但要能表达"没有数据点"。"""
    raw = build_chart_docx([sample_line_chart()])
    payload = {"categories": [], "series": [{"name": "x", "values": []},
                                            {"name": "y", "values": []}]}
    out, _ = apply_chart_data(raw, {"chart_季度销售额": payload})
    assert _cache_values(_parts(out)[LINE])["categories"] == []


def test_C21_chart_var_name_safe():
    used = set()
    n1 = chart_var_name({"title": "季度 销售/额（万）", "index": 1}, used)
    assert n1 == "chart_季度销售额万", n1
    # 同名图表必须派生不同变量名（否则后一个会覆盖前一个的数据）
    n2 = chart_var_name({"title": "季度 销售/额（万）", "index": 2}, used)
    assert n2 != n1, (n1, n2)
    assert n2.endswith("_2"), n2
    n3 = chart_var_name({"title": "", "index": 3}, used)
    assert n3 == "chart_图3", n3


def test_C22_no_chart_data_returns_original():
    raw = build_chart_docx([sample_line_chart()])
    out, changes = apply_chart_data(raw, {"某个文本变量": "值"})
    assert changes == []
    assert _parts(out) == _parts(raw), "无图表数据时文档不应被重写"


# --------------------------------------------------------------------------- #
# 安全：C23–C26
# --------------------------------------------------------------------------- #
def test_C23_xml_injection_escaped():
    """分类名里的 XML 元字符必须被转义，否则 docx 直接损坏。"""
    raw = build_chart_docx([sample_line_chart()])
    evil = '<script>alert(1)</script> & "quote"'
    payload = {"categories": [evil] * 4,
               "series": [{"name": "<b>x</b>", "values": [1, 2, 3, 4]},
                          {"name": "y", "values": [1, 2, 3, 4]}]}
    out, _ = apply_chart_data(raw, {"chart_季度销售额": payload})
    parts = _parts(out)
    from lxml import etree
    etree.fromstring(parts[LINE])  # 必须仍是合法 XML
    got = _cache_values(parts[LINE])
    assert got["categories"][0] == evil
    assert got["series"][0]["name"] == "<b>x</b>"


def test_C24_long_category_rejected():
    meta = {"title": "t", "series_count": 1}
    errs = validate_payload(meta, {"categories": ["x" * 300],
                                   "series": [{"name": "a", "values": [1]}]})
    assert any("超过" in e["message"] for e in errs), errs


def test_C25_too_many_points_rejected():
    meta = {"title": "t", "series_count": 1}
    n = chartdata.MAX_POINTS + 1
    errs = validate_payload(meta, {"series": [{"name": "a", "values": [1] * n}]})
    assert any("上限" in e["message"] for e in errs), errs


def test_C26_control_chars_in_series_name_stripped():
    raw = build_chart_docx([sample_line_chart()])
    payload = {"categories": ["a", "b", "c", "d"],
               "series": [{"name": "坏\x00名\x07字", "values": [1, 2, 3, 4]},
                          {"name": "y", "values": [1, 2, 3, 4]}]}
    out, _ = apply_chart_data(raw, {"chart_季度销售额": payload})
    parts = _parts(out)
    from lxml import etree
    etree.fromstring(parts[LINE])
    name = _cache_values(parts[LINE])["series"][0]["name"]
    assert "\x00" not in name and "\x07" not in name, repr(name)


# --------------------------------------------------------------------------- #
# 回归：C27–C28
# --------------------------------------------------------------------------- #
def test_C27_no_chart_template_untouched():
    from docx import Document
    doc = Document()
    doc.add_paragraph("普通文档 {{变量}}")
    buf = BytesIO()
    doc.save(buf)
    raw = buf.getvalue()
    assert list_charts(raw) == []
    out, changes = apply_chart_data(raw, {"chart_x": {"series": []}})
    assert changes == []
    assert out == raw


def test_C28_real_template_unaffected():
    path = os.path.join(os.path.dirname(RENDER_DIR), "samples", "templates",
                        "audit-report.docx")
    if not os.path.exists(path):
        return
    with open(path, "rb") as f:
        raw = f.read()
    assert list_charts(raw) == []
    out, changes = apply_chart_data(raw, {"chart_任何": {"series": []}})
    assert changes == [] and out == raw


def test_C29_oversized_chart_part_skipped():
    """压缩炸弹防护：部件解压后超过上限就跳过，不能把内存吃干。"""
    raw = build_chart_docx([sample_line_chart()])
    old = chartdata.MAX_CHART_PART_BYTES
    chartdata.MAX_CHART_PART_BYTES = 10
    try:
        assert list_charts(raw) == [], "超限部件应当被跳过"
        out, changes = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
        assert changes == []
        assert _parts(out) == _parts(raw)
    finally:
        chartdata.MAX_CHART_PART_BYTES = old


def test_C30_chart_without_embedded_workbook():
    """图表没有内嵌工作簿（Word 里被删过数据）时，只靠缓存也要能改。"""
    raw = build_chart_docx([sample_line_chart()], with_workbook=False)
    out, changes = apply_chart_data(raw, {"chart_季度销售额": _line_payload()})
    assert len(changes) == 1
    assert changes[0]["workbook"] is None, "不该凭空造一个工作簿出来"
    assert _cache_values(_parts(out)[LINE])["series"][0]["values"] == \
        ["100", "200", "300", "400"]


if __name__ == "__main__":
    ok = fail = 0
    names = sorted(n for n in globals() if n.startswith("test_"))
    for name in names:
        fn = globals()[name]
        try:
            fn()
            print("PASS  %s" % name)
            ok += 1
        except AssertionError as e:
            print("FAIL  %s -> %s" % (name, e))
            fail += 1
        except Exception as e:  # noqa: BLE001
            print("ERROR %s -> %s: %s" % (name, type(e).__name__, e))
            fail += 1
    print("\n%d passed, %d failed" % (ok, fail))
    sys.exit(1 if fail else 0)
