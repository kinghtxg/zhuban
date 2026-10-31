"""LibreOffice / poppler 封装（CONTRACT §3.4）：docx->pdf->png，profile 隔离，信号量限流，水印。"""
from __future__ import annotations

import asyncio
import os
import shutil
import subprocess
import uuid
from typing import List

from . import config

# 全局并发信号量（LibreOffice headless 多实例需独立 profile）
_semaphore = asyncio.Semaphore(config.LO_CONCURRENCY)


def _which(names: List[str]) -> str | None:
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
    return _which(["soffice", "soffice.exe", "libreoffice", "libreoffice.exe"])


def pdftoppm_path() -> str | None:
    return _which(["pdftoppm", "pdftoppm.exe"])


def libreoffice_available() -> bool:
    return soffice_path() is not None


def poppler_available() -> bool:
    return pdftoppm_path() is not None


async def convert_docx_to_pdf(docx_bytes: bytes, workdir: str) -> bytes:
    """调用 LibreOffice 把 docx 转 pdf。失败抛 RuntimeError。"""
    soffice = soffice_path()
    if not soffice:
        raise RuntimeError("LibreOffice 不可用")
    os.makedirs(workdir, exist_ok=True)
    src = os.path.join(workdir, "input.docx")
    with open(src, "wb") as f:
        f.write(docx_bytes)
    profile = f"file:///tmp/lo_{uuid.uuid4().hex}"
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
            raise RuntimeError(f"LibreOffice 转换超时（{config.RENDER_TIMEOUT_SECONDS}s）")
    pdf_path = os.path.join(workdir, "input.pdf")
    if not os.path.exists(pdf_path):
        raise RuntimeError(f"LibreOffice 未产出 PDF: {stderr.decode('utf-8','ignore')[:500]}")
    with open(pdf_path, "rb") as f:
        return f.read()


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
