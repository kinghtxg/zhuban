# 铸版 ZHUBAN

**把 Word 模板变成一个 HTTP 接口：JSON 进去，docx / PDF 出来。**

|  |  |
|---|---|
| **在线体验** | <https://zb.kinghtxg.com> —— 官方运行实例（账号由管理员开通；想自己试，按第 2 节几分钟装一个） |
| **源码** | <https://github.com/kinghtxg/zhuban> |
| **使用文档** | 控制台「引导与帮助」页，或 [`docs/manual/`](docs/manual/README.md)（5 个分册） |
| **部署运维** | [`docs/部署与运维.md`](docs/部署与运维.md) |
| **许可** | Apache-2.0，可商用、可私有化改造、可再分发 |

> 公开仓是**发布快照**，不含开发历史。原因见 [`deploy/publish-public.sh`](deploy/publish-public.sh) 顶部注释。

---

## 1. 解决什么问题

合同、检测报告、审计报告、验收单这类文档，内容和格式是两件事：格式由业务/法务定死，内容每次都不一样。
手工改 Word 的后果是——格式会漂、忘记改日期、同名文件满地都是、没法批量、没法追溯谁在什么时候用哪份数据出的报告。

铸版把这件事拆开：

- **模板**由懂格式的人做一次（就是个普通 `.docx`，用 Jinja2 风格标签占位）；
- **数据**由业务系统以 JSON 提交；
- **产物**由服务端渲染，全程留痕，可下载、可签名直链转给最终用户。

三种典型用法：

| 场景 | 怎么用 |
|---|---|
| 人工出报告 | 浏览器工作台：选模板 → 填数据（带变量校验和实时预览）→ 下载 docx/PDF |
| 系统对接 | `POST /api/v1/generate`，按 `template_code` 渲染，同步返回或异步轮询 |
| 批量出件 | `records` 数组一次投多条，任务完成后打包 zip 下载 |

## 2. 跑起来

前置：已安装 Docker 与 Docker Compose v2（Windows / macOS 装 Docker Desktop 即可）。
下面两种装法选一种，**两份 compose 的容器名不同，不能同时起在同一目录**。

### 方式 A：拉官方镜像（推荐，不需要 Go / Node / LibreOffice）

镜像在 GitHub 容器仓库，`linux/amd64` 与 `linux/arm64` 双架构，匿名可拉：

```bash
cp .env.example .env       # 至少填 POSTGRES_PASSWORD / SIGNED_URL_SECRET / AUTH_SECRET
#   密钥生成：openssl rand -hex 32
#   可选：写 APP_VERSION=3.4.0 固定版本，不写则跟随 latest

docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
```

| 镜像 | 内容 |
|---|---|
| `ghcr.io/kinghtxg/zhuban` | 主服务（Go + 内嵌前端 + 内置模板） |
| `ghcr.io/kinghtxg/zhuban-render` | 渲染服务（FastAPI + LibreOffice + 中文字体） |

### 方式 B：源码构建

```bash
cp .env.example .env       # 同样填三个密钥
docker compose up -d --build
docker compose ps          # 三个容器均为 healthy
```

### 装完第一步

浏览器打开 `http://<EXTERNAL_BASE_URL>`（默认 `http://localhost:18100`）：

- `.env` 里配了 `ADMIN_USERNAME` + `ADMIN_PASSWORD` → 首个管理员已自动创建，直接登录；
- 没配 → 进入**一次性安装向导**，由页面创建第一个管理员，**装完 `/install` 立即变成 404**。

> ⚠️ 装完之前不要把这个端口暴露到公网——谁先填完向导谁就是管理员。

健康检查：`GET /api/v1/healthz` → `{"status":"ok","db":"ok","render":"ok"}`

> 默认宿主端口 **18100**：Windows 上 8100/8101 常落在 TCP 保留端口段导致无法绑定，故统一避开。
> 端口冲突改 `.env` 的 `WEB_PORT`；容器内固定 8100，render 固定 8101 且不映射宿主。

## 3. 能力

### 模板与渲染

- **上传即扫描**：自动提取占位符，产出变量清单、可直接填充的 JSON 示例、JSON Schema 与分级诊断。
  Word 常把中文占位符拆成多个 run，服务端会**自动合并修复**并提示，不用手工去 XML 里拼。
- **三栏工作台**：变量树 / Monaco JSON 编辑器 / 实时预览页图，支持校验、防抖预览、docx 与 PDF 下载。
- **在线模板编辑器**：把 docx 解析成可编辑块模型，在线改文本、加变量、绑图片；带乐观锁保存为新版本，历史版本一键回滚。
- **图表传参**：模板里的折线图/柱状图/饼图可以换数据，**配色、线宽、字体、图例、坐标轴、网格线一个像素都不动**。
  做法是只重写 `chartN.xml` 里的数据缓存节点，样式节点结构上够不着。分类（数据点）可自由增删，
  **系列数不可增删**，不一致直接报 `chart_invalid` 而不是静默画错。
- **渲染基线**：子进程超时保护、跨 run 标签合并、docx/PDF 双格式、页面缩略图（含水印）。

### 签章

- **签名库**：上传透明底 PNG，或用签名人姓名 + 5 款内置手写风字体（SIL OFL）生成。
- **印章库**：上传印章图，或参数化生成圆形/椭圆形红色公章（单位名称环形排布、五角星）。
- **签章现场出图**：渲染那一刻才生成带署名的图，可叠加服务器时间（精确到秒）。
  时间表达式**原样交给渲染服务求值**，Go 侧不做任何时间计算——避免出现第二份「北京时间怎么算」的答案。
- **审核与授权**：签名/印章库可开启审核（待审核→通过/驳回，意见必填）；授权对象可指定具体用户或 API Key，
  支持单次 / 限次 / 期限三种策略组合，授予、续期、吊销、消耗、拒绝全部入审计。
  未授权调用在生成链路上即被拦截（403 `grant_denied`，原因精确到 `unauthorized` / `exhausted` / `expired` / `not_yet`）。

引用已有签名并盖章时间，数据这样写：

```json
{
  "sig_审批": {
    "$sig": "sig_张三",
    "with_timestamp": true,
    "timestamp": { "$date": "now", "format": "%Y年%m月%d日 %H:%M" },
    "timestamp_prefix": "签署时间："
  }
}
```

`$sig` 指向的**那一条资源**才是被校验和扣次的，参数对象绕不过去；不传 `font` 时继承资源自身字体。

### 权限、审计与合规

- **账号体系**：本地账号密码（argon2id）始终可用，可选叠加 APSSO OIDC（授权码 + PKCE + 标准 Discovery），两者并存。
- **哈希链审计**：全量记录调用人、操作、任务/文档标识、模板编号、`external_ref`、使用的签名印章、时间、来源 IP；
  逐条 SHA-256 哈希链，应用层**只读、无修改/删除接口**，提供全链校验与带校验头的 CSV 导出。
- **合规边界**：电子签名/印章是「图片型版式元素 + 服务器时间 + 全程审计」，
  **不是** CA 数字证书签名，也不做 TSA 时间戳。产品文案、手册与接口命名都按这个口径，不冒称电子签名法意义上的数字签名。

### 开放接口与运维

- **投递即生成**：`POST /api/v1/generate`，同步/异步、批量、`external_ref` 幂等、完成回调、HMAC 签名直链免鉴权下载。
- **文档生成中心**：全部来源（UI / API / 批量）产物留痕，可筛选、重试、查看输入数据再生成、批量 zip。
- **内置演示模板**：全新安装自带 2 个模板——`demo-report`（铸版能力演示，每个语法各一处并带说明，开箱即渲）
  与 `audit-report`（代码审计报告，真实业务样例）。
- **更新提示**：`UPDATE_CHECK=true` 时从 GitHub 读取最新版本，有新版本会在右下角弹窗（`UPDATE_REPO` 可指定检查哪个仓库）。
- **产物保留期**：`RETENTION_DAYS` 到期清理文件，数据库记录可查。

## 4. 模板语法速查

模板就是普通 Word 文件，在需要填内容的地方写标签（docxtpl / Jinja2 语法）：

| 能力 | 写法 | 数据侧 |
| --- | --- | --- |
| 变量 | `{{ 项目编号 }}` | `"项目编号": "DEMO-001"` |
| 表格行循环 | `{%tr for item in items %}` … `{%tr endfor %}` | `"items": [{…}, {…}]` |
| 段落循环 | `{%p for x in list %}` … `{%p endfor %}` | 数组 |
| 条件 | `{% if show_summary %}` … `{% endif %}` | `"show_summary": true` |
| 图片 | `{{ image_logo }}` | `{"type":"image","src":"…","width":40,"height":15}` |
| 二维码 | `{{ qr_编号 }}` | `{"type":"qrcode","text":"…","size":30}` |
| 富文本 | `{{ rich_说明 }}` | `{"type":"markdown","content":"**加粗**"}` |
| 图表 | `{{ chart_季度销售额 }}` | `{"categories":["1月",…],"series":[{"name":"今年","values":[120,…]}]}` |
| 目录/页码 | 插入 Word 目录或页码域 | 渲染时写 `updateFields=true`，打开时自动更新 |

图表变量名按图表标题自动生成（`chart_<标题>`），**不传就不改图**——沿用模板原有数据，
所以图表字段不是必填。散点图用 `xVal` / `yVal` 代替 `cat` / `val`，接口层形态一致。

**两个容易踩的写法问题**：

1. **行循环标签建议独占整行**（`{%tr for %}` 一行、内容行、`{%tr endfor %}` 一行）。
   把 `for` 和 `endfor` 写在同一行会踩到 docxtpl 自身的正则缺陷，报一个跟表格毫无关联的
   `Encountered unknown tag 'endfor'`。本项目已做预处理兼容两种写法，但独占整行更不容易出错。
2. **签章占位不要写 `sig_` / `seal_`** 除非你确实要引用资源——后端会把它们当资源引用去解析，
   没有对应素材时报 `signature_unavailable`。只想放张图就用 `image_` 前缀。

详细写法见 [`docs/manual/01-模板使用手册.md`](docs/manual/01-模板使用手册.md)。

## 5. 开放 API

鉴权：网页「API Key」页创建 Key（`df_` 开头，明文只展示一次），调用时带 `X-API-Key: df_xxxx`，
或直接复用浏览器登录后的会话 Cookie。

```bash
curl -sS -X POST http://localhost:18100/api/v1/generate \
  -H "X-API-Key: df_在APIKey页面创建" \
  -H "Content-Type: application/json" \
  -d '{
    "template_code": "audit-report",
    "sync": true,
    "format": "docx",
    "data": {
      "被测件名称": "示例系统",
      "被测件版本": "v1.0",
      "项目编号": "DEMO-2026-001",
      "测试人员": "张三",
      "测试时间": "2026-10-18",
      "items": [
        { "name": "GB_6.2.1.1 密码安全", "缺陷等级": "高", "次数": 3,
          "缺陷说明": "使用了自定义加密算法",
          "description": "应检查密码相关实现是否符合国家密码管理规定。",
          "mitigation": { "type": "markdown", "content": "- 采用认可的商用密码算法\n- 禁止自行实现密码算法" } }
      ]
    }
  }'
```

同步成功（200）：

```json
{
  "job_id": "jb_20261018_0001",
  "status": "succeeded",
  "sync": true,
  "documents": [{
    "document_id": "dc_20261018_0001",
    "filename": "audit-report_000.docx",
    "download_url": "/api/v1/documents/dc_20261018_0001/download",
    "pdf_download_url": "/api/v1/documents/dc_20261018_0001/download?format=pdf",
    "signed_url": "/api/v1/s/eyJkIjo...."
  }]
}
```

> 上面的字段名是 `audit-report` 真实字段。换模板时要按该模板扫描出的变量清单来填，
> 缺字段会返回 `data_invalid` 并给出具体路径；两个内置模板的完整示例数据见
> [`samples/audit-report.sample.json`](samples/audit-report.sample.json) 与
> [`samples/demo-report.sample.json`](samples/demo-report.sample.json)。

异步批量把 `sync` 换成 `"records": [{…}, {…}]`，返回 202 与 `status_url`，
轮询到 `succeeded` / `partial` / `failed` 后下载 zip（docx/pdf 分目录，失败条目写 `errors.json`）。

几条约定：

- **幂等**：相同 Key（或用户）+ `external_ref` 的重复请求在保留期内返回原任务，不重复渲染，网络重试可安全重发。
- **回调**：终结后向 `callback_url` POST，超时 10s，1s/4s/16s 退避重试 3 次；仅 http/https，
  默认拒绝内网/回环，可用 `CALLBACK_HOST_ALLOWLIST` 精确放行。
- **富文本**走白名单（`b/i/u/s/br/p/span/ul/ol/li/font` 等，只允许颜色/加粗/斜体/下划线），
  事件属性与 `javascript:` 一律清除。

完整参数、错误码、Python/Go 示例见 [`docs/manual/02-API调用手册.md`](docs/manual/02-API调用手册.md)，
每个模板的「接入卡片」里也有可一键复制的 curl/Python/Go 代码。

## 6. 配置

完整清单见 [`.env.example`](.env.example)（每项都有注释），关键项：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `EXTERNAL_BASE_URL` | `http://localhost:18100` | 对外基址。直链、接入卡片、OIDC 回调都靠它，**必须填浏览器实际看到的地址** |
| `WEB_PORT` | `18100` | 宿主映射端口（容器内固定 8100） |
| `POSTGRES_PASSWORD` | 空 | **必填**，镜像版留空会拒绝启动 |
| `SIGNED_URL_SECRET` | 空 | 签名直链 HMAC 密钥；空则启动时随机生成并告警（重启后旧链接失效） |
| `AUTH_SECRET` | 空 | 会话 Cookie 签名密钥；空则随机生成并告警（重启后全部掉线） |
| `APP_VERSION` | 空 | 镜像 tag，留空用 `latest`；生产建议钉死版本号 |
| `SESSION_TTL_DAYS` | `7` | 会话有效期（天），cookie 与 `sessions` 表共用 |
| `RETENTION_DAYS` | `7` | 产物保留期 |
| `AUTH_DEV` | `false` | 信任 `X-Dev-User` 头（**等于免认证**），生产必须 false |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | 空 | 首个管理员种子；留空走页面安装向导 |
| `ALLOW_REGISTER` | `false` | 自助注册；开启后新账号为 pending，需管理员审核 |
| `OIDC_ENABLED` | `false` | 是否显示 APSSO OIDC 登录入口 |
| `OIDC_ISSUER` | 空 | 发行方，自动走 `/.well-known/openid-configuration`，不硬编码端点 |
| `SEED_BUILTIN` | `true` | 启动时播种内置模板（`demo-report`、`audit-report`） |
| `UPDATE_CHECK` / `UPDATE_REPO` | `true` / `kinghtxg/zhuban` | 版本更新提示；离线环境设为 `false` |
| `MAX_RECORDS` / `MAX_JSON_MB` / `MAX_TEMPLATE_MB` | 1000 / 20 / 10 | 批量条数、JSON、模板大小上限 |

> `.env` 已在 `.gitignore` 中，`.env.example` 只含占位值。密钥只走环境变量，日志不打印密码与令牌。

## 7. 架构与目录

```
浏览器 ──HTTP──▶ web（Go 主服务，chi + GORM，内嵌 Vite/React/Arco 构建产物）
                    │  /api/v1（会话 Cookie 或 X-API-Key）
                    ├──▶ PostgreSQL（用户/模板/任务/产物/API Key/签章/授权/审计）
                    └──▶ render（Python FastAPI + docxtpl + LibreOffice + poppler）
                            └── /scan /render /validate /preview /pdf /signature /seal /fonts
```

- **web**：认证、模板与版本管理、任务状态机（单 worker 轮询）、zip 打包、签名直链、回调（SSRF 防护 + 退避重试）、
  签章鉴权（原子扣减）、哈希链审计、内嵌静态前端。
- **render**：无状态、仅内网。跨 run 标签合并、扫描、渲染、LibreOffice 转 PDF、`pdftoppm` 出预览图、
  图片/二维码/富文本注入、签名印章与时间组合图生成。
- **postgres**：持久化数据；模板原文件与产物落宿主机 `./volumes`。

```
zhuban/
├── docker-compose.yml           # 源码构建：postgres + render + web
├── docker-compose.ghcr.yml      # 拉官方镜像（容器名 zhuban-*）
├── Dockerfile.web               # Go 多阶段构建（内嵌前端 dist）
├── embed.go                     # go:embed 前端 dist、官网 site/、样例模板 samples/
├── server/                      # Go 主服务
│   └── internal/                # auth models store api worker seed render
│                                # sigauth(签章授权) auditx(哈希链) netguard callback
├── render-service/              # Python 渲染微服务
│   ├── app/                     # scanner preprocess render docapply chartdata office
│   │                            # signature seal richdata security fonts config
│   ├── fonts/                   # 5 款内置手写风字体（SIL OFL）
│   └── tests/                   # pytest
├── web/                         # Vite + React 18 + TypeScript + Arco Design
│   └── dist/                    # 构建产物已提交，无 Node 环境也能编译
├── site/                        # 官网（纯静态，零构建）
├── samples/                     # 演示模板、审计模板、示例 JSON、模板生成脚本
└── docs/                        # 手册、接口契约、部署运维
```

### 官网是一份文件，两种用法

`site/index.html` + `site/landing.js` 纯静态、零构建，既是可独立托管的宣传页，
也被 embed 成「未登录时的首页」（`/welcome` 恒为官网，`/` 登录后进工作台）。

**仓库已配好 GitHub Pages 自动部署**（`.github/workflows/pages.yml`）：`site/` 一有改动就发布到
`https://<owner>.github.io/<repo>/`。因为页面只有 `index.html` + 同目录 `landing.js`、favicon 是内联
data URI、没有任何 CDN 依赖，挂在子路径下也不会坏。想在别处托管（OSS / nginx）把 `site/` 整个目录
拷过去即可。

> 注意：`install/`（一次性安装向导）**不能**这样独立托管——它要调
> `POST /api/v1/auth/setup`、`PUT /api/v1/admin/mail/settings` 等真实接口，
> 静态托管只能显示表单壳，提交一律 404。它必须跟着主服务一起跑。

改之前注意三条：

1. **脚本必须是外部文件**——全站 CSP 是 `script-src 'self'`，内联 `<script>` 会被拦掉，
   页面照样打开但动效全不动。这个坑在本地 `file://` 直接打开时看不出来（没有 CSP）。
2. **站内链接写的是官方实例绝对地址**（`https://zb.kinghtxg.com/...`），独立托管时相对路径没有对应路由。
   被产品自己提供时，服务端会用 `EXTERNAL_BASE_URL` 替换成本实例地址——
   否则自建实例的用户点「进入控制台」会被送到别人的站上去。
3. **别把它放进 `web/dist`**，那是构建产物，`npm run build` 会整个覆盖。

## 8. 本机开发

需要 Go 1.26+、Python 3.12+、Node 20+；PDF 与预览页图另需 LibreOffice 与 poppler
（不装时 docx 渲染正常，PDF/预览接口返回 502）。

```powershell
# 1) 渲染微服务
cd render-service
python -m venv .venv
.\.venv\Scripts\Activate.ps1
pip install -r requirements.txt
python -m pytest
python -m uvicorn app.main:app --host 0.0.0.0 --port 8101

# 2) 前端（开发模式，/api 代理到 8100）
cd web
npm install
npm run dev                        # http://localhost:5173

# 3) Go 主服务（sqlite 兜底，无需数据库；另起终端）
cd ..
$env:DATABASE_URL="sqlite:///./volumes/zhuban.dev.db"
$env:DATA_DIR="./volumes"
$env:AUTH_DEV="true"
$env:AUTH_SECRET="dev-only-auth-secret"
$env:SIGNED_URL_SECRET="dev-only-sign-secret"
$env:RENDER_SERVICE_URL="http://127.0.0.1:8101"
go run ./server
```

前端构建 `cd web && npm run build` 写入 `web/dist/` 由 Go `//go:embed` 内嵌。

## 9. 测试与安全边界

**测试**：`pytest` 184 项、`go test ./...` 322 个测试函数全绿，前端 `tsc -b` 严格模式零错误。
覆盖跨 run 占位符合并、行循环、条件块、图表数据改写、二维码、富文本、签章占位与授权扣减、
HMAC 签发/校验/过期、SSRF 判定、worker 状态机、OIDC 完整 PKCE 流程、全路由匿名准入矩阵、
官网投递与链接改写。

**安全边界**：

- 仅接受 `.docx`，拒绝 `.docm` 与含宏（`vbaProject.bin`）的文件；模板/JSON/图片均有大小上限。
- 渲染在子进程超时保护内执行，超时杀进程。
- 富文本白名单；回调与图片下载默认拒绝内网/回环/链路本地目标（SSRF 防护）。
- 密码 argon2id；同账号连续失败 5 次锁定 5 分钟，登录/注册/初始化接口按 IP 限流。
- 错误响应统一信封，返回中文可执行的修复指引，不泄漏堆栈、内部路径与 SQL 错误。
- 签章占位由服务端 fail-closed 注入，调用方无法用 data 伪造；授权扣减是单条原子条件 UPDATE，
  并发/重放/幂等语义下不重扣、不漏扣；预览（DryRun）永不扣次。
- 任务、文档、直链均有归属校验，非归属者一律 404（不做 403 的"存在性泄露"）。
- 产物保留期到期清理；签名直链 HMAC 常量时间校验、带过期时间。

发现安全问题请通过 GitHub Issues 或仓库主页的联系方式反馈，不要公开提交细节。

## 10. 许可

Apache License 2.0，全文见 [`LICENSE`](LICENSE)。可商用、可私有化修改、可再分发，
保留版权声明与许可证文本即可。本项目按「现状」提供，不附带任何明示或暗示的担保。
