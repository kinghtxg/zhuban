# -*- coding: utf-8 -*-
"""签名风字体注册表与路径解析（app/fonts.py）。

这一层夹在两件事中间：
- 上游是运维在「字体管理」页上传/删除字体；
- 下游是 Pillow 按 font_id 加载字形画签名。

所以它的失败模式都不是崩，而是**看起来成功了**：

1. 上传的字体写进了目录 A，而服务只扫目录 B → 列表里看不到，用户重复上传；
2. 系统字体被误判成「导入的」→ 前端列出删除按钮，点了报 404，用户以为删了；
3. `resolve_font_path` 挑了个空环境变量目录就放弃 → 明明装了字体却报 KeyError。

第 2 条是这次实修的一个真 bug（前缀匹配 `imported-old` 会命中 `imported`），
`test_under_*` 那几条就是钉住它别再退回去。
"""
import os

import pytest

from app import fonts, winfonts


@pytest.fixture(autouse=True)
def _pin_fontconfig_platform(monkeypatch):
    """整个文件测的都是 **fontconfig（fc-list / fc-cache）那套实现**。

    Windows 上 `installed()` 与 `reload_cache()` 会改走 winfonts（读字体文件的
    name 表 / 写 HKCU），这些用例的 mock 就都打不中了 —— 更糟的是不会报错，
    只会静默地测到一个空实现然后「通过」。所以这里统一把平台钉成非 Windows，
    保证它们在 Windows 开发机上也真的在测 fontconfig 那条路。
    Windows 分支另见 test_winfonts_*.py。
    """
    monkeypatch.setattr(winfonts, "supported", lambda: False)


# ==========================================================================
# 清单
# ==========================================================================

def test_list_fonts_hides_internal_fields():
    """对外的 /fonts 只给 id/name/family/style，不能把 file 路径漏出去。

    file 是相对文件名，泄露它等于告诉外面「字体是按文件名加载的」，
    也为下一版改成绝对路径时埋一个兼容包袱。
    """
    items = fonts.list_fonts()
    assert items, "注册表不该是空的"
    for it in items:
        assert set(it) == {"id", "name", "family", "style"}
        assert "file" not in it


def test_registry_ids_and_files_are_unique():
    """id 撞车会让 /render/fonts/{id} 静默取到另一个字体；文件名撞车同理。"""
    ids = [f["id"] for f in fonts.FONT_REGISTRY]
    files = [f["file"] for f in fonts.FONT_REGISTRY]
    assert len(ids) == len(set(ids)), "字体 id 重复：%s" % ids
    assert len(files) == len(set(files)), "字体文件重复：%s" % files


def test_registry_files_have_font_extensions():
    """注册表里的文件名必须是字体扩展名，否则 resolve 永远找不到。"""
    for f in fonts.FONT_REGISTRY:
        assert f["file"].lower().endswith(fonts.ALLOWED_EXT), \
            "%s 的扩展名不在 %s 里" % (f["file"], fonts.ALLOWED_EXT)


# ==========================================================================
# 路径解析
# ==========================================================================

def test_resolve_unknown_id_returns_none():
    assert fonts.resolve_font_path("no-such-font") is None
    assert fonts.resolve_font_path("") is None


def test_resolve_takes_first_existing_dir(tmp_path, monkeypatch):
    """多个搜索目录里取**第一个真的存在这个文件**的，不是第一个目录。

    镜像里签名风字体在 /opt/docforge/fonts，宿挂载在 /usr/share/fonts/custom；
    前面的目录没有这个文件时必须继续往后找，不能直接返回 None。
    """
    empty = tmp_path / "empty"
    empty.mkdir()
    hit = tmp_path / "hit"
    hit.mkdir()
    meta = fonts.FONT_REGISTRY[0]
    (hit / meta["file"]).write_bytes(b"\x00\x01\x00\x00fake")

    monkeypatch.setattr(fonts, "FONT_SEARCH_DIRS", [str(empty), str(hit)])
    got = fonts.resolve_font_path(meta["id"])
    assert got == str(hit / meta["file"])


def test_resolve_skips_blank_entries(tmp_path, monkeypatch):
    """DOCFORGE_FONT_DIR 未设时是空串，不能拿它去 join 出相对路径。

    `os.path.join("", name)` 会变成 CWD 下的相对文件名 —— 一旦当前目录恰好
    有同名文件就会被当成字体，加载出来是垃圾字形。
    """
    monkeypatch.setattr(fonts, "FONT_SEARCH_DIRS", ["", str(tmp_path)])
    meta = fonts.FONT_REGISTRY[0]
    (tmp_path / meta["file"]).write_bytes(b"x")
    assert fonts.resolve_font_path(meta["id"]) == str(tmp_path / meta["file"])


def test_load_font_raises_keyerror_for_unknown_id():
    with pytest.raises(KeyError):
        fonts.load_font("no-such-font", 32)


def test_load_font_raises_when_file_missing(monkeypatch, tmp_path):
    """注册表里有、磁盘上没有 → 也必须抛，不能返回 None 让调用方在画图时才崩。

    这里刻意收窄到 KeyError：`load_font` 的契约是「找不到可用文件就 KeyError」。
    写成 `(KeyError, OSError)` 等于把「以后改成抛别的」也放行，用例就失去意义了。
    """
    monkeypatch.setattr(fonts, "FONT_SEARCH_DIRS", [str(tmp_path)])
    with pytest.raises(KeyError):
        fonts.load_font(fonts.FONT_REGISTRY[0]["id"], 32)


# ==========================================================================
# imported 判定 —— 这次实修的 bug
# ==========================================================================

@pytest.mark.parametrize("path,expected", [
    ("/usr/share/fonts/imported/x.ttf", True),
    ("/usr/share/fonts/imported/sub/x.ttf", True),          # 子目录也算
    ("/usr/share/fonts/imported-old/x.ttf", False),         # ← 前缀陷阱
    ("/usr/share/fonts/importedness/x.ttf", False),         # ← 前缀陷阱
    ("/usr/share/fonts/custom/x.ttf", False),
    ("/usr/share/fonts/imported", False),                   # 目录本身不算里面
])
def test_under_uses_path_boundary_not_string_prefix(path, expected):
    assert fonts._under(path, "/usr/share/fonts/imported") is expected


def test_under_normalizes_redundant_segments():
    """带 `..` 或重复分隔符的路径要先归一化再比，否则能绕出去。"""
    assert fonts._under("/usr/share/fonts/imported/../custom/x.ttf",
                        "/usr/share/fonts/imported") is False
    assert fonts._under("/usr/share/fonts//imported///x.ttf",
                        "/usr/share/fonts/imported") is True


def test_imported_flag_end_to_end(monkeypatch):
    """installed() 用 _under 判定 imported：系统字体不能被标成可删。"""
    out = (
        "/usr/share/fonts/imported/仿宋_GB2312.ttf\t仿宋_GB2312\tRegular\t0\n"
        "/usr/share/fonts/imported-old/legacy.ttf\tLegacy\tRegular\t0\n"
        "/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc"
        "\tNoto Sans CJK SC\tRegular\t2\n"
    )

    class P:
        returncode = 0
        stdout = out

    import subprocess
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: P())
    monkeypatch.setattr(fonts, "IMPORT_DIR", "/usr/share/fonts/imported")

    got = {f["file"]: f for f in fonts.installed()}
    assert got["仿宋_GB2312.ttf"]["imported"] is True
    assert got["legacy.ttf"]["imported"] is False, \
        "imported-old 被误判成导入字体 → 前端会给出删不掉的删除按钮"
    assert got["NotoSansCJK-Regular.ttc"]["imported"] is False


def test_installed_parses_index_and_family(monkeypatch):
    """fc-list 一行是 file\\tfamily\\tstyle\\tindex；family 可能逗号分隔多个名。"""
    class P:
        returncode = 0
        stdout = ("/usr/share/fonts/x.ttc\tNoto Sans CJK SC,Noto Sans CJK SC Regular"
                  "\tRegular\t2\n")

    import subprocess
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: P())
    item = fonts.installed()[0]
    assert item["family"] == "Noto Sans CJK SC", "多族名只取第一个"
    assert item["style"] == "Regular"
    assert item["file"] == "x.ttc"


def test_installed_returns_empty_when_fc_list_missing(monkeypatch):
    """fc-list 不存在（精简镜像）时返回空表，不能抛 —— 字体管理页还要能打开。"""
    import subprocess

    def boom(*a, **k):
        raise FileNotFoundError("fc-list")

    monkeypatch.setattr(subprocess, "run", boom)
    assert fonts.installed() == []


def test_installed_returns_empty_on_nonzero_exit(monkeypatch):
    class P:
        returncode = 1
        stdout = ""
        stderr = "boom"

    import subprocess
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: P())
    assert fonts.installed() == []


def test_installed_skips_malformed_lines(monkeypatch):
    """列数不足的行要跳过，不能 IndexError 掉整份列表。"""
    class P:
        returncode = 0
        stdout = "garbage\n\t\t\n/ok/a.ttf\tFam\tReg\t0\n"

    import subprocess
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: P())
    got = fonts.installed()
    assert [f["file"] for f in got] == ["a.ttf"]


# ==========================================================================
# 缓存刷新
# ==========================================================================

def test_reload_cache_reports_elapsed_and_errors(monkeypatch):
    """fc-cache 失败不抛：文件已经落盘了，抛异常会让用户以为上传失败。

    测的是 fontconfig 那套实现，所以把平台钉成非 Windows：Windows 上
    reload_cache 改走注册表（见 test_winfonts_wiring），不跑 fc-cache。
    钉住平台是为了让这个用例在 Windows 上也真的测到东西，而不是被分支绕过。
    """
    import subprocess

    from app import winfonts

    monkeypatch.setattr(winfonts, "supported", lambda: False)

    def fake(cmd, **k):
        class P:
            returncode = 0
            stdout = ""
            stderr = ""
        return P()

    monkeypatch.setattr(subprocess, "run", fake)
    res = fonts.reload_cache()
    assert res["ok"] is True
    assert "elapsed_ms" in res and "import_dir" in res


def test_reload_cache_survives_missing_fc_cache(monkeypatch):
    import subprocess

    from app import winfonts

    monkeypatch.setattr(winfonts, "supported", lambda: False)

    def boom(*a, **k):
        raise FileNotFoundError("fc-cache")

    monkeypatch.setattr(subprocess, "run", boom)
    res = fonts.reload_cache()
    assert res["ok"] is False
    assert res["errors"], "失败原因必须带出来，否则排查时只能猜"
