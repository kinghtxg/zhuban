# -*- coding: utf-8 -*-
r"""Windows 字体族名：只能从字体文件的 name 表读，不能靠注册表键名。

## 为什么这条非测不可

体检（fontaudit）要回答「模板里写的族名，这台机器到底有没有」。Linux 上问
fc-list；Windows 上没有 fontconfig，只能自己解析。而 Windows 有两条看起来能用
其实是错的路：

1. **注册表键名** —— 那只是个显示标签，随便写。实测把「方正小标宋简体」的键名
   改成 `ZZBOGUSLABEL (TrueType)`，LibreOffice 按族名照样命中，说明族名来自
   字体文件内部，跟键名无关。拿键名当族名，等于每台机器报的东西都不一样。
2. **文件名** —— `FZXBSJW.TTF` 这种方正缩写，跟族名「方正小标宋简体」毫无关系。

所以唯一正确的来源是 sfnt 里的 `name` 表（nameID=1），而且中文名常常只出现在
某个语言子记录里（platform 3 / langID 0x804），必须**把所有语言版本都收进来**，
否则模板写中文名就查不到。

这里用仓库自带的 fonts/ 做样本（msyh.ttc / simhei.ttf / simsun.ttc），
不需要装 Windows 也能测 —— 解析逻辑平台无关。
"""
import os

import pytest

from app import winfonts

FONTS = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", "..", "fonts"))

CASES = [
    ("simhei.ttf", ["SimHei", "黑体"]),
    ("msyh.ttc", ["Microsoft YaHei", "微软雅黑"]),
    ("simsun.ttc", ["SimSun", "宋体"]),
]


def _have(name):
    return os.path.exists(os.path.join(FONTS, name))


@pytest.mark.parametrize("filename,expect", CASES)
def test_family_names_read_from_font_file(filename, expect):
    if not _have(filename):
        pytest.skip("仓库里没有 %s" % filename)
    got = winfonts._families_in_file(os.path.join(FONTS, filename))
    for name in expect:
        assert name in got, "%s 里没读到族名 %s，读到的是 %s" % (filename, name, sorted(got))


def test_ttc_reads_every_face():
    """simsun.ttc 是一包两面（SimSun + NSimSun）；只读第一面会漏掉新宋体。"""
    path = os.path.join(FONTS, "simsun.ttc")
    if not _have("simsun.ttc"):
        pytest.skip("仓库里没有 simsun.ttc")
    got = winfonts._families_in_file(path)
    assert len(got) >= 2, "TTC 只解出了一张脸：%s" % sorted(got)


def test_non_font_file_yields_nothing(tmp_path):
    """拿到一个不是字体的文件（比如 README）不能抛异常，只能返回空。"""
    p = tmp_path / "not-a-font.ttf"
    p.write_bytes(b"this is definitely not a font file" * 10)
    assert winfonts._families_in_file(str(p)) == set()


def test_truncated_font_yields_nothing(tmp_path):
    p = tmp_path / "cut.ttf"
    p.write_bytes(b"ttcf\x00\x01\x00\x00\x00\x00\x00\x02")  # 声称两面，后面没了
    assert winfonts._families_in_file(str(p)) == set()


def test_unsupported_platform_reports_not_ok():
    """非 Windows 上明确说「不适用」，不能静默返回成功。"""
    if winfonts.supported():
        pytest.skip("本机是 Windows")
    res = winfonts.register([])
    assert res["ok"] is False
    assert any("非 Windows" in e for e in res["errors"])
    assert winfonts.installed_families() == set()


def test_register_without_dirs_reports_reason():
    """没有可注册目录时说清原因，不要返回空的 ok。"""
    if not winfonts.supported():
        pytest.skip("本机不是 Windows")
    res = winfonts.register([])
    assert res["ok"] is False
    assert res["errors"]


def test_font_dirs_parsing(monkeypatch, tmp_path):
    a = tmp_path / "a"
    b = tmp_path / "b"
    a.mkdir()
    b.mkdir()
    monkeypatch.setenv(winfonts.ENV_DIRS, "%s;%s;%s" % (a, b, tmp_path / "missing"))
    got = winfonts.font_dirs()
    assert got == [os.path.abspath(str(a)), os.path.abspath(str(b))]


def test_font_dirs_dedup_and_quotes(monkeypatch, tmp_path):
    a = tmp_path / "a"
    a.mkdir()
    monkeypatch.setenv(winfonts.ENV_DIRS, '"%s";%s' % (a, a))
    got = winfonts.font_dirs()
    assert len(got) == 1, got


# ------------------------------------------------------------ 字体列表（不是体检）
def test_faces_names_keeps_faces_apart():
    """每张脸单独一条：合并成一个大集合，字体列表就会出现重复/错配的行。"""
    path = os.path.join(FONTS, "simsun.ttc")
    if not _have("simsun.ttc"):
        pytest.skip("仓库里没有 simsun.ttc")
    faces = winfonts._faces_names(path)
    assert len(faces) >= 2, "simsun.ttc 应该有多张脸，实际 %d" % len(faces)
    fams = [f.get(winfonts._NAME_ID_FAMILY) for f in faces]
    assert all(fams), fams


def test_ascii_name_comes_first():
    """Latin 名排前面，和 fc-list 的 `SimSun,宋体` 顺序一致。

    不排的话 Windows 上字体列表显示「宋体」、生产显示「SimSun」，
    同一份名单两边看起来对不上。
    """
    assert winfonts._ordered({"宋体", "SimSun"}) == ["SimSun", "宋体"]
    assert winfonts._ordered({"黑体", "SimHei", "微软雅黑"})[0] == "SimHei"


def test_installed_fonts_empty_on_linux(monkeypatch):
    if winfonts.supported():
        pytest.skip("本机是 Windows")
    assert winfonts.installed_fonts() == []
    assert winfonts.all_font_files() == []


def test_installed_font_entries_have_required_keys(monkeypatch, tmp_path):
    """形状必须和 Linux 分支一致，否则 Go 那边解析会静默少字段。"""
    path = os.path.join(FONTS, "simhei.ttf")
    if not _have("simhei.ttf"):
        pytest.skip("仓库里没有 simhei.ttf")
    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(winfonts, "all_font_files", lambda: [path])
    got = winfonts.installed_fonts(str(tmp_path))
    assert got, "至少应该列出一条"
    for item in got:
        assert set(item) == {"file", "family", "style", "path", "index", "imported"}
    assert any("SimHei" in str(i["family"]) for i in got)
    # 不在 import 目录下的，不能标成「已导入」，否则管理员会看到一堆删不掉的条目
    assert all(i["imported"] is False for i in got)


def test_import_dir_marks_uploaded_fonts(monkeypatch, tmp_path):
    path = os.path.join(FONTS, "simhei.ttf")
    if not _have("simhei.ttf"):
        pytest.skip("仓库里没有 simhei.ttf")
    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(winfonts, "all_font_files", lambda: [path])
    got = winfonts.installed_fonts(str(tmp_path))
    assert got and got[0]["imported"] is False
    # 把「导入目录」指成字体所在目录，同样的文件就该被认成导入的
    got2 = winfonts.installed_fonts(FONTS)
    assert got2 and got2[0]["imported"] is True
