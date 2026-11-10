# -*- coding: utf-8 -*-
"""字体体检的「段内中英分工」部分（fontaudit.audit_runs）。

这块要回答的问题很具体：**同一个段落里，汉字用的是哪支字体、西文用的是哪支。**

为什么不能一个段落拉一个族名：
中文模板的正文几乎都是中英混排。OOXML 把两种文字交给不同槽位——
汉字走 `w:rFonts/@w:eastAsia`，西文走 `@w:ascii` / `@w:hAnsi`。
把四个槽位压成一个集合，就会出现三种错：

1. 界面上只有一串并列族名，用户看不出哪个管中文、哪个管英文；
2. 纯英文的 run 上挂着的仿宋也算"用到了仿宋"（模板里"整段刷同一字体"极常见）；
3. 段落没写 rFonts、靠样式链继承的字体完全看不出来——
   `1. 概述` 这种标题就是这类，等于没体检。

这里把这三条都钉死。
"""
import io
import zipfile

import pytest

from app import cjk_fonts, fontaudit

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
A = 'xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"'
XML_HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'


def _docx(parts):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for name, body in parts.items():
            z.writestr(name, body)
    return buf.getvalue()


def _run(text, **attrs):
    """一个 run，text 是文字，attrs 是 rFonts 的属性（eastAsia / ascii / asciiTheme …）。"""
    return "<w:r><w:rPr><w:rFonts %s/></w:rPr><w:t>%s</w:t></w:r>" % (
        " ".join('w:%s="%s"' % (k, v) for k, v in attrs.items()), text)


def _para(*runs, style=None):
    ppr = '<w:pPr><w:pStyle w:val="%s"/></w:pPr>' % style if style else ""
    return "<w:p>%s%s</w:p>" % (ppr, "".join(runs))


def _doc(*paras):
    return XML_HEAD + "<w:document %s><w:body>%s</w:body></w:document>" % (W, "".join(paras))


def _styles(defaults="", *styles):
    return XML_HEAD + "<w:styles %s>%s%s</w:styles>" % (W, defaults, "".join(styles))


def _defaults(**attrs):
    return ("<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts %s/></w:rPr></w:rPrDefault>"
            "</w:docDefaults>" % " ".join('w:%s="%s"' % (k, v) for k, v in attrs.items()))


def _style(sid, based=None, **attrs):
    based_xml = '<w:basedOn w:val="%s"/>' % based if based else ""
    return ('<w:style w:type="paragraph" w:styleId="%s"><w:name w:val="%s"/>%s'
            '<w:rPr><w:rFonts %s/></w:rPr></w:style>'
            % (sid, sid, based_xml,
               " ".join('w:%s="%s"' % (k, v) for k, v in attrs.items())))


THEME = (
    XML_HEAD + "<a:theme %s><a:themeElements><a:fontScheme name=\"x\">"
    "<a:majorFont><a:latin typeface=\"Calibri\"/><a:ea typeface=\"\"/>"
    '<a:font script="Hans" typeface="等线 Light"/></a:majorFont>'
    "<a:minorFont><a:latin typeface=\"Times New Roman\"/><a:ea typeface=\"\"/>"
    '<a:font script="Hans" typeface="宋体"/></a:minorFont>'
    "</a:fontScheme></a:themeElements></a:theme>" % A
)


@pytest.fixture
def no_fontconfig(monkeypatch):
    """把「本机装了什么」钉死，用例结论才与跑测试的机器无关。"""
    monkeypatch.setattr(cjk_fonts, "installed_families",
                        lambda: {"Noto Serif CJK SC", "Noto Sans CJK SC"})
    monkeypatch.setattr(cjk_fonts, "fc_match",
                        lambda fam: {"仿宋_GB2312": "Noto Serif CJK SC",
                                     "黑体": "Noto Sans CJK SC",
                                     "宋体": "Noto Serif CJK SC",
                                     "Arial": "Liberation Sans",
                                     "Times New Roman": "Liberation Serif",
                                     "Calibri": "Liberation Sans"}.get(fam, "DejaVu Sans"))


# ---------------------------------------------------------------- 段内分工

def test_paragraph_reports_cjk_and_latin_separately(no_fontconfig):
    """同一段里：汉字用仿宋、西文用 Times New Roman，必须分别报出来。

    这就是「一个段落内，中文什么字体、英文什么字体」，而不是拉成
    「这一段用了 仿宋_GB2312、Times New Roman」两条并列、不分角色的记录。
    """
    raw = _docx({"word/document.xml": _doc(
        _para(_run("漏洞描述：", eastAsia="仿宋_GB2312", ascii="Times New Roman"),
              _run("UNION SELECT", eastAsia="仿宋_GB2312", ascii="Times New Roman"))
    )})
    rep = fontaudit.audit_runs(raw)
    p = rep["paragraphs"][0]
    assert p["cjk"]["family"] == "仿宋_GB2312"
    assert p["latin"]["family"] == "Times New Roman"


def test_pure_latin_run_does_not_count_eastasia(no_fontconfig):
    """纯英文的 run 上挂着 eastAsia="黑体"，不等于"这段用了黑体"。

    模板里"整段刷同一字体"极常见（四个槽位全填满），
    但这一段一个汉字都没有，黑体根本没被渲染到。算进去只会让
    「本机缺黑体」这种告警指不到真正需要修的地方。
    """
    raw = _docx({"word/document.xml": _doc(
        _para(_run("1. 概述", eastAsia="仿宋_GB2312", ascii="Arial")),
        _para(_run("SELECT 1", eastAsia="黑体", ascii="Arial")),
    )})
    rep = fontaudit.audit_runs(raw)
    second = rep["paragraphs"][1]
    assert second["cjk"] is None, "纯英文段不该报中文字体"
    assert second["latin"]["family"] == "Arial"
    fams = [f["family"] for f in rep["roles"]["eastAsia"]["fonts"]]
    assert "黑体" not in fams, "黑体只在纯英文 run 上出现，不该进中文统计"


def test_roles_are_grouped_by_script(no_fontconfig):
    """roles 必须分成 eastAsia / latin 两组，各自带人读标签。"""
    raw = _docx({"word/document.xml": _doc(
        _para(_run("中文", eastAsia="仿宋_GB2312"), _run("English", ascii="Arial"))
    )})
    rep = fontaudit.audit_runs(raw)
    assert rep["roles"]["eastAsia"]["label"] == "中文"
    assert rep["roles"]["latin"]["label"] == "西文"
    assert {f["family"] for f in rep["roles"]["eastAsia"]["fonts"]} == {"仿宋_GB2312"}
    assert {f["family"] for f in rep["roles"]["latin"]["fonts"]} == {"Arial"}


# ---------------------------------------------------------------- 继承

def test_fonts_inherited_from_style_chain(no_fontconfig):
    """段落没写 rFonts，字体来自 pStyle → basedOn → docDefaults，也必须认出来。

    中文模板里这种段落是多数（`1. 概述` 就是）。老实现只看 run 上的显式属性，
    这些段落全成了"不知道"，体检等于没做。
    """
    raw = _docx({
        "word/document.xml": _doc(_para(_run("概述正文 ABC"), style="Body")),
        "word/styles.xml": _styles(
            _defaults(ascii="Times New Roman", hAnsi="Times New Roman"),
            _style("Normal", eastAsia="宋体"),
            _style("Body", based="Normal", eastAsia="仿宋_GB2312"),
        ),
    })
    p = fontaudit.audit_runs(raw)["paragraphs"][0]
    assert p["cjk"]["family"] == "仿宋_GB2312", "没沿 basedOn 链嫁接到最近的显式声明"
    assert p["latin"]["family"] == "Times New Roman", "docDefaults 的 ascii 被丢了"


def test_theme_slot_reference_is_resolved_to_family(no_fontconfig):
    """`asciiTheme="minorHAnsi"` / `eastAsiaTheme="minorEastAsia"` 要解成真实族名。

    这类段落的中文去哪支字体，只有读了 theme1.xml 才知道。
    解析不出来就只能报"槽位引用"，等于告诉用户一句废话。
    """
    raw = _docx({
        "word/document.xml": _doc(_para(
            _run("中文 English", asciiTheme="minorHAnsi", hAnsiTheme="minorHAnsi",
                 eastAsiaTheme="minorEastAsia"))),
        "word/theme/theme1.xml": THEME,
    })
    p = fontaudit.audit_runs(raw)["paragraphs"][0]
    assert p["latin"]["family"] == "Times New Roman"
    assert p["cjk"]["family"] == "宋体", "minorEastAsia 该取 Hans 槽位"


def test_nested_paragraph_runs_are_not_double_counted(no_fontconfig):
    """文本框里的段落会嵌套在外层段落里，不能被算两遍。"""
    inner = "<w:p>%s</w:p>" % _run("内部", eastAsia="黑体")
    outer_run = _run("外层", eastAsia="仿宋_GB2312")
    raw = _docx({"word/document.xml": _doc(
        "<w:p><w:r><w:rPr><w:rFonts w:eastAsia=\"仿宋_GB2312\"/></w:rPr>"
        "<w:t>外层</w:t></w:r>%s</w:p>" % inner)})
    rep = fontaudit.audit_runs(raw)
    cjk = {f["family"]: f["segments"] for f in rep["roles"]["eastAsia"]["fonts"]}
    assert cjk.get("仿宋_GB2312") == 1
    assert cjk.get("黑体") == 1, "嵌套段落自己算一段，不该被外层带着重复计"


# ---------------------------------------------------------------- 脏族名

def test_wps_style_note_in_family_name_is_not_treated_as_cjk():
    """`Calibri (西文正文)` 是 WPS 把主题槽位的界面名写进了 rFonts。

    老实现因为"族名里有汉字"判成中文字体，再因为本机没有 → missing → severe，
    最后在界面上报「汉字可能显示不出来」。一个西文字体被报成汉字显示不出来。
    """
    assert fontaudit._looks_cjk("Calibri (西文正文)") is False
    assert fontaudit._base_family("Calibri (西文正文)") == "Calibri"
    assert fontaudit._base_family("宋体（正文）") == "宋体"


def test_wps_dirty_family_does_not_raise_severe(no_fontconfig):
    raw = _docx({"word/document.xml": _doc(
        _para(_run("中文", eastAsia="仿宋_GB2312", cs="Calibri (西文正文)")))})
    rep = fontaudit.audit_docx(raw)
    assert "Calibri (西文正文)" not in rep["severe"], "西文字体不该报「汉字显示不出来」"
    assert "Calibri (西文正文)" not in rep["missing"]
    suspects = {s["family"] for s in rep["suspects"]}
    assert "Calibri (西文正文)" in suspects, "脏族名要单独提示，而不是混进缺失清单"


def test_cs_slot_does_not_decide_cjk_missing(no_fontconfig):
    """`cs` 是复杂文种槽位，简体文档里它不承担汉字渲染。"""
    raw = _docx({"word/document.xml": _doc(
        _para(_run("中文", eastAsia="仿宋_GB2312", cs="Arabic Typesetting")))})
    rep = fontaudit.audit_docx(raw)
    assert "Arabic Typesetting" not in rep["severe"]


# ---------------------------------------------------------------- 输出契约

def test_audit_docx_carries_roles_and_paragraphs(no_fontconfig):
    """老字段（fonts/missing/severe/checked）不动，新字段并存。"""
    raw = _docx({"word/document.xml": _doc(
        _para(_run("中文 ABC", eastAsia="仿宋_GB2312", ascii="Times New Roman")))})
    rep = fontaudit.audit_docx(raw)
    for key in ("ok", "checked", "missing", "severe", "fonts"):
        assert key in rep, "老字段丢了会打断前端与 Go 侧"
    assert rep["roles"]["eastAsia"]["fonts"], "缺中文角色汇总"
    assert rep["roles"]["latin"]["fonts"], "缺西文角色汇总"
    assert rep["paragraphs"], "缺段级明细"
    assert rep["paragraphs"][0]["cjk"]["family"] == "仿宋_GB2312"


def test_missing_role_fonts_sort_first(no_fontconfig):
    """角色汇总里，缺的排前面——用户先要看到"哪个不能用"。"""
    raw = _docx({"word/document.xml": _doc(
        _para(_run("甲", eastAsia="Noto Serif CJK SC")),       # exact
        _para(_run("乙", eastAsia="方正小标宋简体")),           # missing
    )})
    fams = [f["family"] for f in fontaudit.audit_runs(raw)["roles"]["eastAsia"]["fonts"]]
    assert fams[0] == "方正小标宋简体"


def test_explicit_family_beats_theme_slot_reference(no_fontconfig):
    """下层 docDefaults 给主题槽位、上层 run 给显式族名时，必须用显式族名。

    真踩过：`eastAsiaTheme="minorEastAsia"` 写在 docDefaults 里，run 上写着
    `eastAsia="仿宋_GB2312"`，实现却让槽位解析结果无条件覆盖族名 ——
    于是整份模板的中文全被报成「宋体」（主题 minorHans 槽位的值），
    而实际排版用的是仿宋。**这类错不会报错，只会让体检结论整体失真。**
    """
    raw = _docx({
        "word/document.xml": _doc(_para(_run("正文 ABC", eastAsia="仿宋_GB2312",
                                            ascii="Times New Roman"))),
        "word/styles.xml": _styles(
            _defaults(asciiTheme="minorHAnsi", hAnsiTheme="minorHAnsi",
                      eastAsiaTheme="minorEastAsia")),
        "word/theme/theme1.xml": THEME,
    })
    p = fontaudit.audit_runs(raw)["paragraphs"][0]
    assert p["cjk"]["family"] == "仿宋_GB2312", "docDefaults 的槽位把 run 上的仿宋顶掉了"
    assert p["latin"]["family"] == "Times New Roman"


def test_limit_paras_caps_example_list(no_fontconfig):
    """段级明细是"示例"不是"全量"：几百段的报告不能整份吐给前端。"""
    paras = [_para(_run("第%d段" % i, eastAsia="仿宋_GB2312")) for i in range(30)]
    raw = _docx({"word/document.xml": _doc(*paras)})
    rep = fontaudit.audit_runs(raw, limit_paras=5)
    assert len(rep["paragraphs"]) == 5
    assert rep["roles"]["eastAsia"]["fonts"][0]["segments"] == 30, "计数不能也被截断"


def test_table_paragraph_is_labelled_as_table(no_fontconfig):
    """表格里的段落要标「表格」，不能跟着 document.xml 一起说成「正文」。

    真踩到：用户拿着报告去正文里找「序号 / 系统名称」这几行——找不到，
    它们在表格里。位置说不准，这份报告就只能当情绪参考。
    说明：表格仍属 word/document.xml，所以是**结构位置**而非文件位置。
    """
    table = ('<w:tbl><w:tr><w:tc>%s</w:tc></w:tr></w:tbl>'
             % _para(_run("系统名称", eastAsia="宋体")))
    raw = _docx({"word/document.xml": _doc(
        _para(_run("正文段落", eastAsia="仿宋_GB2312")), table)})
    rep = fontaudit.audit_runs(raw)
    parts = [p["part"] for p in rep["paragraphs"]]
    assert parts == ["正文", "表格"], "表内段落的位置标错了：%s" % parts
    # 同理，角色块里的 parts 也要能看出这支字体用在了表格上
    fams = {f["family"]: f for f in rep["roles"]["eastAsia"]["fonts"]}
    assert "表格" in fams["宋体"]["parts"]
    assert fams["宋体"]["parts"] != ["正文"]
