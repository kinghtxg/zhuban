# -*- coding: utf-8 -*-
"""扫描语义：枚举提取、循环字段归属、点引用后缀、嵌套循环。

这几条都是「扫错了也不报错」的静默缺陷：变量清单里多一个填不了的字段、
少一个非填不可的字段，界面照常渲染、校验照常全绿，只有生成出来的文档
里留着空白或者整段消失。所以必须有明确的断言钉住。
"""
import copy
from io import BytesIO

from docx import Document

from app.render import validate_data
from app.scanner import scan_docx


def _docx(*texts):
    doc = Document()
    for t in texts:
        doc.add_paragraph(t)
    buf = BytesIO()
    doc.save(buf)
    return buf.getvalue()


def _var(scan, name):
    for v in scan["variables"]:
        if v["name"] == name:
            return v
    return None


def _loop_field(scan, loop, field):
    v = _var(scan, loop)
    assert v is not None, f"未扫出循环 {loop}"
    for f in v["fields"]:
        if f["name"] == field:
            return f
    return None


# ---------------------------------------------------------------- 枚举

def test_enum_from_chinese_comparison():
    """`{% if 风险等级 == "高" %}` → 枚举，且示例值取枚举值而不是占位文本。"""
    scan = scan_docx(_docx("等级：{{ 风险等级 }}", '{% if 风险等级 == "高" %}警示{% endif %}'))
    v = _var(scan, "风险等级")
    assert v["kind"] == "scalar"
    assert v["enum"] == ["高"]
    assert v["sample"] == "高", "示例值必须落在枚举内，否则示例 JSON 自身通不过校验"
    assert scan["json_schema"]["properties"]["风险等级"]["enum"] == ["高"]
    assert scan["sample_json"]["风险等级"] == "高"


def test_enum_single_quote_and_multi_value():
    """单引号合法；多个比较值合并成同一字段的候选集，不重复。"""
    scan = scan_docx(_docx(
        "{{ 状态 }}",
        "{% if 状态 == '通过' %}A{% endif %}",
        '{% if 状态 == "驳回" %}B{% endif %}',
        "{% if 状态 == '通过' %}C{% endif %}",
    ))
    assert _var(scan, "状态")["enum"] == ["通过", "驳回"]


def test_enum_does_not_create_ghost_condition():
    """只被比较、正文没引用的字段也要出现，且**不是**布尔开关。

    原先它只落进 conditions，界面把它画成一个开关；现在归一化成字符串
    枚举字段 —— 否则用户面对一个 True/False 开关，根本没地方填「高」。
    """
    scan = scan_docx(_docx('{% if 风险等级 == "高" %}警示{% endif %}'))
    v = _var(scan, "风险等级")
    assert v is not None, "只在 if 里出现的枚举字段不能整条消失"
    assert v["kind"] == "scalar"
    assert v["enum"] == ["高"]
    kinds = [x["kind"] for x in scan["variables"] if x["name"] == "风险等级"]
    assert kinds == ["scalar"], "不能同时登记成条件和标量（界面上会出现两行同名变量）"


# ---------------------------------------------------------------- 点引用

def test_dotted_ref_with_suffix():
    """`{{ it.备注 or "—" }}` / `{{ it.完成 if ok else "" }}` 里的字段要扫出来。

    点引用用整串锚定匹配时，这两个字段一个都扫不到 → 用户看不到也填不了，
    生成的文档里那两列永远是空的。
    """
    scan = scan_docx(_docx(
        "{% for it in 交付清单 %}",
        '{{ it.名称 }} / {{ it.备注 or "—" }}',
        '{{ "已完成" if it.完成 else "进行中" }}',
        "{% endfor %}",
    ))
    fields = [f["name"] for f in _var(scan, "交付清单")["fields"]]
    assert fields == ["名称", "备注", "完成"]


def test_string_literal_not_a_field():
    """条件表达式里的字面量不是变量（「已完成」「进行中」不该进变量清单）。"""
    scan = scan_docx(_docx(
        "{% for it in X %}",
        '{{ "已完成" if it.完成 else "进行中" }}',
        "{% endfor %}",
    ))
    sample = scan["sample_json"]["X"][0]
    assert set(sample.keys()) == {"完成"}


# ---------------------------------------------------------------- 循环归属

def test_same_loop_var_does_not_cross_contaminate():
    """两个不相干的循环都用 `it`，字段不能互相串。

    中文模板里循环变量几乎都写成 it，只按变量名归属会把内层字段写到外层
    头上：界面让用户填一堆不属于这个表的字段，而真正的字段缺失报错。
    """
    scan = scan_docx(_docx(
        "{% for it in 甲 %}{{ it.名称 }}{% endfor %}",
        "{% for it in 乙 %}{{ it.数量 }}{% endfor %}",
    ))
    assert [f["name"] for f in _var(scan, "甲")["fields"]] == ["名称"]
    assert [f["name"] for f in _var(scan, "乙")["fields"]] == ["数量"]


def test_nested_loop_attached_to_parent():
    """嵌套循环不产生顶层 `g.条目`，数据挂在父项内部。"""
    scan = scan_docx(_docx(
        "{% for g in 问题分组 %}",
        "{{ g.分组 }}（{{ g.条目 | length }} 项）",
        "{% for it in g.条目 %}",
        "{{ it.问题 }}",
        "{% endfor %}",
        "{% endfor %}",
    ))
    names = [v["name"] for v in scan["variables"]]
    assert "g.条目" not in names, "子循环不能成为顶层变量：示例 JSON 会把它平铺在外面"

    parent = _var(scan, "问题分组")
    entry = [f for f in parent["fields"] if f["name"] == "条目"]
    assert len(entry) == 1, "同名数组字段只能出现一次（| length 已经登记过一次）"
    assert entry[0]["kind"] == "loop"
    assert [f["name"] for f in entry[0]["fields"]] == ["问题"]

    item = scan["sample_json"]["问题分组"][0]
    assert isinstance(item["条目"], list) and item["条目"], "示例数据必须嵌套，否则照抄必渲染空表"
    assert "问题" in item["条目"][0]


# ---------------------------------------------------------------- 校验

def test_validate_rejects_bad_enum():
    """写错枚举值必须被拦下 —— 否则警示块静默消失，而接口 200。"""
    tpl = _docx("等级：{{ 风险等级 }}", '{% if 风险等级 == "高" %}警示{% endif %}')
    ok = validate_data(tpl, {"风险等级": "高"})
    assert ok["valid"] is True, ok

    bad = validate_data(tpl, {"风险等级": "高危"})
    assert bad["valid"] is False
    assert bad["errors"] == [
        {"path": "风险等级", "message": "取值必须是 高 之一（当前为「高危」）"}
    ], "报错要回显当前值，否则用户在两三个近义词之间猜是哪一处打错"

    # 空串是「这次不填」，不报枚举错（缺失/必填是另一回事）
    assert all(e["path"] != "风险等级" for e in validate_data(tpl, {"风险等级": ""})["errors"])


def test_validate_rejects_bad_enum_inside_loop():
    tpl = _docx("{% for it in 清单 %}{{ it.等级 }}{% if it.等级 == \"高\" %}!{% endif %}{% endfor %}")
    data = {"清单": [{"等级": "高"}]}
    assert validate_data(tpl, copy.deepcopy(data))["valid"] is True

    bad = {"清单": [{"等级": "高"}, {"等级": "极高"}]}
    errs = validate_data(tpl, bad)["errors"]
    assert errs == [{"path": "清单[1].等级", "message": "取值必须是 高 之一（当前为「极高」）"}], errs


def test_validate_rejects_bad_enum_inside_nested_loop():
    tpl = _docx(
        "{% for g in 分组 %}",
        "{% for it in g.条目 %}{{ it.等级 }}{% if it.等级 == \"低\" %}L{% endif %}{% endfor %}",
        "{% endfor %}",
    )
    data = {"分组": [{"条目": [{"等级": "低"}, {"等级": "中"}]}]}
    errs = validate_data(tpl, data)["errors"]
    assert errs == [{"path": "分组[0].条目[1].等级", "message": "取值必须是 低 之一（当前为「中」）"}], errs
