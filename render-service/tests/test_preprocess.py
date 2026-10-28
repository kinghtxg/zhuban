# -*- coding: utf-8 -*-
"""预处理：跨 run 占位符合并 + 幂等（契约 §3.7 / §3.9 ①⑧）。"""
import zipfile
from io import BytesIO

from lxml import etree

from app.preprocess import preprocess_docx, qn


def _all_text(docx_bytes):
    zf = zipfile.ZipFile(BytesIO(docx_bytes))
    root = etree.fromstring(zf.read("word/document.xml"))
    return "".join(root.itertext())


def test_cross_run_merge_original_template(original_template_bytes):
    """原始模板里 {{被测件版本}} {{项目编号}} 被拆 run，预处理后应合并为完整占位符。"""
    new, diags, changed = preprocess_docx(original_template_bytes)
    assert changed is True
    # 合并后文本里能找到完整占位符
    text = _all_text(new)
    assert "{{被测件版本}}" in text
    assert "{{项目编号}}" in text
    # 至少有一条 warning
    warns = [d for d in diags if d["severity"] == "warning"]
    assert len(warns) >= 1


def test_preprocess_idempotent(original_template_bytes):
    """对已预处理结果再跑一遍，不产生新的修改（幂等）。"""
    once, d1, c1 = preprocess_docx(original_template_bytes)
    twice, d2, c2 = preprocess_docx(once)
    # 第二次不应再合并 run
    assert c2 is False
    # 第二次不应再报合并 warning
    warns2 = [d for d in d2 if d["severity"] == "warning" and "合并" in d["message"]]
    assert warns2 == []
