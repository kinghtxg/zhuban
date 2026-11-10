# -*- coding: utf-8 -*-
"""用真实的扫描器生成变量树夹具，供前端往返测试使用。

手写变量树最大的问题是「照着代码想象扫描器会输出什么」——一旦想象错了，
测试全绿但真机打开模板是空的。这里直接调 render-service 的 scanner，
产物就是线上会拿到的那个结构。

用法（在 render-service 目录下）：
    python ../tools/gen-mdfill-fixtures.py
"""
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, os.path.join(ROOT, "render-service"))

from app.scanner import scan_docx  # noqa: E402

SRC = r"C:\Users\41935\Downloads"
OUT = os.path.join(ROOT, "web", "src", "components", "mdfill", "__fixtures__")

CASES = [
    ("渗透测试报告-模板.docx", "scanner-v1.json"),
    ("渗透测试报告-模板-v2.docx", "scanner-v2.json"),
]

def main():
    os.makedirs(OUT, exist_ok=True)
    for name, dest in CASES:
        path = os.path.join(SRC, name)
        with open(path, "rb") as f:
            result = scan_docx(f.read())
        keep = {
            "variables": result.get("variables", []),
            "sample": result.get("sample_json", {}),
            "schema": result.get("json_schema", {}),
            "charts": result.get("charts", []),
            "diagnostics": result.get("diagnostics", []),
        }
        with open(os.path.join(OUT, dest), "w", encoding="utf-8") as f:
            json.dump(keep, f, ensure_ascii=False, indent=2)
        print(f"{dest}: {len(keep['variables'])} 个顶层变量, "
              f"{len(keep['sample'])} 个示例键")

if __name__ == "__main__":
    main()
