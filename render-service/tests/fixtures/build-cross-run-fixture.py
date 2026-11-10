# -*- coding: utf-8 -*-
"""生成 tests/fixtures/cross-run.docx：一个"占位符被 Word 拆成多个 run"的样本。

为什么要有这份夹具
------------------
Word 在编辑过程中会把一段连续的文本切进多个 `<w:r>`（改过字号、拼写检查、
从别处粘贴都会触发）。docxtpl 只认"一个 run 里完整的 `{{ }}`"，所以渲染前
必须先做跨 run 合并。这条链路只能靠一份**真的被拆过**的 docx 来验——
拿一个"每个占位符都在单个 run 里"的模板去测，合并逻辑一次都不会被触发，
测试永远是绿的。

早期的夹具是从上一个项目的《模板.docx》里裁出来的，那份文件不该继续留在
仓库里；这里改成脚本生成：拆几刀、拆在哪，都写在代码里，可复现、可评审。

用法：
    python render-service/tests/fixtures/build-cross-run-fixture.py
"""
from __future__ import annotations

import os

from docx import Document
from docx.shared import Pt

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "cross-run.docx")

# (段落内各 run 的文本)。刻意把 {{ }} 拆在中间，包括拆在花括号内部——
# 那是最坏的一种：任何按 run 做的正则都会漏。
PARAGRAPHS = [
    ["月度经营分析报告"],
    ["报告期：{{报", "告", "期}}"],
    ["编制单位：{{编制", "单位}}"],
    ["核心指标：共 {{ 指", "标数 }} 项"],
]


def main() -> int:
    doc = Document()
    style = doc.styles["Normal"]
    style.font.name = "宋体"
    style.font.size = Pt(10.5)

    for runs in PARAGRAPHS:
        p = doc.add_paragraph()
        for text in runs:
            p.add_run(text)

    doc.save(OUT)
    print(f"写出 {OUT} ({os.path.getsize(OUT)} B)，{len(PARAGRAPHS)} 段")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
