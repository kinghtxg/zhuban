#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从上一版 Windows 便携包派生一个新版本。

为什么是「派生」而不是「从零打」：
便携包里 99% 的内容（内嵌 Python、LibreOffice、poppler、59 个公文字体）
两版之间完全不变，只有 app/zhuban.exe、app/render/ 的源码、以及顶层三个
说明文件会变。从零打要重新凑齐这些组件（LibreOffice 用 msiexec /a 管理式
解压、poppler 单独下载、字体逐个挑），既慢又容易漏——3.4.19 那次是手工做
的，之后 3.4.20~3.4.22 干脆没重新打包，原因就在这里。

这就带来一个必须防的坑：**底座是旧版，忘记更新某个文件，新包里就还带着
旧代码**。所以脚本结束前会做一遍校验（见 verify()），不接受「看起来打成了」。

用法：
    python deploy/portable-win/make-portable-win.py \
        --base  "Z:/.../3.4.19/Windows便携版/zhuban-3.4.19-windows-x64.zip" \
        --exe   /c/tmp/zhuban-3.4.23-windows-amd64.exe \
        --version 3.4.23 --commit e7cf21b \
        --out   /c/tmp/zhuban-3.4.23-windows-x64.zip
"""
from __future__ import annotations

import argparse
import hashlib
import os
import re
import shutil
import sys
import tempfile
import time
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))          # .../docforge
RENDER_APP = os.path.join(REPO, "render-service", "app")
RENDER_REQ = os.path.join(REPO, "render-service", "requirements.txt")

# 顶层随包文件：仓库里维护的版本 → 包内文件名
TOP_FILES = ["启动铸版.bat", "清除字体注册.bat", "说明.txt"]
# app/ 下由仓库维护的文件
APP_FILES = ["launcher.py", "unregister_fonts.py"]

CHUNK = 1 << 20


def say(msg: str = "") -> None:
    print(msg, flush=True)


# --------------------------------------------------------------------------- 解底座
def unpack(base_zip: str, dst: str) -> None:
    with zipfile.ZipFile(base_zip) as z:
        names = z.namelist()
        say("  底座条目 %d 个" % len(names))
        old_top = names[0].split("/", 1)[0]
        say("  底座顶层目录：%s" % old_top)
        for i, n in enumerate(names, 1):
            rel = n.split("/", 1)[1] if "/" in n else ""
            if not rel:
                continue
            p = os.path.join(dst, rel.replace("/", os.sep))
            if n.endswith("/"):
                os.makedirs(p, exist_ok=True)
                continue
            os.makedirs(os.path.dirname(p), exist_ok=True)
            with z.open(n) as src, open(p, "wb") as out:
                while True:
                    b = src.read(CHUNK)
                    if not b:
                        break
                    out.write(b)
            if i % 4000 == 0:
                say("    …%d/%d" % (i, len(names)))


# --------------------------------------------------------------------------- 覆盖
def overlay(root: str, exe: str, version: str, commit: str) -> None:
    app = os.path.join(root, "app")

    # 1) 主程序
    shutil.copyfile(exe, os.path.join(app, "zhuban.exe"))
    say("  app/zhuban.exe  ← %s (%d 字节)" % (os.path.basename(exe), os.path.getsize(exe)))

    # 2) 渲染服务源码：整个 app/ 目录以仓库为准。
    #    先删掉底座里的 .py，再拷——否则仓库里删掉的文件会在包里留成幽灵模块
    #    （Python 会 import 到它，而代码里已经没人引用了，排查极难）。
    dst_app = os.path.join(app, "render", "app")
    os.makedirs(dst_app, exist_ok=True)
    removed = []
    for f in os.listdir(dst_app):
        if f.endswith(".py"):
            os.remove(os.path.join(dst_app, f))
            removed.append(f)
    copied = []
    for f in sorted(os.listdir(RENDER_APP)):
        if not f.endswith(".py"):
            continue
        shutil.copyfile(os.path.join(RENDER_APP, f), os.path.join(dst_app, f))
        copied.append(f)
    shutil.copyfile(RENDER_REQ, os.path.join(app, "render", "requirements.txt"))
    say("  app/render/app/  %d 个 .py（底座原有 %d 个，全部以仓库为准）"
        % (len(copied), len(removed)))
    missing = set(removed) - set(copied)
    if missing:
        say("    · 仓库中已删除、包里一并移除：%s" % ", ".join(sorted(missing)))

    # 3) 启动器与顶层说明文件
    for f in APP_FILES:
        shutil.copyfile(os.path.join(HERE, f), os.path.join(app, f))
    say("  app/  %s" % "、".join(APP_FILES))

    for f in TOP_FILES:
        src = os.path.join(HERE, f)
        data = open(src, "rb").read()
        if f == "说明.txt":
            data = _stamp_readme(data, version, commit)
        # bat 必须是 CRLF：LF 会被 cmd.exe 切成 'plate'、'包不完?echo' 这种碎片
        if f.endswith(".bat"):
            data = data.replace(b"\r\n", b"\n").replace(b"\n", b"\r\n")
        open(os.path.join(root, f), "wb").write(data)
    say("  顶层 %s" % "、".join(TOP_FILES))


def _stamp_readme(data: bytes, version: str, commit: str) -> bytes:
    """把说明.txt 里的版本号与提交号改成实际值。"""
    text = data.decode("utf-8-sig")
    text = re.sub(r"(便携版\s+)\d+\.\d+\.\d+", r"\g<1>" + version, text, count=1)
    text = re.sub(r"(?m)^(\s*版本\s+)\d+\.\d+\.\d+", r"\g<1>" + version, text)
    text = re.sub(r"(?m)^(\s*提交\s+)[0-9a-f]{7,40}", r"\g<1>" + commit, text)
    return b"\xef\xbb\xbf" + text.encode("utf-8")


# --------------------------------------------------------------------------- 打包
def pack(root: str, out_zip: str, version: str) -> None:
    top = "zhuban-%s-windows-x64" % version
    tmp = out_zip + ".part"
    n = 0
    t0 = time.time()
    with zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames.sort()
            for name in sorted(filenames):
                full = os.path.join(dirpath, name)
                rel = os.path.relpath(full, root).replace(os.sep, "/")
                # Python zipfile 对非 ASCII 名自动置 UTF-8 标志位（bit 11），
                # Windows 资源管理器解压中文名才不会乱码。
                z.write(full, top + "/" + rel)
                n += 1
                if n % 3000 == 0:
                    say("    …%d 个文件，%.0fs" % (n, time.time() - t0))
    os.replace(tmp, out_zip)
    say("  打包 %d 个文件，%.0fs，%.0f MB"
        % (n, time.time() - t0, os.path.getsize(out_zip) / 1024 / 1024))


# --------------------------------------------------------------------------- 校验
def verify(zip_path: str, version: str, commit: str, exe: str) -> None:
    """不接受「看起来打成了」：逐条查证据。"""
    say("")
    say("[校验] %s" % os.path.basename(zip_path))
    problems: list[str] = []

    with zipfile.ZipFile(zip_path) as z:
        names = z.namelist()
        top = "zhuban-%s-windows-x64" % version

        # 1) 顶层目录名与版本一致
        if not all(n.startswith(top + "/") or n == top for n in names):
            bad = [n for n in names if not n.startswith(top + "/")][:3]
            problems.append("顶层目录名不统一：%s" % bad)

        # 2) 必备文件齐
        need = ["app/zhuban.exe", "app/launcher.py", "app/unregister_fonts.py",
                "app/python/python.exe", "app/lo/program/soffice.exe",
                "app/poppler/pdftoppm.exe", "app/poppler/pdftotext.exe",
                "app/render/app/main.py", "app/render/app/align.py",
                "app/render/app/toc.py", "app/render/app/render.py",
                "app/render/app/richdata.py", "启动铸版.bat", "说明.txt"]
        for f in need:
            if top + "/" + f not in names:
                problems.append("缺少 %s" % f)

        # 3) 中文条目带 UTF-8 标志位（否则解压乱码）
        for info in z.infolist():
            if any(ord(c) > 127 for c in info.filename) and not (info.flag_bits & 0x800):
                problems.append("中文名未置 UTF-8 标志位：%s" % info.filename)
                break

        # 4) bat 必须是 CRLF
        for f in ("启动铸版.bat", "清除字体注册.bat"):
            p = top + "/" + f
            if p in names:
                body = z.read(p)
                if body.count(b"\n") != body.count(b"\r\n"):
                    problems.append("%s 不是 CRLF（cmd 会切成碎片）" % f)

        # 5) exe 与传入的一致，且版本/提交真在里面
        exe_in = z.read(top + "/app/zhuban.exe")
        if hashlib.sha256(exe_in).hexdigest() != hashlib.sha256(open(exe, "rb").read()).hexdigest():
            problems.append("包内 zhuban.exe 与传入的 exe 不一致")
        if version.encode() not in exe_in:
            problems.append("zhuban.exe 里没有版本串 %s" % version)
        if commit.encode() not in exe_in:
            problems.append("zhuban.exe 里没有提交号 %s" % commit)

        # 6) 渲染服务源码与仓库逐字节一致（防「底座里的旧代码没换掉」）
        for f in sorted(os.listdir(RENDER_APP)):
            if not f.endswith(".py"):
                continue
            p = top + "/app/render/app/" + f
            if p not in names:
                problems.append("包里缺 render/app/%s" % f)
                continue
            a = z.read(p)
            b = open(os.path.join(RENDER_APP, f), "rb").read()
            if a != b:
                problems.append("render/app/%s 与仓库不一致（包里是旧代码？）" % f)

        # 7) 说明.txt 的版本号与提交号
        readme = z.read(top + "/说明.txt").decode("utf-8-sig")
        if version not in readme:
            problems.append("说明.txt 里没有版本号 %s" % version)
        if commit not in readme:
            problems.append("说明.txt 里没有提交号 %s" % commit)

    if problems:
        say("")
        for p in problems:
            say("  [×] %s" % p)
        say("")
        say("校验未通过，共 %d 项。" % len(problems))
        raise SystemExit(1)

    say("  √ 顶层目录名          %s" % ("zhuban-%s-windows-x64" % version))
    say("  √ 必备文件            %d 项齐全（含 pdftotext、align.py、toc.py）"
        % 14)
    say("  √ 中文条目            UTF-8 标志位已置")
    say("  √ bat 换行            CRLF")
    say("  √ zhuban.exe          与传入 exe 同 sha256，内含 %s / %s" % (version, commit))
    say("  √ render/app/*.py     与仓库逐字节一致")
    say("  √ 说明.txt            版本与提交号正确")


# --------------------------------------------------------------------------- 主流程
def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="上一版便携包 zip")
    ap.add_argument("--exe", required=True, help="新编的 windows-amd64 主程序")
    ap.add_argument("--version", required=True)
    ap.add_argument("--commit", required=True)
    ap.add_argument("--out", required=True, help="输出 zip 路径")
    ap.add_argument("--keep", action="store_true", help="保留解压出来的工作目录")
    a = ap.parse_args()

    for p, what in ((a.base, "底座 zip"), (a.exe, "主程序 exe")):
        if not os.path.isfile(p):
            say("[×] %s 不存在：%s" % (what, p))
            return 1

    work = tempfile.mkdtemp(prefix="zbportable-")
    root = os.path.join(work, "pkg")
    os.makedirs(root, exist_ok=True)
    say("[1/4] 解开底座")
    try:
        unpack(a.base, root)
        say("[2/4] 覆盖主程序与源码")
        overlay(root, a.exe, a.version, a.commit)
        say("[3/4] 打包")
        pack(root, a.out, a.version)
        say("[4/4] 校验")
        verify(a.out, a.version, a.commit, a.exe)
    finally:
        if a.keep:
            say("  工作目录保留：%s" % root)
        else:
            shutil.rmtree(work, ignore_errors=True)

    say("")
    say("完成：%s（%.0f MB）" % (a.out, os.path.getsize(a.out) / 1024 / 1024))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
