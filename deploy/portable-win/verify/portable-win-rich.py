# -*- coding: utf-8 -*-
"""富文本 + 内联截图 专项验收。

为什么单列一项：模板里的 `rich_` 字段在界面上是 contenteditable，用户把
截图直接 Ctrl+V 粘进去，服务端收到的是**一段 HTML**（`<img src="data:...">`
夹在 `<b>`、`<ul>` 之间），走的是 `html_to_richtext` → `InlineImage` 那条路，
和「JSON 里直接给 {"type":"image","src":...}」完全不是一条路径。
3.4.20 上线时踩过：API 直填数据怎么都通，只有真去界面上点「富文本 + 本地
选图」再生成才 422。所以验收必须在 HTML 这条路上走一遍。
"""
from __future__ import annotations

import http.cookiejar
import io
import json
import os
import sys
import urllib.error
import urllib.request
import uuid
import zipfile

BASE = os.environ.get("ZB_BASE", "http://127.0.0.1:18100")
DATA = os.environ.get("ZB_DATA") or r"C:\tmp\zb-test2\zhuban-3.4.23-windows-x64\app\data"
_HERE = os.path.dirname(os.path.abspath(__file__))
# verify -> portable-win -> deploy -> docforge
SAMPLE = os.path.join(os.path.abspath(os.path.join(_HERE, "..", "..", "..")),
                      "samples", "demo-report.sample.json")

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


class C:
    def __init__(self):
        self.cj = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cj))

    def req(self, method, path, obj=None, timeout=300):
        body = None
        ct = None
        if obj is not None:
            body = json.dumps(obj, ensure_ascii=False).encode()
            ct = "application/json"
        r = urllib.request.Request(BASE + path, data=body, method=method)
        if ct:
            r.add_header("Content-Type", ct)
        try:
            with self.op.open(r, timeout=timeout) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()


def parts_of(blob: bytes) -> dict[str, bytes]:
    with zipfile.ZipFile(io.BytesIO(blob)) as z:
        return {n: z.read(n) for n in z.namelist()}


def main() -> int:
    cfg = json.load(open(os.path.join(DATA, "config.json"), encoding="utf-8"))
    c = C()
    st, _ = c.req("POST", "/api/v1/auth/login",
                  {"username": cfg["admin_user"], "password": cfg["admin_password"]})
    if st != 200:
        print("登录失败", st)
        return 1

    # 内置 demo-report 模板
    st, b = c.req("GET", "/api/v1/templates/code/demo-report")
    if st != 200:
        print("取 demo-report 失败", st, b[:200])
        return 1
    tpl = json.loads(b)
    tid = tpl["id"]
    vid = (tpl.get("current_version") or {}).get("id")

    data = json.load(open(SAMPLE, encoding="utf-8"))

    # 一张 1x1 的 PNG（真正走 data URI 解析，不是空串）
    png = ("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8"
           "z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
    html = (
        "<p><b>加粗 R&amp;D</b> 普通文字 1&lt;2&gt;0</p>"
        "<p><script>alert(1)</script>脚本应当被整段丢弃</p>"
        "<ul><li>列表项一</li><li>列表项二 &amp; 更多</li></ul>"
        "<p>下面是一张截图：</p>"
        '<p><img src="data:image/png;base64,%s" /></p>' % png
    )
    data["rich_摘要"] = {"type": "html", "content": html}

    print("=" * 70)
    print(" 富文本 + 内联截图 验收（demo-report）")
    print("=" * 70)

    print("\n[1] 渲染 docx")
    st, body = c.req("POST", "/api/v1/render",
                     {"template_id": tid, "version_id": vid, "data": data, "format": "docx"})
    if st != 200:
        check("渲染成功", False, "%s %s" % (st, body[:300].decode("utf-8", "replace")))
        return 1
    j = json.loads(body)
    doc = (j.get("documents") or [{}])[0]
    url = doc.get("download_url")
    st2, blob = c.req("GET", url)
    check("拿到 docx", st2 == 200 and blob[:2] == b"PK", "%d 字节" % len(blob))

    p = parts_of(blob)
    x = p["word/document.xml"].decode("utf-8", "replace")

    # 文本层
    from lxml import etree
    W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"
    root = etree.fromstring(p["word/document.xml"])
    text = "\n".join("".join(t.text or "" for t in para.iter(W + "t"))
                     for para in root.iter(W + "p"))

    print("\n[2] 富文本内容")
    check("加粗文字保留", "加粗" in text)
    check("R&D 没被吞成 RD", "R&D" in text, "（BS4 吞裸 & 的回归）")
    check("1<2>0 原样保留", "1<2>0" in text)
    check("<script> 内容被整段丢弃（不是只剥标签）", "alert(1)" not in text,
          "" if "alert(1)" not in text else "内容泄漏成正文：%r" % text[text.find("alert"):][:40])
    check("列表两项都在", "列表项一" in text and "列表项二" in text)
    check("列表里的 & 保留", "列表项二 & 更多" in text)

    print("\n[3] 加粗是真的（rPr 里有 b）")
    bold_runs = 0
    for r in root.iter(W + "r"):
        rPr = r.find(W + "rPr")
        if rPr is None:
            continue
        if rPr.find(W + "b") is not None:
            t = "".join(t.text or "" for t in r.iter(W + "t"))
            if "加粗" in t:
                bold_runs += 1
    check("「加粗」所在 run 带 b 标记", bold_runs >= 1)

    print("\n[4] 内联截图")
    media = [n for n in p if n.startswith("word/media/")]
    check("word/media 有图片部件", len(media) >= 1, "%d 个" % len(media))
    check("document.xml 里有 drawing", "<w:drawing>" in x)
    check("图片没把段落样式撑坏（没有裸 <w:t> 里的 img）", "img src" not in x)

    print("\n[5] 二维码与既有图片字段仍在")
    # demo 模板本来就有 banner/sign/seal 三张图 + 一个二维码
    check("图片总数 >= 4（3 张模板图 + 富文本里 1 张）", len(media) >= 4, "%d 个" % len(media))

    print("\n[6] 导出 PDF")
    st, body = c.req("POST", "/api/v1/render",
                     {"template_id": tid, "version_id": vid, "data": data, "format": "pdf"})
    j = json.loads(body)
    jid = j.get("job_id")
    if st == 202:
        import time
        for _ in range(60):
            time.sleep(2)
            _, b2 = c.req("GET", "/api/v1/jobs/" + jid)
            j = json.loads(b2)
            if j.get("status") in ("succeeded", "failed"):
                break
    doc = (j.get("documents") or [{}])[0]
    check("PDF 任务成功", j.get("status") == "succeeded",
          "%s %s" % (j.get("status"), doc.get("error", "")))
    if j.get("status") == "succeeded":
        url = doc.get("pdf_download_url") or doc.get("download_url")
        st2, pdf = c.req("GET", url)
        check("PDF 魔数", st2 == 200 and pdf[:4] == b"%PDF", "%d 字节" % len(pdf))

    print("\n" + "=" * 70)
    print(" 结果：%d 通过 / %d 失败" % (_ok, _fail))
    for b in bad:
        print("   - " + b)
    print("=" * 70)
    return 0 if _fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
