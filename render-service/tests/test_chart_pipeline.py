# -*- coding: utf-8 -*-
"""图表能力：扫描 / 渲染 / 校验 / 编辑器模型 四段链路的集成测试。

单元测试（test_chartdata.py）保证"改数据不改样式"，这份保证**接进产品链路后仍然成立**，
以及一个很容易被忽略的致命点：

  P08 —— 图表所在段落一旦被当成"普通空段落"，用户在编辑器里改相邻文字或触发
  apply，`_rewrite_paragraph_text` 会把整个 <w:drawing> 删掉，图表直接消失。
  这条不是理论风险：docmodel 原先只把"文本框/形状/浮动锚定"判为不可编辑，
  内联图表正好漏在中间。

可直接 `python tests/test_chart_pipeline.py` 跑，也兼容 pytest。
"""
import os
import sys
import zipfile
from io import BytesIO

RENDER_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
for p in (RENDER_DIR, os.path.join(RENDER_DIR, "tests")):
    if p not in sys.path:
        sys.path.insert(0, p)

from app.chartdata import list_charts, style_fingerprint  # noqa: E402
from app.docapply import apply_model  # noqa: E402
from app.docmodel import parse_docx  # noqa: E402
from app.render import render_template, validate_data  # noqa: E402
from app.scanner import scan_docx  # noqa: E402
from chart_fixtures import build_chart_docx, sample_line_chart, sample_pie_chart  # noqa: E402

LINE = "word/charts/chart1.xml"

NEW_DATA = {
    "chart_季度销售额": {
        "categories": ["1月", "2月", "3月"],
        "series": [{"name": "去年", "values": [11, 22, 33]},
                   {"name": "今年", "values": [44, 55, 66]}],
    }
}


def _docx(charts=None) -> bytes:
    return build_chart_docx(charts or [sample_line_chart()])


def _parts(raw: bytes) -> dict:
    z = zipfile.ZipFile(BytesIO(raw))
    return {n: z.read(n) for n in z.namelist()}


# --------------------------------------------------------------------------- #
# 扫描
# --------------------------------------------------------------------------- #
def test_P01_scan_produces_chart_variable():
    res = scan_docx(_docx())
    charts = [v for v in res["variables"] if v["kind"] == "chart"]
    assert len(charts) == 1, res["variables"]
    v = charts[0]
    assert v["name"] == "chart_季度销售额"
    assert v["chart_kind"] == "lineChart"
    assert v["chart_kind_name"] == "折线图"
    assert v["series_count"] == 2
    assert v["point_count"] == 4
    assert v["required"] is False, "图表不传应沿用模板原数据，不该判为必填"


def test_P02_sample_json_is_renderable():
    """示例数据必须能直接跑通试渲染 —— 否则用户第一步就卡住。"""
    raw = _docx()
    res = scan_docx(raw)
    sample = res["sample_json"]
    assert "chart_季度销售额" in sample
    ch = sample["chart_季度销售额"]
    assert len(ch["series"]) == 2
    # 数值应是真数字而不是字符串（用户要在表格里直接编辑）
    assert all(isinstance(x, (int, float)) for x in ch["series"][0]["values"])
    out = render_template(raw, sample)
    got = list_charts(out)[0]
    assert got["series"][0]["values"] == ["120", "180", "150", "220"]


def test_P03_render_applies_chart_data():
    raw = _docx()
    out = render_template(raw, dict(NEW_DATA))
    got = list_charts(out)[0]
    assert got["categories"] == ["1月", "2月", "3月"]
    assert got["series"][0]["values"] == ["11", "22", "33"]
    assert got["series"][1]["values"] == ["44", "55", "66"]


def test_P04_render_without_chart_data_keeps_template_data():
    """不传图表数据时，图表必须保持模板里的原数据（不是清空、不是报错）。"""
    raw = _docx()
    out = render_template(raw, {"无关变量": "x"})
    got = list_charts(out)[0]
    assert got["categories"] == ["Q1", "Q2", "Q3", "Q4"]
    assert got["series"][0]["values"] == ["120", "180", "150", "220"]


def test_P05_validate_reports_series_mismatch():
    raw = _docx()
    bad = {"chart_季度销售额": {"categories": ["a"], "series": [
        {"name": "x", "values": [1]}]}}
    res = validate_data(raw, bad)
    assert res["valid"] is False
    assert any("不能增删系列" in e["message"] for e in res["errors"]), res["errors"]


def test_P06_validate_passes_when_chart_omitted():
    raw = _docx()
    res = validate_data(raw, {})
    assert res["valid"] is True, res["errors"]


def test_P07_validate_passes_with_good_chart_data():
    raw = _docx()
    res = validate_data(raw, dict(NEW_DATA))
    assert res["valid"] is True, res["errors"]


# --------------------------------------------------------------------------- #
# 编辑器模型（P08 是这条链路上最容易出人命的一条）
# --------------------------------------------------------------------------- #
def test_P08_chart_block_not_treated_as_paragraph():
    model = parse_docx(_docx())
    charts = [b for b in model["blocks"] if b["type"] == "chart"]
    assert len(charts) == 1, model["blocks"]
    blk = charts[0]
    assert blk["kind_name"] == "折线图"
    assert blk["title"] == "季度销售额"
    assert blk["series_count"] == 2 and blk["point_count"] == 4
    # 图表是正常能力，不该出现在"降级提示"里
    assert not any("图表" in w and "原样保留" in w for w in model["warnings"]), model["warnings"]


def test_P09_apply_keeps_chart_drawing():
    """改相邻段落文字后，图表必须还在（_rewrite_paragraph_text 不会误删 drawing）。"""
    raw = _docx()
    model = parse_docx(raw)
    # 找一个普通段落改成新文字
    target = next(b for b in model["blocks"]
                  if b["type"] == "paragraph" and b.get("text"))
    target["text"] = "这一段的文字被改过了"
    out = apply_model(raw, {"blocks": model["blocks"]})
    parts = _parts(out)
    doc = parts["word/document.xml"].decode()
    assert "这一段的文字被改过了" in doc
    assert "<c:chart" in doc or "graphicData" in doc, "图表 drawing 被删掉了"
    assert list_charts(out), "apply 之后图表不可读，说明结构被破坏"


def test_P10_apply_preserves_chart_style():
    raw = _docx([sample_line_chart(), sample_pie_chart()])
    before = _parts(raw)
    model = parse_docx(raw)
    for b in model["blocks"]:
        if b["type"] == "paragraph" and b.get("text"):
            b["text"] += "（改）"
    out = apply_model(raw, {"blocks": model["blocks"]})
    after = _parts(out)
    assert style_fingerprint(before[LINE]) == style_fingerprint(after[LINE])


def test_P11_variable_name_matches_editor_block():
    """扫描给出的变量名必须和编辑器块上标注的一致，否则用户对不上号。"""
    raw = _docx([sample_line_chart(), sample_pie_chart()])
    names = [v["name"] for v in scan_docx(raw)["variables"] if v["kind"] == "chart"]
    blocks = [b["variable"] for b in parse_docx(raw)["blocks"] if b["type"] == "chart"]
    assert names == blocks, (names, blocks)


def test_P12_render_then_parse_still_see_chart():
    """渲染产物再进编辑器（用户会下载后再上传），图表仍要能被识别。"""
    raw = _docx()
    out = render_template(raw, dict(NEW_DATA))
    model = parse_docx(out)
    assert [b["type"] for b in model["blocks"]].count("chart") == 1
    assert list_charts(out)[0]["categories"] == ["1月", "2月", "3月"]


if __name__ == "__main__":
    ok = fail = 0
    for name in sorted(n for n in globals() if n.startswith("test_")):
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
