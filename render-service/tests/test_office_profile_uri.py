# -*- coding: utf-8 -*-
r"""LibreOffice 用户配置目录：必须是带盘符的绝对 file URI，且用完必须删掉。

## 两个都是真出过的问题

### 一、`file:///tmp/...` 在 Windows 上会让转换卡死

原实现写的是 `profile = f"file:///tmp/lo_{uuid4().hex}"`。在 Linux 上这是一个
合法的绝对路径；在 Windows 上它**没有盘符**，soffice 会卡在一个看不见的对话框
里直到超时。实测（Windows 10，MSI 管理式解压的 LibreOffice 24.8）：

    参数                               结果
    soffice.exe + file:///C:/tmp/profA  14s 出 PDF
    soffice.com + file:///C:/tmp/profB  14s 出 PDF
    soffice.exe + file:///tmp/profC     45s 超时，零产出

报错只有一句「LibreOffice 转换超时」，看着像 LibreOffice 装坏了，实际是一个
路径字符串。所以判据必须定在「URI 里那个路径在不在 workdir 下」——
把它改成 `Path(...).as_uri()` 之后，Windows 自动补成 `file:///C:/...`。

### 二、每转一次漏 1.1MB / 137 个文件

profile 目录建在 /tmp 下且从不清理。容器里随容器销毁，看不出来；桌面端是常驻
进程，一天几百次预览就是几万个目录堆在 %TEMP%。所以断言「转换结束后 workdir
里不再有 lo-profile-*」。
"""
import asyncio
import os

from pathlib import Path

import pytest

from app import office


class _FakeProc:
    """替掉真实子进程：按 --outdir 造一个 PDF，并记录收到的参数。"""

    def __init__(self, args):
        self.args = args
        self.killed = False

    async def communicate(self):
        outdir = Path(self.args[self.args.index("--outdir") + 1])
        (outdir / "input.pdf").write_bytes(b"%PDF-1.4\n% fake\n")
        return b"", b""

    def kill(self):
        self.killed = True

    async def wait(self):
        return 0


def _patch_soffice(monkeypatch, tmp_path, exists=True):
    exe = tmp_path / ("soffice.exe" if os.name == "nt" else "soffice")
    if exists:
        exe.write_text("#!/bin/sh\n", encoding="utf-8")
    monkeypatch.setattr(office, "soffice_path", lambda: str(exe) if exists else None)
    seen = {}

    async def fake_exec(*args, **kwargs):
        seen["args"] = list(args)
        return _FakeProc(list(args))

    monkeypatch.setattr(asyncio, "create_subprocess_exec", fake_exec)
    return seen


def _uri_of(args):
    for a in args:
        if a.startswith("-env:UserInstallation="):
            return a.split("=", 1)[1]
    return ""


def test_profile_uri_is_absolute_and_inside_workdir(monkeypatch, tmp_path):
    seen = _patch_soffice(monkeypatch, tmp_path)
    workdir = tmp_path / "pdf_abc"
    out = asyncio.run(office.convert_docx_to_pdf(b"fake docx", str(workdir)))
    assert out.startswith(b"%PDF-")

    uri = _uri_of(seen["args"])
    assert uri, "必须把 -env:UserInstallation 传给 soffice"
    # 关键：URI 必须落在 workdir 下。旧实现写死 file:///tmp/...，
    # 在 Windows 上这条断言直接失败——那正是转换卡死的那个字符串。
    assert uri.startswith(Path(str(workdir)).as_uri()), uri
    assert "lo-profile-" in uri


def test_profile_uri_differs_per_conversion(monkeypatch, tmp_path):
    """同一进程里连续两次转换不能共用 profile，否则并发会互相锁住。"""
    seen = _patch_soffice(monkeypatch, tmp_path)
    uris = []
    for _ in range(2):
        workdir = tmp_path / "pdf_same"
        asyncio.run(office.convert_docx_to_pdf(b"fake docx", str(workdir)))
        uris.append(_uri_of(seen["args"]))
    assert uris[0] != uris[1]


def test_profile_dir_removed_after_conversion(monkeypatch, tmp_path):
    _patch_soffice(monkeypatch, tmp_path)
    workdir = tmp_path / "pdf_clean"
    asyncio.run(office.convert_docx_to_pdf(b"fake docx", str(workdir)))
    left = [n for n in os.listdir(workdir) if n.startswith("lo-profile-")]
    assert left == [], "profile 目录没删，桌面端会一直堆在 %TEMP% 里：%s" % left
    # 产物本身要留着
    assert (workdir / "input.pdf").exists()


def test_relative_workdir_still_yields_absolute_uri(monkeypatch, tmp_path):
    """workdir 传相对路径时也不能生成无盘符 URI（Windows 上同样会卡死）。"""
    seen = _patch_soffice(monkeypatch, tmp_path)
    cwd = os.getcwd()
    os.chdir(str(tmp_path))
    try:
        asyncio.run(office.convert_docx_to_pdf(b"fake docx", "rel_out"))
    finally:
        os.chdir(cwd)
    uri = _uri_of(seen["args"])
    parsed = Path(str(tmp_path / "rel_out")).as_uri()
    assert uri.startswith(parsed), uri


def test_missing_pdf_reports_soffice_error(monkeypatch, tmp_path):
    """真没产出 PDF 时，报错要带上 soffice 的输出，不能吞成空。"""
    exe = tmp_path / "soffice"
    exe.write_text("", encoding="utf-8")
    monkeypatch.setattr(office, "soffice_path", lambda: str(exe))

    class _Silent(_FakeProc):
        async def communicate(self):
            return b"", b"no such filter writer_pdf_Export"

    async def fake_exec(*args, **kwargs):
        return _Silent(list(args))

    monkeypatch.setattr(asyncio, "create_subprocess_exec", fake_exec)
    with pytest.raises(RuntimeError) as ei:
        asyncio.run(office.convert_docx_to_pdf(b"x", str(tmp_path / "pdf_none")))
    assert "writer_pdf_Export" in str(ei.value)


def test_no_libreoffice_raises_clean_error(monkeypatch, tmp_path):
    _patch_soffice(monkeypatch, tmp_path, exists=False)
    with pytest.raises(RuntimeError) as ei:
        asyncio.run(office.convert_docx_to_pdf(b"x", str(tmp_path / "pdf_x")))
    assert "LibreOffice 不可用" in str(ei.value)
