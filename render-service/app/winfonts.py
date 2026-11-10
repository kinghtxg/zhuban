# -*- coding: utf-8 -*-
"""Windows 字体注册（免管理员），替代 Linux 侧的 fontconfig。

为什么需要这个模块
------------------
Linux 上「让新字体立刻可用」= 把文件丢进目录 + `fc-cache -f`，LibreOffice 走
fontconfig，下一进程就看得见。Windows 没有对应的机制：LibreOffice 通过
DirectWrite 枚举**系统已注册**的字体，任何「只是放在某个目录里」的字体文件
都不会被识别——包括 LibreOffice 自己的 `Fonts/` 目录。

实测（本机，Windows 10/11，MSI 管理式解压出来的 LibreOffice）：

  - 只把它们放进程序目录时 —— Liberation Serif→Times New Roman、
    Caladea→Cambria、DejaVu Sans→Arial；公文三件套 →
    方正小标宋简体→微软雅黑、仿宋_GB2312→宋体、楷体_GB2312→仿宋。
    文档能出 PDF、页码页边距都对，**只有字形是错的**，是最难被发现的那种错。
  - 往 HKCU 字体表写一条「标签 → 字体文件绝对路径」后，六个全部命中
    （FZXBSJW--GB1-0 / FangSong_GB2312 / KaiTi_GB2312 / FangSong / SimHei / SimSun）。

所以「注册」在 Windows 上就是写注册表。这是 Windows「为当前用户安装字体」
本身做的事：
  - 写 HKCU 不是 HKLM，**不需要管理员权限**；
  - 值可以是任意绝对路径，**不需要把文件复制进 C:\\Windows\\Fonts**；
  - 键名只是显示标签，系统按字体文件内嵌的 name 表认定族名
    （实测把标签写成无意义字符串，按族名照样命中）。

配置
----
`DOCFORGE_WIN_FONT_DIRS`：分号分隔的目录列表（Windows 路径本身就用 `;`），
递归扫描其中的 .ttf/.ttc/.otf/.otc。便携版启动器把「随包带来的字体」和
「运维上传的字体」两个目录都放进来，上传后调 /fonts/reload 即可立刻生效。
"""
from __future__ import annotations

import os
import struct
import sys
import time
from typing import Dict, Iterable, List, Set

FONT_KEY = r"Software\Microsoft\Windows NT\CurrentVersion\Fonts"
FONT_EXT = (".ttf", ".ttc", ".otf", ".otc")
_LABEL_SUFFIX = " (TrueType)"
ENV_DIRS = "DOCFORGE_WIN_FONT_DIRS"

_family_cache: Set[str] | None = None


def supported() -> bool:
    return sys.platform == "win32"


def font_dirs() -> List[str]:
    """读 DOCFORGE_WIN_FONT_DIRS，只保留真实存在的目录。"""
    raw = os.environ.get(ENV_DIRS, "") or ""
    out: List[str] = []
    seen = set()
    for part in raw.split(";"):
        p = part.strip().strip('"')
        if not p:
            continue
        p = os.path.abspath(p)
        if os.path.isdir(p) and p.lower() not in seen:
            seen.add(p.lower())
            out.append(p)
    return out


def _iter_fonts(root: str) -> Iterable[str]:
    for dirpath, _dirnames, filenames in os.walk(root):
        for fn in sorted(filenames):
            if fn.lower().endswith(FONT_EXT):
                yield os.path.join(dirpath, fn)


def _system_font_names() -> set:
    """`C:\\Windows\\Fonts` 里已有的文件名（小写）。

    同名文件跳过，不让「随包带的 simsun.ttc」去和「系统装的 simsun.ttc」抢同一个
    族名：两处文件版本可能不同，DirectWrite 挑哪个不确定，而字体一换、
    字形度量就跟着变——正是「格式不能变」最怕的随机性。系统有的就用系统的。
    """
    win = os.environ.get("SystemRoot") or r"C:\Windows"
    d = os.path.join(win, "Fonts")
    try:
        return {n.lower() for n in os.listdir(d)}
    except OSError:
        return set()


def _norm(path: str) -> str:
    return os.path.normcase(os.path.abspath(path))


def broadcast() -> None:
    """通知已运行的程序字体表变了。失败无所谓：新进程本来就会重读。"""
    try:
        import ctypes

        HWND_BROADCAST = 0xFFFF
        WM_FONTCHANGE = 0x001D
        SMTO_ABORTIFHUNG = 0x0002
        res = ctypes.c_ulong(0)
        ctypes.windll.user32.SendMessageTimeoutW(
            HWND_BROADCAST, WM_FONTCHANGE, 0, 0, SMTO_ABORTIFHUNG, 1000, ctypes.byref(res)
        )
    except Exception:
        pass


def register(dirs: List[str] | None = None) -> Dict[str, object]:
    """把目录下的字体注册到当前用户，返回统计。幂等：已注册的只计数不改写。"""
    started = time.time()
    out: Dict[str, object] = {
        "ok": False,
        "added": 0,
        "skipped": 0,
        "system": 0,
        "total": 0,
        "failed": [],
        "errors": [],
        "dirs": [],
        "elapsed_ms": 0,
    }
    if not supported():
        out["errors"] = ["非 Windows 平台，字体由 fontconfig 管理"]
        out["elapsed_ms"] = int((time.time() - started) * 1000)
        return out

    use = font_dirs() if dirs is None else [os.path.abspath(d) for d in dirs]
    out["dirs"] = use
    if not use:
        out["errors"] = ["%s 未配置或目录都不存在，没有可注册的字体" % ENV_DIRS]
        out["elapsed_ms"] = int((time.time() - started) * 1000)
        return out

    import winreg

    try:
        key = winreg.CreateKeyEx(
            winreg.HKEY_CURRENT_USER, FONT_KEY, 0, winreg.KEY_READ | winreg.KEY_WRITE
        )
    except OSError as exc:
        out["errors"] = ["无法写入 HKCU 字体注册表：%s" % exc]
        out["elapsed_ms"] = int((time.time() - started) * 1000)
        return out

    sys_names = _system_font_names()
    with key:
        registered_paths = set()
        used_labels = set()
        try:
            count = winreg.QueryInfoKey(key)[1]
        except OSError:
            count = 0
        for i in range(count):
            try:
                name, value, _typ = winreg.EnumValue(key, i)
            except OSError:
                continue
            used_labels.add(str(name).lower())
            # HKLM 里的值常是裸文件名（simsun.ttc），相对 C:\Windows\Fonts 而言，
            # 不能当路径解析——那会按当前工作目录补出一个根本不存在的绝对路径。
            if isinstance(value, str) and os.sep in value:
                registered_paths.add(_norm(value))

        for root in use:
            for path in _iter_fonts(root):
                out["total"] = int(out["total"]) + 1  # type: ignore[arg-type]
                if os.path.basename(path).lower() in sys_names:
                    out["system"] = int(out["system"]) + 1  # type: ignore[arg-type]
                    continue
                norm = _norm(path)
                if norm in registered_paths:
                    out["skipped"] = int(out["skipped"]) + 1  # type: ignore[arg-type]
                    continue
                stem = os.path.splitext(os.path.basename(path))[0]
                label = stem + _LABEL_SUFFIX
                if label.lower() in used_labels:
                    # 同名不同文件（custom/ 与 imported/ 都可能带一份），
                    # 加个后缀占位，否则 SetValueEx 会覆盖掉前一条。
                    label = "%s [%s]%s" % (stem, norm[-8:], _LABEL_SUFFIX)
                try:
                    winreg.SetValueEx(key, label, 0, winreg.REG_SZ, path)
                except OSError as exc:
                    out["failed"].append("%s: %s" % (os.path.basename(path), exc))  # type: ignore[union-attr]
                    continue
                registered_paths.add(norm)
                used_labels.add(label.lower())
                out["added"] = int(out["added"]) + 1  # type: ignore[arg-type]

    if int(out["added"]) > 0:  # type: ignore[arg-type]
        # 族名缓存在注册前可能已经算过（体检先跑），新字体要能被看见
        global _family_cache
        _family_cache = None
        broadcast()
    out["ok"] = not out["failed"] and not out["errors"]
    out["elapsed_ms"] = int((time.time() - started) * 1000)
    return out


def reload() -> Dict[str, object]:
    """/fonts/reload 在 Windows 分支调这里，形状与 fonts.reload_cache 一致。"""
    res = register()
    errors = list(res.get("errors") or []) + list(res.get("failed") or [])  # type: ignore[arg-type]
    return {
        "ok": bool(res.get("ok")),
        "elapsed_ms": int(res.get("elapsed_ms") or 0),
        "import_dir": ";".join(res.get("dirs") or []),  # type: ignore[arg-type]
        "registered": int(res.get("added") or 0),
        "errors": errors[:10],
    }


# --------------------------------------------------------------- 族名：自己读 name 表
#
# 体检（fontaudit）要判断「模板里写的族名本机到底有没有」。Linux 上靠 fc-list；
# Windows 上没有 fontconfig，只能自己从字体文件里读 —— 而且**不能**用注册表键名，
# 键名是任意标签（实测写成 ZZBOGUSLABEL 也照样命中），真正的族名在文件的
# name 表里。
#
# 只读 name 表，不整个文件读进来：系统里 simsun.ttc / msyh.ttc 都是十几 MB，
# 逐个全读一遍太浪费。所以按 sfnt 目录 seek 过去读那一段。

_NAME_ID_FAMILY = 1
_NAME_ID_STYLE = 2
_NAME_PLATFORM_WIN = 3
_NAME_PLATFORM_MAC = 1


def _u16(b: bytes, off: int) -> int:
    return struct.unpack(">H", b[off:off + 2])[0]


def _u32(b: bytes, off: int) -> int:
    return struct.unpack(">I", b[off:off + 4])[0]


def _faces(fh) -> List[int]:
    """返回一个字体文件里各张脸的 sfnt 偏移（TTC 有多张）。"""
    fh.seek(0)
    head = fh.read(12)
    if len(head) < 12:
        return []
    if head[:4] == b"ttcf":
        count = _u32(head, 8)
        if count <= 0 or count > 512:
            return []
        fh.seek(12)
        raw = fh.read(4 * count)
        if len(raw) < 4 * count:
            return []
        return [_u32(raw, 4 * i) for i in range(count)]
    return [0]


def _families_in_file(path: str) -> Set[str]:
    out: Set[str] = set()
    for face in _faces_names(path):
        out |= face.get(_NAME_ID_FAMILY, set())
    return out


def _faces_names(path: str) -> List[Dict[int, Set[str]]]:
    """逐张脸返回 name 表里的名字，按 nameID 分组（1=族名，2=字重/样式）。

    TTC 一个文件里有多张脸（simsun.ttc 是 SimSun + NSimSun），必须分别取：
    合并成一个大集合之后就没法回答「这张脸叫什么」了，字体列表会多出重复行。
    """
    out: List[Dict[int, Set[str]]] = []
    try:
        with open(path, "rb") as fh:
            for base in _faces(fh):
                names: Dict[int, Set[str]] = {}
                try:
                    fh.seek(base)
                    hdr = fh.read(12)
                    if len(hdr) < 12:
                        continue
                    ntab = _u16(hdr, 4)
                    if ntab <= 0 or ntab > 256:
                        continue
                    fh.seek(base + 12)
                    dirk = fh.read(16 * ntab)
                except OSError:
                    continue
                name_off = name_len = 0
                for i in range(ntab):
                    rec = dirk[16 * i:16 * i + 16]
                    if len(rec) < 16:
                        break
                    if rec[:4] == b"name":
                        name_off = _u32(rec, 8)
                        name_len = _u32(rec, 12)
                        break
                if not name_off or name_len <= 6 or name_len > 4 << 20:
                    continue
                try:
                    fh.seek(name_off)
                    nt = fh.read(name_len)
                except OSError:
                    continue
                if len(nt) < 6:
                    continue
                count = _u16(nt, 2)
                str_off = _u16(nt, 4)
                for j in range(min(count, 512)):
                    rec = nt[6 + 12 * j:6 + 12 * j + 12]
                    if len(rec) < 12:
                        break
                    plat = _u16(rec, 0)
                    name_id = _u16(rec, 6)
                    length = _u16(rec, 8)
                    off = _u16(rec, 10)
                    if name_id not in (_NAME_ID_FAMILY, _NAME_ID_STYLE) or not length:
                        continue
                    blob = nt[str_off + off:str_off + off + length]
                    if len(blob) < length:
                        continue
                    try:
                        if plat == _NAME_PLATFORM_WIN:
                            # 平台 3 = Windows，编码 1/10，UTF-16BE。
                            # 各语言版本都收：简体族名常只出现在 0x804 里，
                            # 而模板里写的正是那个中文名。
                            txt = blob.decode("utf-16-be", "ignore")
                        elif plat == _NAME_PLATFORM_MAC:
                            txt = blob.decode("mac-roman", "ignore")
                        else:
                            continue
                    except (UnicodeDecodeError, LookupError):
                        continue
                    txt = txt.replace("\x00", "").strip()
                    if txt:
                        names.setdefault(name_id, set()).add(txt)
                if names:
                    out.append(names)
    except OSError:
        return out
    return out


def _registry_font_paths() -> List[str]:
    """HKLM + HKCU 字体表里指向的文件（去重、只保留存在的）。"""
    import winreg

    win = os.environ.get("SystemRoot") or r"C:\Windows"
    sys_dir = os.path.join(win, "Fonts")
    seen: Set[str] = set()
    out: List[str] = []
    for hive in (winreg.HKEY_LOCAL_MACHINE, winreg.HKEY_CURRENT_USER):
        try:
            key = winreg.OpenKey(hive, FONT_KEY, 0, winreg.KEY_READ)
        except OSError:
            continue
        with key:
            try:
                count = winreg.QueryInfoKey(key)[1]
            except OSError:
                continue
            for i in range(count):
                try:
                    _name, value, _typ = winreg.EnumValue(key, i)
                except OSError:
                    continue
                if not isinstance(value, str) or not value:
                    continue
                # HKLM 里常是裸文件名（相对 C:\Windows\Fonts）；
                # HKCU 里是我们写进去的绝对路径。
                path = value if os.sep in value else os.path.join(sys_dir, value)
                if not path.lower().endswith(FONT_EXT):
                    continue
                norm = _norm(path)
                if norm in seen:
                    continue
                seen.add(norm)
                if os.path.isfile(path):
                    out.append(path)
    return out


def installed_families(refresh: bool = False) -> Set[str]:
    """本机**真实可被应用看到**的族名集合。

    范围 = 注册表登记过的字体 + `C:\\Windows\\Fonts` 目录里的文件。
    后者兜底：某些机器装字体只拷文件没写注册表（镜像/克隆盘常见），
    而 LibreOffice 其实看得见 —— 报「缺失」才是假报警。
    """
    global _family_cache
    if _family_cache is not None and not refresh:
        return _family_cache
    out: Set[str] = set()
    for p in all_font_files():
        out |= _families_in_file(p)
    _family_cache = out
    return out


def all_font_files() -> List[str]:
    """本机能被应用看到的所有字体文件（注册表 + 系统目录 + 启动器给的目录）。"""
    files: List[str] = []
    if not supported():
        return files
    seen: Set[str] = set()

    def add(p: str) -> None:
        n = _norm(p)
        if n in seen:
            return
        seen.add(n)
        files.append(p)

    for p in _registry_font_paths():
        add(p)
    win = os.environ.get("SystemRoot") or r"C:\Windows"
    sys_dir = os.path.join(win, "Fonts")
    if os.path.isdir(sys_dir):
        try:
            for fn in sorted(os.listdir(sys_dir)):
                if fn.lower().endswith(FONT_EXT):
                    add(os.path.join(sys_dir, fn))
        except OSError:
            pass
    for d in font_dirs():
        for p in _iter_fonts(d):
            add(p)
    return files


def _ordered(families: Set[str]) -> List[str]:
    """拉丁名排前面。

    与 Linux 侧 fc-list 的输出对齐：fc-list 给的是 `SimSun,宋体`，Go 那边
    取 `split(",")[0]` 当显示名。不排一下，Windows 上可能显示成「宋体」，
    两边的字体列表看起来就不是一回事了。
    """
    return sorted(families, key=lambda s: (not s.isascii(), s))


def installed_fonts(import_dir: str = "") -> List[Dict[str, object]]:
    """Windows 版的「本机装了哪些字体」，形状对齐 fonts.installed()。

    Linux 侧靠 `fc-list` 一次问全；Windows 上没有这个命令，只能自己遍历字体
    文件读 name 表。**不能**因为「读起来麻烦」就返回空列表 —— 字体管理页会
    显示「已导入 0 个」，管理员会以为随包的 59 个公文字体没装上，而他刚在
    别的页面上看到模板体检全是 exact。前后矛盾比不知道更糟。
    """
    out: List[Dict[str, object]] = []
    if not supported():
        return out
    root = ""
    if import_dir:
        try:
            root = os.path.normcase(os.path.realpath(import_dir))
        except OSError:
            root = ""
    for path in all_font_files():
        try:
            real = os.path.normcase(os.path.realpath(path))
        except OSError:
            real = os.path.normcase(path)
        imported = bool(root) and (real == root or real.startswith(root + os.sep))
        for face in _faces_names(path):
            fams = face.get(_NAME_ID_FAMILY) or set()
            if not fams:
                continue
            styles = face.get(_NAME_ID_STYLE) or set()
            out.append({
                "file": os.path.basename(path),
                "family": ",".join(_ordered(fams)),
                "style": ",".join(_ordered(styles)),
                "path": path,
                "index": 0,
                "imported": imported,
            })
    return out

