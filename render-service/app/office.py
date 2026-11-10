"""LibreOffice / poppler 封装（CONTRACT §3.4）：docx->pdf->png，profile 隔离，信号量限流，水印。"""
from __future__ import annotations

import asyncio
import os
import shutil
import subprocess
import sys
import uuid
from pathlib import Path
from typing import List

from . import config

# 全局并发信号量（LibreOffice headless 多实例需独立 profile）
_semaphore = asyncio.Semaphore(config.LO_CONCURRENCY)

# 便携版把 LibreOffice / poppler 放在程序自己的目录里，不写系统 PATH。
# 显式指定既省掉「猜装在哪儿」，也避免用户机器上另外装过一套 LibreOffice 时
# 被 PATH 里那个抢走——两个版本对同一份 docx 的排版判断并不完全一致。
_SOFFICE_ENV = "SOFFICE_PATH"
_PDFTOPPM_ENV = "PDFTOPPM_PATH"
_PDFTOTEXT_ENV = "PDFTOTEXT_PATH"


def _which(names: List[str], env_key: str = "") -> str | None:
    if env_key:
        forced = os.environ.get(env_key, "").strip().strip('"')
        if forced and os.path.exists(forced):
            return forced
    for n in names:
        p = shutil.which(n)
        if p:
            return p
    # Windows 常见安装位置
    for base in (r"C:\Program Files\LibreOffice\program", r"C:\Program Files (x86)\LibreOffice\program"):
        for n in names:
            cand = os.path.join(base, n)
            if os.path.exists(cand):
                return cand
    return None


def soffice_path() -> str | None:
    # 顺序有讲究：Windows 上 LibreOffice 同时提供 soffice.com 与 soffice.exe，
    # 两者都是启动器，但 shutil.which("soffice") 会因 PATHEXT 先命中 .com。
    # .com 是控制台程序，每转换一次就闪一个黑窗口；.exe 是图形子系统，headless
    # 下不弹窗。桌面端跑预览时这个差别很显眼，所以显式排前面。
    if sys.platform == "win32":
        return _which(["soffice.exe", "soffice", "libreoffice.exe"], _SOFFICE_ENV)
    return _which(["soffice", "libreoffice"], _SOFFICE_ENV)


def pdftoppm_path() -> str | None:
    return _which(["pdftoppm.exe", "pdftoppm"], _PDFTOPPM_ENV)


def libreoffice_available() -> bool:
    return soffice_path() is not None


def poppler_available() -> bool:
    return pdftoppm_path() is not None


async def convert_docx_to_pdf(docx_bytes: bytes, workdir: str) -> bytes:
    """调用 LibreOffice 把 docx 转 pdf。失败抛 RuntimeError。"""
    soffice = soffice_path()
    if not soffice:
        raise RuntimeError("LibreOffice 不可用")
    workdir = os.path.abspath(workdir)
    os.makedirs(workdir, exist_ok=True)
    src = os.path.join(workdir, "input.docx")
    with open(src, "wb") as f:
        f.write(docx_bytes)
    # 用户配置目录必须由 Path.as_uri() 生成，不能手写 "file:///tmp/..."：
    # Linux 上 file:///tmp/x 是合法绝对路径，Windows 上则是**没有盘符**的
    # file URI —— soffice 会卡在一个看不见的对话框里直到超时（实测 45s 零产出，
    # 只剩一句「转换超时」，看着像 LibreOffice 装坏了）。as_uri() 在 Windows
    # 上补成 file:///C:/...，两端都对。放在 workdir 里，转换结束随它一起删。
    profile_dir = os.path.join(workdir, "lo-profile-" + uuid.uuid4().hex)
    profile = Path(profile_dir).as_uri()
    try:
        async with _semaphore:
            proc = await asyncio.create_subprocess_exec(
                soffice,
                f"-env:UserInstallation={profile}",
                "--headless",
                "--convert-to", "pdf",
                "--outdir", workdir,
                src,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            try:
                stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=config.RENDER_TIMEOUT_SECONDS)
            except asyncio.TimeoutError:
                proc.kill()
                await proc.wait()
                raise RuntimeError(f"LibreOffice 转换超时（{config.RENDER_TIMEOUT_SECONDS}s）")
    finally:
        # 每份 profile 约 1.1MB / 137 个文件。容器里随容器销毁，无所谓；
        # 桌面端是常驻进程，每转一次就留一份，一周能堆出几千个目录。
        shutil.rmtree(profile_dir, ignore_errors=True)
    pdf_path = os.path.join(workdir, "input.pdf")
    if not os.path.exists(pdf_path):
        raise RuntimeError(f"LibreOffice 未产出 PDF: {stderr.decode('utf-8','ignore')[:500]}")
    with open(pdf_path, "rb") as f:
        return f.read()


def pdftotext_path() -> str | None:
    p = _which(["pdftotext.exe", "pdftotext"], _PDFTOTEXT_ENV)
    if p:
        return p
    # 便携版把 poppler 放在程序自己的 bin 目录，只给了 pdftoppm 的路径；
    # 同目录下的 pdftotext 直接拿来用，不再要求用户配第二个环境变量。
    ppm = pdftoppm_path()
    if ppm:
        cand = os.path.join(os.path.dirname(ppm),
                            "pdftotext.exe" if sys.platform == "win32" else "pdftotext")
        if os.path.exists(cand):
            return cand
    return None


def pdf_to_text(pdf_bytes: bytes) -> str:
    """PDF 逐页文字（页之间是 \\f）。目录页码靠它量。

    走 stdin/stdout 不落临时文件：这个方法在一次渲染里可能被调两三次，
    每次都写一份 PDF 到磁盘再删，容器里很快就堆满 inode。
    """
    exe = pdftotext_path()
    if not exe:
        raise RuntimeError("poppler pdftotext 不可用")
    proc = subprocess.run(
        [exe, "-layout", "-enc", "UTF-8", "-", "-"],
        input=pdf_bytes, capture_output=True, timeout=config.RENDER_TIMEOUT_SECONDS,
    )
    if proc.returncode != 0:
        raise RuntimeError("pdftotext 失败: " + proc.stderr.decode("utf-8", "ignore")[:300])
    return proc.stdout.decode("utf-8", "ignore")


def pdf_to_pngs(pdf_bytes: bytes, workdir: str, dpi: int, max_pages: int) -> List[bytes]:
    """pdftoppm 逐页出 PNG。"""
    pdftoppm = pdftoppm_path()
    if not pdftoppm:
        raise RuntimeError("poppler pdftoppm 不可用")
    pdf_path = os.path.join(workdir, "doc.pdf")
    with open(pdf_path, "wb") as f:
        f.write(pdf_bytes)
    prefix = os.path.join(workdir, "page")
    subprocess.run(
        [pdftoppm, "-png", "-r", str(dpi), "-f", "1", "-l", str(max_pages), pdf_path, prefix],
        check=True, capture_output=True, timeout=config.RENDER_TIMEOUT_SECONDS,
    )
    pngs = []
    idx = 1
    while True:
        # pdftoppm 命名：page-1.png / page-01.png
        for name in (f"page-{idx}.png", f"page-{idx:02d}.png", f"page-{idx:03d}.png"):
            cand = os.path.join(workdir, name)
            if os.path.exists(cand):
                with open(cand, "rb") as f:
                    pngs.append(f.read())
                break
        else:
            break
        idx += 1
    return pngs


def add_watermark(png_bytes: bytes, text: str) -> bytes:
    """Pillow 加居中斜向半透明水印。"""
    from PIL import Image, ImageDraw, ImageFont
    import io as _io
    if not text:
        return png_bytes
    img = Image.open(_io.BytesIO(png_bytes)).convert("RGBA")
    overlay = Image.new("RGBA", img.size, (255, 255, 255, 0))
    draw = ImageDraw.Draw(overlay)
    w, h = img.size
    font_size = max(20, w // 25)
    # 候选中文字体：Linux Noto CJK -> DejaVu -> Windows msyh/simhei -> 点阵兜底
    font = None
    for cand in (
        "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
        "/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        "C:/Windows/Fonts/msyh.ttc",
        "C:/Windows/Fonts/simhei.ttf",
    ):
        try:
            if os.path.exists(cand):
                font = ImageFont.truetype(cand, font_size)
                break
        except Exception:
            continue
    if font is None:
        font = ImageFont.load_default()
    # 平铺斜向水印
    for y in range(-100, h + 200, max(140, font_size * 6)):
        for x in range(-100, w + 300, max(300, font_size * 16)):
            draw.text((x, y), text, font=font, fill=(200, 200, 200, 90))
    combined = Image.alpha_composite(img, overlay).convert("RGB")
    buf = _io.BytesIO()
    combined.save(buf, format="PNG")
    return buf.getvalue()
