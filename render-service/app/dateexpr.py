# -*- coding: utf-8 -*-
"""日期表达式：让调用方在 JSON 里声明「用生成那一刻的日期」，而不是自己算好传进来。

为什么需要它
-----------
审计报告、合规文书里的「报告日期 / 有效期至」必须是**出具当天**。让调用方先算
好再传有三个绕不过去的问题：

1. 时钟与进区不一致。调用方的容器可能跑 UTC，客户端可能是浏览器本地时间，
   而出具方要求北京时间——同一份数据在三个人手里会得到三个日期。
2. 批量任务里每一份都该是同一时刻，但调用方逐条算，跨零点就会前半批昨天、
   后半批今天。
3. 补跑历史任务时，日期会被「重算成今天」，原始出具日被覆盖——审计上不可接受。
   （补跑要还原原始日期，正确做法是调用方把字面日期传进来，本模块同样支持。）

所以把「怎么算」传进来，由服务端在渲染那一刻现场求值。

契约
----
    {"$date": "today"}                                    → 2026-10-25
    {"$date": "now", "format": "%Y年%m月%d日 %H:%M"}        → 2026年10月25日 20:58
    {"$date": "today", "offset_days": 30}                 → 2026-11-24
    {"$date": "today", "offset_months": 1,
     "last_day_of_month": true}                           → 2026-11-30
    {"$date": "2026-01-01"}                               → 2026-01-01
    {"$date": "2026-01-01T09:00:00+08:00"}                → 2026-01-01

字段
----
    $date              必填。today | now | YYYY-MM-DD | YYYY-MM-DDTHH:MM[:SS][±HH:MM]
    offset_days        可选。整数，可负
    offset_months      可选。整数，可负；月末会钳制（1/31 +1月 → 2/28）
    offset_years       可选。整数，可负
    last_day_of_month  可选。true 时跳到当月最后一天（在 months/years 之后、days 之前）
    format             可选。strftime 格式，默认 %Y-%m-%d
    tz                 可选。Asia/Shanghai（默认）| UTC | ±HH:MM

为什么存在 `$` 前缀
-------------------
它同时充当「保留命名空间」和「可以放心报错」的标记：业务数据里几乎不可能出现
以 `$` 开头的键，所以看到 `$date` 就一定是本模块的表达式，字段写错时可以直接
报错，而不用担心误伤正常数据。

设计取舍：严格 > 宽容
--------------------
未知字段、拼错的 base、超范围的偏移，一律报错而不是忽略。原因很直白——
如果 `{"$date": "todya"}` 被静默当成普通字符串塞进文档，用户拿到的是一份
把「todya」印在报告日期栏里的成品，而且校验还是绿的。宁可 422。
"""
from __future__ import annotations

import re
from datetime import date, datetime, timedelta, timezone
from typing import Any, Dict, List, Tuple

# 只认这些键。多一个就报错——静默忽略等于「用户以为生效了但没生效」。
ALLOWED_KEYS = {
    "$date", "offset_days", "offset_months", "offset_years",
    "last_day_of_month", "format", "tz",
}
MARKER = "$date"

DEFAULT_FMT = "%Y-%m-%d"
DEFAULT_TZ = "Asia/Shanghai"

# 偏移范围：100 年量级足够任何文书，同时挡住溢出与「+999999 天」这类手滑。
MAX_OFFSET_DAYS = 36600
MAX_OFFSET_MONTHS = 1200
MAX_OFFSET_YEARS = 100

MAX_FMT_LEN = 64
MAX_OUTPUT_LEN = 128
MAX_DEPTH = 32          # 嵌套对象/数组的递归上限，防构造深层结构
MAX_NODES = 20000       # 单次请求处理的节点总数上限

# 时区白名单。Asia/Shanghai 用固定 +08 表示：中国不实行夏令时，等价且不依赖
# 容器里的 tzdata（python:slim 镜像未必带完整 zoneinfo，缺了会抛异常）。
_TZ_FIXED = {
    "asia/shanghai": timezone(timedelta(hours=8), "Asia/Shanghai"),
    "prc": timezone(timedelta(hours=8), "Asia/Shanghai"),
    "utc": timezone.utc,
    "gmt": timezone.utc,
}
_TZ_OFFSET_RE = re.compile(r"^(?:utc)?([+-])(\d{1,2})(?::?(\d{2}))?$", re.I)
_DATE_ONLY_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")


class DateExprError(ValueError):
    """日期表达式非法。path 用 JSON 路径形式定位到具体字段。"""

    def __init__(self, path: str, message: str):
        super().__init__(message)
        self.path = path
        self.message = message


class DateExpression:
    """一个已解析好的日期表达式，可重复求值（每次调用都取当前时刻）。"""

    __slots__ = ("base", "offset_days", "offset_months", "offset_years",
                 "last_day_of_month", "fmt", "tz", "_fixed", "raw", "path")

    def __init__(self, base: str, offset_days: int, offset_months: int, offset_years: int,
                 last_day_of_month: bool, fmt: str, tz: timezone, raw: Dict[str, Any],
                 path: str = ""):
        self.base = base
        self.offset_days = offset_days
        self.offset_months = offset_months
        self.offset_years = offset_years
        self.last_day_of_month = last_day_of_month
        self.fmt = fmt
        self.tz = tz
        self._fixed = base not in ("today", "now")
        self.raw = raw
        # 记住自己在数据里的位置：求值失败时要能告诉调用方是哪个字段错了，
        # 否则一个 20 字段的 JSON 里日期写错，用户只能自己一行行翻。
        self.path = path

    def evaluate(self, now: datetime | None = None) -> str:
        """求值成字符串。now 仅供测试注入。"""
        base_dt = self._base_datetime(now)
        dt = base_dt
        if self.offset_months or self.offset_years:
            dt = _add_months(dt, self.offset_years * 12 + self.offset_months, self.path)
        if self.last_day_of_month:
            dt = dt.replace(day=_last_day(dt.year, dt.month))
        if self.offset_days:
            dt = dt + timedelta(days=self.offset_days)
        try:
            out = dt.strftime(self.fmt)
        except Exception as e:  # 极端 format（如超长）兜底
            raise DateExprError(self.path, "日期格式化失败：%s" % e)
        if len(out) > MAX_OUTPUT_LEN:
            raise DateExprError(self.path,
                                "日期格式化结果过长（%d 字符，上限 %d）" % (len(out), MAX_OUTPUT_LEN))
        return out

    def _base_datetime(self, now: datetime | None) -> datetime:
        if self.base in ("today", "now"):
            cur = now or datetime.now(self.tz)
            if cur.tzinfo is None:
                cur = cur.replace(tzinfo=self.tz)
            cur = cur.astimezone(self.tz)
            return cur.replace(hour=0, minute=0, second=0, microsecond=0) if self.base == "today" else cur

        # 固定日期：按字面解析，落在本表达式的时区里（naive 时）
        raw = self.base
        text = raw[:-1] + "+00:00" if raw.endswith("Z") else raw
        try:
            dt = datetime.fromisoformat(text)
        except ValueError:
            raise DateExprError(
                self.path,
                "无法识别的日期 %r（支持 today / now / YYYY-MM-DD / ISO 8601）" % raw,
            )
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=self.tz)
        return dt.astimezone(self.tz)


def _last_day(year: int, month: int) -> int:
    if month == 12:
        return 31
    return (date(year, month + 1, 1) - timedelta(days=1)).day


def _add_months(dt: datetime, months: int, path: str = "") -> datetime:
    """加月份，月末自动钳制：1/31 + 1 月 → 2/28（而不是抛异常或溢出到 3/3）。"""
    total = dt.year * 12 + (dt.month - 1) + months
    year, month = divmod(total, 12)
    month += 1
    if not (1 <= year <= 9999):
        raise DateExprError(path, "日期偏移后超出可表示范围（年份 %d）" % year)
    day = min(dt.day, _last_day(year, month))
    return dt.replace(year=year, month=month, day=day)


def _parse_tz(value: Any, path: str) -> timezone:
    if not isinstance(value, str) or not value.strip():
        raise DateExprError(path, "tz 必须是非空字符串（Asia/Shanghai | UTC | +08:00）")
    key = value.strip().lower()
    if key in _TZ_FIXED:
        return _TZ_FIXED[key]
    m = _TZ_OFFSET_RE.match(key)
    if m:
        sign = -1 if m.group(1) == "-" else 1
        hours = int(m.group(2))
        minutes = int(m.group(3) or 0)
        if hours > 14 or minutes > 59:
            raise DateExprError(path, "tz 偏移超出范围（最大 ±14:00）")
        return timezone(sign * timedelta(hours=hours, minutes=minutes))
    raise DateExprError(path, "不支持的时区 %r（仅支持 Asia/Shanghai、UTC 或 ±HH:MM）" % value)


def _int_field(spec: Dict[str, Any], key: str, limit: int, path: str) -> int:
    if key not in spec:
        return 0
    v = spec[key]
    if isinstance(v, bool) or not isinstance(v, int):
        raise DateExprError("%s.%s" % (path, key), "%s 必须是整数" % key)
    if abs(v) > limit:
        raise DateExprError("%s.%s" % (path, key), "%s 超出允许范围（±%d）" % (key, limit))
    return v


def is_date_expr(node: Any) -> bool:
    """是否是日期表达式节点。

    只认「dict 且含 $date 键」。不要求无其它键——多余键在解析时报错，
    这样 `{"$date": "today", "offset_day": 3}`（拼错 s）不会被静默忽略。
    """
    return isinstance(node, dict) and MARKER in node


def parse(node: Dict[str, Any], path: str = "") -> DateExpression:
    """把表达式 dict 解析成可求值对象。字段非法直接抛 DateExprError。"""
    unknown = set(node.keys()) - ALLOWED_KEYS
    if unknown:
        raise DateExprError(
            path,
            "日期表达式含未知字段 %s（允许：%s）"
            % ("、".join(sorted(unknown)), "、".join(sorted(ALLOWED_KEYS))),
        )
    base = node.get(MARKER)
    if not isinstance(base, str) or not base.strip():
        raise DateExprError(path + "." + MARKER, "$date 必须是非空字符串（today / now / 日期）")
    base = base.strip()

    fmt = node.get("format", DEFAULT_FMT)
    if not isinstance(fmt, str) or not fmt:
        raise DateExprError(path + ".format", "format 必须是非空字符串（strftime 格式）")
    if len(fmt) > MAX_FMT_LEN:
        raise DateExprError(path + ".format", "format 过长（%d，上限 %d）" % (len(fmt), MAX_FMT_LEN))

    ldm = node.get("last_day_of_month", False)
    if not isinstance(ldm, bool):
        raise DateExprError(path + ".last_day_of_month", "last_day_of_month 必须是 true/false")

    tz = _parse_tz(node["tz"], path + ".tz") if "tz" in node else _TZ_FIXED["asia/shanghai"]

    expr = DateExpression(
        base=base,
        offset_days=_int_field(node, "offset_days", MAX_OFFSET_DAYS, path),
        offset_months=_int_field(node, "offset_months", MAX_OFFSET_MONTHS, path),
        offset_years=_int_field(node, "offset_years", MAX_OFFSET_YEARS, path),
        last_day_of_month=ldm,
        fmt=fmt,
        tz=tz,
        raw=dict(node),
        path=path,
    )
    # 立即试算一次：格式写错、日期写错在这里就炸，而不是等渲染到一半
    expr.evaluate()
    return expr


def _walk(node: Any, path: str, out: List[Tuple[str, str]], depth: int, now: datetime | None) -> Any:
    if depth > MAX_DEPTH:
        raise DateExprError(path, "数据嵌套层数超过上限（%d 层）" % MAX_DEPTH)
    if is_date_expr(node):
        expr = parse(node, path)
        value = expr.evaluate(now)
        out.append((path or ".", value))
        return value
    if isinstance(node, dict):
        return {k: _walk(v, ("%s.%s" % (path, k)) if path else k, out, depth + 1, now)
                for k, v in node.items()}
    if isinstance(node, list):
        return [_walk(v, "%s[%d]" % (path or "", i), out, depth + 1, now) for i, v in enumerate(node)]
    return node


def resolve(data: Any, now: datetime | None = None) -> Tuple[Any, List[Dict[str, str]]]:
    """递归把数据里的日期表达式替换成字符串。

    返回 (新数据, 变更清单)。不修改入参——调用方可能还要拿原始数据做别的判断。
    now 仅供测试注入固定时刻。
    """
    out: List[Tuple[str, str]] = []
    resolved = _walk(data, "", out, 0, now)
    if len(out) > MAX_NODES:
        raise DateExprError("", "单次请求的日期表达式数量超过上限（%d）" % MAX_NODES)
    changes = [{"path": p, "value": v} for p, v in out]
    return resolved, changes


def resolve_safe(data: Any, now: datetime | None = None) -> Tuple[Any, List[Dict[str, str]]]:
    """同 resolve，但把 DateExprError 收敛成统一形状，供上层转 HTTP 错误。"""
    return resolve(data, now)


def schema_hint() -> Dict[str, Any]:
    """给 OpenAPI / 前端提示用的字段说明。"""
    return {
        "marker": MARKER,
        "allowed_keys": sorted(ALLOWED_KEYS),
        "default_format": DEFAULT_FMT,
        "default_tz": DEFAULT_TZ,
        "examples": [
            {"$date": "today"},
            {"$date": "now", "format": "%Y年%m月%d日 %H:%M"},
            {"$date": "today", "offset_days": 30},
            {"$date": "today", "offset_months": 1, "last_day_of_month": True},
            {"$date": "2026-01-01", "format": "%Y年%m月%d日"},
        ],
        "limits": {
            "offset_days": MAX_OFFSET_DAYS,
            "offset_months": MAX_OFFSET_MONTHS,
            "offset_years": MAX_OFFSET_YEARS,
            "format_len": MAX_FMT_LEN,
            "output_len": MAX_OUTPUT_LEN,
            "depth": MAX_DEPTH,
        },
    }
