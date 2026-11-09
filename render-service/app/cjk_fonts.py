# -*- coding: utf-8 -*-
"""中文字体族名映射：让「宋体」「黑体」在 Linux 上落到简体中文字面。

## 问题

模板里写的是「宋体」「黑体」「微软雅黑」——这些是 Windows 的族名。Linux 上
装的是 Noto CJK，一个 TTC 文件里含 SC/TC/JP/KR 四张脸。缺映射时按机器状态
落到两种坏结果：

- 有 Noto CJK：挑到 **JP 那一面** —— 文本是中文，字形是日式，「直」「骨」「次」
  写法不一样（直的那一竖、骨的框内方向、次的第二笔）；
- 一个 CJK 都没有：落到 DejaVu Sans —— 连汉字都没有，逐字回退，PDF 体积翻倍。

两种都**没有任何报错**：接口 200，正文全对，只有字形不对。

## 做法：先问有没有，只给缺的补

关键教训（2026-11-01 实测）：`<alias><prefer>` 在**已经装了 SimSun 的机器上
同样会把 SimSun 换掉**。我原以为 prefer 只是「往候选队列尾部追加、原族名仍排
第一」，实际 fontconfig 在家族列表里得分并列时按列表顺序取，Noto 排在后面
反而被选中。生产容器就是真装了 simsun/simhei/msyh 的，一份不分青红皂白的
映射等于把用户的正版字体换掉，而且只有肉眼看 PDF 才发现。

所以映射不能靠 fontconfig 语法写「有则用旧的」，只能**装之前先查**：

1. `fc-list` 列出本机真实存在的族名（别名不会改变这个列表）；
2. 某个族名已经存在 → **跳过**，一个字都不写；
3. 不存在 → 追加一条 `<alias><prefer>` 指到简体中文面。

于是：装了正版字体的机器上这份配置几乎是空的；缺字体的机器上才有内容。
两种机器用同一份代码，互不干扰。

## 时机：必须等字体可见之后再决定

容器里的中文正版字体是**运行时挂载**进来的（`/usr/share/fonts/custom`），
构建期看不到。所以判定和写入都放在**容器启动时**（entrypoint 以 root 跑一次
`--install`，随后 setpriv 降权到 appuser 跑服务），而不是镜像构建期。
源码安装则由服务进程启动时自补（通常就是 root）。

## 装在哪

- 系统目录：`/etc/fonts/conf.d/99-zhuban-cjk-alias.conf`（root 可写时）
- 退路：`$XDG_CONFIG_HOME/fontconfig/conf.d/`（fontconfig 在部分发行版会读）

两条路径都幂等：内容一致不重写、不重刷缓存；没有要补的族名时会把旧文件删掉
（否则留着上次的配置，装了正版字体之后仍然被抢）。
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
from typing import Dict, List, Optional, Sequence, Set, Tuple

CONF_NAME = "99-zhuban-cjk-alias.conf"

# （族名, 候选链）。左边是模板/Word 里会出现的写法，右边是**候选链**：
# 从左到右取第一个本机真的装了的；一个都没装就用最后一个（Noto，镜像里必有）。
#
# 第二段是**公文用字**（GB/T 9704-2012 党政机关公文格式）：
#   标题 方正小标宋简体 / 正文 仿宋_GB2312 / 一级标题 黑体 / 二级标题 楷体_GB2312。
# 这些族名 Linux 上一个都没有，实测（2026-11-04 演示机）全部落到 DejaVu Sans
# ——那个字体一个汉字都没有；LibreOffice 自己会回退，但一律退成 Noto Sans，
# 结果就是**整篇公文只有一种黑体**，靠字体区分层级的排版意义全丢。
# 这里按字形气质兜底：仿宋/楷体/小标宋/中宋都属衬线，走 Serif；黑体走 Sans。
#
# 为什么要候选链而不是直接写 Noto：`楷体`/`仿宋` 兜到 Noto Serif CJK SC 只是
# 「勉强算衬线」，字形其实是**宋体**——分类都不对。本机装了真楷体/真仿宋时
# 就该用真的。写死成 Noto 会把已经装好的正版字体挡在外面，等于白装。
#
# 注意：fontconfig 的 alias 只能指定族，**不能指定字重**。小标宋本质是加粗宋体，
# 一个都没装时只能映射到常规的 Noto Serif CJK SC；要标题看起来够重，模板里该处
# 应设加粗，或者安装真正的小标宋字体（装了之后自动走候选链，不会退到 Noto）。
_SERIF = "Noto Serif CJK SC"
_SANS = "Noto Sans CJK SC"
# 楷体族：真楷体优先。裸名「楷体」在装了 GB2312 版时也用它，字形一致。
_KAI = ("KaiTi_GB2312", "楷体_GB2312", "方正楷体简体", "方正楷体_GBK")
# 仿宋族：同上。
_FS = ("FangSong_GB2312", "仿宋_GB2312", "方正仿宋简体", "方正仿宋_GBK")
# 小标宋：GB2312 / GBK 两个写法互相兜底，都没有才退 Serif。
_XBS = ("方正小标宋简体", "方正小标宋_GBK", "方正小标宋_GB2312")

ALIASES: List[Tuple[str, Tuple[str, ...]]] = [
    # ---- 通用 Windows 族名 ----
    ("宋体", (_SERIF,)),
    ("SimSun", (_SERIF,)),
    ("新宋体", (_SERIF,)),
    ("NSimSun", (_SERIF,)),
    ("仿宋", _FS + (_SERIF,)),
    ("FangSong", _FS + (_SERIF,)),
    ("楷体", _KAI + (_SERIF,)),
    ("KaiTi", _KAI + (_SERIF,)),
    ("黑体", (_SANS,)),
    ("SimHei", (_SANS,)),
    ("微软雅黑", (_SANS,)),
    ("Microsoft YaHei", (_SANS,)),
    ("等线", (_SANS,)),
    ("DengXian", (_SANS,)),

    # ---- 公文正文：仿宋_GB2312 ----
    ("仿宋_GB2312", _FS + (_SERIF,)),
    ("FangSong_GB2312", _FS + (_SERIF,)),
    ("方正仿宋简体", ("方正仿宋简体", "FZFangSong-Z02S") + _FS + (_SERIF,)),
    ("方正仿宋_GBK", ("方正仿宋_GBK", "FZFangSong-Z02") + _FS + (_SERIF,)),
    ("方正中雅宋_GBK", ("方正中雅宋_GBK", _SERIF)),

    # ---- 公文二级标题：楷体_GB2312 ----
    ("楷体_GB2312", _KAI + (_SERIF,)),
    ("KaiTi_GB2312", _KAI + (_SERIF,)),
    ("方正楷体简体", ("方正楷体简体", "FZKai-Z03S") + _KAI + (_SERIF,)),
    ("方正楷体_GBK", ("方正楷体_GBK", "FZKai-Z03") + _KAI + (_SERIF,)),

    # ---- 公文标题：方正小标宋 ----
    ("方正小标宋简体", _XBS + (_SERIF,)),
    ("方正小标宋_GBK", _XBS + (_SERIF,)),
    ("方正小标宋_GB2312", _XBS + (_SERIF,)),

    # ---- 华文系（Office for Mac 常见）----
    ("华文中宋", (_SERIF,)),
    ("STZhongsong", (_SERIF,)),
    ("华文仿宋", _FS + (_SERIF,)),
    ("STFangsong", _FS + (_SERIF,)),
    ("华文楷体", _KAI + (_SERIF,)),
    ("STKaiti", _KAI + (_SERIF,)),
    ("华文黑体", (_SANS,)),
    ("STHeiti", (_SANS,)),

    # ---- 其他方正常用 ----
    ("方正书宋简体", ("方正书宋简体", "FZShuSong-Z01S", _SERIF)),
    ("方正黑体简体", ("方正黑体简体", "FZHei-B01S", _SANS)),
]

# ⚠️ 这里**不要**写 `FZFangSong-Z02S` / `FZXiaoBiaoSong-B05S` 这类名字。
# 那是方正字体的 **PostScript 名**，而 fontconfig 的 <alias> 只按**族名(family)**
# 匹配，PostScript 名根本不在 family 列表里，写进配置等于没写。
# 实测（2026-11-04 演示机）：加了这 5 条之后 fc-match 仍然返回 DejaVu Sans，
# 而体检把它们判成失败 → /healthz 的 zh_fonts 从 true 变 false，
# 明明渲染没问题却报红。Word 的 w:rFonts 存的是族名（「方正仿宋_GBK」），
# 不会存 PostScript 名，所以这些条目本来也没有使用场景。

# 判定「这个族名落到了简体中文」的允许清单。
# 不写死成「必须等于 Noto ... SC」：真装了 SimSun 的机器上答案就是 SimSun，
# 那也是对的。要拦的是「落到了 JP/KR/TC」和「落到了完全没有汉字的字体」。
#
# GB2312 / FZ / 方正 / 华文 / ST* 这几项是给「真的装了公文字体」的机器准备的：
# 装了正版「FangSong_GB2312」之后 fc-match 返回的就是那个拉丁族名，
# 不含中文也不含 " SC"，漏掉它会让体检**误报失败**（明明装对了却报红）。
_OK_MARKERS = (
    " SC", "SimSun", "SimHei", "YaHei", "DengXian",
    "宋体", "黑体", "雅黑", "楷体", "仿宋", "等线",
    "Source Han", "Noto Sans SC", "Noto Serif SC",
    "GB2312", "GBK", "FZ", "方正", "华文",
    "STZhong", "STFang", "STKai", "STHei",
)
_BAD_MARKERS = (" JP", " KR", " TC", "Noto Sans CJK J", "Noto Serif CJK J")


# --------------------------------------------------------------------------
# 查询
# --------------------------------------------------------------------------

def fc_match(family: str) -> str:
    """问 fontconfig：这个族名最终会落到哪个字体上。"""
    try:
        p = subprocess.run(
            ["fc-match", "-f", "%{family}", family],
            capture_output=True, timeout=10, text=True,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return ""
    if p.returncode != 0:
        return ""
    return (p.stdout or "").strip()


def installed_families() -> Set[str]:
    """本机**真实存在**的族名集合（fc-list 的视角，别名不影响它）。

    这是「该不该补映射」的唯一依据。用它而不是 fc-match：fc-match 会把我们
    自己写进去的别名算作已解决，于是第二次启动时判定「已经好了」→ 删掉配置
    → 下次又缺 → 来回抖。fc-list 只反映磁盘上的字体文件，不存在这个自指问题。
    """
    try:
        p = subprocess.run(
            ["fc-list", "--format", "%{family}\n"],
            capture_output=True, timeout=30, text=True,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return set()
    if p.returncode != 0:
        return set()
    out: Set[str] = set()
    for line in (p.stdout or "").splitlines():
        for name in line.split(","):
            name = name.strip()
            if name:
                out.add(name)
    return out


def pick_target(candidates: Sequence[str], have: Set[str]) -> str:
    """从候选链里挑第一个本机真有的；都没有就用最后一个（Noto 兜底）。

    最后一个必须是「镜像里一定存在」的那一族，否则会挑出一个同样不存在的
    族名，等于没写。
    """
    for c in candidates:
        if c in have:
            return c
    return candidates[-1]


def needed_aliases(installed: Optional[Set[str]] = None) -> List[Tuple[str, str]]:
    """只返回「本机缺这个族名」的那些映射，目标按候选链解析成具体一族。"""
    have = installed if installed is not None else installed_families()
    out: List[Tuple[str, str]] = []
    for name, candidates in ALIASES:
        if name in have:
            continue
        out.append((name, pick_target(candidates, have)))
    return out


def classify(resolved: str) -> bool:
    """判断 fc-match 返回的族名是不是「简体中文可用」的。

    fc-match 用 %{family} 取值时可能是逗号分隔的多个族名（simsun.ttc 就同时
    报 SimSun 和 宋体），所以用包含判断，不做等值比较。
    """
    fam = (resolved or "").strip()
    if not fam:
        return False
    if any(mark in fam for mark in _BAD_MARKERS):
        return False
    return any(mark in fam for mark in _OK_MARKERS)


# --------------------------------------------------------------------------
# 生成与落盘
# --------------------------------------------------------------------------

def conf_xml(aliases: Optional[Sequence[Tuple[str, str]]] = None) -> str:
    """生成 fontconfig 片段。aliases 为空时生成一个只有注释的空配置。

    不传 aliases 时铺**全部**族名、目标取候选链末位（Noto 兜底）。
    刻意不去查本机装了什么：这个形态是给测试和「看看配置长什么样」用的，
    结果必须与机器无关；真正落盘走 `write_conf(aliases=needed_aliases())`。
    """
    pairs = [(n, c[-1]) for n, c in ALIASES] if aliases is None else list(aliases)
    lines = [
        '<?xml version="1.0"?>',
        '<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">',
        "<!-- 由铸版 render-service 自动生成，请勿手工修改（下次启动会重写）。",
        "     只包含本机**缺少**的中文族名：模板里写的是 宋体/黑体/微软雅黑，",
        "     Linux 上通常没有，缺了会被 fontconfig 挑到日文字面，或者落到",
        "     完全没有汉字的字体上（都不报错，只有字形不对）。",
        "     已经装了正版字体的族名不会出现在这里——写进去反而会把它们抢走。 -->",
        "<fontconfig>",
    ]
    for name, target in pairs:
        lines.append(
            '  <alias binding="same"><family>%s</family>'
            "<prefer><family>%s</family></prefer></alias>" % (_xml_escape(name), target)
        )
    lines.append("</fontconfig>")
    return "\n".join(lines) + "\n"


def _xml_escape(s: str) -> str:
    return (s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
             .replace('"', "&quot;"))


def target_dirs() -> List[str]:
    """按优先级列出可写的 fontconfig 配置目录。"""
    dirs: List[str] = []
    if os.path.isdir("/etc/fonts/conf.d"):
        dirs.append("/etc/fonts/conf.d")
    xdg = os.environ.get("XDG_CONFIG_HOME") or ""
    home = os.environ.get("HOME") or os.path.expanduser("~")
    if not xdg and home and home != "~":
        xdg = os.path.join(home, ".config")
    if xdg:
        dirs.append(os.path.join(xdg, "fontconfig", "conf.d"))
    return dirs


def _existing_confs() -> List[str]:
    """我们写过的配置（用于再无必要补映射时清理）。"""
    out = []
    for d in target_dirs():
        p = os.path.join(d, CONF_NAME)
        if os.path.isfile(p):
            out.append(p)
    return out


def write_conf(dirs: Optional[Sequence[str]] = None,
               aliases: Optional[Sequence[Tuple[str, str]]] = None) -> Optional[str]:
    """把片段写进第一个可写目录。返回落盘路径；一个都写不了返回 None。

    内容一致就不写（避免每次启动都动文件、都刷缓存）。
    """
    payload = conf_xml(aliases)
    for d in (dirs if dirs is not None else target_dirs()):
        try:
            os.makedirs(d, exist_ok=True)
        except OSError:
            continue
        if not os.access(d, os.W_OK):
            continue
        path = os.path.join(d, CONF_NAME)
        try:
            if os.path.isfile(path):
                with open(path, "r", encoding="utf-8") as f:
                    if f.read() == payload:
                        return path
            with open(path, "w", encoding="utf-8") as f:
                f.write(payload)
            return path
        except OSError:
            continue
    return None


def reload_cache(timeout: int = 60) -> bool:
    """fc-cache -f。失败不抛：字体配置已经写下去了，下次进程启动仍会读到。"""
    try:
        subprocess.run(["fc-cache", "-f"], capture_output=True, timeout=timeout)
        return True
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return False


def install() -> Dict[str, object]:
    """按本机实际情况补映射。幂等，可反复调用。

    没有需要补的族名时，会把历史遗留的配置文件删掉 —— 否则「装了正版字体
    之后仍然被自己的旧配置抢走」，正是这次要修的那个坑。
    """
    needed = needed_aliases()
    if not needed:
        removed = []
        for p in _existing_confs():
            try:
                os.remove(p)
                removed.append(p)
            except OSError:
                pass
        if removed:
            reload_cache()
        return {"conf": None, "written": False, "aliases": 0, "removed": removed}
    path = write_conf(aliases=needed)
    if path is not None:
        reload_cache()
    return {"conf": path, "written": path is not None,
            "aliases": len(needed), "removed": []}


def diagnose(families: Optional[Sequence[str]] = None) -> Dict[str, object]:
    """逐个族名体检，返回结构化结果（给 /healthz 与 /fonts/zh 用）。"""
    fams = list(families or [name for name, _ in ALIASES])
    checks = []
    for fam in fams:
        resolved = fc_match(fam)
        checks.append({
            "family": fam,
            "resolved": resolved,
            "ok": classify(resolved),
        })
    bad = [c for c in checks if not c["ok"]]
    return {
        "ok": not bad,
        "failed": [c["family"] for c in bad],
        "checks": checks,
    }


_cache: Optional[Dict[str, object]] = None


def ensure(refresh: bool = True) -> Dict[str, object]:
    """启动时调一次：补齐配置、体检、缓存结果。

    任何一步失败都不抛异常 —— 字体不对不至于让整个渲染服务起不来，
    但必须留下可见的记录（日志 warning + /healthz 里的 failed 列表）。
    """
    global _cache
    if _cache is not None and not refresh:
        return _cache
    try:
        inst = install()
    except Exception as e:  # noqa: BLE001
        inst = {"conf": None, "written": False, "error": str(e)[:200]}
    try:
        st = diagnose()
    except Exception as e:  # noqa: BLE001
        st = {"ok": False, "failed": ["(诊断失败)"], "checks": [], "error": str(e)[:200]}
    st["install"] = inst
    _cache = st

    if not st.get("ok"):
        # 这条日志是给运维看的：出现它说明 PDF 里的汉字可能是日文字形。
        sys.stderr.write(
            "[zhuban.fonts] 中文字体族名未落到简中字面：%s（补了 %s 条映射，配置 %s）。"
            "请确认已安装 fonts-noto-cjk\n"
            % (", ".join(st.get("failed") or []), inst.get("aliases"), inst.get("conf"))
        )
    return st


def status() -> Dict[str, object]:
    """取缓存结果；没跑过就现跑一次。"""
    return _cache if _cache is not None else ensure()


def _main(argv: List[str]) -> int:
    cmd = argv[1] if len(argv) > 1 else "--install"
    if cmd in ("--install", "--check"):
        st = ensure()
        print(json.dumps(st, ensure_ascii=False, indent=2))
        # --check 用于 CI / 部署验收：不达标要非零退出，否则就是「装完没验证」
        if cmd == "--check" and not st.get("ok"):
            return 1
        return 0
    print("用法: python -m app.cjk_fonts [--install|--check]", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(_main(sys.argv))
