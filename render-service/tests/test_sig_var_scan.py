# -*- coding: utf-8 -*-
"""sig_/seal_ 系统解析变量的扫描与校验（V3 端到端授权修复）。

模板里的 {{sig_xxx}}/{{seal_xxx}} 由服务端 sigauth 渲染前注入，不是用户投递数据。
扫描应把它们标成 kind=image + system_resolved，但不进 required/sample；
validate_data 即使遇到 required 里的 sig_/seal_ 也跳过缺失检查。
"""
from io import BytesIO

from docx import Document

from app.scanner import scan_docx, is_system_sig_var
from app.render import validate_data


def _docx_with(text: str) -> bytes:
    doc = Document()
    doc.add_paragraph(text)
    buf = BytesIO()
    doc.save(buf)
    return buf.getvalue()


def test_sig_var_scanned_as_image_not_required():
    raw = _docx_with("签字：{{sig_abc}} 印章：{{seal_xyz}} 姓名：{{姓名}}")
    res = scan_docx(raw, repair=False)

    kinds = {v["name"]: v for v in res["variables"]}
    assert kinds["sig_abc"]["kind"] == "image"
    assert kinds["sig_abc"].get("system_resolved") is True
    assert kinds["seal_xyz"]["kind"] == "image"
    assert kinds["seal_xyz"].get("system_resolved") is True

    required = res["json_schema"]["required"]
    assert "sig_abc" not in required
    assert "seal_xyz" not in required
    # 普通变量仍 required
    assert "姓名" in required

    # sample 不给假值
    assert "sig_abc" not in res["sample_json"]
    assert "seal_xyz" not in res["sample_json"]
    assert "姓名" in res["sample_json"]


def test_validate_skips_sig_required():
    raw = _docx_with("签字：{{sig_abc}} 姓名：{{姓名}}")
    # data 不含 sig_abc（用户不投递），只给普通变量 → 不应报 sig_abc 字段缺失
    out = validate_data(raw, {"姓名": "张三"})
    assert out["valid"], out["errors"]
    assert not any(e["path"] == "sig_abc" for e in out["errors"])


def test_validate_still_errors_normal_missing():
    raw = _docx_with("签字：{{sig_abc}} 姓名：{{姓名}}")
    # 普通变量缺失仍要报错
    out = validate_data(raw, {})
    assert not out["valid"]
    assert any(e["path"] == "姓名" and e["message"] == "字段缺失" for e in out["errors"])
    # 但 sig_abc 不报错
    assert not any(e["path"] == "sig_abc" for e in out["errors"])


def test_is_system_sig_var_helper():
    assert is_system_sig_var("sig_abc")
    assert is_system_sig_var("seal_xyz")
    assert not is_system_sig_var("image_logo")
    assert not is_system_sig_var("姓名")
