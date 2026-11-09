# -*- coding: utf-8 -*-
r"""嵌套循环 + 中文循环变量 / 中文字段名。

模型：`{%p for 章 in 章节 %}{%p for 条 in 章.条款 %}`。数据必须挂在父项内部
（`章节[i].条款[j]`），所以子循环绝不能变成顶层变量。

scanner 里识别子循环的那一行原先写 `^({IDENT})\.([中文字符集]+)$`，基名用
**纯 ASCII** 的 IDENT。中文模板的嵌套循环几乎必然写成 `章.条款`——基名
匹配不上，子循环就退化成顶层变量，名字叫「章.条款」。后果不是「少一个字段」：

  · schema.required 里多出顶层 key「章.条款」，而正确数据是 章节[i].条款
    → 校验报「字段缺失」，**嵌套循环模板一次都投不进去**（接口 200、页面
    只显示一个红字，用户根本想不到是扫描器把子循环当成顶层了）；
  · 变量面板出现「章.条款」这么个看不懂的输入项，真正要填的 章节 项里
    反而没有 条款。

这条路径此前测不出来，因为既有用例（test_scan_semantics）用的是 ASCII
循环变量 `g.条目` —— 正好绕开了这个分支。
"""
import re
import zipfile
from io import BytesIO

from docx import Document

from app.render import render_template, validate_data
from app.scanner import scan_docx

CHAPTERS = [
    {"序号": "一", "名称": "总则",
     "条款": [{"编号": "（一）", "内容": "适用于本市行政区域。"},
              {"编号": "（二）", "内容": "市城市管理部门负责。"}]},
    {"序号": "二", "名称": "经营许可",
     "条款": [{"编号": "（一）", "内容": "应当取得经营许可证。"}]},
]


def _docx(paragraphs):
    d = Document()
    for t in paragraphs:
        d.add_paragraph(t)
    buf = BytesIO()
    d.save(buf)
    return buf.getvalue()


def _nested_cjk():
    return _docx([
        "{%p for 章 in 章节 %}",
        "{{ 章.序号 }}、{{ 章.名称 }}",
        "{%p for 条 in 章.条款 %}",
        "{{ 条.编号 }}{{ 条.内容 }}",
        "{%p endfor %}",
        "{%p endfor %}",
    ])


def _nested_ascii():
    return _docx([
        "{%p for c in 章节 %}",
        "{{ c.序号 }}、{{ c.名称 }}",
        "{%p for it in c.条款 %}",
        "{{ it.编号 }}{{ it.内容 }}",
        "{%p endfor %}",
        "{%p endfor %}",
    ])


def _by_name(result):
    return {v["name"]: v for v in result["variables"]}


def test_no_top_level_variable_named_after_child_loop():
    """「章.条款」不能成为顶层变量——正确数据是 章节[i].条款。"""
    vs = _by_name(scan_docx(_nested_cjk()))
    assert "章.条款" not in vs, f"子循环被挂到了最外层：{list(vs)}"
    assert set(vs) == {"章节"}, list(vs)


def test_child_loop_is_a_field_of_parent():
    vs = _by_name(scan_docx(_nested_cjk()))
    parent = vs["章节"]
    assert parent["kind"] == "loop"
    names = [f["name"] for f in parent["fields"]]
    assert "序号" in names and "名称" in names, names
    child = [f for f in parent["fields"] if f["name"] == "条款"]
    assert child, f"父循环里没有 条款 字段：{names}"
    assert child[0]["kind"] == "loop", child[0]
    assert [f["name"] for f in child[0]["fields"]] == ["编号", "内容"], child[0]["fields"]


def test_nested_cjk_template_passes_validation():
    """这条是用户看到的那一步：正确数据必须过校验。"""
    raw = _nested_cjk()
    out = validate_data(raw, {"章节": CHAPTERS})
    assert out["valid"], out["errors"]


def test_nested_cjk_template_renders_every_item():
    raw = _nested_cjk()
    blob = render_template(raw, {"章节": CHAPTERS})
    xml = zipfile.ZipFile(BytesIO(blob)).read("word/document.xml").decode("utf-8")
    text = "".join(re.findall(r"<w:t[^>]*>([^<]*)</w:t>", xml))
    for ch in CHAPTERS:
        assert ch["名称"] in text, text
        for c in ch["条款"]:
            assert c["内容"] in text, text
    assert "{%" not in xml and "{{" not in xml


def test_ascii_nested_loop_unchanged():
    """ASCII 循环变量的既有行为不能被这次放宽改坏（回归钉子）。"""
    vs = _by_name(scan_docx(_nested_ascii()))
    assert set(vs) == {"章节"}, list(vs)
    child = [f for f in vs["章节"]["fields"] if f["name"] == "条款"]
    assert child and child[0]["kind"] == "loop", child


def test_cjk_subscript_base_belongs_to_loop():
    """`条["内容"]` 与 `条.内容` 等价，基名是中文循环变量时也要归给循环。"""
    raw = _docx([
        "{%p for 条 in 条款 %}",
        '{{ 条["编号"] }}{{ 条["内容"] }}',
        "{%p endfor %}",
    ])
    vs = _by_name(scan_docx(raw))
    assert "条" not in vs, f"中文循环变量被当成标量：{list(vs)}"
    assert [f["name"] for f in vs["条款"]["fields"]] == ["编号", "内容"], vs["条款"]["fields"]
