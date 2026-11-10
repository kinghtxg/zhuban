# -*- coding: utf-8 -*-
"""中文字体族名映射（app/cjk_fonts.py）。

这个模块的失败模式是「什么都没发生」：PDF 照常生成、正文全对，只有汉字是
日文字形。所以测试要钉住的不是「函数能跑」，而是三件容易在重构中悄悄丢掉的事：

1. 族名清单不能漏（漏一个就是一类模板的 PDF 变日文字形）；
2. **本机已经有的族名一个字都不能写**（写进去会把正版 SimSun/msyh 抢走，
   生产实例就踩过这个坑，而且只有对比字形才能发现）；
3. 判定逻辑要把「落到了 JP/KR/TC」和「落到了没有汉字的字体」都算失败，
   不能只看有没有返回东西。
"""
import os

import pytest

from app import cjk_fonts


# ---------------------------------------------------------------- 生成

def test_conf_covers_every_alias_by_default():
    """默认（没给 skip 信息时）要覆盖全部族名，一个都不能少。

    ALIASES 现在是（族名, 候选链），默认形态取候选链末位（Noto 兜底），
    所以这里逐个族名核对「名字在、且目标是它自己的某个候选」。
    """
    xml = cjk_fonts.conf_xml()
    for name, candidates in cjk_fonts.ALIASES:
        assert "<family>%s</family>" % name in xml, "漏了族名 %s" % name
        assert any(xml.count(c) >= 1 for c in candidates), \
            "族名 %s 的目标落到了候选链之外" % name
    assert xml.startswith('<?xml version="1.0"?>')
    assert xml.rstrip().endswith("</fontconfig>")


def test_conf_never_forces_family():
    """不许出现 match/assign：那是强制改写，会抢掉用户装的正版字体。"""
    xml = cjk_fonts.conf_xml()
    assert "assign" not in xml
    assert "<match" not in xml
    assert 'binding="same"' in xml


def test_conf_only_contains_given_aliases():
    xml = cjk_fonts.conf_xml([("宋体", "Noto Serif CJK SC")])
    assert "<family>宋体</family>" in xml
    assert "<family>黑体</family>" not in xml
    assert "<family>sans-serif</family>" not in xml


# ---------------------------------------------------------------- 该不该补

def test_needed_aliases_skips_families_already_installed():
    """本机真有这个字体就不补 —— 这是本次最关键的一条。

    `fc-list` 报出来 `SimSun,宋体` 说明系统里有正版宋体，此时再写一条
    「宋体 → Noto Serif CJK SC」会把正版换掉。实测 alias/prefer 也会抢，
    所以只能靠「装之前先问有没有」。
    """
    installed = {"SimSun", "宋体", "SimHei", "黑体", "Microsoft YaHei", "微软雅黑"}
    needed = dict(cjk_fonts.needed_aliases(installed))
    assert "宋体" not in needed
    assert "SimSun" not in needed
    assert "黑体" not in needed
    assert "微软雅黑" not in needed
    # 缺的仍然要补
    assert needed.get("仿宋") == "Noto Serif CJK SC"
    assert needed.get("等线") == "Noto Sans CJK SC"


def test_needed_aliases_all_when_machine_has_none():
    needed = cjk_fonts.needed_aliases(set())
    assert len(needed) == len(cjk_fonts.ALIASES)


def test_pick_target_prefers_first_installed_candidate():
    """候选链从左往右取第一个装了的 —— 有真楷体就不许退到 Noto。"""
    have = {"Noto Serif CJK SC", "KaiTi_GB2312"}
    assert cjk_fonts.pick_target(("方正楷体简体", "KaiTi_GB2312", "Noto Serif CJK SC"),
                                 have) == "KaiTi_GB2312"
    assert cjk_fonts.pick_target(("方正楷体简体", "Noto Serif CJK SC"),
                                 have) == "Noto Serif CJK SC"


def test_pick_target_falls_back_to_last_even_if_nothing_installed():
    """一个候选都没装时返回末位（Noto），不能返回空。"""
    assert cjk_fonts.pick_target(("KaiTi_GB2312", "Noto Serif CJK SC"), set()) \
        == "Noto Serif CJK SC"


def test_kaiti_and_fangsong_use_real_font_when_installed():
    """楷体/仿宋 兜到 Noto Serif 其实是**宋体**字形，分类都不对。

    装了真楷体/真仿宋之后必须走真的，否则等于白装。这一条钉住候选链的效果。
    """
    have = {"Noto Serif CJK SC", "Noto Sans CJK SC",
            "KaiTi_GB2312", "楷体_GB2312", "FangSong_GB2312", "仿宋_GB2312"}
    needed = dict(cjk_fonts.needed_aliases(have))
    assert needed.get("楷体") == "KaiTi_GB2312"
    assert needed.get("KaiTi") == "KaiTi_GB2312"
    assert needed.get("华文楷体") == "KaiTi_GB2312"
    assert needed.get("仿宋") == "FangSong_GB2312"
    assert needed.get("STFangsong") == "FangSong_GB2312"
    # 族名本身已存在 → 一个字都不许写，否则会把真字体抢走
    assert "楷体_GB2312" not in needed
    assert "仿宋_GB2312" not in needed


def test_needed_aliases_targets_are_strings():
    """目标必须是单个族名（字符串），不是候选链元组。

    漏了 pick_target 就会把元组写进 %s，fontconfig 里出现 "(...)" 这种
    非法族名 —— 语法不报错，但映射等于没有。
    """
    for name, target in cjk_fonts.needed_aliases({"Noto Serif CJK SC"}):
        assert isinstance(target, str), "%s 的目标不是字符串" % name


def test_install_skips_installed_and_writes_rest(monkeypatch):
    """装：只写缺的那部分，并把条数报出来，便于在日志里对账。"""
    monkeypatch.setattr(cjk_fonts, "installed_families",
                        lambda: {"SimSun", "宋体", "SimHei", "黑体"})
    written = {}

    def fake_write(dirs=None, aliases=None):
        written["aliases"] = list(aliases or [])
        return "/tmp/whatever.conf"

    monkeypatch.setattr(cjk_fonts, "write_conf", fake_write)
    monkeypatch.setattr(cjk_fonts, "reload_cache", lambda *a, **k: True)

    st = cjk_fonts.install()
    assert st["written"] is True
    assert "宋体" not in dict(written["aliases"])
    assert "仿宋" in dict(written["aliases"])
    assert st["aliases"] == len(written["aliases"])


def test_install_removes_stale_conf_when_nothing_needed(tmp_path, monkeypatch):
    """机器后来装了正版字体 → 必须把我们上次写的配置删掉。

    否则历史遗留的配置仍然生效，正版字体继续被抢 —— 这正是「先修好又回退」
    的那种事故。
    """
    conf = tmp_path / cjk_fonts.CONF_NAME
    conf.write_text("<fontconfig/>", encoding="utf-8")
    monkeypatch.setattr(cjk_fonts, "target_dirs", lambda: [str(tmp_path)])
    monkeypatch.setattr(cjk_fonts, "installed_families",
                        lambda: {n for n, _ in cjk_fonts.ALIASES})
    monkeypatch.setattr(cjk_fonts, "reload_cache", lambda *a, **k: True)

    st = cjk_fonts.install()
    assert st["written"] is False
    assert st["aliases"] == 0
    assert not conf.exists(), "该删的旧配置没删，正版字体还会被抢"
    assert st["removed"] == [str(conf)]


def test_install_noop_without_stale_file(tmp_path, monkeypatch):
    monkeypatch.setattr(cjk_fonts, "target_dirs", lambda: [str(tmp_path)])
    monkeypatch.setattr(cjk_fonts, "installed_families",
                        lambda: {n for n, _ in cjk_fonts.ALIASES})
    st = cjk_fonts.install()
    assert st["written"] is False and st["aliases"] == 0 and st["removed"] == []


# ---------------------------------------------------------------- 判定

@pytest.mark.parametrize("resolved", [
    "Noto Serif CJK SC",
    "Noto Sans CJK SC,Noto Sans CJK SC",
    "SimSun,宋体",          # 生产实例真装了 Windows 字体
    "SimHei,黑体",
    "Microsoft YaHei,微软雅黑",
    "Source Han Serif SC",
])
def test_classify_accepts_simplified_chinese(resolved):
    assert cjk_fonts.classify(resolved) is True


@pytest.mark.parametrize("resolved", [
    "Noto Serif CJK JP",     # 这次要修的就是它
    "Noto Sans CJK KR",
    "Noto Serif CJK TC",
    "Noto Sans CJK J",       # 老版本 fontconfig 会截断族名
    "DejaVu Sans",           # 没有汉字，会渲染成方框
    "",
])
def test_classify_rejects_wrong_or_missing(resolved):
    assert cjk_fonts.classify(resolved) is False


def test_installed_families_parses_comma_separated(monkeypatch):
    """fc-list 一行可能是 `SimSun,宋体`，两个名字都要算「本机有」。"""
    class P:
        returncode = 0
        stdout = "SimSun,宋体\nMicrosoft YaHei,微软雅黑\nNoto Serif CJK SC\n"

    monkeypatch.setattr(cjk_fonts.subprocess, "run", lambda *a, **k: P())
    fams = cjk_fonts.installed_families()
    assert {"SimSun", "宋体", "微软雅黑", "Noto Serif CJK SC"} <= fams


# ---------------------------------------------------------------- 落盘

def test_write_conf_is_idempotent(tmp_path):
    """同一份内容不重复写：每次启动都动文件会让 fc-cache 白刷。"""
    dirs = [str(tmp_path)]
    first = cjk_fonts.write_conf(dirs, cjk_fonts.ALIASES)
    assert first is not None and os.path.isfile(first)

    mtime = os.stat(first).st_mtime_ns
    second = cjk_fonts.write_conf(dirs, cjk_fonts.ALIASES)
    assert second == first
    assert os.stat(first).st_mtime_ns == mtime, "内容没变就不该重写"

    with open(first, "w", encoding="utf-8") as f:
        f.write("<fontconfig/>")
    cjk_fonts.write_conf(dirs, cjk_fonts.ALIASES)
    with open(first, encoding="utf-8") as f:
        assert "<family>宋体</family>" in f.read()


def test_write_conf_falls_back_to_next_writable_dir(tmp_path):
    """第一个目录建不出来时要继续往后找，不能直接放弃。

    用「拿一个普通文件当目录用」来制造失败，而不是 chmod：Windows 上
    chmod 0o500 并不会真的去掉写权限，用例会在开发机全绿、在 CI 上假绿。
    """
    blocker = tmp_path / "occupied"
    blocker.write_text("not a dir", encoding="utf-8")
    good = tmp_path / "good"
    good.mkdir()

    path = cjk_fonts.write_conf([str(blocker), str(good)], cjk_fonts.ALIASES)
    assert path is not None
    assert os.path.dirname(path) == str(good)


# ---------------------------------------------------------------- 体检

def test_diagnose_reports_failed_families(monkeypatch):
    """有族名落错时要能点名，而不是只回一个笼统的 false。

    这里测的是 **fontconfig 那套实现**（fc-match 的落点判定）。Windows 上
    diagnose 走另一条分支（直接读字体文件的 name 表，没有「落点」这回事），
    所以先把平台钉成非 Windows —— 否则在 Windows 机器上跑这个用例会变成
    「什么都没测却显示通过」。
    """
    from app import winfonts

    monkeypatch.setattr(winfonts, "supported", lambda: False)
    monkeypatch.setattr(cjk_fonts, "fc_match",
                        lambda f: "Noto Serif CJK JP" if f == "宋体" else "Noto Sans CJK SC")
    st = cjk_fonts.diagnose(["宋体", "黑体"])
    assert st["ok"] is False
    assert st["failed"] == ["宋体"]
    assert [c["family"] for c in st["checks"]] == ["宋体", "黑体"]


def test_diagnose_all_good(monkeypatch):
    from app import winfonts

    monkeypatch.setattr(winfonts, "supported", lambda: False)
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda f: "Noto Serif CJK SC")
    st = cjk_fonts.diagnose(["宋体", "黑体"])
    assert st["ok"] is True and st["failed"] == []


def test_ensure_caches_and_never_raises(monkeypatch):
    """ensure() 不抛异常：字体不对不至于让整个渲染服务起不来。"""
    monkeypatch.setattr(cjk_fonts, "install", lambda: {"conf": None, "written": False})
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda f: "")
    cjk_fonts._cache = None
    st = cjk_fonts.ensure()
    assert st["ok"] is False
    assert cjk_fonts.status() is st, "第二次取应命中缓存，不能每次都跑 fc-match"


# ---------------------------------------------------------------- 表结构的硬约束
#
# 下面几条不是"跑一下看返回值"，而是钉住这张表的**形状**。表里加错一条，
# 后果都是静默的：写出一条指向不存在族名的 alias，fontconfig 不报错，
# 渲染照常，只有某类模板的 PDF 字形不对。

def test_alias_names_are_family_names_not_postscript_names():
    """左列必须是**族名**，不能是方正字体的 PostScript 名。

    历史上往表里加过 `FZFangSong-Z02S` / `FZXiaoBiaoSong-B05S` 这类名字，
    结果 fc-match 仍然返回 DejaVu —— 因为 fontconfig 的 `<alias>` 只按
    family 匹配，PostScript 名根本不在 family 列表里。更糟的是体检把它们
    判成失败，/healthz 的 zh_fonts 从 true 变 false，明明渲染没问题却报红。

    方正 PS 名的形态是 `FZ<字母>-<大写字母><两位数字>`，左列一律不许出现。
    """
    import re
    pat = re.compile(r"^FZ[A-Za-z]+-[A-Z]\d{2}")
    bad = [name for name, _ in cjk_fonts.ALIASES if pat.match(name)]
    assert not bad, "这些是 PostScript 名，写在族名位置等于没写：%s" % bad


def test_every_chain_ends_with_a_font_that_the_image_always_has():
    """每条候选链的**末位**必须是镜像里一定存在的族名（Noto）。

    `pick_target` 在候选一个都没装时返回末位。末位如果也是个不存在的族名，
    生成的 alias 就指向空气 —— fontconfig 静默接受，渲染照常回退，没人知道
    这条映射没生效。所以末位只能是 Noto SC 这两支。
    """
    always = {"Noto Serif CJK SC", "Noto Sans CJK SC"}
    bad = [(name, chain[-1]) for name, chain in cjk_fonts.ALIASES
           if chain[-1] not in always]
    assert not bad, "这些族名的兜底目标不在镜像里，映射会静默失效：%s" % bad


def test_chain_candidates_are_non_empty():
    bad = [name for name, chain in cjk_fonts.ALIASES if not chain]
    assert not bad, "候选链是空的，pick_target 会 IndexError：%s" % bad


def test_alias_names_are_unique():
    """重名会让后一条覆盖前一条，前一条的候选链白写。"""
    names = [name for name, _ in cjk_fonts.ALIASES]
    dup = {n for n in names if names.count(n) > 1}
    assert not dup, "族名重复：%s" % sorted(dup)


def test_alias_names_have_no_whitespace_or_comma():
    """族名里的逗号会被 fc-list 的解析切坏；首尾空白会让匹配永远不成立。"""
    bad = [n for n, _ in cjk_fonts.ALIASES if n != n.strip() or "," in n]
    assert not bad, "族名含首尾空白或逗号：%r" % bad


def test_serif_and_sans_are_used_consistently():
    """黑体系走 Sans、宋/仿宋/楷体系走 Serif —— 分类错了字形气质就错了。

    仿宋兜到黑体比兜到宋体更离谱：公文正文会整篇变成无衬线。
    """
    by_name = dict(cjk_fonts.ALIASES)
    for name in ("黑体", "SimHei", "微软雅黑", "等线", "华文黑体"):
        assert by_name[name][-1] == "Noto Sans CJK SC", "%s 应该走 Sans" % name
    for name in ("宋体", "SimSun", "新宋体", "华文中宋"):
        assert by_name[name][-1] == "Noto Serif CJK SC", "%s 应该走 Serif" % name


# ---------------------------------------------------------------- 生成的 XML

def test_conf_xml_is_well_formed():
    """配置文件必须能被 XML 解析器读进去。

    fontconfig 遇到语法错误会**整份忽略**，而且默认不打日志 —— 症状就是
    「映射配了但没生效」，非常难查。所以至少要保证格式良好。
    """
    from lxml import etree

    for xml in (cjk_fonts.conf_xml(), cjk_fonts.conf_xml([("宋体", "Noto Serif CJK SC")])):
        root = etree.fromstring(xml.encode("utf-8"))
        assert etree.QName(root).localname == "fontconfig"


def test_conf_xml_escapes_special_characters():
    """族名里出现 & < > " 也不能破坏 XML。

    现在的表里没有这种名字，但族名来自模板，未来放开「自定义族名」就会遇到。
    """
    from lxml import etree

    xml = cjk_fonts.conf_xml([('A&B<C>"D', "Noto Serif CJK SC")])
    root = etree.fromstring(xml.encode("utf-8"))
    fams = [e.text for e in root.iter("family")]
    assert 'A&B<C>"D' in fams, "转义后取不回原名：%s" % fams


def test_conf_xml_targets_are_never_empty():
    from lxml import etree

    root = etree.fromstring(cjk_fonts.conf_xml().encode("utf-8"))
    for alias in root.iter("alias"):
        fams = [e.text for e in alias.iter("family")]
        assert all(f for f in fams), "出现了空族名：%s" % fams


# ---------------------------------------------------------------- 字重后缀

def test_classify_accepts_family_with_weight_suffix():
    """fc-match 可能返回 `Noto Sans CJK SC,Noto Sans CJK SC Black`。

    体检拿整串去 classify，带字重后缀也要能认出来，否则装对了也报红。
    """
    assert cjk_fonts.classify("Noto Sans CJK SC,Noto Sans CJK SC Black") is True
    assert cjk_fonts.classify("Noto Serif CJK SC,Noto Serif CJK SC DemiLight") is True
