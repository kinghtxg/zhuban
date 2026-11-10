# -*- coding: utf-8 -*-
"""中文循环变量名：`{% for 条 in 条款 %}` 必须被识别成循环。

背景：scanner 的 loop_re 原先用 IDENT（纯 ASCII）匹配循环变量，理由是
「Jinja 的循环变量必须是合法 Python 标识符」。但 Python 3 的标识符规则
本来就允许中文，Jinja2 3.1.6 对 `{% for 条 in 条款 %}{{ 条.编号 }}{% endfor %}`
编译与渲染都正常（本文件最后一条用例钉住这一点）。

只认 ASCII 的后果不是「少一个字段」，而是**整个循环凭空消失**：
  loops 里没有这条循环、迭代对象也不登记；
  循环变量反过来被当成一个标量字段（界面让用户填名叫「条」的字符串）；
  {{ 条.编号 }} 渲染时 undefined，输出空串。
而扫描结果全绿、零诊断 —— 中文模板作者几乎必然写成中文循环变量，
这批模板会整体失效，且没人能看出是扫描器的锅。

修复是两处：loop_re 的标识符放宽到中文；_refs 里点引用链的基名判断
补上「在循环栈里」这一支（否则 `条.编号` 仍会掉进标量分支）。
"""
from io import BytesIO

from docx import Document

from app.render import render_template
from app.scanner import scan_docx

CLAUSES = [
    {"编号": "（一）", "小标题": "加强组织领导", "正文": "各单位要压实责任。"},
    {"编号": "（二）", "小标题": "严格督导考核", "正文": "纳入年度考核。"},
]


def _docx(paragraphs):
    d = Document()
    for t in paragraphs:
        d.add_paragraph(t)
    buf = BytesIO()
    d.save(buf)
    return buf.getvalue()


def _para_loop_docx():
    return _docx([
        "各有关单位：",
        "{%p for 条 in 条款 %}",
        "{{ 条.编号 }}{{ 条.小标题 }}。{{ 条.正文 }}",
        "{%p endfor %}",
    ])


def _table_loop_docx():
    d = Document()
    d.add_paragraph("附件清单")
    t = d.add_table(rows=2, cols=3)
    for i, h in enumerate(["名称", "份数", "备注"]):
        t.rows[0].cells[i].text = h
    t.rows[1].cells[0].text = "{%tr for 附 in 附件清单 %}{{ 附.名称 }}"
    t.rows[1].cells[1].text = "{{ 附.份数 }}"
    t.rows[1].cells[2].text = "{{ 附.备注 }}{%tr endfor %}"
    buf = BytesIO()
    d.save(buf)
    return buf.getvalue()


def _by_name(result):
    return {v["name"]: v for v in result["variables"]}


def test_cjk_loop_var_in_paragraph_is_a_loop():
    r = scan_docx(_para_loop_docx())
    vs = _by_name(r)
    assert "条款" in vs, f"迭代对象没登记成变量：{list(vs)}"
    assert vs["条款"]["kind"] == "loop", vs["条款"]
    assert [f["name"] for f in vs["条款"]["fields"]] == ["编号", "小标题", "正文"], vs["条款"]["fields"]


def test_cjk_loop_var_does_not_leak_as_scalar():
    """`条` 是循环变量，不能变成要用户填的标量字段。"""
    r = scan_docx(_para_loop_docx())
    names = list(_by_name(r))
    assert "条" not in names, f"循环变量被当成了标量字段：{names}"


def test_cjk_loop_var_in_table_is_a_loop():
    r = scan_docx(_table_loop_docx())
    vs = _by_name(r)
    assert "附件清单" in vs and vs["附件清单"]["kind"] == "loop", vs
    assert [f["name"] for f in vs["附件清单"]["fields"]] == ["名称", "份数", "备注"]
    assert "附" not in vs, list(vs)


def test_cjk_loop_var_renders_every_item():
    """识别对了还要渲染对：两项都要出现，且不能残留标签。"""
    out = render_template(_para_loop_docx(), {"条款": CLAUSES})
    import zipfile
    xml = zipfile.ZipFile(BytesIO(out)).read("word/document.xml").decode("utf-8")
    text = "".join(__import__("re").findall(r"<w:t[^>]*>([^<]*)</w:t>", xml))
    for c in CLAUSES:
        assert c["小标题"] in text, text
        assert c["正文"] in text, text
    assert "{%" not in xml and "{{" not in xml
    # 正文里不该出现变量名本身
    assert "条.编号" not in text


def test_cjk_loop_var_with_condition_still_works():
    """中文循环变量与中文条件变量共存时，两者各归各位。"""
    raw = _docx([
        "{%p for 条 in 条款 %}",
        "{{ 条.编号 }}{{ 条.正文 }}",
        "{%p endfor %}",
        "{%p if 是否公开 %}",
        "此件公开发布。",
        "{%p endif %}",
    ])
    vs = _by_name(scan_docx(raw))
    assert vs["条款"]["kind"] == "loop"
    assert vs["是否公开"]["kind"] == "condition", vs.get("是否公开")
    assert "条" not in vs
