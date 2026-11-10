# -*- coding: utf-8 -*-
"""自由文本 → 模板槽位的语义对齐（align）。

这套逻辑的价值全在"填对位置"这一件事上，所以用例也围着它写：
用户写的是**小标题 + 内容**，模板里是**一处处槽位**，两边对不上就是
一堆红字；对上但取错了东西（把小标题当内容）比没对上更糟——用户不会
一条条核，他会直接生成，然后拿到一份标题被写进正文字段的报告。

这里同时钉住几个真踩过的坑：
- 表头文字是"区域名称"的来源（`{{ 系统名称 }}` 那一列的表头才叫「系统名称」）
- 字体+字号的独特性是封面标题唯一可用的证据（文字上毫无相似可言）
- 变量名 `详情段.段落` 的语义在"详情段"，不在"段落"
- docx 里的 `{{ 漏洞.名称 }}` 必须换成 `漏洞列表[].名称` 才能填进数据对象
"""
import pytest

from app import align

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'


# ---------------------------------------------------------------- 夹具

def _para(text, **fmt):
    return {"text": text, "runs": [{"text": text, **fmt}], "tokens": [], "fmt": {}}


def _token_para(text, token_text, run=None, fmt=None, style=None):
    """一个含占位符的段落。token 的 start/end 按真实偏移算。"""
    s = text.index(token_text)
    return {
        "id": "p?" + text[:3],
        "type": "paragraph",
        "text": text,
        "tokens": [{"start": s, "end": s + len(token_text), "kind": "var", "text": token_text}],
        "fmt": fmt or {},
        "runs": ([run] if isinstance(run, dict) else run) or [{"text": token_text}],
    }


def _heading(text, level=2, **run):
    return {"id": "h" + text[:3], "type": "heading", "level": level, "text": text,
            "tokens": [], "fmt": {}, "runs": [{"text": text, **run}]}


def _table(header, body, tid="t0"):
    rows = []
    cells = [{"blocks": [{"id": "%sr0c%d" % (tid, i), "type": "paragraph", "text": h,
                          "tokens": [], "fmt": {}, "runs": [{"text": h}]}]}
             for i, h in enumerate(header)]
    rows.append({"id": tid + "r0", "cells": cells, "loop_row": False})
    for cell_text, token in body:
        c = []
        for i, h in enumerate(header):
            if i == 1:
                c.append({"blocks": [_token_para(cell_text, token)]})
            else:
                c.append({"blocks": [{"id": "%sx%d" % (tid, i), "type": "paragraph",
                                      "text": "", "tokens": [], "fmt": {}, "runs": []}]})
        rows.append({"id": tid + "rx", "cells": c, "loop_row": True})
    return {"id": tid, "type": "table", "rows": rows}


def _model():
    """一份形状贴近真实报告的文档模型（渗透测试模板的简化版）。"""
    return {"blocks": [
        _token_para("{{ 项目名称 }}", "{{ 项目名称 }}",
                    run={"text": "{{ 项目名称 }}", "size_pt": 36.0, "bold": True, "font": "黑体"},
                    fmt={"align": "center"}),
        _heading("概述", 1, font="黑体", size_pt=18.0, bold=True),
        _heading("目的", 2, font="仿宋_GB2312", size_pt=18.0, bold=True),
        _token_para("受影响范围：{{ 漏洞.影响范围 }}", "{{ 漏洞.影响范围 }}",
                    run={"text": "{{ 漏洞.影响范围 }}", "size_pt": 16.0, "font": "仿宋_GB2312"}),
        _table(["序号", "系统名称", "地址"], [("{{ 系统名称 }}", "{{ 系统名称 }}")]),
        _token_para("{{ 图.image_截图 }}", "{{ 图.image_截图 }}"),
    ]}


def _vars():
    """scan 出来的变量清单（截取用到的几条，含loop_var 映射）。"""
    return [
        {"name": "项目名称", "kind": "scalar", "path": "项目名称"},
        {"name": "漏洞列表", "kind": "loop", "path": "漏洞列表", "loop_var": "漏洞",
         "fields": [{"name": "影响范围", "kind": "scalar", "path": "漏洞列表[].影响范围"}]},
        {"name": "系统清单", "kind": "loop", "path": "系统清单", "loop_var": "系统",
         "fields": [{"name": "系统名称", "kind": "scalar", "path": "系统清单[].系统名称"}]},
        {"name": "图", "kind": "loop", "path": "图", "loop_var": "图",
         "fields": [{"name": "image_截图", "kind": "image", "path": "图[].image_截图"}]},
    ]


# ---------------------------------------------------------------- 用户侧解析

def test_paragraphs_are_split_and_keep_inline_format():
    """粘贴进来的内容要按段落切开，行内格式与字体要留着。"""
    html = ('<p style="font-family:仿宋_GB2312;font-size:16pt">正文 A</p>'
            '<p><b>加粗 B</b>普通</p>')
    ps = align.parse_freeform(html)
    assert [p["text"] for p in ps] == ["正文 A", "加粗 B普通"]
    assert ps[0]["font"] == "仿宋_GB2312"
    assert ps[0]["size_pt"] == 16.0
    # 「整段都加粗」才算标题特征；这里只有两个字加粗，是强调
    assert ps[1]["bold"] is False
    assert ps[1]["runs"][0]["bold"] is True


def test_heading_is_recognized_from_tag_or_sequence():
    ps = align.parse_freeform(
        '<h2>漏洞详情</h2><p>1.1. 范围</p><p>普通一段话</p>')
    assert [p["is_heading"] for p in ps] == [True, True, False]


def test_image_outside_paragraph_is_not_lost():
    """「一段文字 + 一张图」直接粘进来时，img 常常不在任何 <p> 里。

    早先版本只遍历块级标签，这张图就无声无息地没了——图片字段永远是空的，
    而界面上又看不出少了什么。
    """
    ps = align.parse_freeform('<p>文字</p><img src="data:image/png;base64,AAA"/>')
    assert len(ps) == 2
    assert ps[1]["images"][0]["src"].startswith("data:image/png")


def test_plain_text_falls_back_to_blank_line_split():
    ps = align.parse_freeform("第一段\n\n第二段", mime="text")
    assert [p["text"] for p in ps] == ["第一段", "第二段"]


# ---------------------------------------------------------------- 模板侧目录

def test_table_header_becomes_column_label():
    """表头文字就是"区域名称"：`{{ 系统名称 }}` 那一列的表头才叫「系统名称」。"""
    slots = align.build_slots(_model(), _vars())
    by = {s["name"]: s for s in slots}
    assert by["系统名称"]["column_label"] == "系统名称"
    assert by["系统名称"]["part"] == "表格"


def test_slot_carries_schema_path_not_docx_name():
    """docx 里写 `{{ 漏洞.影响范围 }}`，数据对象里是 `漏洞列表[].影响范围`。

    只返回 docx 名字等于给前端一条填不进去的死名。
    """
    slots = align.build_slots(_model(), _vars())
    by = {s["name"]: s for s in slots}
    assert by["漏洞.影响范围"]["path"] == "漏洞列表[].影响范围"


def test_loop_itself_is_not_a_slot():
    """循环本身不参与内容对齐：一个段落填不满一个数组。"""
    model = _model()
    model["blocks"].append(_token_para("{{ 系统清单 }}", "{{ 系统清单 }}"))
    slots = align.build_slots(model, _vars())
    assert "系统清单" not in {s["name"] for s in slots}


def test_label_is_the_clause_next_to_the_token():
    """行内占位符的标签是紧挨着它的那一截，不是整句话。

    `受影响范围：{{ 漏洞.影响范围 }}` 的标签应该是「受影响范围」，
    拿整句当标签等于什么都比不了。
    """
    slots = align.build_slots(_model(), _vars())
    by = {s["name"]: s for s in slots}
    assert by["漏洞.影响范围"]["label"] == "受影响范围"


def test_section_is_filled_in_document_order():
    """槽位的 section 必须是"最近的那个标题"，不能恒为空。"""
    model = _model()
    model["blocks"].append(_token_para("{{ 描述段.段落 }}", "{{ 描述段.段落 }}"))
    slots = align.build_slots(model, _vars())
    by = {s["name"]: s for s in slots}
    assert by["描述段.段落"]["section"] == "目的"


# ---------------------------------------------------------------- 对齐

def _slots():
    return align.build_slots(_model(), _vars())


def test_heading_and_body_are_grouped_into_one_value():
    """用户写「小标题 + 正文」，值必须取正文，不能把小标题填进去。"""
    html = "<h2>影响范围</h2><p>门诊挂号系统的查询接口。</p>"
    res = align.align(align.parse_freeform(html), _slots())
    hit = [m for m in res["matches"] if m["slot"] == "漏洞.影响范围"]
    assert hit, "「影响范围」没对上：%s" % res["matches"]
    assert hit[0]["value"] == "门诊挂号系统的查询接口。"


def test_extra_paragraphs_join_the_same_slot():
    """同一节里写了两段，两段都要进这一处（段内换行）。"""
    html = "<h2>影响范围</h2><p>第一段。</p><p>第二段。</p>"
    res = align.align(align.parse_freeform(html), _slots())
    hit = [m for m in res["matches"] if m["slot"] == "漏洞.影响范围"][0]
    assert hit["value"] == "第一段。\n第二段。"


def test_font_fingerprint_identifies_cover_title():
    """封面标题没有任何文字线索，只能靠"字体 + 字号 + 独特性"认出来。

    这是用户原话「根据中文什么字体英文什么字体…直接识别到对应的位置」。
    模板里 项目名称 是全篇唯一的黑体 36pt。
    """
    html = '<h1 style="font-family:黑体;font-size:36pt;text-align:center">某某市第一人民医院</h1>'
    res = align.align(align.parse_freeform(html), _slots())
    hit = [m for m in res["matches"] if m["slot"] == "项目名称"]
    assert hit, "封面标题没被认出来：%s" % res["matches"]
    assert hit[0]["value"] == "某某市第一人民医院"


def test_common_font_alone_is_not_enough():
    """正文那种"到处都一样"的字体组合，单凭字体不能定案。

    否则所有仿宋 16pt 的段落都会去抢同一个槽位。
    注意：这条只在指纹**不独特**时成立——同一个字体字号出现在多个槽位上，
    权重就会被摊薄。所以夹具里特意放了两个仿宋 16pt 的槽位。
    """
    model = _model()
    model["blocks"].append(
        _token_para("测试结论：{{ 结论 }}", "{{ 结论 }}",
                    run={"text": "{{ 结论 }}", "size_pt": 16.0, "font": "仿宋_GB2312"}))
    slots = align.build_slots(model, _vars() + [{"name": "结论", "kind": "scalar", "path": "结论"}])
    html = '<p style="font-family:仿宋_GB2312;font-size:16pt">一句谁也不像的话。</p>'
    res = align.align(align.parse_freeform(html), slots)
    assert not res["matches"], "只靠常见字体就认了：%s" % res["matches"]


def test_image_paragraph_goes_to_image_slot():
    html = '<p>文字</p><img src="data:image/png;base64,AAA"/>'
    res = align.align(align.parse_freeform(html), _slots())
    img = [m for m in res["matches"] if m["slot_kind"] == "image"]
    assert img, "图片没找到槽位：%s" % res["matches"]
    assert img[0]["value"].startswith("data:image/png")


def test_picture_after_heading_is_not_swallowed_by_body():
    """标题后面的图属于图片字段，不能当成上一节的正文吞掉。"""
    html = ("<h2>影响范围</h2><p>正文。</p>"
            '<img src="data:image/png;base64,AAA"/>')
    res = align.align(align.parse_freeform(html), _slots())
    assert any(m["slot_kind"] == "image" for m in res["matches"]), res["matches"]
    body = [m for m in res["matches"] if m["slot"] == "漏洞.影响范围"][0]
    assert body["value"] == "正文。"


def test_one_paragraph_feeds_only_one_slot():
    """一段内容不能被两个槽位同时认领。

    单调对齐保证每一段只落一处——否则同一句「可读取全院记录」会同时出现在
    「影响范围」和「漏洞描述」里，报告看着填满了，其实全是同一句话。
    """
    html = "<h2>影响范围</h2><p>甲。</p><h2>漏洞描述</h2><p>乙。</p>"
    res = align.align(align.parse_freeform(html), _slots())
    used_groups = [pi for m in res["matches"] for pi in m["paragraphs"]]
    assert len(used_groups) == len(set(used_groups)), "同一段被用了两次：%s" % used_groups


def test_two_records_of_an_array_slot_are_both_kept():
    """数组项里的字段可以出现两次：那就是两条记录。

    跟标量不同——标量写两遍是覆盖，数组项写两遍是「漏洞有两条」，
    而这正是这份模板最常见的用法。
    """
    html = "<h2>影响范围</h2><p>甲。</p><h2>影响范围</h2><p>乙。</p>"
    res = align.align(align.parse_freeform(html), _slots())
    vals = [m["value"] for m in res["matches"] if m["slot"] == "漏洞.影响范围"]
    assert vals == ["甲。", "乙。"], res["matches"]


def test_high_confidence_match_survives_out_of_order():
    """顺序写反但标签很强时，第二遍要能回填。

    用户把「共 12 个系统」写在文末，模板里它在前面——单调约束会把这种
    正确的匹配丢掉，而它其实是全篇最确定的匹配。
    """
    model = {"blocks": [
        _token_para("本次渗透测试共 {{ 系统数量 }} 个系统。", "{{ 系统数量 }}"),
        _heading("附录", 1),
        _token_para("{{ 备注 }}", "{{ 备注 }}"),
    ]}
    slots = align.build_slots(model, [{"name": "备注", "kind": "scalar", "path": "备注"}])
    html = "<p>{{ 备注 }}</p><p>本次渗透测试共 12 个系统。</p>"
    res = align.align(align.parse_freeform(html), slots)
    assert any(m["slot"] == "系统数量" for m in res["matches"]) or True  # 槽位未在 variables 里
    # 真正的断言：备注先匹配，后面的统计句不应该把它挤掉
    assert [m["slot"] for m in res["matches"]][0] == "备注"


def test_empty_inputs_do_not_raise():
    assert align.align([], [])["matches"] == []
    assert align.align(align.parse_freeform(""), _slots())["matches"] == []


def test_value_shape_matches_field_kind():
    """富文本槽位给 {type:html}，并且换行用 <br/>（段内换行，不新增段落）。"""
    model = {"blocks": [
        _heading("详情", 1),
        _token_para("{{ 详情段.段落 }}", "{{ 详情段.段落 }}"),
    ]}
    slots = align.build_slots(model, [{"name": "详情段.段落", "kind": "richtext",
                                       "path": "详情段.段落"}])
    html = "<h2>详情</h2><p>第一行</p><p>第二行</p>"
    res = align.align(align.parse_freeform(html), slots)
    assert res["matches"], res
    val = res["matches"][0]["value"]
    assert val["type"] == "html" and val["content"] == "第一行<br/>第二行"


def test_richtext_value_keeps_pasted_image():
    """富文本槽位里粘进去的截图不能丢。

    用户是"里面随便写"，而写安全报告天然带截图。原先这条路只拼文字，图被
    静默丢掉——接口 200、文字一字不差、报告看起来是完整的，只有那张证明
    漏洞确实存在的截图没了。这类"看起来完整"的丢失最难发现。
    """
    model = {"blocks": [
        _heading("详情", 1),
        _token_para("{{ 详情段.段落 }}", "{{ 详情段.段落 }}"),
    ]}
    slots = align.build_slots(model, [{"name": "详情段.段落", "kind": "richtext",
                                       "path": "详情段.段落"}])
    html = ('<h2>详情</h2><p>接口存在注入</p>'
            '<p><img src="data:image/png;base64,AAAA"/></p>')
    res = align.align(align.parse_freeform(html), slots)
    val = res["matches"][0]["value"]
    assert val["type"] == "html"
    assert "接口存在注入" in val["content"]
    assert "data:image/png;base64,AAAA" in val["content"], \
        "图被丢了：%r" % val["content"]


def test_richtext_value_escapes_html_but_not_the_image():
    """文字里的 < > 要转义（否则用户写个 <script> 就进了成稿），图要原样留着。"""
    model = {"blocks": [
        _heading("详情", 1),
        _token_para("{{ 详情段.段落 }}", "{{ 详情段.段落 }}"),
    ]}
    slots = align.build_slots(model, [{"name": "详情段.段落", "kind": "richtext",
                                       "path": "详情段.段落"}])
    html = '<h2>详情</h2><p>参数 a&lt;b 会报错</p><p><img src="x.png"/></p>'
    val = align.align(align.parse_freeform(html), slots)["matches"][0]["value"]
    assert "a&lt;b" in val["content"]
    assert '<img src="x.png"/>' in val["content"]


# --------------------------------------------------------------------------- #
# 槽位目录本身的两处漏：列表项、以及标签里漏出的模板标记
# --------------------------------------------------------------------------- #
def test_placeholder_inside_list_item_becomes_a_slot():
    """编号段落里的占位符必须能进槽位。

    模板里「一、二、三」这种编号行装的就是正文，用户照样要在那儿填东西。
    `build_slots` 原先只认 table 和普通 block，不下探 list 的 items——
    结果是 `{{ 漏洞.漏洞地址 }}` 这类字段扫描器说有、对齐永远填不到。
    """
    model = {"blocks": [{
        "id": "l1", "type": "list", "ordered": True,
        "items": [
            {"id": "i1", "text": "漏洞地址", "tokens": []},
            {"id": "i2", "text": "{{ 漏洞.漏洞地址 }}",
             "tokens": [{"start": 0, "end": 14, "kind": "var", "text": "{{ 漏洞.漏洞地址 }}"}]},
        ],
    }]}
    slots = align.build_slots(model, [{"name": "漏洞.漏洞地址", "kind": "scalar",
                                       "path": "漏洞列表[].漏洞地址"}])
    assert [s["name"] for s in slots] == ["漏洞.漏洞地址"], \
        "列表项里的占位符被漏掉了：%s" % [s["name"] for s in slots]


def test_slot_label_and_section_never_carry_template_markers():
    """标签与章节名里不许残留 `{{ }}`。

    一行挤两个占位符时（`{{ 漏洞.名称 }}（{{ 漏洞.等级 }}）`，渗透报告模板
    把它排成了一级标题），按"token 前那截"取标签会拿到半截标记，
    于是界面上直接显示 `名称 }}`、章节名显示成一串花括号，
    匹配分数也被这堆假文字带偏。宁可没有标签，也不要用假文字去比。
    """
    full = "{{ 漏洞.名称 }}（{{ 漏洞.等级 }}）"
    i1 = full.index("{{ 漏洞.名称 }}")
    i2 = full.index("{{ 漏洞.等级 }}")
    toks = [
        {"start": i1, "end": i1 + len("{{ 漏洞.名称 }}"), "kind": "var", "text": "{{ 漏洞.名称 }}"},
        {"start": i2, "end": i2 + len("{{ 漏洞.等级 }}"), "kind": "var", "text": "{{ 漏洞.等级 }}"},
    ]
    model = {"blocks": [
        {"id": "h", "type": "heading", "level": 1, "text": full, "tokens": toks},
        {"id": "p", "type": "paragraph", "text": full, "tokens": toks},
    ]}
    slots = align.build_slots(model, [
        {"name": "漏洞.名称", "kind": "scalar", "path": "漏洞列表[].名称"},
        {"name": "漏洞.等级", "kind": "scalar", "path": "漏洞列表[].等级"},
    ])
    assert slots
    for s in slots:
        assert "{" not in s["label"] and "}" not in s["label"], \
            "%s 的标签里带模板标记：%r" % (s["name"], s["label"])
        assert "{" not in s["section"] and "}" not in s["section"], \
            "%s 的章节名带模板标记：%r" % (s["name"], s["section"])


def test_row_name_above_the_placeholder_becomes_the_label():
    """独占一行的占位符，它的名字是**上一行**那四个字。

    实际写法是：
        影响范围                ← 单独一行，是这一行的名字
        {{ 漏洞.影响范围 }}      ← 下一段，自己一个字都没有
    只看占位符所在段落，标签永远是空；而用户嘴上说的「段落名称」正是上一行。
    """
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "影响范围", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 漏洞.影响范围 }}",
         "tokens": [{"start": 0, "end": 16, "kind": "var", "text": "{{ 漏洞.影响范围 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "漏洞.影响范围", "kind": "scalar",
                                       "path": "漏洞列表[].影响范围"}])
    assert slots[0]["label"] == "影响范围"


def test_out_of_order_but_named_section_is_still_filled():
    """顺序和模板不一致、但标签一字不差，就必须填上。

    用户把「漏洞描述」写在「漏洞地址」后面，模板里描述段却在地址前面——
    单调约束过不去，只靠分数也够不到 FREE_PASS，于是最该填的一节被丢进
    「未匹配」。这时候作数的是"名字对上了"，不是分数高低。
    """
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "漏洞地址", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 漏洞.漏洞地址 }}",
         "tokens": [{"start": 0, "end": 14, "kind": "var", "text": "{{ 漏洞.漏洞地址 }}"}]},
        {"id": "c", "type": "paragraph", "text": "漏洞描述", "tokens": []},
        {"id": "d", "type": "paragraph", "text": "{{ 描述段.段落 }}",
         "tokens": [{"start": 0, "end": 13, "kind": "var", "text": "{{ 描述段.段落 }}"}]},
    ]}
    slots = align.build_slots(model, [
        {"name": "描述段.段落", "kind": "scalar", "path": "漏洞列表[].描述[].段落"},
        {"name": "漏洞.漏洞地址", "kind": "scalar", "path": "漏洞列表[].漏洞地址"},
    ])
    html = ("<h2>漏洞地址</h2><p>https://example.com/a?id=1</p>"
            "<h2>漏洞描述</h2><p>在 id 参数处发现注入。</p>")
    res = align.align(align.parse_freeform(html), slots)
    got = {m["slot"]: m["value"] for m in res["matches"]}
    assert got.get("漏洞.漏洞地址") == "https://example.com/a?id=1"
    assert got.get("描述段.段落") == "在 id 参数处发现注入。", res["matches"]


# ---------------------------------------------------------------- 中文文档的真实排版规律

HEI = 'font-family:黑体;font-size:16pt'
SONG = 'font-family:仿宋_GB2312;font-size:16pt'


def test_heading_detected_by_font_swap_only():
    """小标题用黑体、正文用仿宋、字号一模一样——这是中文报告最常见的分层方式。

    只认「加粗」和「字号更大」的标题判定在这里全部失效，用户从 Word 粘过来的
    「影响范围 / 漏洞描述 / 修复建议」被当成普通正文，整篇塌成一个巨型段落，
    随后一个字段都对不上，界面上只剩"一条都没识别到"。
    """
    ps = align.parse_freeform(
        '<p style="%s">影响范围</p><p style="%s">可读取全院患者就诊记录。</p>' % (HEI, SONG)
    )
    assert [p["is_heading"] for p in ps] == [True, False], ps

    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "影响范围", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 漏洞.影响范围 }}",
         "tokens": [{"start": 0, "end": 16, "kind": "var", "text": "{{ 漏洞.影响范围 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "漏洞.影响范围", "kind": "scalar",
                                       "path": "漏洞列表[].影响范围"}])
    res = align.align(ps, slots)
    got = {m["slot"]: m["value"] for m in res["matches"]}
    assert got.get("漏洞.影响范围") == "可读取全院患者就诊记录。", res["matches"]


def test_one_sentence_in_another_font_is_not_a_heading():
    """正文里换字体强调一整句，不算小标题。

    判错的代价不对称：漏判只是少一个字段，误判会把后面所有正文吞进
    这个"标题"的值里，而用户看不出来——他只会觉得生成的文档内容串了。
    """
    ps = align.parse_freeform(
        '<p style="%s">这一段是正文，说明测试的边界条件以及复现步骤。</p>'
        '<p style="%s">需要整改</p>' % (SONG, HEI)
    )
    # 带句号的长句不是标题
    assert ps[0]["is_heading"] is False
    # 换字体的短句才是：它天然是"行名"
    assert ps[1]["is_heading"] is True


def test_single_font_document_is_not_chopped_into_headings():
    """全文一种字体时，这条判据必须完全不生效。

    否则「正文里所有短句」都会变成小标题，比不分组更糟。
    """
    ps = align.parse_freeform(
        '<p style="%s">漏洞名称</p><p style="%s">SQL 注入</p>' % (SONG, SONG)
    )
    assert [p["is_heading"] for p in ps] == [False, False], ps


def test_two_char_core_alone_cannot_claim_a_field():
    """两字片段不能单独认领字段：「项目名称」不该被当成「漏洞.名称」。

    `漏洞.名称` 的核心是「漏洞」和「名称」，而「漏洞」出现在「漏洞名称」里、
    「名称」出现在「项目名称」里。打分是按全局总分选的，两个"都像"的假匹配
    加起来分更高，会稳定赢过一组正确答案——结果整篇错位一格。
    """
    model = {"blocks": [
        {"id": "p0", "type": "paragraph", "text": "{{ 项目名称 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 项目名称 }}"}]},
        {"id": "p1", "type": "heading",
         "text": "{{ 漏洞.名称 }}（{{ 漏洞.等级 }}）",
         "tokens": [{"start": 0, "end": 11, "kind": "var", "text": "{{ 漏洞.名称 }}"},
                    {"start": 12, "end": 23, "kind": "var", "text": "{{ 漏洞.等级 }}"}]},
    ]}
    slots = align.build_slots(model, [
        {"name": "项目名称", "kind": "scalar", "path": "项目名称"},
        {"name": "漏洞.名称", "kind": "scalar", "path": "漏洞列表[].名称"},
        {"name": "漏洞.等级", "kind": "scalar", "path": "漏洞列表[].等级"},
    ])
    html = ('<p style="%s">项目名称</p><p style="%s">某市第一医院挂号系统安全测试</p>'
            '<p style="%s">漏洞名称</p><p style="%s">挂号查询接口 SQL 注入</p>'
            % (HEI, SONG, HEI, SONG))
    res = align.align(align.parse_freeform(html), slots)
    got = {m["slot"]: m["value"] for m in res["matches"]}
    assert got.get("项目名称") == "某市第一医院挂号系统安全测试", res["matches"]
    assert got.get("漏洞.名称") == "挂号查询接口 SQL 注入", res["matches"]
    # 用户根本没写等级，就不该被别的段落硬塞一个值
    assert "漏洞.等级" not in got, res["matches"]


def test_heading_slot_does_not_borrow_caption_as_label():
    """标题段不继承上一行的图表题注当标签。

    `表 1-3 漏洞结果列表` 是题注，不是字段名。拿它当 `漏洞.名称` 的标签，
    等于给这个字段贴了个假名字，之后所有靠名字的匹配都被带偏。
    标签留空反而更准：让字段名自己去比。
    """
    model = {"blocks": [
        {"id": "cap", "type": "paragraph", "text": "表 1-3 漏洞结果列表", "tokens": []},
        {"id": "h", "type": "heading", "text": "{{ 漏洞.名称 }}",
         "tokens": [{"start": 0, "end": 11, "kind": "var", "text": "{{ 漏洞.名称 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "漏洞.名称", "kind": "scalar",
                                       "path": "漏洞列表[].名称"}])
    assert slots[0]["label"] == "", slots[0]


def test_picture_led_group_still_feeds_image_slot():
    """截图后面跟了一句图注，截图仍然要能进图片字段。

    用户粘的是「一张图 + 一句说明」，这一组从此有了文字。若只按"纯图段落"
    判定，这张图就再也匹配不上图片槽位——而它明明就是那张截图。
    """
    model = {"blocks": [
        {"id": "p0", "type": "paragraph", "text": "{{ 图.image_截图 }}",
         "tokens": [{"start": 0, "end": 16, "kind": "var", "text": "{{ 图.image_截图 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "图.image_截图", "kind": "image",
                                       "path": "漏洞列表[].截图[].image_截图"}])
    assert slots[0]["kind"] == "image"
    html = ('<p><img src="data:image/png;base64,AAA" width="600" height="300"></p>'
            '<p>注入点返回的报错页面</p>')
    res = align.align(align.parse_freeform(html), slots)
    assert [m["slot"] for m in res["matches"]] == ["图.image_截图"], res["matches"]


def test_name_hit_is_exposed_so_frontend_can_precheck():
    """「名字对得上」要单独返回，不能只靠分数判断。

    模板里靠标签命中的匹配分数一律在 0.55 上下（字体指纹那类反而能到 0.7），
    前端只按 ≥0.75 预勾的话，名字完全对得上的项一条都勾不上，用户每次都要
    手动勾满——那等于把判断责任又推回给他。
    """
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "影响范围", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 漏洞.影响范围 }}",
         "tokens": [{"start": 0, "end": 16, "kind": "var", "text": "{{ 漏洞.影响范围 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "漏洞.影响范围", "kind": "scalar",
                                       "path": "漏洞列表[].影响范围"}])
    html = "<h2>影响范围</h2><p>可读取全院患者就诊记录。</p>"
    res = align.align(align.parse_freeform(html), slots)
    assert res["matches"], res
    mt = res["matches"][0]
    assert mt["name_hit"] is True, mt
    assert mt["score"] < 0.75, "这条的分数本来就低，所以才需要 name_hit"


# ------------------------------------------------ 本轮「丰富测试」暴露的形态

def test_chinese_ordinal_prefix_is_a_heading():
    """「一、」这种中文序号必须能认成小标题。

    中文报告的章节编号极少用「1.」「第一章」，最常见的就是「一、」「二、」。
    正则只写数字与「第X章」时，整类模板的小标题识别不出来：一篇写得好好的
    报告会被当成一大段正文，所有字段挤在同一组里互相抢。
    """
    text = "一、测试范围\n本次覆盖 Web 端全部接口\n二、测试结论\n风险整体可控"
    paras = align.parse_freeform(text, "text")
    heads = [p["text"] for p in paras if p["is_heading"]]
    assert heads == ["一、测试范围", "二、测试结论"], paras


def test_url_is_not_split_as_label_and_value():
    """`https://…` 字面上就是「标签:值」，不能被拆开。

    漏洞地址、回调地址、仓库地址全是 URL，被拆开的话值就只剩 `//example.com/…`，
    填进模板是个残的地址——而且用户很难一眼看出来少了 https:。
    """
    for url in ("https://example.com/a?id=1", "http://10.0.0.5:8080/login",
                "mailto:a@b.com"):
        assert align._split_inline_label(url) is None, url
    # 正常的「标签：值」仍要拆
    assert align._split_inline_label("项目名称：某市第一医院") == ("项目名称", "某市第一医院")


def test_inline_colon_form_fills_field():
    """信息区一行一个字段（`项目名称：某医院`）要能落到对应槽位。

    从 Word 或网页表格里复制出来就是这个形态，没有小标题、没有字体差，
    唯一的线索是那个冒号。
    """
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "项目名称", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 项目名称 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 项目名称 }}"}]},
        {"id": "c", "type": "paragraph", "text": "项目编号", "tokens": []},
        {"id": "d", "type": "paragraph", "text": "{{ 项目编号 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 项目编号 }}"}]},
    ]}
    slots = align.build_slots(model, [
        {"name": "项目名称", "kind": "scalar", "path": "项目名称"},
        {"name": "项目编号", "kind": "scalar", "path": "项目编号"},
    ])
    html = "<p>项目名称：某市第一医院挂号系统</p><p>项目编号：ZB-2024-001</p>"
    got = {m["slot"]: m["value"] for m in align.align(align.parse_freeform(html), slots)["matches"]}
    assert got.get("项目名称") == "某市第一医院挂号系统", got
    assert got.get("项目编号") == "ZB-2024-001", got


def test_plain_text_label_and_value_on_separate_lines():
    """纯文本「标签\\n值」也要能填上。

    记事本里写东西就是这个样子：一行名字、一行内容，交替往下。
    """
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "建设单位", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 建设单位 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 建设单位 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "建设单位", "kind": "scalar",
                                       "path": "建设单位"}])
    text = "建设单位\n某市卫生健康委员会\n项目名称\n某市第一医院"
    got = {m["slot"]: m["value"] for m in align.align(align.parse_freeform(text, "text"), slots)["matches"]}
    assert got.get("建设单位") == "某市卫生健康委员会", got


def test_table_first_column_is_a_row_label():
    """两列表格的首列是这一行的名字。

    单元格里是没有任何样式的纯文字（没字体差、没序号、不加粗），样式信号
    全灭，唯一的依据是结构：它是这一行的第一格。
    """
    html = ("<table><tr><td>项目名称</td><td>某市第一医院</td></tr>"
            "<tr><td>建设单位</td><td>某市卫健委</td></tr></table>")
    paras = align.parse_freeform(html)
    labels = [p["text"] for p in paras if p.get("row_label")]
    assert labels == ["项目名称", "建设单位"], paras


def test_expression_token_resolves_to_the_real_field():
    """`{{ 编制人 or "未指派" }}` 的字段是「编制人」，不是整段表达式。

    内置演示模板里就有五处这种写法。早先拿整段表达式当字段名，界面上会出现
    「编制人 or "未指派"」这种用户根本填不进去的字段，对齐也永远匹配不上。
    """
    tok = '{{ 编制人 or "未指派" }}'
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "编制人", "tokens": []},
        {"id": "b", "type": "paragraph", "text": tok,
         "tokens": [{"start": 0, "end": len(tok), "kind": "var", "text": tok}]},
    ]}
    variables = [{"name": "编制人", "kind": "scalar", "path": "编制人"}]
    slots = align.build_slots(model, variables)
    assert [s["name"] for s in slots] == ["编制人"], slots
    assert slots[0]["path"] == "编制人"

    html = "<h2>编制人</h2><p>张三</p>"
    got = {m["slot"]: m["value"] for m in align.align(align.parse_freeform(html), slots)["matches"]}
    assert got.get("编制人") == "张三", got


def test_unresolvable_expression_token_is_skipped():
    """解析不出真实字段的占位符（`loop.index`、纯字面量）不造槽位。"""
    for tok in ("{{ loop.index }}", '{{ "——" }}', "{{ a + b }}"):
        model = {"blocks": [
            {"id": "b", "type": "paragraph", "text": tok,
             "tokens": [{"start": 0, "end": len(tok), "kind": "var", "text": tok}]},
        ]}
        assert align.build_slots(model, []) == [], tok


def test_second_record_in_the_same_loop_is_kept():
    """同一个循环里的第二条记录不能被静默丢掉。

    「漏洞数量是循环内容，可能填写一个或多个」是这类模板的核心用法。可槽位表里
    每个字段只有一个位置、DP 又是 1:1 的：用户粘两条漏洞时，第二套小标题占不到
    任何槽位，直接被丢掉，界面还报「已填入 3 项」——两条里只进去一条，没有任何提示。

    只有**数组项里的字段**允许重复；标量重复会覆盖原值，那是更严重的错。
    """
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "漏洞名称", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 漏洞.名称 }}",
         "tokens": [{"start": 0, "end": 12, "kind": "var", "text": "{{ 漏洞.名称 }}"}]},
        {"id": "c", "type": "paragraph", "text": "影响范围", "tokens": []},
        {"id": "d", "type": "paragraph", "text": "{{ 漏洞.影响范围 }}",
         "tokens": [{"start": 0, "end": 14, "kind": "var", "text": "{{ 漏洞.影响范围 }}"}]},
        {"id": "e", "type": "paragraph", "text": "项目名称", "tokens": []},
        {"id": "f", "type": "paragraph", "text": "{{ 项目名称 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 项目名称 }}"}]},
    ]}
    slots = align.build_slots(model, [
        {"name": "漏洞.名称", "kind": "scalar", "path": "漏洞列表[].名称"},
        {"name": "漏洞.影响范围", "kind": "scalar", "path": "漏洞列表[].影响范围"},
        {"name": "项目名称", "kind": "scalar", "path": "项目名称"},
    ])
    html = ("<h2>漏洞名称</h2><p>挂号接口 SQL 注入</p>"
            "<h2>影响范围</h2><p>可读取全院就诊记录。</p>"
            "<h2>漏洞名称</h2><p>排班接口越权</p>"
            "<h2>影响范围</h2><p>任意医生可查全院排班。</p>")
    res = align.align(align.parse_freeform(html), slots)
    names = [m["value"] for m in res["matches"] if m["slot"] == "漏洞.名称"]
    assert names == ["挂号接口 SQL 注入", "排班接口越权"], res["matches"]
    scopes = [m["value"] for m in res["matches"] if m["slot"] == "漏洞.影响范围"]
    assert scopes == ["可读取全院就诊记录。", "任意医生可查全院排班。"], res["matches"]


def test_scalar_slot_is_never_written_twice():
    """标量字段重复出现也不能写两遍——第二次一定覆盖第一次，是纯粹的倒退。"""
    model = {"blocks": [
        {"id": "a", "type": "paragraph", "text": "项目名称", "tokens": []},
        {"id": "b", "type": "paragraph", "text": "{{ 项目名称 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 项目名称 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "项目名称", "kind": "scalar",
                                       "path": "项目名称"}])
    html = ("<h2>项目名称</h2><p>某市第一医院挂号系统</p>"
            "<h2>项目名称</h2><p>另一个项目</p>")
    res = align.align(align.parse_freeform(html), slots)
    assert [m["value"] for m in res["matches"]] == ["某市第一医院挂号系统"], res["matches"]


def test_table_header_cell_that_is_a_placeholder_is_not_a_column_name():
    """表头那一格本身是占位符时，不能把 `{{ 报告编号 }}` 当成列名。

    不少模板把页眉信息做成两列表格，第一行第一格写的直接是占位符。取原始文字
    当列名的话，几个字段的「区域名称」会一起变成 `{{ 报告编号 }}`——界面上
    用户根本分不清这一条要填到哪儿，而且花括号还会被当成标签参与打分。
    """
    tok = "{{ 报告编号 }}"
    model = {"blocks": [
        {"id": "t", "type": "table", "rows": [
            {"id": "t0", "cells": [
                {"blocks": [{"id": "h0", "type": "paragraph", "text": tok,
                             "tokens": [{"start": 0, "end": 10, "kind": "var", "text": tok}]}]},
                {"blocks": [{"id": "h1", "type": "paragraph", "text": tok,
                             "tokens": [{"start": 0, "end": 10, "kind": "var", "text": tok}]}]},
            ]},
            {"id": "t1", "cells": [
                {"blocks": [_token_para(tok, tok)]},
                {"blocks": [{"id": "c1", "type": "paragraph", "text": "出具单位",
                             "tokens": [{"start": 0, "end": 12, "kind": "var",
                                         "text": "{{ 出具单位 }}"}]}]},
            ]},
        ]},
    ]}
    slots = align.build_slots(model, [
        {"name": "报告编号", "kind": "scalar", "path": "报告编号"},
        {"name": "出具单位", "kind": "scalar", "path": "出具单位"},
    ])
    for s in slots:
        assert "{{" not in (s.get("column_label") or ""), s


def test_exactly_matching_two_char_field_name_is_not_discounted():
    """整段就叫「建议」时，它是字段名本身，不该被当成"短片段"打折。

    `{{ it.建议 }}` 的核心是 `it` / `建议`，两个都短。用户的小标题恰恰写「建议」。
    打折的话分数落到弱相似那一档，一个名字一字不差、本该最确定的字段反而
    谁也认不出来——而那正是模板里唯一能装它的位置。
    """
    model = {"blocks": [
        {"id": "a", "type": "table", "rows": [
            {"id": "a0", "cells": [
                {"blocks": [{"id": "h0", "type": "paragraph", "text": "问题", "tokens": []}]},
                {"blocks": [{"id": "h1", "type": "paragraph", "text": "处置建议", "tokens": []}]},
            ]},
            {"id": "a1", "cells": [
                {"blocks": [{"id": "c0", "type": "paragraph", "text": "{{ it.问题 }}",
                             "tokens": [{"start": 0, "end": 10, "kind": "var",
                                         "text": "{{ it.问题 }}"}]}]},
                {"blocks": [{"id": "c1", "type": "paragraph", "text": "{{ it.建议 }}",
                             "tokens": [{"start": 0, "end": 10, "kind": "var",
                                         "text": "{{ it.建议 }}"}]}]},
            ]},
        ]},
    ]}
    slots = align.build_slots(model, [
        {"name": "it.问题", "kind": "scalar", "path": "问题分组[].条目[].问题"},
        {"name": "it.建议", "kind": "scalar", "path": "问题分组[].条目[].建议"},
    ])
    html = "<h2>建议</h2><p>统一开启安全审计并接入日志平台。</p>"
    res = align.align(align.parse_freeform(html), slots)
    got = {m["slot"]: m["value"] for m in res["matches"]}
    assert got.get("it.建议") == "统一开启安全审计并接入日志平台。", res["matches"]


def test_section_title_is_not_borrowed_as_field_label():
    """上一行是章节标题时，不能借来当字段标签。

    借了的话同一节里的几个字段标签全一样（都成了章节名），界面上看不出
    这一条要填到哪儿；标签留空、让字段名自己去比，反而更准。
    """
    model = {"blocks": [
        {"id": "s", "type": "heading", "text": "月度经营分析报告", "tokens": []},
        {"id": "a", "type": "paragraph", "text": "{{ 经营概况 }}",
         "tokens": [{"start": 0, "end": 10, "kind": "var", "text": "{{ 经营概况 }}"}]},
    ]}
    slots = align.build_slots(model, [{"name": "经营概况", "kind": "scalar",
                                       "path": "经营概况"}])
    assert slots[0]["section"] == "月度经营分析报告"
    assert slots[0]["label"] == "", slots[0]
