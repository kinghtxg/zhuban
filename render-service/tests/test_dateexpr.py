# -*- coding: utf-8 -*-
"""日期表达式测试（D01–D30）。

关注点排序：正确性 < 边界 < 「不能静默错」。最后一条最要紧——
日期错了不会报错，只会印在报告上，等审计时才发现。
"""
from __future__ import annotations

import os
import sys
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app import dateexpr  # noqa: E402

CST = timezone(timedelta(hours=8))


def r(spec, now=None):
    out, _ = dateexpr.resolve({"x": spec}, now)
    return out["x"]


def expect_error(spec, path_part=""):
    try:
        dateexpr.resolve({"x": spec})
    except dateexpr.DateExprError as e:
        if path_part and path_part not in e.path:
            raise AssertionError("错误路径不含 %r：%r" % (path_part, e.path))
        return e
    raise AssertionError("应当报错但通过了：%r" % (spec,))


# ---------------- 基础 ----------------

def test_D01_today():
    assert r({"$date": "today"}, datetime(2026, 10, 25, 20, 58, tzinfo=CST)) == "2026-10-25"


def test_D02_now_with_format():
    got = r({"$date": "now", "format": "%Y年%m月%d日 %H:%M"},
            datetime(2026, 10, 25, 9, 5, tzinfo=CST))
    assert got == "2026年10月25日 09:05"


def test_D03_fixed_date():
    assert r({"$date": "2026-01-01"}) == "2026-01-01"


def test_D04_fixed_datetime_with_offset():
    got = r({"$date": "2026-01-01T09:30:00+08:00", "format": "%Y-%m-%d %H:%M"})
    assert got == "2026-01-01 09:30"


def test_D05_default_format_is_iso_date():
    assert dateexpr.DEFAULT_FMT == "%Y-%m-%d"


# ---------------- 偏移 ----------------

def test_D06_offset_days():
    assert r({"$date": "today", "offset_days": 30},
             datetime(2026, 10, 25, tzinfo=CST)) == "2026-11-24"


def test_D07_offset_days_negative():
    assert r({"$date": "today", "offset_days": -10},
             datetime(2026, 10, 25, tzinfo=CST)) == "2026-10-15"


def test_D08_offset_months():
    assert r({"$date": "today", "offset_months": 3},
             datetime(2026, 1, 15, tzinfo=CST)) == "2026-04-15"


def test_D09_offset_years_leap_day():
    # 2024-02-29 +1 年 → 2025-02-28（钳制，不抛异常）
    assert r({"$date": "2024-02-29", "offset_years": 1}) == "2025-02-28"


def test_D10_month_end_clamp():
    # 1/31 +1 月 → 2/28（2026 非闰年）
    assert r({"$date": "2026-01-31", "offset_months": 1}) == "2026-02-28"


def test_D11_last_day_of_month():
    assert r({"$date": "today", "last_day_of_month": True},
             datetime(2026, 2, 10, tzinfo=CST)) == "2026-02-28"


def test_D12_last_day_after_month_offset():
    # 下个月的最后一天：这是「有效期至下月末」的典型写法
    assert r({"$date": "today", "offset_months": 1, "last_day_of_month": True},
             datetime(2026, 1, 31, tzinfo=CST)) == "2026-02-28"


def test_D13_days_apply_after_last_day():
    assert r({"$date": "today", "last_day_of_month": True, "offset_days": 1},
             datetime(2026, 4, 10, tzinfo=CST)) == "2026-05-01"


# ---------------- 时区 ----------------

def test_D14_utc_differs_across_midnight():
    # 北京 2026-01-01 01:00 = UTC 2025-12-31 17:00
    now = datetime(2026, 1, 1, 1, 0, tzinfo=CST)
    assert r({"$date": "today"}, now) == "2026-01-01"
    assert r({"$date": "today", "tz": "UTC"}, now) == "2025-12-31"


def test_D15_fixed_offset_tz():
    now = datetime(2026, 1, 1, 1, 0, tzinfo=CST)
    assert r({"$date": "today", "tz": "+00:00"}, now) == "2025-12-31"
    assert r({"$date": "today", "tz": "-05:00"}, now) == "2025-12-31"


def test_D16_reject_unknown_tz():
    expect_error({"$date": "today", "tz": "Mars/Olympus"}, ".tz")


# ---------------- 严格性：不能静默错 ----------------

def test_D17_unknown_field_rejected():
    e = expect_error({"$date": "today", "offset_day": 3}, "x")
    assert "offset_day" in e.message


def test_D18_bad_base_rejected():
    expect_error({"$date": "todya"})


def test_D19_empty_base_rejected():
    expect_error({"$date": "  "}, "$date")


def test_D20_non_string_base_rejected():
    expect_error({"$date": 20260101}, "$date")


def test_D21_offset_must_be_int():
    expect_error({"$date": "today", "offset_days": "30"}, "offset_days")


def test_D22_bool_is_not_int():
    # Python 里 True 是 int 的子类，必须显式排除，否则 True 会被当成 +1 天
    expect_error({"$date": "today", "offset_days": True}, "offset_days")


def test_D23_offset_out_of_range():
    expect_error({"$date": "today", "offset_days": 999999}, "offset_days")


def test_D24_format_too_long():
    expect_error({"$date": "today", "format": "%Y" * 40}, "format")


def test_D25_last_day_must_be_bool():
    expect_error({"$date": "today", "last_day_of_month": "yes"}, "last_day_of_month")


# ---------------- 不误伤 ----------------

def test_D26_plain_dict_untouched():
    data = {"报告日期": "2026-10-25", "nested": {"a": 1}, "arr": [1, 2]}
    out, changes = dateexpr.resolve(data)
    assert out == data
    assert changes == []


def test_D27_does_not_mutate_input():
    data = {"d": {"$date": "2026-01-01"}}
    out, _ = dateexpr.resolve(data)
    assert out["d"] == "2026-01-01"
    assert isinstance(data["d"], dict), "入参被就地修改了"


def test_D28_nested_and_array():
    data = {
        "报告": {"签发日": {"$date": "2026-01-01"}},
        "items": [{"开始": {"$date": "2026-02-01"}}, {"开始": {"$date": "2026-03-01"}}],
    }
    out, changes = dateexpr.resolve(data)
    assert out["报告"]["签发日"] == "2026-01-01"
    assert out["items"][0]["开始"] == "2026-02-01"
    assert out["items"][1]["开始"] == "2026-03-01"
    assert len(changes) == 3
    assert "items[1].开始" in [c["path"] for c in changes]


def test_D29_path_is_reported():
    try:
        dateexpr.resolve({"a": {"b": {"$date": "bad-date"}}})
    except dateexpr.DateExprError as e:
        assert e.path == "a.b", "错误路径不准，前端无法定位：%r" % e.path
        return
    raise AssertionError("应当报错")


def test_D30_depth_limit():
    node = {"$date": "today"}
    for _ in range(dateexpr.MAX_DEPTH + 5):
        node = {"k": node}
    try:
        dateexpr.resolve(node)
    except dateexpr.DateExprError as e:
        assert "嵌套" in e.message
        return
    raise AssertionError("超深嵌套应当被拒绝")


if __name__ == "__main__":
    fns = [(k, v) for k, v in sorted(globals().items()) if k.startswith("test_")]
    ok = fail = 0
    for name, fn in fns:
        try:
            fn()
            print("[PASS] %s" % name)
            ok += 1
        except Exception as e:  # noqa: BLE001
            print("[FAIL] %s -> %r" % (name, e))
            fail += 1
    print("-" * 60)
    print("%d passed, %d failed" % (ok, fail))
    sys.exit(1 if fail else 0)
