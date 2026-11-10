# -*- coding: utf-8 -*-
r"""Windows 分支的接线：字体刷新与字体体检都必须改走注册表 / name 表。

这两处很容易「改了却没人调用」——原来的实现只认 fontconfig：

- `fonts.reload_cache()` 直接跑 `fc-cache -f`。Windows 上这个命令不存在，
  每次都返回 `ok=false`，前端提示「字体已保存，但刷新缓存失败，可稍后重试或
  重启渲染服务」。而重启也没用：LibreOffice 根本不看目录。用户会一直重试。
- `cjk_fonts.diagnose()` 靠 `fc-match` 判断族名落点。Windows 上拿不到结果，
  `judge()` 会走 `if not resolved` 分支 → **所有中文字体都报 missing**。
  这是反向的假报警：明明装了 59 个公文字体，体检表上一片红。假报多了，
  真的缺字体那次就没人信了。

所以这两个接线必须钉住：Windows 上走新路径、非 Windows 上行为一个字不变。
"""
import pytest

from app import cjk_fonts, fonts, winfonts


# --------------------------------------------------------------- fonts.reload_cache
def test_reload_cache_delegates_on_windows(monkeypatch):
    monkeypatch.setattr(winfonts, "supported", lambda: True)
    called = {}

    def fake_reload():
        called["n"] = called.get("n", 0) + 1
        return {"ok": True, "elapsed_ms": 1, "import_dir": "x", "registered": 7, "errors": []}

    monkeypatch.setattr(winfonts, "reload", fake_reload)
    res = fonts.reload_cache()
    assert called.get("n") == 1, "Windows 上必须走注册表，不能去跑 fc-cache"
    assert res["ok"] is True
    assert res["registered"] == 7


def test_reload_cache_still_fc_cache_on_linux(monkeypatch):
    """非 Windows 分支不能被顺手改掉。"""
    monkeypatch.setattr(winfonts, "supported", lambda: False)

    class _P:
        returncode = 0
        stderr = ""
        stdout = ""

    import subprocess

    seen = []

    def fake_run(cmd, **kwargs):
        seen.append(cmd)
        return _P()

    monkeypatch.setattr(subprocess, "run", fake_run)
    res = fonts.reload_cache()
    assert res["ok"] is True
    assert seen and seen[0][:2] == ["fc-cache", "-f"]


def test_reload_cache_reports_missing_fc_cache(monkeypatch):
    monkeypatch.setattr(winfonts, "supported", lambda: False)
    import subprocess

    def boom(cmd, **kwargs):
        raise FileNotFoundError("fc-cache")

    monkeypatch.setattr(subprocess, "run", boom)
    res = fonts.reload_cache()
    assert res["ok"] is False
    assert any("fc-cache" in e for e in res["errors"])


# --------------------------------------------------------------- cjk_fonts.diagnose
def test_diagnose_uses_installed_families_on_windows(monkeypatch):
    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(
        winfonts, "installed_families",
        lambda refresh=False: {"宋体", "SimSun", "仿宋_GB2312"},
    )
    res = cjk_fonts.diagnose(["宋体", "SimSun", "仿宋_GB2312", "华文中宋"])
    assert res["platform"] == "windows"
    ok = {c["family"]: c["ok"] for c in res["checks"]}
    assert ok["宋体"] is True
    assert ok["仿宋_GB2312"] is True
    # 本机真没有的必须如实报缺失，不能因为「反正是 Windows」就放过
    assert ok["华文中宋"] is False
    assert res["failed"] == ["华文中宋"]


def test_diagnose_all_present_is_ok(monkeypatch):
    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(winfonts, "installed_families", lambda refresh=False: {"宋体"})
    res = cjk_fonts.diagnose(["宋体"])
    assert res["ok"] is True and res["failed"] == []


def test_ensure_skips_fontconfig_install_on_windows(monkeypatch):
    """Windows 上不能往用户目录写 fontconfig 配置——那是给 Linux 用的。"""
    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(winfonts, "installed_families", lambda refresh=False: set())
    wrote = {"n": 0}

    def fake_install():
        wrote["n"] += 1
        return {"conf": "/should/not/happen", "written": True}

    monkeypatch.setattr(cjk_fonts, "install", fake_install)
    cjk_fonts._cache = None
    try:
        res = cjk_fonts.ensure()
    finally:
        cjk_fonts._cache = None
    assert wrote["n"] == 0, "Windows 上不该调用 install()"
    assert res["install"]["skipped"] == "windows"
    # 一个字体都没有 → 必须报不 ok（而不是拿 True 糊过去）
    assert res["ok"] is False


# --------------------------------------------------------------- judge 接线
def test_judge_marks_installed_family_exact(monkeypatch):
    from app import fontaudit

    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(winfonts, "installed_families", lambda refresh=False: {"方正小标宋简体"})
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: {"方正小标宋简体"})
    status, resolved = fontaudit.judge("方正小标宋简体")
    assert status == "exact"
    assert resolved == "方正小标宋简体"


def test_judge_marks_absent_cjk_family_missing(monkeypatch):
    from app import fontaudit

    monkeypatch.setattr(winfonts, "supported", lambda: True)
    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: set())
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda f: "")
    status, _ = fontaudit.judge("方正小标宋简体")
    assert status == "missing"


def test_judge_leaves_latin_alone(monkeypatch):
    from app import fontaudit

    monkeypatch.setattr(cjk_fonts, "installed_families", lambda: set())
    monkeypatch.setattr(cjk_fonts, "fc_match", lambda f: "")
    status, _ = fontaudit.judge("Times New Roman")
    assert status == "latin", "拉丁字体缺失不该当成中文缺字告警"
