# -*- coding: utf-8 -*-
"""撤销「铸版」登记到当前用户的公文字体（卸载用）。

只删**指向本文件夹**的登记项：判断依据是字体文件路径是否在当前包的目录下。
系统自带字体（C:\\Windows\\Fonts）一条都不碰 —— 「一键清理」把用户的系统
字体一起删掉是不可接受的。
"""
from __future__ import annotations

import os
import sys

APP = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(APP)
FONT_KEY = r"Software\Microsoft\Windows NT\CurrentVersion\Fonts"


def main() -> int:
    if sys.platform != "win32":
        print("只适用于 Windows")
        return 1
    import winreg

    root = os.path.normcase(os.path.abspath(ROOT))
    try:
        key = winreg.OpenKey(
            winreg.HKEY_CURRENT_USER, FONT_KEY, 0,
            winreg.KEY_READ | winreg.KEY_WRITE,
        )
    except OSError as exc:
        print("打不开当前用户的字体注册表：%s" % exc)
        return 1

    doomed = []
    with key:
        try:
            count = winreg.QueryInfoKey(key)[1]
        except OSError:
            count = 0
        for i in range(count):
            try:
                name, value, _typ = winreg.EnumValue(key, i)
            except OSError:
                continue
            if not isinstance(value, str) or os.sep not in value:
                continue
            if os.path.normcase(os.path.abspath(value)).startswith(root):
                doomed.append(name)

        removed = 0
        for name in doomed:
            try:
                winreg.DeleteValue(key, name)
                removed += 1
            except OSError as exc:
                print("  ! 删不掉 %s：%s" % (name, exc))

    print("已撤销 %d 条字体登记（系统自带字体未动）" % removed)

    # 字体表变了要通知一声，已开的程序（Word、LibreOffice）才会重新枚举
    try:
        import ctypes

        res = ctypes.c_ulong(0)
        ctypes.windll.user32.SendMessageTimeoutW(
            0xFFFF, 0x001D, 0, 0, 0x0002, 1000, ctypes.byref(res)
        )
    except Exception:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
