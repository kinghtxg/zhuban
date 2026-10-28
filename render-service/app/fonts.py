# -*- coding: utf-8 -*-
"""内置签名风字体注册表与加载器（PRD V3 §6.1）。

字体文件位于 render-service/fonts/（开发态）与镜像内 /usr/share/fonts/custom。
全部 SIL OFL 1.1 可商用。本模块负责：
  - 提供 /fonts 接口的字体清单；
  - 按 font_id 解析字体文件绝对路径（多路径探测）；
  - 用 Pillow ImageFont 按目标像素尺寸加载字体。
"""
from __future__ import annotations

import os
from typing import Dict, List, Optional

from PIL import ImageFont

# 仓库内字体目录（app/ 的上一级）
_PKG_DIR = os.path.dirname(os.path.abspath(__file__))
_REPO_FONTS = os.path.normpath(os.path.join(_PKG_DIR, "..", "fonts"))

# 搜索顺序（覆盖优先级）：
#   1. DOCFORGE_FONT_DIR 显式指定（最高）
#   2. /opt/docforge/fonts 镜像内置签名风字体（不被宿挂载遮蔽的固定路径）
#   3. 开发态 _REPO_FONTS（render-service/fonts）
#   4. /usr/share/fonts/custom 运维挂载的文档字体（simhei/msyh 兜底，最低）
FONT_SEARCH_DIRS: List[str] = [
    os.environ.get("DOCFORGE_FONT_DIR", ""),
    "/opt/docforge/fonts",
    _REPO_FONTS,
    "/usr/share/fonts/custom",
]

# id -> 字体元数据。file 为相对 fonts 目录的文件名。
FONT_REGISTRY: List[Dict[str, str]] = [
    {
        "id": "ma-shan-zheng",
        "name": "马善政毛笔楷书",
        "family": "Ma Shan Zheng",
        "style": "brush",
        "file": "MaShanZheng-Regular.ttf",
    },
    {
        "id": "liu-jian-mao-cao",
        "name": "柳建毛草",
        "family": "Liu Jian Mao Cao",
        "style": "brush",
        "file": "LiuJianMaoCao-Regular.ttf",
    },
    {
        "id": "zhi-mang-xing",
        "name": "志莽行书",
        "family": "Zhi Mang Xing",
        "style": "brush",
        "file": "ZhiMangXing-Regular.ttf",
    },
    {
        "id": "long-cang",
        "name": "龙藏体",
        "family": "Long Cang",
        "style": "brush",
        "file": "LongCang-Regular.ttf",
    },
    {
        "id": "dancing-script",
        "name": "Dancing Script",
        "family": "Dancing Script",
        "style": "cursive",
        "file": "DancingScript.ttf",
    },
]

_FONT_BY_ID: Dict[str, Dict[str, str]] = {f["id"]: f for f in FONT_REGISTRY}


def list_fonts() -> List[Dict[str, str]]:
    """GET /fonts 返回体（不含 file 路径等内部字段）。"""
    return [
        {"id": f["id"], "name": f["name"], "family": f["family"], "style": f["style"]}
        for f in FONT_REGISTRY
    ]


def resolve_font_path(font_id: str) -> Optional[str]:
    """按 font_id 找到字体文件绝对路径；找不到返回 None。"""
    meta = _FONT_BY_ID.get(font_id)
    if meta is None:
        return None
    fname = meta["file"]
    for d in FONT_SEARCH_DIRS:
        if not d:
            continue
        cand = os.path.join(d, fname)
        if os.path.isfile(cand):
            return cand
    return None


def load_font(font_id: str, px_size: int) -> ImageFont.FreeTypeFont:
    """加载指定字号（像素）的字体；font_id 非法抛 KeyError，文件缺失抛 OSError。"""
    path = resolve_font_path(font_id)
    if path is None:
        raise KeyError(f"未知字体 id: {font_id}")
    return ImageFont.truetype(path, px_size)


# 运维导入的字体目录（宿主 ./volumes/fonts → 容器 /usr/share/fonts/imported）。
# 之所以不复用 /usr/share/fonts/custom：那是只读挂载的仓库字体，混在一起删不掉。
IMPORT_DIR = os.environ.get("FONT_IMPORT_DIR", "/usr/share/fonts/imported")

ALLOWED_EXT = (".ttf", ".ttc", ".otf", ".otc")


def reload_cache(timeout: int = 30) -> Dict[str, object]:
    """执行 fc-cache -f，返回耗时与当前字体数。

    失败不抛异常——上传的字体文件已经落盘，只是这次没能立刻生效，
    交给调用方决定要不要提示用户重试。抛异常会让用户以为文件没传上去。
    """
    import subprocess
    import time

    started = time.time()
    cmds = [["fc-cache", "-f"], ["fc-cache", "-f", IMPORT_DIR]]
    errors = []
    for cmd in cmds:
        try:
            p = subprocess.run(cmd, capture_output=True, timeout=timeout, text=True)
            if p.returncode != 0:
                # 目录不存在时 fc-cache 会非零退出，不算致命
                errors.append("%s: %s" % (" ".join(cmd), (p.stderr or "").strip()[:200]))
        except FileNotFoundError:
            errors.append("fc-cache 不可用（镜像缺少 fontconfig）")
            break
        except subprocess.TimeoutExpired:
            errors.append("%s 超时（%ds）" % (" ".join(cmd), timeout))
            break
    return {
        "ok": not errors,
        "elapsed_ms": int((time.time() - started) * 1000),
        "import_dir": IMPORT_DIR,
        "errors": errors,
    }


def installed() -> List[Dict[str, object]]:
    """用 fc-list 列出 fontconfig 认识的全部字体。

    解析 fc-list 而不是自己读 ttf name 表：name 表有 16 个平台/语言组合，
    自己解析极易在中文字体上取到乱码；fontconfig 已经处理好了。
    这里用 \\t 做分隔（family 里可能含逗号，逗号分隔会切坏）。
    """
    import subprocess

    out: List[Dict[str, object]] = []
    try:
        p = subprocess.run(
            ["fc-list", "--format", "%{file}\t%{family}\t%{style}\t%{index}\n"],
            capture_output=True, timeout=20, text=True,
        )
        if p.returncode != 0:
            return out
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return out

    import_real = os.path.realpath(IMPORT_DIR)
    for line in p.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) < 3 or not parts[0]:
            continue
        path = parts[0]
        out.append({
            "file": os.path.basename(path),
            "family": parts[1].split(",")[0].strip(),
            "style": parts[2].split(",")[0].strip(),
            "path": path,
            "imported": os.path.realpath(path).startswith(import_real),
        })
    return out
