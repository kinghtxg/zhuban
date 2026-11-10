# -*- coding: utf-8 -*-
"""Windows 便携版验收：冒烟 + 各种输入 + 格式不变。

跑法（服务已在 18100 起来）：
    python deploy/portable-win/verify/portable-win-accept.py \
        --base http://127.0.0.1:18100 \
        --data <解压目录>/app/data

`--data` 是便携版的 app/data，脚本从里面的 config.json 读管理员口令
（口令是随机生成的，所以不能硬编码）。
模板样例默认取归档里的 03-文档资料，可用 ZB_PENTEST_DIR 覆盖。
"""
from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import sys
import urllib.error
import urllib.request
import uuid
import zipfile

_HERE = os.path.dirname(os.path.abspath(__file__))
# verify -> portable-win -> deploy -> docforge -> 01-源代码 -> 铸版-ZHUBAN
_ARCHIVE = os.path.abspath(os.path.join(_HERE, "..", "..", "..", "..", ".."))
TEMPLATE_DIR = os.environ.get("ZB_PENTEST_DIR") or os.path.join(
    _ARCHIVE, "03-文档资料", "模板样例-渗透测试报告")
TPL = os.path.join(TEMPLATE_DIR, "渗透测试报告-模板.docx")
FULL = os.path.join(TEMPLATE_DIR, "data-full.json")
SHOTS = os.path.join(TEMPLATE_DIR, "data-with-shots.json")

_ok = 0
_fail = 0
_skip = 0
_failures: list[str] = []


def check(name: str, cond: bool, detail: str = "") -> bool:
    global _ok, _fail
    if cond:
        _ok += 1
        print("  √ %s%s" % (name, ("  " + detail) if detail else ""))
    else:
        _fail += 1
        _failures.append("%s  %s" % (name, detail))
        print("  × %s%s" % (name, ("  " + detail) if detail else ""))
    return cond


class Client:
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.cj = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cj))

    def raw(self, method: str, path: str, body: bytes | None = None,
            ctype: str | None = None, timeout: int = 180):
        url = self.base + path
        r = urllib.request.Request(url, data=body, method=method)
        if ctype:
            r.add_header("Content-Type", ctype)
        try:
            with self.op.open(r, timeout=timeout) as resp:
                return resp.status, resp.read(), dict(resp.headers)
        except urllib.error.HTTPError as e:
            return e.code, e.read(), dict(e.headers)

    def json(self, method: str, path: str, obj=None, timeout: int = 180):
        body = None
        ctype = None
        if obj is not None:
            body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
            ctype = "application/json"
        st, data, _h = self.raw(method, path, body, ctype, timeout)
        try:
            return st, json.loads(data.decode("utf-8") or "{}")
        except ValueError:
            return st, {"_raw": data[:400].decode("utf-8", "replace")}

    def multipart(self, path: str, fields: dict, files: dict, timeout: int = 300):
        b = "----zb" + uuid.uuid4().hex
        out = []
        for k, v in fields.items():
            out.append(("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n"
                        % (b, k, v)).encode("utf-8"))
        for k, (fn, content, ct) in files.items():
            out.append(("--%s\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n"
                        "Content-Type: %s\r\n\r\n" % (b, k, fn, ct)).encode("utf-8"))
            out.append(content)
            out.append(b"\r\n")
        out.append(("--%s--\r\n" % b).encode("utf-8"))
        body = b"".join(out)
        return self.raw("POST", path, body, "multipart/form-data; boundary=" + b, timeout)


# --------------------------------------------------------------------------- 工具
def load_json(p: str) -> dict:
    with open(p, encoding="utf-8") as f:
        return json.load(f)


def deep(d: dict) -> dict:
    return json.loads(json.dumps(d, ensure_ascii=False))


def docx_parts(blob: bytes) -> dict[str, bytes]:
    import io
    out = {}
    with zipfile.ZipFile(io.BytesIO(blob)) as z:
        for n in z.namelist():
            out[n] = z.read(n)
    return out


def para_props(document_xml: bytes) -> list[tuple]:
    """每段的「版式签名」：(pPr 签名, 每个 run 的字体字号, 段落文字)。

    为什么要带文字：docxtpl 会把 `{%p for %}` / `{%p endfor %}` 这种**控制段
    整段删掉**，那是它的正常行为，不是「版式丢失」。断言必须能把这类段落
    认出来，否则每次都是一堆假红——假报多了，真报警就没人看了。
    """
    from lxml import etree
    W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
    ns = {"w": W}
    root = etree.fromstring(document_xml)
    sigs = []
    for p in root.iter("{%s}p" % W):
        pPr = p.find("w:pPr", ns)
        bits = []
        if pPr is not None:
            for tag in ("pStyle", "jc", "ind", "spacing", "outlineLvl"):
                e = pPr.find("w:" + tag, ns)
                if e is not None:
                    # 只留属性值，不带命名空间前缀（否则比的是前缀而不是值）
                    attrs = tuple(sorted((k.split("}")[-1], v) for k, v in e.attrib.items()))
                    bits.append((tag, attrs))
        runs = []
        for r in p.iter("{%s}r" % W):
            rPr = r.find("w:rPr", ns)
            got = {}
            if rPr is not None:
                for tag in ("sz", "rFonts", "b", "i"):
                    e = rPr.find("w:" + tag, ns)
                    if e is not None:
                        got[tag] = tuple(sorted((k.split("}")[-1], v)
                                                for k, v in e.attrib.items()))
            if got:
                runs.append(tuple(sorted(got.items())))
        text = "".join(t.text or "" for t in p.iter("{%s}t" % W))
        sigs.append((tuple(bits), tuple(runs), text))
    return sigs


def count_placeholders(parts: dict[str, bytes]) -> int:
    n = 0
    for name, body in parts.items():
        if name.startswith("word/") and name.endswith(".xml"):
            t = body.decode("utf-8", "replace")
            n += t.count("{{") + t.count("{%")
    return n


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:18100")
    ap.add_argument("--data", required=True, help="便携版 app/data 目录")
    a = ap.parse_args()

    cfg = load_json(os.path.join(a.data, "config.json"))
    c = Client(a.base)

    print("=" * 70)
    print(" 铸版 Windows 便携版 验收")
    print("=" * 70)

    # ---------------------------------------------------------------- 1 登录
    print("\n[1] 登录")
    st, body = c.json("POST", "/api/v1/auth/login",
                      {"username": cfg["admin_user"], "password": cfg["admin_password"]})
    check("登录返回 200", st == 200, "got %s" % st)
    if st != 200:
        print(json.dumps(body, ensure_ascii=False)[:300])
        return 1

    st, ver = c.json("GET", "/api/v1/version")
    check("版本信息", st == 200 and ver.get("version") == "3.4.23",
          "%s / %s" % (ver.get("version"), ver.get("commit")))
    import re as _re
    check("提交号非空且是短 hash",
          bool(_re.fullmatch(r"[0-9a-f]{7,40}", str(ver.get("commit") or ""))),
          str(ver.get("commit")))
    check("不是 dev 构建", not ver.get("dev"), "dev=%s" % ver.get("dev"))

    # ---------------------------------------------------------------- 2 上传模板
    print("\n[2] 上传渗透测试报告模板")
    raw = open(TPL, "rb").read()
    st, up_raw, _h = c.multipart("/api/v1/templates",
                                 {"code": "pentest-acc", "name": "渗透测试报告（验收）",
                                  "category": "安全服务", "description": "便携版验收用"},
                                 {"file": ("渗透测试报告-模板.docx", raw,
                                           "application/vnd.openxmlformats-officedocument.wordprocessingml.document")})
    try:
        up = json.loads(up_raw.decode("utf-8") or "{}")
    except ValueError:
        up = {"_raw": up_raw[:300].decode("utf-8", "replace")}
    if st == 409:
        # 已经传过：找出来复用
        st2, lst = c.json("GET", "/api/v1/templates?q=pentest-acc&page=1&page_size=5")
        tid = None
        for it in (lst.get("items") or []):
            if it.get("code") == "pentest-acc":
                tid = it.get("id")
                break
        check("模板已存在，复用", bool(tid), str(tid))
    else:
        check("上传返回 201", st == 201, "got %s %s" % (st, str(up)[:200]))
        tid = (up.get("id") or (up.get("template") or {}).get("id"))
    if not tid:
        print("  无法拿到 template_id，终止")
        return 1

    st, det = c.json("GET", "/api/v1/templates/" + tid)
    cv = det.get("current_version") or {}
    vid = cv.get("id")
    variables = cv.get("variables") or []
    check("拿到当前版本", bool(vid), str(vid))
    check("变量数量 > 10", len(variables) > 10, "%d 个" % len(variables))

    full = load_json(FULL)
    shots = load_json(SHOTS)
    print("  数据：%d 标量 + %d 条漏洞（含截图版 %d 条）"
          % (len([k for k in full if not isinstance(full[k], list)]),
             len(full.get("漏洞列表") or []), len(shots.get("漏洞列表") or [])))

    # ---------------------------------------------------------------- 3 各种输入
    print("\n[3] 各种输入：校验")

    def validate(name: str, data: dict, expect_ok: bool, note: str = ""):
        st, r = c.json("POST", "/api/v1/validate",
                       {"template_id": tid, "version_id": vid, "data": data})
        valid = bool(r.get("valid"))
        errs = r.get("errors") or []
        check(name, st == 200 and valid == expect_ok,
              "%s valid=%s errors=%d %s" % (st, valid, len(errs), note))
        return r

    validate("完整数据（4 条漏洞，无截图）", full, True)
    validate("含截图数据（2 张图）", shots, True)

    empty_loops = deep(full)
    empty_loops["漏洞列表"] = []
    validate("零条漏洞", empty_loops, True)

    one = deep(full)
    one["漏洞列表"] = [full["漏洞列表"][0]]
    validate("一条漏洞", one, True)

    seven = deep(full)
    seven["漏洞列表"] = [deep(full["漏洞列表"][i % 4]) for i in range(7)]
    validate("七条漏洞", seven, True)

    # 本轮修复：嵌套循环漏字段必须报错（以前会说 valid，然后静默丢图）
    missing_shot = deep(full)
    for it in missing_shot["漏洞列表"]:
        it.pop("截图", None)
    r = validate("嵌套循环缺「截图」键 → 应报错", missing_shot, False)
    check("报错路径指向嵌套层",
          any("截图" in (e.get("path") or "") for e in (r.get("errors") or [])),
          str((r.get("errors") or [])[:2])[:160])

    # 截图键名漏 image_ 前缀：渲染宽容，但校验必须提示
    bad_prefix = deep(full)
    bad_prefix["漏洞列表"][0]["截图"] = [{"截图": {"type": "image", "src": ""}}]
    st, r = c.json("POST", "/api/v1/validate",
                   {"template_id": tid, "version_id": vid, "data": bad_prefix})
    check("截图键名漏 image_ 前缀被识别", (not r.get("valid")) or bool(r.get("warnings")),
          "valid=%s warn=%s err=%s" % (r.get("valid"), len(r.get("warnings") or []),
                                       len(r.get("errors") or [])))

    # 标量边界
    for label, val, ok in [
        ("空串", "", True),
        ("39 字长标题", "尧天灾备云安全等级保护测评与渗透测试综合服务项目二期", True),
        ("含 & 与 <", "A&B <script>x</script> 1<2>0", True),
        ("含引号", "他说\"这是'引用'\"", True),
        ("emoji 与生僻字", "𠮷野家 🚀 ㈱", True),
    ]:
        d = deep(full)
        d["项目名称"] = val
        validate("标量 %s" % label, d, ok)

    # ---------------------------------------------------------------- 4 渲染
    print("\n[4] 渲染 docx")

    def render(name: str, data: dict, fmt: str = "docx", expect: int = 200):
        """走 /api/v1/render（同步单份）→ 拿 documents[0] 的下载链接 → 取字节。"""
        import time
        t0 = time.time()
        st, r = c.json("POST", "/api/v1/render",
                       {"template_id": tid, "version_id": vid,
                        "data": data, "format": fmt}, timeout=300)
        if st == 202:
            # 同步超时转异步：轮询到终结
            jid = r.get("job_id")
            for _ in range(60):
                time.sleep(1)
                st2, j = c.json("GET", "/api/v1/jobs/" + jid)
                if j.get("status") in ("succeeded", "failed"):
                    r, st = j, st2
                    break
        doc = (r.get("documents") or [{}])[0]
        # 纯 pdf 任务没有 docx，download_url 本身就指向 pdf（见 CONTRACT §4.4）
        url = doc.get("pdf_download_url") or doc.get("download_url") if fmt == "pdf" \
            else doc.get("download_url")
        if st != 200 or not url:
            check("%s → %d" % (name, expect), False,
                  "%s %s" % (st, json.dumps(r, ensure_ascii=False)[:200]))
            return st, b""
        st2, blob, _ = c.raw("GET", url, timeout=300)
        check("%s → %d" % (name, expect), st2 == expect,
              "%s %d 字节 %.1fs" % (st2, len(blob), time.time() - t0))
        return st2, blob

    st, out_full = render("完整数据 docx", full)
    if st != 200:
        print(out_full[:400].decode("utf-8", "replace"))
        return 1
    parts = docx_parts(out_full)
    check("docx 是 zip（PK）", out_full[:2] == b"PK", str(out_full[:4]))
    check("无残留占位符", count_placeholders(parts) == 0,
          "剩 %d 处" % count_placeholders(parts))

    import re
    from lxml import etree
    WX = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"
    doc_xml = parts["word/document.xml"].decode("utf-8", "replace")

    def all_text(p: dict[str, bytes]) -> str:
        """全文（正文 + 页眉页脚 + 脚注……）。

        只查 document.xml 是错的：这个模板的「报告日期」写在 footer1.xml 里，
        按正文查会得出「日期没渲染出来」的假结论——而它其实好好地印在页脚上。
        """
        out = []
        for name, body in sorted(p.items()):
            if name.startswith("word/") and name.endswith(".xml"):
                try:
                    root = etree.fromstring(body)
                except Exception:
                    continue
                for para in root.iter(WX + "p"):
                    out.append("".join(t.text or "" for t in para.iter(WX + "t")))
        return "\n".join(out)

    whole = all_text(parts)
    for want in ("尧天灾备云", "2026年11月", "https://URLXXX/login?redirect=/index"):
        check("文档含 %r" % want, want in whole,
              "" if want in whole else "（含页眉页脚全文共 %d 字）" % len(whole))

    print("\n[5] 特殊字符不被吃掉/不破坏文档")
    d = deep(full)
    d["项目名称"] = "A&B <script>alert(1)</script> 1<2>0 \"引号\" '单引号'"
    st, blob = render("含 & < > 引号", d)
    if st == 200:
        x = docx_parts(blob)["word/document.xml"].decode("utf-8", "replace")
        # XML 里应当是转义后的实体；解出来才是原文
        check("& 被转义为 &amp;", "&amp;" in x)
        check("< 被转义为 &lt;", "&lt;script&gt;" in x)
        check("没有真的 <script> 元素", "<script>" not in x)
        from lxml import etree
        try:
            etree.fromstring(docx_parts(blob)["word/document.xml"])
            check("document.xml 仍是合法 XML", True)
        except Exception as e:
            check("document.xml 仍是合法 XML", False, str(e)[:120])
        # 文本层应原样保留
        import io
        W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"
        root = etree.fromstring(docx_parts(blob)["word/document.xml"])
        texts = "".join(t.text or "" for t in root.iter(W + "t"))
        check("文本原样保留 A&B", "A&B" in texts)
        check("文本原样保留 <script>", "<script>alert(1)</script>" in texts)
        check("文本原样保留 1<2>0", "1<2>0" in texts)

    print("\n[6] 截图输入")
    st, blob_shots = render("含 2 张截图 docx", shots)
    if st == 200:
        p2 = docx_parts(blob_shots)
        media = [n for n in p2 if n.startswith("word/media/")]
        check("截图进了 media 目录", len(media) >= 1, "%d 个：%s" % (len(media), media[:3]))
        check("drawing 元素存在", b"<w:drawing>" in p2["word/document.xml"])

    st, pdf_shots = render("含截图 PDF", shots, fmt="pdf")
    check("PDF 魔数 %PDF", pdf_shots[:4] == b"%PDF" if st == 200 else False,
          str(pdf_shots[:8]))

    print("\n[7] 循环边界")
    for n, data in (("0 条", empty_loops), ("1 条", one), ("7 条", seven)):
        st, blob = render("%s漏洞 docx" % n, data)
        if st == 200:
            check("%s 无残留占位符" % n, count_placeholders(docx_parts(blob)) == 0)

    print("\n[8] 格式不变性（docx 层）")
    tpl_parts = docx_parts(raw)
    sig_tpl = para_props(tpl_parts["word/document.xml"])
    sig_out = para_props(parts["word/document.xml"])

    # 控制段（{%p for %} / {%p endfor %}/ 图片占位段）会被 docxtpl 消费掉，
    # 它们不进比对；其余段落的「样式+对齐+缩进+间距」必须原样存在。
    def is_ctrl(sig) -> bool:
        return "{%" in sig[2] or "image_" in sig[2]

    ppr_tpl = {s[0] for s in sig_tpl if not is_ctrl(s)}
    ppr_out = {s[0] for s in sig_out}
    new = ppr_out - ppr_tpl
    check("没有出现模板里不存在的段落版式", not new,
          "%d 种新增：%s" % (len(new), str(sorted(new)[:1])[:200] if new else ""))
    lost = ppr_tpl - ppr_out
    check("固定段落版式未丢失", not lost, "%d 种丢失" % len(lost))
    if lost:
        for i, s in enumerate(sorted(lost, key=lambda x: -len(x))[:5]):
            txts = [t[2][:50] for t in sig_tpl if t[0] == s][:2]
            print("      丢失#%d: %s  模板文字=%s" % (i + 1, s, txts))

    # 字体字号：模板里出现过的每个 (字体, 字号, 粗, 斜) 组合，输出里都得还在。
    # 这条比「逐段比」更抗干扰：变量值一换，run 的切分位置就变了，逐段比必然假红。
    runs_tpl = {r for s in sig_tpl if not is_ctrl(s) for r in s[1]}
    runs_out = {r for s in sig_out for r in s[1]}
    lost_runs = runs_tpl - runs_out
    check("字体字号组合未丢失", not lost_runs,
          "%d 种丢失：%s" % (len(lost_runs), str(sorted(lost_runs)[:2])[:240] if lost_runs else ""))
    extra_runs = runs_out - runs_tpl
    check("没有引入模板外的字体字号", not extra_runs,
          "%d 种新增：%s" % (len(extra_runs), str(sorted(extra_runs)[:2])[:240] if extra_runs else ""))

    # 页面设置
    from lxml import etree
    W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"
    def sect(pxml: bytes):
        r = etree.fromstring(pxml)
        out = []
        for s in r.iter(W + "sectPr"):
            out.append(etree.tostring(s))
        return out
    check("分节/页面设置完全一致", sect(tpl_parts["word/document.xml"]) == sect(parts["word/document.xml"]),
          "模板 %d 节 / 输出 %d 节" % (len(sect(tpl_parts["word/document.xml"])),
                                   len(sect(parts["word/document.xml"]))))

    print("\n" + "=" * 70)
    print(" 结果：%d 通过 / %d 失败" % (_ok, _fail))
    if _failures:
        print(" 失败项：")
        for f in _failures:
            print("   - " + f)
    print("=" * 70)
    return 0 if _fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
