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


# ---------------------------------------------------------------- 引用写法的各种变体
#
# 下面这一组说的是同一件事：**字段扫不出来，用户就填不了，而扫描全绿**。
# 模板作者写占位符的手法是散的（点引用、方括号、or 兜底、行内三目、写在
# if 里），scanner 之前是按写法一个个补的，补一种漏一种。现在统一走
# scanner._refs()，这几条用例就是钉住它的边界。

def test_subscript_access_yields_loop_field():
    """`{{ it["备注"] }}` 与 `{{ it.备注 }}` 等价。

    中文键做不了合法标识符，从别的系统迁过来的模板作者天生更爱写方括号。
    扫不出这一列，用户在界面上根本看不到「备注」，表格里那一列恒为空。
    """
    scan = scan_docx(_docx("{% for it in 清单 %}{{ it[\"备注\"] }}{% endfor %}"))
    assert _loop_field(scan, "清单", "备注") is not None
    assert scan["sample_json"]["清单"][0]["备注"] == "（待填）"


def test_subscript_and_dot_mixed_single_quote():
    """单引号下标、与点引用混用，字段集合取并集且不重复。"""
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{{ it['编号'] }}{{ it.名称 }}{{ it[\"编号\"] }}{% endfor %}"
    ))
    fields = [f["name"] for f in _var(scan, "清单")["fields"]]
    assert fields == ["编号", "名称"], fields


def test_loop_field_referenced_in_if_belongs_to_loop():
    """`{% if it.完成 %}` 里的字段归循环，不归顶层开关。

    原先 if 分支只抽裸名字：循环内一个字段都扫不出来（示例 JSON 的循环项是
    空对象 {}），顶层却多出一个布尔「完成」。同一份模板，正文写
    `{{ it.完成 }}` 是对的、写在 if 里就错——用户猜不到这个规律。
    """
    scan = scan_docx(_docx(
        "{% for it in 清单 %}",
        "{% if it.完成 %}已完成{% endif %}",
        "{% endfor %}",
    ))
    assert _var(scan, "完成") is None, "循环字段不该在顶层变成开关"
    assert _loop_field(scan, "清单", "完成") is not None
    assert scan["sample_json"]["清单"][0], "循环项不能是空对象"


def test_loop_field_in_if_with_comparison_keeps_enum():
    """if 里的循环字段既归位、又保留枚举提取（两条逻辑不能互相吃掉）。"""
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{% if it.等级 == \"高\" %}!{% endif %}{{ it.等级 }}{% endfor %}"
    ))
    f = _loop_field(scan, "清单", "等级")
    assert f is not None and f["enum"] == ["高"]


def test_inline_ternary_condition_is_boolean():
    """`{{ it.完成 if 已交付 else "" }}` 里的 已交付 是开关，不是文本框。

    当文本处理的话示例值会是「（待填）」——非空字符串恒为真，用户拿到手的
    示例 JSON 一渲染就走 if 分支，永远看不出这个开关是干嘛的。
    """
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{{ it.完成 if 已交付 else \"\" }}{% endfor %}"
    ))
    v = _var(scan, "已交付")
    assert v is not None and v["kind"] == "condition"
    assert scan["sample_json"]["已交付"] is True
    assert _loop_field(scan, "清单", "完成") is not None


def test_inline_ternary_value_stays_fillable():
    """三目里的**取值**仍然是文本字段，不能跟着条件一起变成开关。

    `{{ 项目名称 if 已交付 else "" }}`：项目名称要人填，已交付才是开关。
    一锅端的话界面上只有一个 True/False，用户没地方写项目名，而且示例值
    True 会原样印在正文里（渲染成字符串 "True"）。
    """
    scan = scan_docx(_docx("{{ 项目名称 if 已交付 else \"\" }}"))
    p = _var(scan, "项目名称")
    assert p is not None and p["kind"] == "scalar"
    assert scan["sample_json"]["项目名称"] == "（待填）"
    c = _var(scan, "已交付")
    assert c is not None and c["kind"] == "condition"


def test_inline_ternary_in_loop_keeps_both_sides():
    """循环里的三目：then 分支的词是循环字段，条件仍是顶层开关。"""
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{{ \"完成\" if it.完成 else \"未完成\" }}{{ it.名称 }}{% endfor %}"
    ))
    fields = [f["name"] for f in _var(scan, "清单")["fields"]]
    assert fields == ["完成", "名称"], fields
    assert _var(scan, "完成") is None
    assert _var(scan, "未完成") is None, "字面量不是变量"


def test_or_fallback_field_and_filter_do_not_leak_ghost_vars():
    """`or 兜底` 要留下字段名，`| default(...)` 不能把 default 变成变量。

    这两个方向都要防：漏了字段用户填不了，多了假变量用户一脸问号。
    """
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{{ it.备注 or \"—\" }}{{ it.名称 | default(\"—\") }}{% endfor %}",
        "{{ 附件 | default(\"—\") }}",
    ))
    assert _loop_field(scan, "清单", "备注") is not None
    assert _loop_field(scan, "清单", "名称") is not None
    assert _var(scan, "default") is None, "过滤器名不是变量"
    assert _var(scan, "附件") is not None


def test_arithmetic_and_builtins_do_not_become_variables():
    """`{{ 序号 + 1 }}`、`{{ loop.index }}` 不能生成名为「1」「loop」的变量。"""
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{{ loop.index }}|{{ it.序号 + 1 }}{% endfor %}"
    ))
    names = [v["name"] for v in scan["variables"]]
    assert names == ["清单"], names
    assert [f["name"] for f in _var(scan, "清单")["fields"]] == ["序号"]


def test_pipe_length_keeps_base_variable():
    """`{{ 清单 | length }}` 在循环外引用数组：基名要留下，别整条被过滤器吃掉。"""
    scan = scan_docx(_docx(
        "{% for it in 清单 %}{{ it.名称 }}{% endfor %}",
        "共 {{ 清单 | length }} 项",
    ))
    assert _var(scan, "清单") is not None
    assert _var(scan, "length") is None
