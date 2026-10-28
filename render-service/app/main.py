"""docforge Python 渲染微服务 HTTP 入口（CONTRACT §3，容器内监听 8101）。"""
from __future__ import annotations

import json
import time
import traceback
import uuid
from typing import Dict

from fastapi import FastAPI, Request, UploadFile, File, Form
from fastapi.responses import Response, JSONResponse
import asyncio
from lxml import etree

from . import config, office
from .render import render_template, scan, validate_data
from .security import SecurityError, validate_docx
from .docmodel import parse_docx
from .docapply import apply_model
from . import fonts as fonts_mod
from . import signature as signature_mod
from .signature import render_signature_png
from .seal import render_seal_png

app = FastAPI(title="zhuban render-service", version=config.VERSION)

# preview 临时存储：preview_id -> {'pages':[bytes],'created':float,'dir':str}
PREVIEW_STORE: Dict[str, dict] = {}


@app.exception_handler(SecurityError)
async def security_error_handler(request: Request, exc: SecurityError):
    return JSONResponse(
        status_code=exc.status,
        content={
            "error": {
                "code": exc.code,
                "message": exc.message,
                "details": exc.details,
                "request_id": uuid.uuid4().hex,
            }
        },
    )


def _parse_data(data: str):
    try:
        return json.loads(data)
    except Exception as e:
        raise SecurityError("data_invalid", f"data 不是合法 JSON：{e}", status=422)


@app.get("/healthz")
async def healthz():
    return {
        "status": "ok",
        "libreoffice": office.libreoffice_available(),
        "poppler": office.poppler_available(),
        "version": config.VERSION,
    }


@app.post("/scan")
async def scan_endpoint(file: UploadFile = File(...), repair: str = Form("true")):
    raw = await file.read()
    repair_flag = repair.strip().lower() in ("1", "true", "yes")
    result = scan(raw, repair=repair_flag)
    return {
        "variables": result["variables"],
        "tags": result["tags"],
        "sample_json": result["sample_json"],
        "json_schema": result["json_schema"],
        "diagnostics": result["diagnostics"],
        "repaired": result["repaired"],
    }


@app.post("/parse")
async def parse_endpoint(file: UploadFile = File(...)):
    """docx -> 块级结构化文档模型（PRD V2 §7.1）。只读，不修改原文件。

    这里必须接住异常，不是为了"让接口别 500"，而是为了**把失败说成人话**。
    实测以下输入都会抛裸 Python 异常（document.xml 不是合法 XML、结构异常、
    未知元素组合），裸异常经 FastAPI 变成 500，Go 侧只能转成
    "服务内部错误，请稍后重试"，用户根本分不清是自己文件的问题还是系统的问题。

    另外返回文案必须讲清楚一件事：**解析失败不影响生成文档**。
    结构解析走的是 block 模型这条链，生成走的是占位符替换那条链，两者不共用代码。
    不说明这一点，用户会以为整个模板废了，于是跑去 Word 里反复改结构。
    """
    raw = await file.read()
    validate_docx(raw)
    try:
        return parse_docx(raw)
    except etree.XMLSyntaxError as e:
        print("[parse] XMLSyntaxError: %s" % traceback.format_exc(), flush=True)
        raise SecurityError(
            "template_invalid",
            "模板的 word/document.xml 不是合法 XML（第 %s 行附近）。"
            "文件可能在传输中损坏，或被非 Word 工具改写过。"
            "在线结构编辑不可用，但模板仍可用于生成文档。" % getattr(e, "lineno", "?"),
            status=422,
        )
    except Exception as e:  # noqa: BLE001 - 这里要的就是兜住所有结构异常并如实上报
        print("[parse] %s: %s" % (type(e).__name__, traceback.format_exc()), flush=True)
        raise SecurityError(
            "template_invalid",
            "模板结构无法解析（%s: %s）。"
            "这段文档里有系统识别不了的结构，在线结构编辑不可用，但模板仍可用于生成文档。"
            % (type(e).__name__, str(e)[:160]),
            status=422,
        )


@app.post("/apply")
async def apply_endpoint(file: UploadFile = File(...), model: str = Form(...)):
    """块级模型 -> docx 原地回写（PRD V2 §7.1），返回回写后的 docx 二进制。"""
    raw = await file.read()
    validate_docx(raw)
    try:
        model_obj = json.loads(model)
    except Exception as e:
        raise SecurityError("data_invalid", f"model 不是合法 JSON：{e}", status=422)
    if not isinstance(model_obj, dict) or "blocks" not in model_obj:
        raise SecurityError("data_invalid", "model 必须是含 blocks 数组的对象", status=422)
    out_bytes = apply_model(raw, model_obj)
    return Response(
        content=out_bytes,
        media_type="application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        headers={"Content-Disposition": 'attachment; filename="applied.docx"'},
    )


@app.post("/render")
async def render_endpoint(file: UploadFile = File(...), data: str = Form("{}"), format: str = Form("docx")):
    raw = await file.read()
    ctx = _parse_data(data)
    docx_bytes = render_template(raw, ctx, fmt="docx")
    if format == "pdf":
        if not office.libreoffice_available():
            raise SecurityError("conversion_failed", "LibreOffice 不可用，无法导出 PDF", status=502)
        workdir = f"{config.TMP_DIR}/pdf_{uuid.uuid4().hex}"
        pdf_bytes = await office.convert_docx_to_pdf(docx_bytes, workdir)
        return Response(
            content=pdf_bytes,
            media_type="application/pdf",
            headers={"Content-Disposition": 'attachment; filename="rendered.pdf"'},
        )
    return Response(
        content=docx_bytes,
        media_type="application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        headers={"Content-Disposition": 'attachment; filename="rendered.docx"'},
    )


@app.post("/validate")
async def validate_endpoint(file: UploadFile = File(...), data: str = Form("{}")):
    raw = await file.read()
    ctx = _parse_data(data)
    return validate_data(raw, ctx)


@app.post("/preview")
async def preview_endpoint(
    file: UploadFile = File(...),
    data: str = Form("{}"),
    max_pages: int = Form(6),
    dpi: int = Form(110),
    # 水印文案与前端界面同名（铸版）。改名时两边必须一起改，
    # 否则用户看到的预览水印和界面品牌对不上。
    watermark: str = Form("铸版预览"),
):
    raw = await file.read()
    ctx = _parse_data(data)
    max_pages = min(max(1, max_pages), config.PREVIEW_MAX_PAGES)
    dpi = min(max(72, dpi), 200)
    docx_bytes = render_template(raw, ctx, fmt="docx")
    if not office.libreoffice_available() or not office.poppler_available():
        raise SecurityError("conversion_failed", "LibreOffice/poppler 不可用，无法预览", status=502)
    pv_id = "pv_" + uuid.uuid4().hex
    workdir = f"{config.TMP_DIR}/preview_{pv_id}"
    pdf_bytes = await office.convert_docx_to_pdf(docx_bytes, workdir)
    pngs = office.pdf_to_pngs(pdf_bytes, workdir, dpi, max_pages)
    from PIL import Image
    import io as _io
    pages_meta = []
    page_bytes_list = []
    for i, png in enumerate(pngs, start=1):
        wm_png = office.add_watermark(png, watermark)
        page_bytes_list.append(wm_png)
        im = Image.open(_io.BytesIO(wm_png))
        pages_meta.append({"page": i, "url": f"/preview/{pv_id}/page/{i}", "width": im.width, "height": im.height})
    PREVIEW_STORE[pv_id] = {"pages": page_bytes_list, "created": time.time(), "dir": workdir}
    return {
        "preview_id": pv_id,
        "page_count": len(pngs),
        "returned_pages": len(pngs),
        "pages": pages_meta,
    }


@app.get("/preview/{preview_id}/page/{n}")
async def preview_page(preview_id: str, n: int):
    rec = PREVIEW_STORE.get(preview_id)
    if not rec:
        return JSONResponse(status_code=404, content={"error": {"code": "not_found", "message": "预览不存在或已过期"}})
    age = time.time() - rec["created"]
    if age > config.PREVIEW_TTL_MINUTES * 60:
        PREVIEW_STORE.pop(preview_id, None)
        return JSONResponse(status_code=404, content={"error": {"code": "expired", "message": "预览已过期"}})
    if n < 1 or n > len(rec["pages"]):
        return JSONResponse(status_code=404, content={"error": {"code": "not_found", "message": "页码不存在"}})
    return Response(content=rec["pages"][n - 1], media_type="image/png")


# ---------------------------------------------------------------------------
# V3 B1 渲染：电子签名 / 印章 / 字体清单（PRD V3 §6.1）
# ---------------------------------------------------------------------------

def _truthy(v: str) -> bool:
    return (v or "").strip().lower() in ("1", "true", "yes", "on")


@app.get("/fonts")
async def list_fonts():
    """列出内置签名风字体。"""
    return {"fonts": fonts_mod.list_fonts()}


@app.post("/fonts/reload")
async def reload_fonts():
    """刷新 fontconfig 缓存，让新导入的字体对 LibreOffice 立即可见。

    只做 `fc-cache -f`，不重启任何进程：渲染是每次起一个新的 soffice 子进程，
    它会重新读 fontconfig 缓存，所以刷完即生效。
    """
    return fonts_mod.reload_cache()


@app.get("/fonts/installed")
async def installed_fonts():
    """列出 fontconfig 当前认识的全部字体（含运维导入的）。"""
    return {"fonts": fonts_mod.installed()}


@app.post("/signature/render")
async def signature_render(
    signer_name: str = Form(...),
    font_id: str = Form(...),
    width_mm: float = Form(60.0),
    with_timestamp: str = Form("false"),
    timestamp: str = Form(""),
    timestamp_expr: str = Form(""),
    timestamp_prefix: str = Form(""),
):
    """渲染电子签名 PNG（透明底 RGBA）。

    timestamp    字面 ISO 8601 时间（预生成固定时刻用）。
    timestamp_expr 日期表达式 JSON，**出图这一刻**现场求值——同一张签名图在
                 每份产物上印各自出具的时间，就是靠它。
    """
    name = (signer_name or "").strip()
    if not (2 <= len(name) <= 20):
        raise SecurityError("invalid_param", "signer_name 需为 2-20 字", status=422)
    if fonts_mod.resolve_font_path(font_id) is None:
        raise SecurityError("invalid_font", f"非法 font_id：{font_id}", status=422)
    width_mm = min(max(20.0, float(width_mm)), 120.0)
    try:
        ts_text = signature_mod.resolve_timestamp_text(timestamp, timestamp_expr)
    except ValueError as e:
        raise SecurityError("invalid_timestamp", str(e), status=422)
    # 表达式既然给了，就是要印出来——不能要求调用方重复声明 with_timestamp
    stamp_on = _truthy(with_timestamp) or bool((timestamp_expr or "").strip())
    png = render_signature_png(
        name, font_id, width_mm, stamp_on, timestamp or None,
        timestamp_text=ts_text,
        timestamp_prefix=timestamp_prefix if timestamp_prefix != "" else signature_mod.DEFAULT_TS_PREFIX,
    )
    return Response(content=png, media_type="image/png")


@app.post("/seal/render")
async def seal_render(
    unit_name: str = Form(...),
    seal_name: str = Form(...),
    shape: str = Form("circle"),
    radius_mm: float = Form(20.0),
    star: str = Form("true"),
):
    """渲染电子印章 PNG（透明底 RGBA，印章红 #C8102E）。"""
    unit_name = (unit_name or "").strip()
    seal_name = (seal_name or "").strip()
    if not unit_name and not seal_name:
        raise SecurityError("invalid_param", "unit_name 与 seal_name 不能同时为空", status=422)
    if shape not in ("circle", "ellipse"):
        raise SecurityError("invalid_param", "shape 仅支持 circle/ellipse", status=422)
    radius_mm = min(max(10.0, float(radius_mm)), 40.0)
    try:
        png = render_seal_png(unit_name, seal_name, shape, radius_mm, _truthy(star))
    except ValueError as e:
        raise SecurityError("invalid_param", str(e), status=422)
    return Response(content=png, media_type="image/png")
