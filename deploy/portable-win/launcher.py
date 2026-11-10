# -*- coding: utf-8 -*-
"""铸版 ZHUBAN · Windows 便携版启动器。

双击「启动铸版.bat」后由本脚本接手，用户不需要装 Go / Python / LibreOffice / 数据库：

  1. 准备数据目录，首次运行生成端口、密钥与管理员口令（存 data/config.json）
  2. 把随包的 59 个公文字体注册到当前用户（HKCU，免管理员，见 winfonts 说明）
  3. 拉起 render-service（uvicorn + LibreOffice + poppler）
  4. 等两个服务都健康后拉起主程序，再打开浏览器
  5. Ctrl+C 或关闭窗口时把两个子进程一起收掉

不用 .env 文件：主程序只读环境变量，配置直接以子进程环境的形式传进去，
少一个「改了 .env 没生效」的坑。
"""
from __future__ import annotations

import json
import os
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
import webbrowser

APP = os.path.dirname(os.path.abspath(__file__))
PKG = os.path.dirname(APP)
DATA = os.path.join(APP, "data")
LOGS = os.path.join(DATA, "logs")
TMP = os.path.join(DATA, "tmp")
UPLOAD_FONTS = os.path.join(DATA, "fonts")
BUNDLE_FONTS = os.path.join(APP, "fonts")
CONFIG = os.path.join(DATA, "config.json")

PYTHON = os.path.join(APP, "python", "python.exe")
ZHUBAN = os.path.join(APP, "zhuban.exe")
RENDER_DIR = os.path.join(APP, "render")
SOFFICE = os.path.join(APP, "lo", "program", "soffice.exe")
PDFTOPPM = os.path.join(APP, "poppler", "pdftoppm.exe")
# pdftotext 用于「目录页码两遍排版」：第一遍排完量出每个标题落在第几页，
# 回填后再排一遍。缺了它 PDF 照样能出，但目录里的页码会停在模板作者留下的
# 旧值上——而且渲染服务是**静默降级**（toc.measure_text 失败就 break），
# 用户只会看到「目录页码不对」，不会看到任何报错。所以这里显式传路径，
# 并在启动时检查，缺了要吭声。
PDFTOTEXT = os.path.join(APP, "poppler", "pdftotext.exe")

DEFAULTS = {
    "http_port": 18100,
    "render_port": 18101,
    "lo_concurrency": 2,
    "admin_user": "admin",
    "admin_password": "",
    "auth_secret": "",
    "signed_url_secret": "",
}

_children: list[tuple[str, subprocess.Popen]] = []


# --------------------------------------------------------------------------- 输出
def say(msg: str = "") -> None:
    print(msg, flush=True)


def _utf8_console() -> None:
    try:
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass


def die(msg: str) -> None:
    say("")
    say("[×] " + msg)
    say("")
    # 不在这里等回车：外层 启动铸版.bat 有 pause，等服务方式跑时也不该挂住
    raise SystemExit(1)


# --------------------------------------------------------------------------- 基础
def ensure_dirs() -> None:
    for d in (DATA, LOGS, TMP, UPLOAD_FONTS):
        os.makedirs(d, exist_ok=True)


def load_config() -> dict:
    cfg = dict(DEFAULTS)
    if os.path.exists(CONFIG):
        try:
            with open(CONFIG, "r", encoding="utf-8") as f:
                cfg.update(json.load(f) or {})
        except (OSError, ValueError):
            say("  ! data/config.json 读不出来，按默认值重新生成")
    changed = False
    if not cfg.get("auth_secret"):
        cfg["auth_secret"] = secrets.token_hex(32)
        changed = True
    if not cfg.get("signed_url_secret"):
        cfg["signed_url_secret"] = secrets.token_hex(32)
        changed = True
    if not cfg.get("admin_password"):
        # 8 位起、字母+数字（ValidatePasswordRule）。12 位足够，且好抄。
        cfg["admin_password"] = "Zhuban" + "".join(secrets.choice("0123456789") for _ in range(6))
        changed = True
    if changed:
        save_config(cfg)
    return cfg


def save_config(cfg: dict) -> None:
    os.makedirs(DATA, exist_ok=True)
    tmp = CONFIG + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    os.replace(tmp, CONFIG)


def port_free(port: int) -> bool:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("127.0.0.1", port))
        return True
    except OSError:
        return False
    finally:
        s.close()


def pick_port(preferred: int, taken: set) -> int:
    for p in range(preferred, preferred + 30):
        if p not in taken and port_free(p):
            return p
    raise RuntimeError("端口 %d~%d 都被占用了" % (preferred, preferred + 30))


def http_ok(url: str, timeout: float = 1.5) -> bool:
    # 必须显式禁用代理：企业电脑上常设 HTTP_PROXY/HTTPS_PROXY，urllib 会连
    # 127.0.0.1 也往代理送，于是「本机服务明明起来了却报未就绪」，排查半天
    # 发现是本机代理返回 502。空 ProxyHandler 就是「本机直连」。
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(url, timeout=timeout) as resp:
            return 200 <= resp.status < 400
    except (urllib.error.URLError, OSError, ValueError):
        return False


def wait_http(name: str, url: str, proc: subprocess.Popen, log: str, seconds: int) -> None:
    started = time.time()
    last = 0
    while time.time() - started < seconds:
        if proc.poll() is not None:
            say("")
            say("[×] %s 启动后立刻退出（退出码 %s），日志末尾：" % (name, proc.returncode))
            for line in tail(log, 25):
                say("    " + line)
            die("%s 没能启动" % name)
        if http_ok(url):
            say("  √ %s 就绪（%.1fs）" % (name, time.time() - started))
            return
        waited = int(time.time() - started)
        if waited and waited % 10 == 0 and waited != last:
            last = waited
            say("    …%s 启动中，已等 %ds" % (name, waited))
        time.sleep(0.5)
    say("")
    say("[×] %s 超过 %ds 仍未就绪，日志末尾：" % (name, seconds))
    for line in tail(log, 25):
        say("    " + line)
    die("%s 启动超时" % name)


def tail(path: str, n: int) -> list[str]:
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            return [ln.rstrip("\n") for ln in f.readlines()[-n:]]
    except OSError:
        return ["（没有日志文件）"]


# --------------------------------------------------------------------------- 字体
def register_fonts() -> None:
    if sys.platform != "win32":
        return
    sys.path.insert(0, RENDER_DIR)
    try:
        from app import winfonts  # noqa: F401  仅取注册函数，不需要起服务
    except Exception as exc:
        say("  ! 字体注册模块加载失败，跳过：%s" % exc)
        return
    dirs = [d for d in (BUNDLE_FONTS, UPLOAD_FONTS) if os.path.isdir(d)]
    res = winfonts.register(dirs)
    added = res.get("added", 0)
    skipped = res.get("skipped", 0)
    system = res.get("system", 0)
    total = res.get("total", 0)
    if res.get("ok"):
        say("  √ 公文字体：共 %d 个，新注册 %d，已注册 %d，系统自带 %d"
            % (total, added, skipped, system))
    else:
        # 注册失败不拦着启动：非中文文档照样能渲染，只是公文字体会回退。
        say("  ! 公文字体注册未全部成功：%s" % (res.get("errors") or res.get("failed")))
        say("    文档仍可生成，但方正/GB2312 系列会回退成系统字体。")


# --------------------------------------------------------------------------- 子进程
def spawn(name: str, cmd: list[str], cwd: str, env: dict, log_path: str) -> subprocess.Popen:
    f = open(log_path, "w", encoding="utf-8", errors="replace")
    proc = subprocess.Popen(
        cmd,
        cwd=cwd,
        env=env,
        stdout=f,
        stderr=subprocess.STDOUT,
        stdin=subprocess.DEVNULL,
        creationflags=getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0),
    )
    _children.append((name, proc))
    return proc


def stop_all() -> None:
    for name, proc in _children:
        if proc.poll() is not None:
            continue
        try:
            proc.terminate()
        except OSError:
            continue
    deadline = time.time() + 6
    for name, proc in _children:
        while proc.poll() is None and time.time() < deadline:
            time.sleep(0.2)
    for name, proc in _children:
        if proc.poll() is None:
            # soffice.bin 之类的孙进程不跟着走，按进程树强杀
            subprocess.run(
                ["taskkill", "/F", "/T", "/PID", str(proc.pid)],
                capture_output=True,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )


# --------------------------------------------------------------------------- 主流程
def main() -> int:
    _utf8_console()
    say("=" * 68)
    say("  铸版 ZHUBAN · 公文模板渲染  (Windows 便携版)")
    say("=" * 68)
    say("")

    for path, what in ((PYTHON, "内嵌 Python"), (ZHUBAN, "主程序"), (SOFFICE, "LibreOffice")):
        if not os.path.exists(path):
            die("缺少%s：%s\n包不完整，请重新解压完整压缩包（不要只解压一部分）。"
                % (what, path))
    if not os.path.exists(PDFTOTEXT):
        # 不拦着启动：docx 导出与 PDF 出图都不受影响，只有目录页码不重算。
        # 但必须说出来——静默降级比报错难查一百倍。
        say("  ! 缺少 pdftotext.exe：PDF 里的目录页码不会重算（docx 正常）")

    ensure_dirs()
    cfg = load_config()

    say("[1/4] 注册公文字体")
    register_fonts()

    say("")
    say("[2/4] 分配端口")
    core_port = cfg["http_port"]
    if not port_free(core_port):
        if http_ok("http://127.0.0.1:%d/api/v1/healthz" % core_port):
            say("  · 铸版已经在运行了，直接把浏览器切过去")
            say("    要停止它，回到之前那个控制台窗口按 Ctrl+C。")
            webbrowser.open("http://127.0.0.1:%d/" % core_port)
            say("")
            try:
                input("按回车关闭本窗口（服务继续运行）…")
            except Exception:
                pass
            return 0
        core_port = pick_port(core_port + 1, set())
        say("  · %d 被别的程序占用，改用 %d" % (cfg["http_port"], core_port))
    render_port = cfg["render_port"]
    if not port_free(render_port):
        render_port = pick_port(render_port + 1, {core_port})
    say("  √ 主程序 %d，渲染服务 %d" % (core_port, render_port))

    say("")
    say("[3/4] 启动渲染服务（LibreOffice 出图，首次启动约 20 秒）")
    env = os.environ.copy()
    env["PATH"] = os.pathsep.join(
        [os.path.dirname(SOFFICE), os.path.dirname(PDFTOPPM), env.get("PATH", "")]
    )
    env["SOFFICE_PATH"] = SOFFICE
    env["PDFTOPPM_PATH"] = PDFTOPPM
    env["PDFTOTEXT_PATH"] = PDFTOTEXT
    env["PORT"] = str(render_port)
    env["TMP_DIR"] = TMP
    env["LO_CONCURRENCY"] = str(cfg.get("lo_concurrency") or 2)
    env["RENDER_TIMEOUT_SECONDS"] = "180"
    # Pillow 画签章用的字体目录
    env["DOCFORGE_FONT_DIR"] = os.path.join(BUNDLE_FONTS, "custom")
    # 上传字体后 /fonts/reload 要注册哪些目录（Windows 没有 fontconfig）
    env["DOCFORGE_WIN_FONT_DIRS"] = os.pathsep.join([BUNDLE_FONTS, UPLOAD_FONTS])
    # 「已导入」的判定边界：Go 把上传的字体写到 FONT_DIR，渲染服务靠这个目录
    # 区分「随包的/系统的」和「管理员传上来的」。不设的话默认是 Linux 的
    # /usr/share/fonts/imported，Windows 上不存在 → 字体管理页永远是「已导入 0 个」。
    env["FONT_IMPORT_DIR"] = UPLOAD_FONTS
    env["PYTHONUTF8"] = "1"
    env["PYTHONIOENCODING"] = "utf-8"
    render_log = os.path.join(LOGS, "render.log")
    render_proc = spawn(
        "render",
        [PYTHON, "-m", "uvicorn", "app.main:app",
         "--host", "127.0.0.1", "--port", str(render_port), "--log-level", "warning"],
        RENDER_DIR,
        env,
        render_log,
    )
    wait_http("渲染服务", "http://127.0.0.1:%d/healthz" % render_port, render_proc, render_log, 180)

    say("")
    say("[4/4] 启动铸版主程序")
    env2 = os.environ.copy()
    base = "http://127.0.0.1:%d" % core_port
    env2["HTTP_ADDR"] = "127.0.0.1:%d" % core_port
    env2["EXTERNAL_BASE_URL"] = base
    # 三斜杠规范形式，库文件落在 data/ 下，整个目录拷走即可迁移
    env2["DATABASE_URL"] = "sqlite:///" + os.path.join(DATA, "zhuban.db").replace("\\", "/")
    env2["DATA_DIR"] = DATA
    env2["FONT_DIR"] = UPLOAD_FONTS
    env2["RENDER_SERVICE_URL"] = "http://127.0.0.1:%d" % render_port
    env2["AUTH_SECRET"] = cfg["auth_secret"]
    env2["SIGNED_URL_SECRET"] = cfg["signed_url_secret"]
    env2["ADMIN_USERNAME"] = cfg["admin_user"]
    env2["ADMIN_PASSWORD"] = cfg["admin_password"]
    env2["LANDING_ENABLED"] = "false"
    env2["ALLOW_REGISTER"] = "false"
    # Go 侧「等渲染服务多久」。必须和上面渲染服务自己的 RENDER_TIMEOUT_SECONDS
    # 对齐：桌面机上导出一份带截图的报告要做「目录页码两遍排版」（每遍一次
    # LibreOffice 转换），实测 70 秒不够，任务会稳定 failed，而报错只有一句
    # context deadline exceeded —— 用户看到的是「生成失败」，却不知道只是
    # 本机慢了。服务器版有同样的变量，默认 70 秒。
    env2["RENDER_TIMEOUT_SECONDS"] = "180"
    env2["PREVIEW_TIMEOUT_SECONDS"] = "180"
    core_log = os.path.join(LOGS, "core.log")
    core_proc = spawn("core", [ZHUBAN], APP, env2, core_log)
    # 180 秒而不是 60：首次运行要同时做三件慢事——注册 54 个字体并广播
    # WM_FONTCHANGE（整机字体表重建）、建库（SQLite 逐表 AutoMigrate）、
    # 渲染服务预热 LibreOffice。三者叠加实测能到 60 秒开外，撞在 60 秒这个
    # 边界上就会「明明起来了却报启动超时」，然后被当成失败杀掉、用户以为装坏了。
    # 之后再启动只需 1 秒，所以放宽只影响最坏情况。
    wait_http("主程序", base + "/api/v1/healthz", core_proc, core_log, 180)

    say("")
    say("=" * 68)
    say("  已就绪：%s" % base)
    say("  登录账号：%s" % cfg["admin_user"])
    say("  登录口令：%s" % cfg["admin_password"])
    say("  （口令也记在 data\\config.json，登录后可自行修改）")
    say("=" * 68)
    say("")
    say("  浏览器已打开。关闭本窗口即停止服务。")
    say("  日志：data\\logs\\    数据：data\\zhuban.db")
    say("")

    try:
        if os.environ.get("ZB_NO_BROWSER") != "1":
            webbrowser.open(base + "/")
        else:
            say("  · 已跳过自动打开浏览器（ZB_NO_BROWSER=1）")
    except Exception:
        say("  ! 没能自动打开浏览器，请手动访问上面的地址")

    try:
        while True:
            time.sleep(1.5)
            if core_proc.poll() is not None:
                say("")
                say("[×] 主程序已退出（退出码 %s）。日志末尾：" % core_proc.returncode)
                for line in tail(core_log, 20):
                    say("    " + line)
                return 1
            if render_proc.poll() is not None:
                say("")
                say("[×] 渲染服务已退出（退出码 %s）。日志末尾：" % render_proc.returncode)
                for line in tail(render_log, 20):
                    say("    " + line)
                return 1
    except KeyboardInterrupt:
        say("")
        say("正在停止服务…")
        return 0
    finally:
        stop_all()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except SystemExit:
        raise
    except Exception as exc:  # 兜底：不要让窗口一闪而过
        import traceback

        traceback.print_exc()
        die("启动失败：%s" % exc)
