# -*- coding: utf-8 -*-
"""模板字体体检（app/fontaudit.py）。

这个模块的失败模式是两种，方向相反：

1. **漏报**：模板用了本机没有的字体，却什么提示都不给。用户在自己 Windows 上
   打开是好的，导出 PDF 才发现字形变了——那时候已经发出去了。
2. **噪音**：把 theme1.xml 里泰文/高棉文的默认字体一并报成"缺失"，
   一屏三四十条，用户看一眼就关掉，真正的 `仿宋_GB2312` 被埋掉。

所以用例要同时钉住这两头：该报的必须报，不该报的一个都不许出现。
"""
import io
import zipfile

import pytest
from fastapi.testclient import TestClient

from app import cjk_fonts, fontaudit, main as main_mod


# ---------------------------------------------------------------- 造 docx

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
A = 'xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"'


def _docx(parts):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for name, body in parts.items():
            z.writestr(name, body)
    return buf.getvalue()


def _doc(rfonts_attrs):
    """生成一份只含一个段落的 document.xml，run 上带给定 rFonts 属性。"""
    attrs = " ".join('w:%s="%s"' % (k, v) for k, v in rfonts_attrs.items())
    return (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        "<w:document %s><w:body><w:p><w:r><w:rPr><w:rFonts %s/>"
        "</w:rPr><w:t>正文</w:t></w:r></w:p></w:body></w:document>" % (W, attrs)
    )


THEME = (
    '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
    "<a:theme %s><a:themeElements><a:fontScheme name=\"x\">"
    "<a:majorFont><a:latin typeface=\"Calibri\"/><a:ea typeface=\"\"/>"
    '<a:font script="Hans" typeface="等线 Light"/>'
    '<a:font script="Thai" typeface="Angsana New"/>'
    '<a:font script="Khmr" typeface="DaunPenh"/>'
    '<a:font script="Hant" typeface="新細明體"/></a:majorFont>'
    "<a:minorFont><a:latin typeface=\"Cambria\"/><a:ea typeface=\"\"/>"
    '<a:font script="Hans" typeface="宋体"/></a:minorFont>'
    "</a:fontScheme></a:themeElements></a:theme>" % A
)


@pytest.fixture
def no_fontconfig(monkeypatch):
    """把「本机装了什么」钉死，测试结果才与跑测试的机器无关。"""
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: {"Noto Serif CJK SC",
                                                                  "Noto Sans CJK SC"})
    monkeypatch.setattr(cjk_fonts, "fc_match",
                        lambda fam: {"仿宋_GB2312": "Noto Serif CJK SC",
                                     "黑体": "Noto Sans CJK SC",
                                     "Arial": "Liberation Sans"}.get(fam, "DejaVu Sans"))


# ---------------------------------------------------------------- 收集

def test_collects_explicit_font_in_body(no_fontconfig):
    raw = _docx({"word/document.xml": _doc({"eastAsia": "仿宋_GB2312",
                                           "ascii": "Times New Roman"})})
    got = {f["family"]: f for f in fontaudit.audit_docx(raw)["fonts"]}
    assert "仿宋_GB2312" in got
    assert got["仿宋_GB2312"]["parts"] == ["正文"]


def test_skips_theme_slot_references(no_fontconfig):
    """`w:eastAsiaTheme="minorEastAsia"` 是槽位引用，不是族名。

    把它当族名去 fc-match 必然落到兜底字体，于是每份模板都报一堆假缺失。
    """
    raw = _docx({"word/document.xml": _doc({"eastAsiaTheme": "minorEastAsia",
                                           "asciiTheme": "minorHAnsi",
                                           "eastAsia": "+mn-ea"})})
    rep = fontaudit.audit_docx(raw)
    assert rep["fonts"] == [], "槽位引用被当成族名了"
    assert rep["checked"] == 0


def test_theme_only_takes_hans_and_latin_slots(no_fontconfig):
    """theme 里泰文/高棉文/繁体槽位的字体不该出现在体检结果里。"""
    raw = _docx({"word/document.xml": _doc({}), "word/theme/theme1.xml": THEME})
    fams = {f["family"] for f in fontaudit.audit_docx(raw)["fonts"]}
    assert fams == {"Calibri", "Cambria", "宋体", "等线 Light"}
    assert "Angsana New" not in fams, "泰文字体混进来了（噪音）"
    assert "DaunPenh" not in fams
    assert "新細明體" not in fams


def test_headers_and_footers_are_reported(no_fontconfig):
    raw = _docx({
        "word/document.xml": _doc({}),
        "word/header1.xml": _doc({"eastAsia": "楷体_GB2312"}),
    })
    got = {f["family"]: f for f in fontaudit.audit_docx(raw)["fonts"]}
    assert got["楷体_GB2312"]["parts"] == ["页眉"]


# ---------------------------------------------------------------- 判定

def test_judge_exact_when_installed():
    assert fontaudit.judge("宋体", have={"宋体"}) == ("exact", "宋体")


def test_judge_exact_is_case_insensitive(monkeypatch):
    """fc-match 大小写不敏感，返回的是规范名；比对要忽略大小写，否则误报成回落。"""
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: set())
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda fam: "SimSun,宋体")
    status, resolved = fontaudit.judge("simsun", have=set())
    assert status == "exact"
    assert "SimSun" in resolved


def test_judge_fallback_when_resolved_to_other_cjk(monkeypatch):
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: set())
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda fam: "Noto Serif CJK SC")
    status, resolved = fontaudit.judge("仿宋_GB2312", have=set())
    assert status == "fallback"
    assert "Noto Serif" in resolved


def test_judge_missing_when_no_cjk_at_all(monkeypatch):
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: set())
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda fam: "DejaVu Sans")
    assert fontaudit.judge("方正小标宋简体", have=set())[0] == "missing"


def test_judge_latin_family_is_not_flagged(monkeypatch):
    """Arial 落到 Liberation Sans 是正常替换，不该进"缺字体"清单。"""
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: set())
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda fam: "Liberation Sans")
    status, _ = fontaudit.judge("Arial", have=set())
    assert status == "latin"


def test_audit_missing_list_matches_fonts(no_fontconfig):
    raw = _docx({"word/document.xml": _doc({"eastAsia": "仿宋_GB2312",
                                           "ascii": "Arial"})})
    rep = fontaudit.audit_docx(raw)
    assert "仿宋_GB2312" in rep["missing"]
    assert "Arial" not in rep["missing"], "拉丁字体不该出现在缺失清单里"
    # 仿宋_GB2312 → Noto Serif CJK SC（有汉字），所以不算 severe
    assert rep["severe"] == []


# ---------------------------------------------------------------- 健壮性

def test_broken_part_does_not_kill_the_audit(no_fontconfig):
    """单个部件 XML 坏掉，其它部件照样体检。别让一处坏结构毁掉整份报告。"""
    raw = _docx({
        "word/document.xml": _doc({"eastAsia": "仿宋_GB2312"}),
        "word/header1.xml": "<w:hdr 这不是合法 XML",
    })
    rep = fontaudit.audit_docx(raw)
    assert rep["ok"] is True
    assert "仿宋_GB2312" in {f["family"] for f in rep["fonts"]}


def test_non_zip_returns_ok_false():
    rep = fontaudit.audit_docx(b"not a docx at all")
    assert rep["ok"] is False
    assert rep["fonts"] == []


# ---------------------------------------------------------------- 接口

def test_scan_endpoint_carries_font_audit(business_template_bytes):
    """上传模板时扫描的响应里必须带字体体检——那是唯一能当面说清楚的机会。"""
    client = TestClient(main_mod.app)
    r = client.post("/scan", files={
        "file": ("t.docx", business_template_bytes,
                 "application/vnd.openxmlformats-officedocument.wordprocessingml.document")})
    assert r.status_code == 200
    body = r.json()
    assert "font_audit" in body
    assert body["font_audit"]["ok"] is True
    assert "fonts" in body["font_audit"]


def test_fontaudit_endpoint_rejects_non_docx():
    client = TestClient(main_mod.app)
    r = client.post("/fontaudit", files={"file": ("x.docx", b"nope", "application/octet-stream")})
    assert r.status_code in (415, 422)


# ---------------------------------------------------------------- ok 字段的语义

def test_ok_means_the_audit_ran_not_that_fonts_are_present(no_fontconfig):
    """`ok=True` 只代表「体检跑完了」，**不代表「字体都在」**。

    这个字段名很容易被误读成后者，然后前端拿它当绿灯用 —— 模板缺三个字体
    也会显示"一切正常"。这条用例把语义钉死：ok 与 missing 并存是正常的。
    """
    raw = _docx({"word/document.xml": _doc({"eastAsia": "方正小标宋简体"})})
    rep = fontaudit.audit_docx(raw)
    assert rep["ok"] is True, "体检本身是成功的"
    assert rep["missing"], "字体确实缺，missing 必须非空"
    assert rep["severe"], "落到无汉字字体，severe 必须非空"


def test_severe_is_a_subset_of_missing(no_fontconfig):
    """severe ⊂ missing：severe 是 missing 里更坏的那一档。

    两者搞反会让 UI 把"会被替换字形"（fallback）也标成红叉，或者反过来把
    "汉字根本没字形"标成黄叹号 —— 严重程度是给用户排序用的，不能糊。
    """
    raw = _docx({"word/document.xml": _doc({"eastAsia": "仿宋_GB2312",
                                           "ascii": "Arial"})})
    rep = fontaudit.audit_docx(raw)
    assert set(rep["severe"]) <= set(rep["missing"])
    # 仿宋_GB2312 → Noto Serif CJK SC，汉字有字形，属 fallback，不该进 severe
    assert "仿宋_GB2312" in rep["missing"]
    assert "仿宋_GB2312" not in rep["severe"]


def test_checked_counts_every_distinct_family(no_fontconfig):
    raw = _docx({"word/document.xml": _doc({"eastAsia": "仿宋_GB2312",
                                           "ascii": "Times New Roman",
                                           "cs": "Arial"})})
    rep = fontaudit.audit_docx(raw)
    assert rep["checked"] == len(rep["fonts"]) == 3


# ---------------------------------------------------------------- theme 的克制

def test_theme_skips_japanese_and_korean_slots(no_fontconfig):
    """日文/韩文槽位与繁体一样，报了也处理不了，只会淹掉真正的缺口。"""
    theme = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        "<a:theme %s><a:themeElements><a:fontScheme name=\"x\">"
        "<a:majorFont><a:latin typeface=\"Calibri\"/>"
        '<a:font script="Jpan" typeface="ＭＳ 明朝"/>'
        '<a:font script="Kore" typeface="Malgun Gothic"/>'
        '<a:font script="Arab" typeface="Times New Roman"/>'
        '<a:font script="Hans" typeface="宋体"/></a:majorFont>'
        "<a:minorFont><a:latin typeface=\"Calibri\"/></a:minorFont>"
        "</a:fontScheme></a:themeElements></a:theme>" % A
    )
    raw = _docx({"word/document.xml": _doc({}), "word/theme/theme1.xml": theme})
    fams = {f["family"] for f in fontaudit.audit_docx(raw)["fonts"]}
    assert "宋体" in fams and "Calibri" in fams
    assert "ＭＳ 明朝" not in fams, "日文字体混进来了（噪音）"
    assert "Malgun Gothic" not in fams, "韩文字体混进来了（噪音）"


def test_theme_empty_typeface_is_ignored(no_fontconfig):
    """`<a:ea typeface=""/>` 是合法的"留空"，不是族名。"""
    raw = _docx({"word/document.xml": _doc({}), "word/theme/theme1.xml": THEME})
    fams = {f["family"] for f in fontaudit.audit_docx(raw)["fonts"]}
    assert "" not in fams


# ---------------------------------------------------------------- 部件归集

def test_same_family_in_many_parts_is_deduped_and_ordered(no_fontconfig):
    """同一字体出现在正文和页眉，parts 要去重且三个部件都在。

    不去重会显示「正文、页眉、正文」这种，用户以为有第三处要改。
    刻意**不**断言顺序：zip 里的部件顺序不是产品契约，钉死它会让用例
    在换一个打包工具时无故变红，而那时候真正要守的"去重"反而没人看。
    """
    raw = _docx({
        "word/document.xml": _doc({"eastAsia": "仿宋_GB2312"}),
        "word/header1.xml": _doc({"eastAsia": "仿宋_GB2312"}),
        "word/styles.xml": _doc({"eastAsia": "仿宋_GB2312"}),
    })
    got = {f["family"]: f for f in fontaudit.audit_docx(raw)["fonts"]}
    parts = got["仿宋_GB2312"]["parts"]
    assert len(parts) == 3, "部件被重复记了：%s" % parts
    assert set(parts) == {"正文", "样式", "页眉"}


def test_styles_part_is_audited(no_fontconfig):
    """模板正文没写死字体、靠样式表给的情况极常见，漏了这份就等于没体检。"""
    raw = _docx({
        "word/document.xml": _doc({}),
        "word/styles.xml": _doc({"eastAsia": "黑体"}),
    })
    got = {f["family"]: f for f in fontaudit.audit_docx(raw)["fonts"]}
    assert got["黑体"]["parts"] == ["样式"]


def test_numbering_part_is_audited(no_fontconfig):
    """编号符号可以单独指定字体，公文里常被改成仿宋。"""
    raw = _docx({
        "word/document.xml": _doc({}),
        "word/numbering.xml": _doc({"eastAsia": "仿宋_GB2312"}),
    })
    fams = {f["family"] for f in fontaudit.audit_docx(raw)["fonts"]}
    assert "仿宋_GB2312" in fams


# ---------------------------------------------------------------- 大小写与拉丁

def test_looks_cjk_is_case_insensitive():
    """Word 里写 `simsun` 和 `SimSun` 是同一件事，查表不能漏。"""
    assert fontaudit._looks_cjk("simsun") is True
    assert fontaudit._looks_cjk("MICROSOFT YAHEI") is True
    assert fontaudit._looks_cjk("Arial") is False
    assert fontaudit._looks_cjk("方正小标宋简体") is True


def test_judge_latin_when_fc_match_returns_nothing(monkeypatch):
    """fc-match 不可用时，拉丁字体不该被报成「缺失」制造噪音。"""
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda f: "")
    assert fontaudit.judge("Arial", have=set())[0] == "latin"
    assert fontaudit.judge("黑体", have=set())[0] == "missing"
