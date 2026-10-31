"""识别口径一致性守护测试。

背景：模板导入识别存在三个真相来源 ——
  1. /scan（scanner.py）产出变量清单、示例 JSON、JSON Schema
  2. /parse（docmodel.py）产出编辑器块模型
  3. Go 侧 sigauth 再从 variables 里挑 sig_/seal_

其中 1 与 2 各自实现了「前缀 → 类型」判定，曾出现 sig_/seal_ 只在 scanner
识别、docmodel 漏判的情况，导致**编辑器里签章占位退化为普通文本**——用户看到
的就是「导入识别有问题」。

本测试锁定两侧口径一致，防止再次分叉。
"""

import pytest

from app import docmodel, preprocess, scanner

# scanner 用 "richtext"，docmodel（编辑器块类型）用 "rich"，指同一类，
# 显式声明为别名，避免把命名差异误判为口径分叉。
KIND_ALIAS = {"richtext": "rich"}

PROBES = [
    "sig_alice",
    "seal_main",
    "qr_invoice",
    "image_logo",
    "img_photo",
    "rich_intro",
    "md_body",
    "html_note",
    "plain_var",
    "金额",
    "items",
    "item.name",
]


def _norm(kind: str) -> str:
    return KIND_ALIAS.get(kind, kind)


@pytest.mark.parametrize("name", PROBES)
def test_kind_parity(name: str) -> None:
    """/scan 与 /parse 的前缀判定必须一致。"""
    s = _norm(scanner._kind_by_name(name, "scalar"))
    d = _norm(docmodel._kind_by_name(name, "scalar"))
    assert s == d, f"前缀口径分叉：{name} → /scan={s}, /parse={d}"


def test_sig_seal_are_image_in_editor() -> None:
    """签章占位在编辑器里必须是图片块，不能退化成普通文本。"""
    assert docmodel._kind_by_name("sig_zhangsan") == "image"
    assert docmodel._kind_by_name("seal_gongzhang") == "image"


def test_token_regex_consistent() -> None:
    """三处标签正则必须一致；任一处改动都必须同步。"""
    pats = {
        "scanner": scanner.TOKEN_RE.pattern,
        "docmodel": docmodel.TOKEN_RE.pattern,
        "preprocess": preprocess.TAG_RE.pattern,
    }
    assert len(set(pats.values())) == 1, f"标签正则已分叉：{pats}"


def test_system_sig_var_semantics() -> None:
    """is_system_sig_var 的判定应与 sig_/seal_ 前缀规则保持一致。"""
    assert scanner.is_system_sig_var("sig_zhangsan")
    assert scanner.is_system_sig_var("seal_gongzhang")
    assert not scanner.is_system_sig_var("image_logo")
    assert not scanner.is_system_sig_var("qr_code")


def test_sig_vars_not_in_sample_json() -> None:
    """系统签章变量不得进入用户填写示例（否则会误导用户去填签章）。"""
    raw = open("../samples/templates/audit-report.docx", "rb").read()
    result = scanner.scan_docx(raw, repair=True)
    names = {v["name"] for v in result["variables"]}
    # 该模板无签章占位；此断言防御未来给样例模板加签章时漏改过滤逻辑
    assert not any(n.startswith(("sig_", "seal_")) for n in names)
