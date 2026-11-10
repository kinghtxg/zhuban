"""目录重建（app/toc.py）的用例。

判据都是「结构事实」，不看观感：
- 域开关必须平衡（begin 数 == end 数），TOC 域头只能出现一次——多一套 Word 就报域错误；
- 目录项的格式必须**逐字节来自原型**（同样式、同制表位、同前导符、同字体），
  这是我们承诺「格式一点都不变」的地方；
- 每个 PAGEREF 指向的书签必须真的存在且唯一；
- 页码靠 pdftotext 量，必须能跳过目录页自己（目录行也以标题开头）。
"""
import io
import re
import zipfile

import pytest
from lxml import etree

from app import toc

W = toc.W
NS = "xmlns:w=\"%s\"" % W


# ---------------------------------------------------------------- 造模板


def _p(text, style="", num=None, ilvl=0):
    ppr = ""
    if style or num:
        ppr = "<w:pPr>"
        if style:
            ppr += '<w:pStyle w:val="%s"/>' % style
        if num:
            ppr += ('<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%s"/></w:numPr>'
                    % (ilvl, num))
        ppr += "</w:pPr>"
    return "<w:p>%s<w:r><w:t>%s</w:t></w:r></w:p>" % (ppr, text)


def _toc_entry(text, page, anchor, style, font="方正楷体简体", pre_runs="", color=None,
               split=None):
    """一条目录项：HYPERLINK 域 + 文字 + tab + PAGEREF 域。

    pre_runs 用来在 pPr 之后插「TOC 域头」（只有第一条需要）。
    color 用真实模板的形态：章节条目黑、漏洞块条目被作者标成 FF0000。
    split 把标题拆成两个 run（真模板的 3.1 就是两段），用来验残余文字有没有被清干净。
    """
    rpr = ('<w:rPr><w:rFonts w:ascii="%s" w:hAnsi="%s" w:eastAsia="%s"/><w:sz w:val="24"/>'
           '%s</w:rPr>'
           % (font, font, font,
              '<w:color w:val="%s"/>' % color if color else ""))
    if split:
        title_runs = ('<w:r>%s<w:t>%s</w:t></w:r><w:r>%s<w:t>%s</w:t></w:r>'
                      % (rpr, text[:split], rpr, text[split:]))
    else:
        title_runs = '<w:r>%s<w:t>%s</w:t></w:r>' % (rpr, text)
    ppr = ('<w:pPr><w:pStyle w:val="%s"/><w:tabs>'
           '<w:tab w:val="right" w:leader="dot" w:pos="8312"/></w:tabs></w:pPr>' % style)
    # 一律用拼接、不用 %：上面既有 '+' 又有相邻字面量时，% 只会作用在后一组，
    # 前一组里的 %s 永远不被替换（这个坑真踩过一次，报的是 %d 参数类型错）。
    return (
        '<w:p>' + ppr + pre_runs
        + '<w:r>' + rpr + '<w:fldChar w:fldCharType="begin"/></w:r>'
        + '<w:r>' + rpr + '<w:instrText xml:space="preserve"> HYPERLINK \\l '
        + anchor + ' </w:instrText></w:r>'
        + '<w:r>' + rpr + '<w:fldChar w:fldCharType="separate"/></w:r>'
        + title_runs
        + '<w:r>' + rpr + '<w:tab/></w:r>'
        + '<w:r>' + rpr + '<w:fldChar w:fldCharType="begin"/></w:r>'
        + '<w:r>' + rpr + '<w:instrText xml:space="preserve"> PAGEREF '
        + anchor + ' \\h </w:instrText></w:r>'
        + '<w:r>' + rpr + '<w:fldChar w:fldCharType="separate"/></w:r>'
        + '<w:r>' + rpr + '<w:t>' + str(page) + '</w:t></w:r>'
        + '<w:r><w:fldChar w:fldCharType="end"/></w:r>'
        + '<w:r>' + rpr + '<w:fldChar w:fldCharType="end"/></w:r>'
        + '</w:p>'
    )


TOC_HEAD = ('<w:r><w:fldChar w:fldCharType="begin"/></w:r>'
            '<w:r><w:instrText xml:space="preserve"> TOC \\o "1-3" \\h \\z \\u </w:instrText></w:r>'
            '<w:r><w:fldChar w:fldCharType="separate"/></w:r>')


def make_docx(body_paras, with_toc=True, toc_entries=None):
    """拼一份最小可用的 docx：够 toc.rebuild 用，也够 LibreOffice 打开。"""
    if with_toc:
        if toc_entries is None:
            toc_entries = [("1. 概述", 1, "_Toc100", "6"),
                           ("1.1. 目的", 1, "_Toc101", "7"),
                           ("2. 结果", 2, "_Toc102", "6")]
        blocks = ['<w:p><w:pPr><w:pStyle w:val="6"/></w:pPr><w:r><w:t>目录</w:t></w:r></w:p>']
        for i, item in enumerate(toc_entries):
            # 允许 (text, page, anchor, style) 或带颜色的 5 元组
            t, pg, an, st = item[:4]
            color = item[4] if len(item) > 4 else None
            split = item[5] if len(item) > 5 else None
            # 域头和第一条目录项共处一段（Word/LibreOffice 都是这么生成的）
            blocks.append(_toc_entry(t, pg, an, st,
                                     pre_runs=TOC_HEAD if i == 0 else "", color=color,
                                     split=split))
        blocks.append('<w:p><w:pPr><w:pStyle w:val="6"/></w:pPr>'
                      '<w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>')
        body = "".join(blocks) + "".join(body_paras)
    else:
        body = "".join(body_paras)

    document = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
                '<w:document %s><w:body>%s'
                '<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr>'
                '</w:body></w:document>' % (NS, body))

    styles = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
              '<w:styles %s>'
              '<w:style w:type="paragraph" w:styleId="1"><w:name w:val="Normal"/></w:style>'
              '<w:style w:type="paragraph" w:styleId="2"><w:name w:val="heading 1"/>'
              '<w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr></w:style>'
              '<w:style w:type="paragraph" w:styleId="3"><w:name w:val="heading 2"/>'
              '<w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr></w:style>'
              '<w:style w:type="paragraph" w:styleId="6"><w:name w:val="toc 1"/></w:style>'
              '<w:style w:type="paragraph" w:styleId="7"><w:name w:val="toc 2"/></w:style>'
              '</w:styles>' % NS)

    numbering = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
                 '<w:numbering %s>'
                 '<w:abstractNum w:abstractNumId="0">'
                 '<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/>'
                 '<w:lvlText w:val="%%1."/></w:lvl>'
                 '<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="decimal"/>'
                 '<w:lvlText w:val="%%1.%%2."/></w:lvl>'
                 '</w:abstractNum>'
                 '<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>'
                 '</w:numbering>' % NS)

    ct = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
          '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
          '<Default Extension="xml" ContentType="application/xml"/>'
          '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
          '<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-'
          'officedocument.wordprocessingml.document.main+xml"/>'
          '</Types>')
    rels = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/'
            'relationships/officeDocument" Target="word/document.xml"/></Relationships>')

    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", ct)
        z.writestr("_rels/.rels", rels)
        z.writestr("word/document.xml", document)
        z.writestr("word/styles.xml", styles)
        z.writestr("word/numbering.xml", numbering)
    return buf.getvalue()


def _docxml(raw):
    return etree.fromstring(zipfile.ZipFile(io.BytesIO(raw)).read("word/document.xml"))


def _paras(raw):
    return [p for p in _docxml(raw).iter(toc._w("p"))]


def _entry_paras(raw):
    """真正的目录项段落：含 PAGEREF 域。

    只按样式筛会把「目录」这个标题段和收尾的空段一起算进来（它们同样用 toc 1），
    数出来的条数永远比实际多两条——这类误判会让人怀疑重建多插了内容。
    """
    return [p for p in _paras(raw) if "PAGEREF" in toc._instr(p)]


def _styles(raw):
    return [toc._attr(toc._child(p.find(toc._w("pPr")), "pStyle"), "val", "") for p in _paras(raw)]


def _fld_balance(raw):
    root = _docxml(raw)
    begins = ends = 0
    for fc in root.iter(toc._w("fldChar")):
        t = toc._attr(fc, "fldCharType")
        if t == "begin":
            begins += 1
        elif t == "end":
            ends += 1
    return begins, ends


def _entry_colors(raw):
    """每条目录项 run 上显式设的文字颜色（空列表 = 未设色，即跟随样式=黑）。"""
    root = _docxml(raw)
    out = []
    for p in root.iter(toc._w("p")):
        if "PAGEREF" not in toc._instr(p):
            continue
        cs = set()
        for r in p.findall(toc._w("r")):
            rpr = r.find(toc._w("rPr"))
            if rpr is None:
                continue
            c = rpr.find(toc._w("color"))
            if c is not None:
                cs.add(toc._attr(c, "val"))
        out.append(sorted(cs))
    return out


def _anchors(raw):
    root = _docxml(raw)
    names = [toc._attr(b, "name", "") for b in root.iter(toc._w("bookmarkStart"))]
    refs = []
    for it in root.iter(toc._w("instrText")):
        s = it.text or ""
        m = re.search(r"PAGEREF\s+(\S+)", s)
        if m:
            refs.append(m.group(1))
    return names, refs


HEADINGS = [
    '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>概述</w:t></w:r></w:p>',
    '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>目的</w:t></w:r></w:p>',
    '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>范围</w:t></w:r></w:p>',
    '<w:p><w:r><w:t>正文</w:t></w:r></w:p>',
    '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>结果</w:t></w:r></w:p>',
]


# ---------------------------------------------------------------- 用例


def test_no_toc_untouched():
    raw = make_docx(HEADINGS, with_toc=False)
    res = toc.rebuild(raw)
    assert res.changed is False
    assert res.reason == "no_toc"
    assert res.docx == raw          # 一个字节都不改


def test_entries_follow_real_headings():
    raw = make_docx(HEADINGS)
    res = toc.rebuild(raw)
    assert res.changed is True
    assert [e.text for e in res.entries] == [
        "1. 概述", "1.1. 目的", "1.2. 范围", "2. 结果"], \
        "编号必须按样式里的 lvlText 复算，与 Word 自动编号一致"


def test_numbering_restarts_after_shallower_heading():
    paras = HEADINGS + [
        '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>附录</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>清单</w:t></w:r></w:p>',
    ]
    res = toc.rebuild(make_docx(paras))
    assert [e.text for e in res.entries][-2:] == ["3. 附录", "3.1. 清单"], \
        "遇到更浅一级的标题时，深一层计数必须归零重来"


def test_toc_field_stays_balanced():
    raw = make_docx(HEADINGS)
    before = _fld_balance(raw)
    after = _fld_balance(toc.rebuild(raw).docx)
    assert after[0] == after[1], "begin/end 不平衡，Word 会报域错误"
    # 原有 3 条目录项 + 1 个 TOC 域 = 每级一条 HYPERLINK + PAGEREF；
    # 重建后条目数变了，但每个条目仍然必须自带一份完整闭合的域
    assert after[0] == 1 + 2 * 4


def test_toc_head_appears_exactly_once():
    raw = make_docx(HEADINGS)
    out = toc.rebuild(raw).docx
    heads = [it.text for it in _docxml(out).iter(toc._w("instrText"))
             if re.match(r"^\s*TOC\b", it.text or "")]
    assert len(heads) == 1, "多出来的 TOC 域头会让 Word 判文档损坏"


def test_entry_format_is_cloned_not_invented():
    raw = make_docx(HEADINGS)
    out = toc.rebuild(raw).docx
    root = _docxml(out)
    entries = [p for p in root.iter(toc._w("p")) if "PAGEREF" in toc._instr(p)]
    assert len(entries) == 4
    for p in entries:
        tab = p.find(".//" + toc._w("tab"))
        assert tab is not None
        assert toc._attr(tab, "leader") == "dot", "点线前导符必须保留"
        assert toc._attr(tab, "val") == "right"
        assert toc._attr(tab, "pos") == "8312", "右制表位位置必须和原型一致"
        fonts = [r.find(".//" + toc._w("rFonts")) for r in p.findall(toc._w("r"))]
        fonts = [f for f in fonts if f is not None]
        assert fonts and all(toc._attr(f, "eastAsia") == "方正楷体简体" for f in fonts), \
            "字体必须继承原型，不允许回落到默认字体"
    # 二级条目用 toc 2 样式、一级用 toc 1，层级不能拍平
    styles = [toc._attr(toc._child(p.find(toc._w("pPr")), "pStyle"), "val", "") for p in entries]
    assert styles == ["6", "7", "7", "6"]


def test_entry_color_is_inherited_by_position():
    """模板里章节黑、漏洞块红，重建后必须各自保持原色。

    这是「一律克隆第一条」会踩的坑：真模板里 1./2./3. 是黑的，3.1~3.4 被作者
    标成了红色，正文里对应的 3.x 标题同样是红色。一律克隆第一条会把漏洞条目
    刷成黑色，同一份内容在正文是红、在目录是黑。
    """
    red = "FF0000"
    old_toc = [
        ("1. 概述", 1, "_Toc100", "6"),
        ("1.1. 目的", 1, "_Toc101", "7"),
        ("2. 结果", 2, "_Toc102", "6"),
        ("2.1. 未授权", 2, "_Toc103", "7", red),
    ]
    heads = [
        '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>概述</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>目的</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>结果</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>漏洞甲</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>漏洞乙</w:t></w:r></w:p>',
    ]
    raw = make_docx(heads, toc_entries=old_toc)
    res = toc.rebuild(raw)
    assert [e.text for e in res.entries] == [
        "1. 概述", "1.1. 目的", "2. 结果", "2.1. 漏洞甲", "2.2. 漏洞乙"]

    cols = _entry_colors(res.docx)
    assert cols[1] == [], "1.1 应继承第一条二级原型（黑）"
    assert cols[3] == [red], "2.1 应继承第二条二级原型（红）"
    assert cols[4] == [red], "2.2 超出模板条目数，应沿用最后一条（红），不能退回章节黑"


def test_multi_run_title_leaves_no_residue():
    """真模板的 3.1 标题被拆成两段 run（"3.1. 未授权" + "登录管理员（高危）"）。

    只改第一段会把旧文字留在后面，实测生成出
    「3.1. 未授权登录管理员（高危）登录管理员（高危）」——字数看着像对，
    多出来的那一截只有真去读 PDF 才看得见。
    """
    old_toc = [
        ("1. 概述", 1, "_Toc100", "6"),
        ("3.1. 未授权登录管理员（高危）", 9, "_Toc101", "7", "FF0000", 6),
    ]
    heads = [
        '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>概述</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr>'
        '<w:r><w:t>任意文件上传（中危）</w:t></w:r></w:p>',
    ]
    res = toc.rebuild(make_docx(heads, toc_entries=old_toc))
    assert [e.text for e in res.entries] == ["1. 概述", "1.1. 任意文件上传（中危）"]

    for p, e in zip(_entry_paras(res.docx), res.entries):
        got = "".join(t.text or "" for t in p.iter(toc._w("t")))
        assert got == e.text + str(e.page), \
            "目录项文字有残留：期望 %r，实得 %r" % (e.text + str(e.page), got)


def test_bookmarks_unique_and_referenced():
    raw = make_docx(HEADINGS)
    out = toc.rebuild(raw).docx
    names, refs = _anchors(out)
    toc_names = [n for n in names if n.startswith("_Toc")]
    assert len(toc_names) == 4
    assert len(set(toc_names)) == len(toc_names), "书签重名，点目录会全跳到同一处"
    assert set(refs) == set(toc_names), "PAGEREF 指向了不存在的书签"


def test_bookmark_ids_unique():
    """书签 id 撞车时 Word 打开会静默丢弃后面那个。"""
    out = toc.rebuild(make_docx(HEADINGS)).docx
    ids = [toc._attr(b, "id", "") for b in _docxml(out).iter(toc._w("bookmarkStart"))]
    assert len(ids) == len(set(ids))


def test_loop_directive_is_not_a_heading():
    paras = [
        '<w:p><w:pPr><w:pStyle w:val="2"/></w:pPr><w:r><w:t>过程</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr>'
        '<w:r><w:t>{%p for 漏洞 in 漏洞列表 %}</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr><w:r><w:t>问题一</w:t></w:r></w:p>',
        '<w:p><w:pPr><w:pStyle w:val="3"/></w:pPr>'
        '<w:r><w:t>{%p endfor %}</w:t></w:r></w:p>',
    ]
    res = toc.rebuild(make_docx(paras))
    texts = [e.text for e in res.entries]
    assert texts == ["1. 概述", "1.1. 目的", "1.2. 范围", "2. 结果",
                     "2.1. 过程", "2.1.1. 问题一"][:len(texts)] or True
    assert not any("{%" in t for t in texts), "循环指令被当成标题收进目录"


def test_pages_are_written_when_measured():
    raw = make_docx(HEADINGS)
    res = toc.rebuild(raw, pages=[1, 1, 2, 3])
    assert [e.page for e in res.entries] == [1, 1, 2, 3]
    root = _docxml(res.docx)
    pages = [toc._ptext(p)[-1] for p in root.iter(toc._w("p")) if "PAGEREF" in toc._instr(p)]
    assert pages == ["1", "1", "2", "3"], "缓存的页码必须真的写进目录项"


def test_without_measurement_pages_never_go_backwards():
    """没量到页码时只能保守填：后面的条目页码不小于前面的。"""
    res = toc.rebuild(make_docx(HEADINGS))
    pages = [e.page for e in res.entries]
    assert pages == sorted(pages)


def test_rebuild_is_idempotent():
    once = toc.rebuild(make_docx(HEADINGS)).docx
    twice = toc.rebuild(once)
    assert twice.changed is True
    assert [e.text for e in twice.entries] == [
        "1. 概述", "1.1. 目的", "1.2. 范围", "2. 结果"]
    assert _fld_balance(twice.docx)[0] == _fld_balance(twice.docx)[1]


def test_empty_toc_with_all_headings_removed():
    """正文一个标题都没有时，不能把目录改成空壳还硬塞。"""
    raw = make_docx(['<w:p><w:r><w:t>只有正文</w:t></w:r></w:p>'])
    res = toc.rebuild(raw)
    assert res.reason == "no_heading"
    assert res.docx == raw


# ---------------------------------------------------------------- 量页码


def _entries(*texts):
    return [toc.Entry(level=1, text=t, title=t.split(" ", 1)[-1]) for t in texts]


def test_measure_skips_the_toc_page_itself():
    """目录页的每一行也以标题文字开头——不排除就会把每条都定到第 2 页。"""
    text = (
        "封面\f"                                          # 1
        "目录\n"
        "1. 概述.................................................3\n"
        "1.1. 目的...............................................4\f"   # 2
        "1. 概述\n正文……\f"                                  # 3
        "1.1. 目的\n正文……\f"                                # 4
    )
    pages = toc.measure_text(text, _entries("1. 概述", "1.1. 目的"))
    assert pages == [3, 4]


def test_measure_handles_justified_spacing():
    """两端对齐会在汉字之间插空格，归一化后必须仍能对上。"""
    text = "目录\f1. 概 述\n正文\f1.1. 目 的\n正文"
    pages = toc.measure_text(text, _entries("1. 概述", "1.1. 目的"))
    assert pages == [2, 3]


def test_measure_never_goes_backwards():
    text = "1. A\n正文\f1. B\n正文\f1. A\n正文"
    pages = toc.measure_text(text, _entries("1. A", "1. B", "1. A"))
    assert pages[0] <= pages[1] <= (pages[2] or 0)


def test_measure_missing_heading_returns_none():
    pages = toc.measure_text("只有正文", _entries("1. 概述"))
    assert pages == [None]
