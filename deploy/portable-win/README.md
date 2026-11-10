# Windows 便携版

「双击 `启动铸版.bat` 就能出 PDF」的单机形态：内嵌 Python 3.11 + LibreOffice +
poppler + 58 个公文字体，不需要装任何东西、不需要管理员权限、不需要联网。

这个目录是**启动器与打包流程的源头**。便携包本身（730 MB）不进 git，
在 `04-编译产物/<版本>/Windows便携版/`。

## 目录

| 文件 | 作用 |
|---|---|
| `launcher.py` | 启动器：准备 data 目录、注册字体、拉起渲染服务与主程序、等健康检查、退出时收进程 |
| `unregister_fonts.py` | 撤销字体登记。**只删指向本文件夹的**，系统自带字体一条不动 |
| `启动铸版.bat` | 双击入口（CRLF，见下方「行尾」） |
| `清除字体注册.bat` | 撤销入口 |
| `说明.txt` | 随包说明。版本号与提交号由打包脚本盖上，仓库里这份是模板 |
| `make-portable-win.py` | 打包脚本：从上一版派生新版 + 七项校验 |
| `verify/` | 三份验收脚本，见下 |

## 打包

便携包里 99% 的内容（内嵌 Python、LibreOffice、poppler、字体）两版之间不变，
只有 `zhuban.exe`、`render/app/*.py` 和三个说明文件会动，所以是**从上一版派生**：

```bash
cd 01-源代码/docforge

# 1. 前端产物（vite 在 Z: 盘上会因路径解析成 UNC 而失败，需先拷到本地盘构建）
npm --prefix web run build

# 2. 交叉编译，版本号与提交号必须真的注入（脚本会 grep 二进制核对）
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w \
    -X docforge/server/internal/version.Version=<版本> \
    -X docforge/server/internal/version.Commit=<短提交> \
    -X docforge/server/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o /tmp/zhuban-windows-amd64.exe ./server

# 3. 派生 + 打包 + 校验
python deploy/portable-win/make-portable-win.py \
  --base  <上一版便携包.zip> \
  --exe   /tmp/zhuban-windows-amd64.exe \
  --version 3.4.24 --commit <短提交> \
  --out   /tmp/zhuban-3.4.24-windows-x64.zip
```

### 为什么要七项校验

派生策略必然带一个坑：**忘记更新某个文件，新包里就还带着旧代码**，
而本地跑起来一切正常（因为测的是刚编的 exe，不是包里的）。

所以脚本在打包完成后逐项核，任一项不过就以非零码退出：

1. 顶层目录名与版本号一致
2. 14 个必备文件齐（含 `pdftotext.exe`、`align.py`、`toc.py`——后两个是 3.4.20/3.4.21
   新增的模块，从 3.4.19 派生时最容易漏）
3. 中文条目在 zip 里置了 UTF-8 标志位（bit 11），否则解压乱码
4. 两个 `.bat` 是 CRLF
5. 包内 `zhuban.exe` 与传入的 exe **同 sha256**，且**内含**版本号与提交号
6. `render/app/*.py` 与仓库**逐字节**一致
7. `说明.txt` 里的版本号与提交号正确

## 验收

服务起来后跑，三个脚本互不依赖：

```bash
# 50 项：登录、上传模板、各种输入、渲染、格式不变性
python deploy/portable-win/verify/portable-win-accept.py \
  --base http://127.0.0.1:18100 --data <解压目录>/app/data

# 12 项：PDF 物理层（页数、尺寸、内嵌字体、版心、行距、目录页码）
ZB_POPPLER=<解压目录>/app/poppler \
  python deploy/portable-win/verify/portable-win-pdf.py out.pdf

# 14 项：富文本 + 内联截图（UI 上粘贴截图走的那条路）
ZB_DATA=<解压目录>/app/data python deploy/portable-win/verify/portable-win-rich.py
```

`--data` 指向便携版的 `app/data`，脚本从里面的 `config.json` 读管理员口令
（口令每次装机随机生成，不能硬编码）。

**为什么要验到 PDF 物理层**：断言 docx 的 `rFonts` 写着「宋体」不等于用户看到宋体。
字体没注册时 LibreOffice 会静默替换，docx 上一个字都没变。唯一可信的判据是
PDF 里内嵌了哪些字体、每段文字落在什么坐标。

## 几个必须记住的坑

**行尾**。`.bat` 必须是 CRLF：cmd.exe 按行硬解析，LF 结尾会被切成 `'plate'`、
`'包不完?echo'` 这种碎片报错。`.gitattributes` 已显式声明 `*.bat text eol=crlf`——
**不能靠 `core.autocrlf`**，那是本机设置；换台没开的机器检出就是 LF，
而打包脚本是在本机跑的，本地怎么测都是好的。

**`pdftotext` 不能缺**。目录页码是「两遍排版」量出来的，靠 `pdftotext`。
缺了它渲染服务是**静默降级**（`toc.measure_text` 失败就 `break`），
PDF 照样出，只是目录页码停在模板作者留下的旧值上，用户看不到任何报错。
启动器现在会在缺文件时直接说出来。

**首次启动慢**。要同时做三件事：注册 54 个字体并广播 `WM_FONTCHANGE`
（整机字体表重建）、建库（SQLite 逐表 AutoMigrate）、预热 LibreOffice。
实测 29.1 秒。等待上限是 180 秒（原先 60 秒会误判成「启动超时」然后被被杀）。
之后每次启动约 1 秒。

**Go 侧超时**。`RENDER_TIMEOUT_SECONDS` 在便携版设 180：一份含 7 张截图的报告
要做两遍排版，每遍一次 LibreOffice 转换，桌面机上约 100 秒。默认 70 秒必然失败，
而错误只有一句 `context deadline exceeded`，看起来像渲染服务挂了。

**字体登记按族名做键**。两个不同目录的便携包注册同名族体会互相覆盖，
所以撤一个目录的登记可能顺带把另一个的也撤掉。测试多个包时留意。

## 改了代码之后

`app/render/` 下是渲染服务的源码（不进 `app/python` 的 site-packages，
由 `uvicorn` 以 `app.main:app` 直接加载）。改 `render-service/` 之后重新走一遍
上面的打包命令即可，不需要重装 Python 依赖——`requirements.txt` 若没变，
内嵌 Python 的 site-packages 不用动。
