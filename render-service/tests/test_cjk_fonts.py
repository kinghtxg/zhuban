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
    """默认（没给 skip 信息时）要覆盖全部族名，一个都不能少。"""
    xml = cjk_fonts.conf_xml()
    for name, target in cjk_fonts.ALIASES:
        assert "<family>%s</family>" % name in xml, "漏了族名 %s" % name
        assert xml.count(target) >= 1
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
    """有族名落错时要能点名，而不是只回一个笼统的 false。"""
    monkeypatch.setattr(cjk_fonts, "fc_match",
                        lambda f: "Noto Serif CJK JP" if f == "宋体" else "Noto Sans CJK SC")
    st = cjk_fonts.diagnose(["宋体", "黑体"])
    assert st["ok"] is False
    assert st["failed"] == ["宋体"]
    assert [c["family"] for c in st["checks"]] == ["宋体", "黑体"]


def test_diagnose_all_good(monkeypatch):
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
