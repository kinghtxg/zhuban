# -*- coding: utf-8 -*-
"""/scan 输出形状、/validate 缺字段路径（契约 §3.9 ⑥⑦）。"""
import json

from fastapi.testclient import TestClient

from app.main import app

client = TestClient(app)


def test_scan_shape(migrated_template_bytes):
    resp = client.post(
        "/scan",
        files={"file": ("t.docx", migrated_template_bytes, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")},
        data={"repair": "true"},
    )
    assert resp.status_code == 200, resp.text
    body = resp.json()
    for key in ("variables", "tags", "sample_json", "json_schema", "diagnostics", "repaired"):
        assert key in body, f"缺少字段 {key}"
    kinds = {v["kind"] for v in body["variables"]}
    # 迁移模板应识别出 loop(items) 与若干 scalar
    assert "loop" in kinds
    loops = [v for v in body["variables"] if v["kind"] == "loop"]
    assert loops and loops[0]["name"] == "items"
    field_names = {f["name"] for f in loops[0]["fields"]}
    assert {"name", "缺陷等级", "次数", "description", "缺陷说明", "mitigation"} <= field_names


def test_validate_missing_field_path(migrated_template_bytes):
    # 缺 items 与必填顶层字段
    bad = {"被测件名称": "x"}
    resp = client.post(
        "/validate",
        files={"file": ("t.docx", migrated_template_bytes, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")},
        data={"data": json.dumps(bad)},
    )
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["valid"] is False
    paths = [e["path"] for e in body["errors"]]
    assert "items" in paths
    # items 内缺字段
    bad2 = {"被测件名称": "x", "被测件版本": "v", "项目编号": "p", "测试人员": "t", "测试时间": "t",
            "items": [{"name": "n"}]}
    resp2 = client.post(
        "/validate",
        files={"file": ("t.docx", migrated_template_bytes, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")},
        data={"data": json.dumps(bad2)},
    )
    body2 = resp2.json()
    paths2 = [e["path"] for e in body2["errors"]]
    assert any(p.startswith("items[0].") for p in paths2)


def test_healthz():
    resp = client.get("/healthz")
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "ok"
    assert "libreoffice" in body and "poppler" in body
