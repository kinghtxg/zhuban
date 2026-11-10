# -*- coding: utf-8 -*-
"""PDF 物理层验收：页数、页面尺寸、字体是否真命中、版心与行距、目录页码。

为什么非要做这一层：断言 docx 的 rFonts 是「宋体」不等于用户看到宋体——
字体没注册时 LibreOffice 会静默替换成别的字形，docx 上一个字都没变。
唯一可信的判据在 PDF 里：内嵌了哪些字体、每段文字落在什么坐标。
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys

POPPLER = os.environ.get("ZB_POPPLER") or r"C:\tmp\zb-test2\zhuban-3.4.23-windows-x64\app\poppler"
PDF = sys.argv[1] if len(sys.argv) > 1 else r"C:\tmp\out-shots.pdf"

_ok = _fail = 0
bad: list[str] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    global _ok, _fail
    if cond:
        _ok += 1
        print("  √ %s%s" % (name, ("  " + detail) if detail else ""))
    else:
        _fail += 1
        bad.append("%s  %s" % (name, detail))
        print("  × %s%s" % (name, ("  " + detail) if detail else ""))


def run(exe: str, *args: str) -> str:
    p = os.path.join(POPPLER, exe)
    r = subprocess.run([p, *args], capture_output=True)
    return (r.stdout or b"").decode("utf-8", "replace")


def main() -> int:
    print("=" * 70)
    print(" PDF 物理层验收：%s" % os.path.basename(PDF))
    print("=" * 70)
    if not os.path.isfile(PDF):
        print("文件不存在：%s" % PDF)
        return 1

    # ---------------------------------------------------------------- 基本信息
    print("\n[1] 页面")
    info = run("pdfinfo.exe", PDF)
    pages = int(re.search(r"Pages:\s+(\d+)", info).group(1))
    size = re.search(r"Page size:\s+([\d.]+) x ([\d.]+)", info)
    w, h = float(size.group(1)), float(size.group(2))
    check("页数合理（5~20 页）", 5 <= pages <= 20, "%d 页" % pages)
    # A4 = 595.276 x 841.89 pt，允许 0.5pt 误差
    check("页面是 A4 纵向", abs(w - 595.276) < 0.5 and abs(h - 841.89) < 0.5,
          "%.2f x %.2f pt" % (w, h))

    # ---------------------------------------------------------------- 字体
    print("\n[2] 字体（PDF 里实际嵌了什么）")
    fonts_raw = run("pdffonts.exe", PDF)
    lines = [l for l in fonts_raw.splitlines() if l.strip()][2:]
    names, not_embedded = [], []
    for l in lines:
        cols = l.split()
        if len(cols) < 5:
            continue
        names.append(cols[0])
        # pdffonts 列：name type encoding emb sub uni object ID
        if len(cols) >= 5 and cols[3] == "no":
            not_embedded.append(cols[0])
    check("有字体（不是空白页）", bool(names), "%d 支" % len(names))
    check("全部内嵌", not not_embedded,
          ("未内嵌：%s" % not_embedded[:4]) if not_embedded else "%d 支都内嵌" % len(names))

    # 随包字体应当出现。中文正文用宋体族；标题用黑体族。
    joined = " ".join(names)
    has_cjk = bool(re.search(r"(SimSun|Song|FZ|Noto.*CJK|SourceHan|STSong|Kai|Hei|Fang)",
                             joined, re.I))
    check("中文正文字体命中随包字体", has_cjk, joined[:160])

    # 不该出现的情况：整篇只有 Times/Liberation（说明中文字体全回退了）
    only_latin = all(re.search(r"(Times|Liberation|DejaVu|Arial|Caladea|Carlito)", n, re.I)
                     for n in names)
    check("不是全篇西文字体（中文没整段回退）", not only_latin, "%d 支" % len(names))

    # ---------------------------------------------------------------- 坐标
    print("\n[3] 版心与行距（按坐标，不按肉眼）")
    bbox = run("pdftotext.exe", "-bbox", "-f", "2", "-l", "3", PDF, "-")
    xs, ys = [], []
    for m in re.finditer(r'<word xMin="([\d.]+)" yMin="([\d.]+)" xMax="([\d.]+)" yMax="([\d.]+)">([^<]*)</word>',
                         bbox):
        x0, y0, x1, y1, txt = float(m.group(1)), float(m.group(2)), float(m.group(3)), float(m.group(4)), m.group(5)
        if not txt.strip():
            continue
        xs.append((x0, x1, y0, txt))
    check("第 2~3 页有文字", len(xs) > 50, "%d 个词" % len(xs))
    if xs:
        left = min(x[0] for x in xs)
        right = max(x[1] for x in xs)
        check("版心左边距在同一量级（60~120pt）", 60 <= left <= 120, "xMin=%.2f" % left)
        check("版心右边距不溢出（<= 页面宽）", right <= w - 20, "xMax=%.2f / 页宽 %.2f" % (right, w))
        # 行距：同一列相邻行的 yMin 差值。页面里还有标题、段间距、列表项，
        # 所以只要求「存在一个稳定主导值」而不是「全都一样」——正文 12pt 字
        # 配 1.5 倍行距正好落在 24pt 左右，这个值应是出现最多的那个。
        col = [x for x in xs if abs(x[0] - left) < 2]
        ys_col = sorted({round(x[2], 2) for x in col})
        gaps = [round(b - a, 2) for a, b in zip(ys_col, ys_col[1:]) if 5 < b - a < 60]
        if gaps:
            from collections import Counter
            common, cnt = Counter(gaps).most_common(1)[0]
            check("行距有稳定主导值且在正文量级（20~30pt）",
                  20 <= common <= 30 and cnt >= 3,
                  "主导 %.2fpt 出现 %d/%d 次" % (common, cnt, len(gaps)))

    # ---------------------------------------------------------------- 目录页码
    print("\n[4] 目录页码与实际页码")
    txt = run("pdftotext.exe", "-layout", PDF, "-")
    # 目录行特征：标题 + 点线 + 页码
    toc_rows = []
    for line in txt.splitlines():
        m = re.match(r"^\s*(.+?)[\s.·…]{3,}\s*(\d{1,3})\s*$", line)
        if m and len(m.group(1).strip()) >= 2:
            toc_rows.append((m.group(1).strip(), int(m.group(2))))
    if toc_rows:
        check("目录有页码", True, "%d 条" % len(toc_rows))
        # 找正文所在页时**必须排除目录页自己**：目录行同样以标题文字开头，
        # 不排除就会把「目录页」当成标题所在页，得出「页码全都少 1」的假结论。
        # 判据是这一页上有大量「标题+点线+页码」行。
        pages_txt = txt.split("\f")
        toc_pages = set()
        for i, p in enumerate(pages_txt, 1):
            n = len([1 for l in p.splitlines()
                     if re.match(r"^\s*(.+?)[\s.·…]{3,}\s*(\d{1,3})\s*$", l)])
            if n >= 3:
                toc_pages.add(i)
        wrong = []
        for title, page in toc_rows[:6]:
            key = re.sub(r"\s+", "", title)[:12]
            actual = None
            for i, p in enumerate(pages_txt, 1):
                if i in toc_pages:
                    continue
                if key and key in re.sub(r"\s+", "", p):
                    actual = i
                    break
            if actual is not None and actual != page:
                wrong.append((title[:14], page, actual))
        check("目录页码与正文一致（抽查前 6 条）", not wrong,
              ("错位：%s" % wrong[:3]) if wrong else "全部一致（已排除目录页 %s）"
              % sorted(toc_pages))
    else:
        print("  · 这份文档没有可解析的目录行，跳过")

    print("\n" + "=" * 70)
    print(" 结果：%d 通过 / %d 失败" % (_ok, _fail))
    for b in bad:
        print("   - " + b)
    print("=" * 70)
    return 0 if _fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
