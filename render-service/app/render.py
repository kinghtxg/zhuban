"""渲染管道（CONTRACT §3.2）：校验 -> 预处理 -> 扫描阻断 -> 上下文注入 -> docxtpl -> updateFields。"""
from __future__ import annotations

import json
import zipfile
from io import BytesIO
from typing import Any, Tuple

from docxtpl import DocxTemplate
from lxml import etree

from . import chartdata, config, dateexpr, richdata
from .preprocess import normalize_row_tags_docx, preprocess_docx, qn
from .scanner import scan_docx
from .security import SecurityError, validate_docx

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"


def _update_fields(docx_bytes: bytes) -> bytes:
    """在 word/settings.xml 写入 updateFields=true（Word 打开时自动更新目录/域）。"""
    zin = zipfile.ZipFile(BytesIO(docx_bytes))
    out = BytesIO()
    names = zin.namelist()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            data = zin.read(item.filename)
            if item.filename == "word/settings.xml":
                root = etree.fromstring(data)
                # 移除已有 updateFields
                for uf in root.findall(qn("updateFields")):
                    root.remove(uf)
                uf = etree.SubElement(root, qn("updateFields"))
                uf.set(qn("val"), "true")
                data = etree.tostring(root, xml_declaration=True, encoding="UTF-8", standalone=True)
            zout.writestr(item, data)
    zin.close()
    return out.getvalue()


def scan(raw: bytes, repair: bool = True) -> dict:
    result = scan_docx(raw, repair=repair)
    result.pop("_repaired_bytes", None)
    return result


def scan_with_bytes(raw: bytes, repair: bool = True) -> dict:
    """返回完整结果（含 _repaired_bytes），供 render/validate 复用。"""
    return scan_docx(raw, repair=repair)


def render_template(raw: bytes, data: dict, fmt: str = "docx") -> bytes:
    validate_docx(raw)
    # 日期表达式先求值：{"$date":"today"} 这类要换成渲染那一刻的字符串，
    # 必须在 docxtpl 之前做完，否则会把字面量塞进文档。
    data = _resolve_dates(data)
    scan_result = scan_docx(raw, repair=True)
    repaired = scan_result["_repaired_bytes"]
    # 行级 {%tr %} 标签必须在扫描**之后**才搬到行外：搬完它们就不再是 <w:t>
    # 里的文字，扫描器读不到，会把循环变量当成普通标量（详见
    # preprocess.normalize_row_tags_docx 的注释）。
    repaired, _ = normalize_row_tags_docx(repaired)
    # error 级诊断阻断
    errors = [d for d in scan_result["diagnostics"] if d["severity"] == "error"]
    if errors:
        raise SecurityError(
            "template_invalid",
            "模板存在错误级诊断，请修复后再渲染",
            details=[{"path": d.get("location", ""), "message": d["message"]} for d in errors],
            status=400,
        )
    try:
        doc = DocxTemplate(BytesIO(repaired))
        context = richdata.resolve_context(data, doc)
        doc.render(context)
        buf = BytesIO()
        doc.save(buf)
        rendered = buf.getvalue()
    except SecurityError:
        raise
    except Exception as e:  # docxtpl / jinja / 数据错误
        raise SecurityError(
            "data_invalid",
            f"渲染失败：{e}",
            details=[{"path": ".", "message": str(e)[:300]}],
            status=422,
        )
    rendered = _update_fields(rendered)

    # 图表数据必须放在 docxtpl **之后**写：docxtpl 会整包重写 docx，
    # 先改 chartN.xml 会被它的保存动作覆盖掉。
    try:
        rendered, chart_changes = chartdata.apply_chart_data(rendered, data)
    except chartdata.ChartError as e:
        raise SecurityError(e.code, e.message,
                            details=[{"path": e.chart or "chart", "message": e.message}],
                            status=422)
    except Exception as e:  # noqa: BLE001 - 图表是"增强能力"，不能因它整份生成失败
        raise SecurityError("chart_invalid",
                            "图表数据写入失败：%s" % str(e)[:200],
                            details=[{"path": "chart", "message": str(e)[:300]}],
                            status=422)
    if chart_changes:
        print("[render] 图表已更新: %s" % ", ".join(
            "%s(%s, %d系列/%d点)" % (c["variable"], c["kind_name"], c["series"], c["points"])
            for c in chart_changes), flush=True)
    return rendered


def _resolve_dates(data: Any) -> Any:
    """把数据里的日期表达式求值成字符串。

    只替换形如 {"$date": ...} 的节点，其它结构原样返回。表达式非法一律 422：
    静默变成普通字符串是最坏的结果（报告日期栏里印着 "todya"，校验还是绿的）。
    """
    if not isinstance(data, (dict, list)):
        return data
    try:
        resolved, changes = dateexpr.resolve(data)
    except dateexpr.DateExprError as e:
        raise SecurityError(
            "data_invalid",
            "日期表达式有误：%s" % e.message,
            details=[{"path": e.path or ".", "message": e.message}],
            status=422,
        )
    if changes:
        print("[render] 日期已现场生成: %s" % ", ".join(
            "%s=%s" % (c["path"], c["value"]) for c in changes), flush=True)
    return resolved


def _enum_error(allowed: list, actual: str) -> str:
    """枚举不符的报错文案。

    必须把**当前值**原样回显。只说「取值不合法」等于让用户在两三个中文
    词之间猜是哪个字打错了（「高」/「高危」/「高风险」），差一个字肉眼
    极难发现，而报错里带上当前值一眼就能看出多出来的字。
    """
    return "取值必须是 %s 之一（当前为「%s」）" % (" / ".join(allowed), actual)


def validate_data(raw: bytes, data: Any) -> dict:
    """校验三层：JSON 可解析（调用方已解析）、schema required/类型、jinja 编译。"""
    errors = []
    warnings = []
    if not isinstance(data, dict):
        return {"valid": False, "errors": [{"path": ".", "message": "data 必须是 JSON 对象"}], "warnings": []}

    # 校验必须和渲染看到同一份数据：日期表达式要先求值，否则 schema 会把它当成
    # 类型不符的对象 reject，而渲染其实是好的——「校验不过但能渲染」比反过来更坑人。
    try:
        data = _resolve_dates(data)
    except SecurityError as e:
        return {
            "valid": False,
            "errors": (e.details or [{"path": ".", "message": e.message}]),
            "warnings": [],
        }

    scan_result = scan_docx(raw, repair=True)
    # 模板语法错误
    for d in scan_result["diagnostics"]:
        if d["severity"] == "error":
            errors.append({"path": d.get("location", ""), "message": d["message"]})

    schema = scan_result["json_schema"]
    for req in schema.get("required", []):
        # sig_/seal_ 由服务端 sigauth 在渲染前注入，用户不投递 → 兼容旧模板已烘焙进
        # schema.required 的历史记录，此处跳过缺失检查。
        if req.startswith("sig_") or req.startswith("seal_"):
            continue
        if req not in data:
            errors.append({"path": req, "message": "字段缺失"})

    # 数组类型检查
    for name, prop in schema.get("properties", {}).items():
        if name not in data:
            continue
        if prop.get("type") == "array" and not isinstance(data[name], list):
            errors.append({"path": name, "message": "应为数组"})
        if prop.get("type") == "object" and not isinstance(data[name], dict):
            errors.append({"path": name, "message": "应为对象"})

    # 循环字段检查
    for var in scan_result["variables"]:
        if var.get("kind") != "loop":
            continue
        items = data.get(var["name"])
        if not isinstance(items, list):
            continue
        field_names = [f["name"] for f in var.get("fields", [])]
        for i, it in enumerate(items):
            if not isinstance(it, dict):
                errors.append({"path": f"{var['name']}[{i}]", "message": "应为对象"})
                continue
            for fn in field_names:
                if fn not in it:
                    errors.append({"path": f"{var['name']}[{i}].{fn}", "message": "字段缺失"})

    # 枚举校验：模板里 `{% if 风险等级 == "高" %}` 的比较值就是该字段的合法取值。
    #
    # 这一段是「写错值 = 静默降级」的唯一防线。Jinja 的 if 遇到不匹配的值
    # 不会报错，只是走 else —— 用户把「高」写成「高危」，成品就悄悄少一段
    # 警示块，而接口 200、页面全绿、谁也不知道。必须在校验期就拦下来。
    #
    # 只在字段**存在且非空**时校验：缺失已由 required 报过一次（重复报错
    # 只是噪音），空串则留给「这次确实不填」的用法。
    def check_fields(items: list, base: str, fields: list) -> None:
        """在数组 items 的每一项上校验一层字段；遇到嵌套循环字段递归进去。"""
        for f in fields:
            fname = f.get("name")
            if f.get("kind") == "loop":
                for i, it in enumerate(items):
                    if isinstance(it, dict) and isinstance(it.get(fname), list):
                        check_fields(it[fname], f"{base}[{i}].{fname}", f.get("fields") or [])
                continue
            allowed = f.get("enum") or []
            if not allowed:
                continue
            for i, it in enumerate(items):
                if not isinstance(it, dict):
                    continue
                val = it.get(fname)
                if isinstance(val, str) and val.strip() != "" and val not in allowed:
                    errors.append({"path": f"{base}[{i}].{fname}",
                                   "message": _enum_error(allowed, val)})

    for var in scan_result["variables"]:
        kind = var.get("kind")
        if kind == "scalar":
            allowed = var.get("enum") or []
            val = data.get(var["name"])
            if allowed and isinstance(val, str) and val.strip() != "" and val not in allowed:
                errors.append({"path": var["name"], "message": _enum_error(allowed, val)})
        elif kind == "loop":
            items = data.get(var["name"])
            if not isinstance(items, list):
                continue
            check_fields(items, var["name"], var.get("fields") or [])

    # 图表数据校验：只在用户真的传了这个图表时才校验（不传＝沿用模板原有数据）
    for ch in scan_result.get("charts") or []:
        name = ch["variable"]
        if name not in data:
            continue
        errors.extend(chartdata.validate_payload(ch, data[name], name))

    return {"valid": len(errors) == 0, "errors": errors, "warnings": warnings}
