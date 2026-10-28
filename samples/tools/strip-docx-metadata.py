#!/usr/bin/env python3
"""剥离 .docx / .xlsx 里的个人与设备信息（docProps 三件套）。

为什么需要这个：Office 文件不是"纯内容"，它在 `docProps/` 里存了一整套履历——
谁创建的、谁最后改的、用什么软件、以及 WPS 特有的设备与账号标识：

    core.xml    lastModifiedBy = 使用者的昵称/网名
    app.xml     Application    = "WPS Office_12.1.0.28505_F1E327BC-269C-435d-...",
                                尾部那串 GUID 是这台机器的 WPS 安装实例 ID
    custom.xml  一串 base64，解出来是 {"hdid":"...","userId":"..."}
                                hdid = 设备指纹，userId = WPS 账号 ID

这些东西肉眼看不见（要解压才看得到），所以会一路跟着仓库发到公开平台。
`deploy/publish-public.sh` 的文本扫描按设计跳过了 *.docx（二进制没法 grep），
于是这层必须单独兜住。

用法：
    python samples/tools/strip-docx-metadata.py                 # 清理 samples/ 下所有 Office 文件
    python samples/tools/strip-docx-metadata.py a.docx b.xlsx   # 清理指定文件
    python samples/tools/strip-docx-metadata.py --check         # 只报告，不改

清理策略是「重写这三个 XML」而不是删文件：删掉 docProps/custom.xml 会让
[Content_Types].xml 与 _rels/.rels 里的引用悬空，Word 打开时报文档损坏。
保留空的自定义属性容器是合法的。
"""

import sys
import os
import zipfile
import shutil
import tempfile
import glob

CORE_XML = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" \
xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" \
xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">\
<dc:title>铸版示例模板</dc:title>\
<dc:creator>zhuban</dc:creator>\
<cp:lastModifiedBy>zhuban</cp:lastModifiedBy>\
<cp:revision>1</cp:revision>\
</cp:coreProperties>"""

APP_XML = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" \
xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">\
<Application>Microsoft Office Word</Application>\
<DocSecurity>0</DocSecurity>\
<ScaleCrop>false</ScaleCrop>\
<LinksUpToDate>false</LinksUpToDate>\
<SharedDoc>false</SharedDoc>\
<HyperlinksChanged>false</HyperlinksChanged>\
<AppVersion>16.0000</AppVersion>\
</Properties>"""

CUSTOM_XML = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" \
xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"/>"""

REPLACEMENTS = {
    "docProps/core.xml": CORE_XML,
    "docProps/app.xml": APP_XML,
    "docProps/custom.xml": CUSTOM_XML,
}

# 报告用：这些串出现在 docProps 里就值得说一声
SUSPECT_MARKERS = ("WPS", "取名", "hdid", "userId", "Kingsoft")


def inspect(path):
    """返回 (是否含可疑信息, 可疑片段列表)。"""
    found = []
    try:
        with zipfile.ZipFile(path) as z:
            for name in REPLACEMENTS:
                if name in z.namelist():
                    raw = z.read(name).decode("utf-8", "replace")
                    for marker in SUSPECT_MARKERS:
                        if marker in raw:
                            found.append("%s 含 %r" % (name, marker))
    except zipfile.BadZipFile:
        return None, ["不是合法的 zip / Office 文件"]
    return bool(found), found


def strip(path):
    """重写 docProps 三件套，其余条目逐字节保持原样。"""
    tmp_fd, tmp_path = tempfile.mkstemp(suffix=".tmp")
    os.close(tmp_fd)
    try:
        with zipfile.ZipFile(path) as src, \
                zipfile.ZipFile(tmp_path, "w", zipfile.ZIP_DEFLATED) as dst:
            for item in src.infolist():
                data = src.read(item.filename)
                if item.filename in REPLACEMENTS:
                    data = REPLACEMENTS[item.filename].encode("utf-8")
                # 保持压缩方式与时间戳，尽量让文件只在这几个节点上有差异
                dst.writestr(item, data)
        shutil.move(tmp_path, path)
    finally:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)


def main(argv):
    check_only = "--check" in argv
    args = [a for a in argv if not a.startswith("--")]

    if args:
        targets = args
    else:
        # 默认扫全仓库，不是只扫 samples/：测试 fixture 里也藏着同样的模板副本
        # （render-service/tests/fixtures/original-template.docx），那里一样带着
        # 创建者昵称和 WPS 账号 ID。只盯着 samples/ 会漏。
        targets = []
        for pat in ("**/*.docx", "**/*.xlsx", "**/*.pptx"):
            targets.extend(glob.glob(pat, recursive=True))
        targets = sorted(set(
            t for t in targets
            if "node_modules" not in t.replace("\\", "/")
            and "/.git/" not in t.replace("\\", "/")
            and not t.replace("\\", "/").startswith(".git/")
        ))

    if not targets:
        print("没有找到待处理的 Office 文件")
        return 0

    dirty = 0
    for path in targets:
        suspect, found = inspect(path)
        if suspect is None:
            print("  ✗ %s：%s" % (path, found[0]))
            continue
        if not suspect:
            print("  ✓ %-46s 干净" % path)
            continue

        dirty += 1
        print("  ! %-46s 含 %d 处痕迹" % (path, len(found)))
        for f in found:
            print("      - %s" % f)
        if not check_only:
            strip(path)
            still, found2 = inspect(path)
            if still:
                print("      ✗ 清理后仍有残留：%s" % found2)
                return 1
            print("      → 已剥离")

    print()
    if check_only:
        print("只检查，未改动。发现 %d 个文件需要清理。" % dirty)
        return 1 if dirty else 0
    print("完成：%d 个文件被清理。" % dirty)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
