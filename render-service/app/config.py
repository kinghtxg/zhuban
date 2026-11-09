"""docforge render-service 运行配置（环境变量覆盖，见 CONTRACT §3.8）。"""
from __future__ import annotations

import os


def _int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, default))
    except (TypeError, ValueError):
        return default


def _bool(name: str, default: bool) -> bool:
    v = os.environ.get(name)
    if v is None:
        return default
    return v.strip().lower() in ("1", "true", "yes", "on")


VERSION = "1.0.0"
PORT = _int("PORT", 8101)
MAX_TEMPLATE_MB = _int("MAX_TEMPLATE_MB", 10)
MAX_DATA_MB = _int("MAX_DATA_MB", 20)
MAX_IMAGE_MB = _int("MAX_IMAGE_MB", 5)
RENDER_TIMEOUT_SECONDS = _int("RENDER_TIMEOUT_SECONDS", 60)
LO_CONCURRENCY = _int("LO_CONCURRENCY", 2)
PREVIEW_TTL_MINUTES = _int("PREVIEW_TTL_MINUTES", 30)
PREVIEW_MAX_PAGES = _int("PREVIEW_MAX_PAGES", 20)
IMAGE_ALLOW_PRIVATE = _bool("IMAGE_ALLOW_PRIVATE", False)

# 内联图片的最大摆放尺寸（mm）——「插图片把模板格式搞乱」的兜底闸门。
#
# A4 纵向、左右各 3.17cm 页边距时，版心宽约 146.5mm。图片一旦超过版心，
# 行内放不下，Word 会把段落挤成两行、表格行高被顶开，版面就散了。
# 这两个值不是审美参数，是「不破坏版面」的硬上限：超过就等比缩小。
# 需要更大的图（比如整页插图）时，用浮动图片（wp:anchor）另做，不走行内。
MAX_INLINE_IMAGE_WIDTH_MM = _int("MAX_INLINE_IMAGE_WIDTH_MM", 146)
MAX_INLINE_IMAGE_HEIGHT_MM = _int("MAX_INLINE_IMAGE_HEIGHT_MM", 60)
# 源图没有给尺寸、也没有 DPI 时，按这个 DPI 折算物理尺寸
DEFAULT_IMAGE_DPI = _int("DEFAULT_IMAGE_DPI", 96)

_tmp = os.environ.get("TMP_DIR", "/tmp/docforge")
TMP_DIR = _tmp
os.makedirs(TMP_DIR, exist_ok=True)

MAX_TEMPLATE_BYTES = MAX_TEMPLATE_MB * 1024 * 1024
MAX_DATA_BYTES = MAX_DATA_MB * 1024 * 1024
MAX_IMAGE_BYTES = MAX_IMAGE_MB * 1024 * 1024
