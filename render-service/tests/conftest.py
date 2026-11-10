# -*- coding: utf-8 -*-
"""渲染服务测试的共享夹具。

三份真实文件，各有各的用途，别混着用：

- `business_template_bytes` + `sample_data`：内置业务样例模板（月度经营分析报告）
  与配套数据。验"一份正常模板能不能扫、能不能渲、编辑能不能落"。
- `cross_run_bytes`：占位符被 Word 拆到多个 run 的样本，专供预处理合并用例。
- `blank_template_bytes`：模板制作向导用的空白模板。
"""
import json
import os
import sys

import pytest

RENDER_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if RENDER_DIR not in sys.path:
    sys.path.insert(0, RENDER_DIR)

DOCFORGE = os.path.dirname(RENDER_DIR)
CROSS_RUN_TEMPLATE = os.path.join(RENDER_DIR, "tests", "fixtures", "cross-run.docx")
BUSINESS_TEMPLATE = os.path.join(DOCFORGE, "samples", "templates", "biz-report.docx")
SAMPLE_JSON = os.path.join(DOCFORGE, "samples", "biz-report.sample.json")
BLANK_TEMPLATE = os.path.join(DOCFORGE, "samples", "blank-template.docx")


@pytest.fixture(scope="session")
def cross_run_bytes():
    with open(CROSS_RUN_TEMPLATE, "rb") as f:
        return f.read()


@pytest.fixture(scope="session")
def business_template_bytes():
    with open(BUSINESS_TEMPLATE, "rb") as f:
        return f.read()


@pytest.fixture(scope="session")
def sample_data():
    with open(SAMPLE_JSON, encoding="utf-8") as f:
        return json.load(f)


@pytest.fixture(scope="session")
def blank_template_bytes():
    with open(BLANK_TEMPLATE, "rb") as f:
        return f.read()
