# -*- coding: utf-8 -*-
import json
import os
import sys
import shutil
import zipfile
from io import BytesIO

import pytest

RENDER_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if RENDER_DIR not in sys.path:
    sys.path.insert(0, RENDER_DIR)

DOCFORGE = os.path.dirname(RENDER_DIR)
ORIGINAL_TEMPLATE = os.path.join(RENDER_DIR, "tests", "fixtures", "original-template.docx")
MIGRATED_TEMPLATE = os.path.join(DOCFORGE, "samples", "templates", "audit-report.docx")
SAMPLE_JSON = os.path.join(DOCFORGE, "samples", "audit-report.sample.json")
BLANK_TEMPLATE = os.path.join(DOCFORGE, "samples", "blank-template.docx")


@pytest.fixture(scope="session")
def original_template_bytes():
    with open(ORIGINAL_TEMPLATE, "rb") as f:
        return f.read()


@pytest.fixture(scope="session")
def migrated_template_bytes():
    with open(MIGRATED_TEMPLATE, "rb") as f:
        return f.read()


@pytest.fixture(scope="session")
def sample_data():
    with open(SAMPLE_JSON, encoding="utf-8") as f:
        return json.load(f)


@pytest.fixture(scope="session")
def blank_template_bytes():
    with open(BLANK_TEMPLATE, "rb") as f:
        return f.read()
