# docforge 内部接口契约（三方并行开发的唯一对接口径，冻结）

本文件是 render-service / server / web 三方并行开发的共同契约，优先于口头约定。
需求规格见 PRD（`./prd/docforge-PRD-通用Word模板渲染服务.md`）；本文件只固化**接口、枚举、数据形状、目录归属与验收口径**。
三方不得擅自改本文件；如确有冲突，先在本文件修订后再改代码。

## 0. 目录归属（严禁越界写文件）

| 目录 | 归属 | 说明 |
| --- | --- | --- |
| `render-service/` | 渲染服务子代理 | Python FastAPI + pytest + Dockerfile |
| `samples/` | 渲染服务子代理 | 迁移模板、示例 JSON、空白示例模板（Go 已 embed，**替换文件、保持文件名不变**） |
| `server/` | Go 服务子代理 | 全部 Go 业务代码；`go.mod`/`embed.go` 在仓库根，已建好，**不要移动** |
| `web/`（除 `web/dist/`） | 前端子代理 | Vite + React 18 + TS + Arco 工程 |
| `web/dist/` | 前端构建产物（任何人手工改了会被 `npm run build` 覆盖）；Go 内嵌此目录 |
| `docker-compose.yml`、`Dockerfile.*`、`.env.example`、`README.md`、`docs/` | 集成方 | 子代理不要创建这几个文件 |
| 仓库根 `go.mod`、`embed.go` | 已冻结，Go 子代理可加依赖（go mod tidy），不得改包名/模块名/embed 路径 |

模块名：`docforge`（根包名也是 `docforge`）。server 代码在 `docforge/server` 下，引用根包内嵌资源用 `import "docforge"`。

## 1. 全局枚举（所有服务共用，字符串值固定）

- 任务来源 `source`：`ui` | `sync_api` | `auto_api` | `batch`
  - UI `/render` → `ui`；`/generate` 同步单份 → `sync_api`；异步单份 → `auto_api`；`records` 多条 → `batch`
- 任务状态 `job.status`：`queued` | `running` | `succeeded` | `partial` | `failed`
- 单文档状态 `document.status`：`queued` | `running` | `succeeded` | `failed`
- 回调状态 `job.callback_status`：`""`（无 callback） | `pending` | `succeeded` | `failed`
- 输出格式 `format`：`docx` | `pdf` | `both`（`both` 仅任务/产物层用；每份产物同时产出 docx 与 pdf）
- 模板变量类型 `kind`：`scalar` | `loop` | `condition` | `image` | `qrcode` | `richtext`
- 模板可见性：`private` | `public`
- 诊断级别：`error` | `warning` | `info`

时间字段统一 RFC3339 UTC（`2026-10-18T03:04:05Z`），前端按本地时区显示。ID 格式：
- 任务 `jb_YYYYMMDD_0001`（当日 4 位序号）；单文档 `dc_YYYYMMDD_0001`；模板 `tpl_` + 12 位 hex；版本 `ver_` + 12 位 hex；API Key 实体 `ak_` + 12 位 hex；预览 `pv_` + uuid。

## 2. 统一错误信封（所有 HTTP 服务一致）

```json
{ "error": { "code": "data_invalid", "message": "中文、可执行、面向用户的修复指引", "details": [ {"path": "items[0].name", "message": "字段缺失"} ], "request_id": "..." } }
```

- render-service 用 HTTP 状态：400 `template_invalid`（模板语法/结构错误）、415 `unsupported_media_type`（非 docx 或含宏）、413 `payload_too_large`、422 `data_invalid`（数据校验/渲染期数据错误）、502 `conversion_failed`（LibreOffice 失败）、504 `timeout`（渲染超时）、500 `internal_error`。
- Go 服务透传/转译 render-service 的错误为同一信封；**绝不把堆栈、内部路径、SQL 错误返回前端**，日志里记完整错误。
- 前端收到任何错误都要展示 `message`，并把 `details[].path` 定位到 JSON 编辑器对应路径。

## 3. render-service HTTP 契约（FastAPI，容器内监听 8101）

无状态、无鉴权（仅内网，Go 服务访问；compose 不映射宿主端口）。所有接口 `multipart/form-data`，文件字段名 `file`。

### 3.1 `POST /scan`

入参：`file`（docx，必填）；`repair`（text，默认 `true`：扫描前先做跨 run 合并预处理）。

200：
```json
{
  "variables": [
    {"name": "被测件名称", "kind": "scalar", "type": "string", "sample": "示例文本", "path": "被测件名称", "required": true},
    {"name": "items", "kind": "loop", "iterable": "items", "loop_var": "item", "sample_size": 2,
     "fields": [
       {"name": "name", "kind": "scalar", "type": "string", "sample": "GB_6.2.1.1 密码安全", "path": "items[].name"},
       {"name": "缺陷等级", "kind": "scalar", "type": "string", "sample": "高", "path": "items[].缺陷等级"},
       {"name": "mitigation", "kind": "richtext", "type": "object", "path": "items[].mitigation"}
     ]},
    {"name": "show_summary", "kind": "condition", "expression": "show_summary", "path": "show_summary"},
    {"name": "image_logo", "kind": "image", "type": "object", "path": "image_logo"},
    {"name": "qr_报告编号", "kind": "qrcode", "type": "object", "path": "qr_报告编号"},
    {"name": "rich_说明", "kind": "richtext", "type": "object", "path": "rich_说明"}
  ],
  "tags": ["for", "if", "tr"],
  "sample_json": { "...": "可直接填的完整示例（每个循环给 2 条示例）" },
  "json_schema": {
    "type": "object",
    "properties": { "被测件名称": {"type":"string"}, "items": {"type":"array","items":{"type":"object","properties":{...}}} ,
      "image_logo": {"type":"object","required":["type","src"]}, "qr_报告编号": {"type":"object","required":["type","text"]},
      "rich_说明": {"type":"object","required":["type","content"]} },
    "required": ["被测件名称", "items"]
  },
  "diagnostics": [
    {"severity": "warning", "message": "占位符 {{被测件版本}} 被 Word 拆到 3 个 run，已自动合并修复", "location": "表格第 1 行单元格"},
    {"severity": "error", "message": "for 标签未配对（缺少 endfor）", "location": "段落 12"}
  ],
  "repaired": true
}
```

扫描推导规则：
- 预处理后用 jinja2 AST（`Environment.parse` + `meta.find_undeclared_variables`）提取变量；
- `for x in items` 产出 `loop` 变量；循环体内 `x.field` 归为该 loop 的 fields；
- `{% if cond %}` 中引用的裸变量（且不是循环字段）标 `condition`；`if x == "高"` 这类比较要把枚举值 `"高"` 提取进该字段 schema 的 `enum`；
- 命名前缀约定：`qr_` → qrcode；`image_`/`img_` → image；`rich_`/`html_`/`md_` → richtext；
- 标量示例按键名启发式（含"时间/日期"给日期示例、含"等级"给 `高`、含"次数/数量"给整数、其余给中文占位示例）；
- 模板里出现但语法解析不了的标签进 `diagnostics`（error），并给出"在 Word 中把占位符重新输入为纯文本"的修复指引。

### 3.2 `POST /render`

入参：`file`（docx）、`data`（text，JSON 字符串）、`format`（text，`docx` 默认 | `pdf`）。
- 200：直接返回二进制；`Content-Type` 分别为 `application/vnd.openxmlformats-officedocument.wordprocessingml.document` 或 `application/pdf`；
- `Content-Disposition: attachment; filename="rendered.docx"`（pdf 同名 .pdf）。
- 处理顺序：校验 docx（zip、`word/document.xml` 存在、拒 docm/宏部件 `vbaProject.bin`）→ 跨 run 预处理 → 扫描校验（仅阻断 error 级诊断）→ 构造上下文（图片/二维码/富文本对象）→ docxtpl render → 写 `updateFields=true`（settings.xml 没有则加）→ format=pdf 时 LibreOffice 转换。
- 渲染在子进程/超时保护内：超过 `RENDER_TIMEOUT_SECONDS` 杀子进程并返回 504。
- 单条数据渲染失败返回 422，details 给出**字段路径 + docxtpl 原文错误摘要 + 修复建议**。

### 3.3 `POST /validate`

入参：`file`、`data`。200：
```json
{ "valid": true, "errors": [ {"path": "items[0].name", "message": "字段缺失"} ], "warnings": [ {"path": "...", "message": "..."} ] }
```
校验三层：JSON 可解析；数据满足 /scan 产出的 JSON Schema（required/类型/enum）；用空写方式跑一次 Jinja 编译（不实际出文件）捕获模板语法错误。

### 3.4 `POST /preview` 与 `GET /preview/{preview_id}/page/{n}`

入参：`file`、`data`、`max_pages`（默认 6，硬上限 20）、`dpi`（默认 110，上限 200）、`watermark`（text，默认 `docforge 预览`，传空串关闭）。
200：
```json
{ "preview_id": "pv_<uuid>", "page_count": 12, "returned_pages": 6,
  "pages": [ {"page": 1, "url": "/preview/pv_xxx/page/1", "width": 936, "height": 1324} ] }
```
流程：render docx → soffice 转 pdf（独立 `-env:UserInstallation=file:///tmp/lo_<uuid>` profile 隔离，信号量限并发 2）→ pdftoppm 出 png（poppler-utils）→ 加居中斜向水印（Pillow）→ 存临时目录 30 分钟。
单页 GET 返回 `image/png`，过期 404。本机无 LibreOffice 时这些接口返回 502 `conversion_failed`，且 `/healthz` 中 `libreoffice:false`；**单测在无 LibreOffice 时跳过 pdf/预览部分**。

### 3.5 `GET /healthz`

`{"status":"ok","libreoffice":true,"poppler":true,"version":"1.0.0"}`。

### 3.6 数据侧特殊值（在渲染前由 render-service 把 dict 里的这些对象替换为 docxtpl 对象）

- 图片：`{"type":"image","src":"https://a/x.png 或 data:image/png;base64,xxxx","width":120,"height":80}`，width/height 单位 **毫米 mm**；URL 下载限制：仅 http/https、超时 10s、`MAX_IMAGE_MB`（默认 5）、跟随重定向 ≤2 次、解析 IP 拒绝回环/内网（与回调同策略，可被 `IMAGE_ALLOW_PRIVATE=true` 放开）。

  **尺寸自适应规则（V4，`richdata.fit_image_size`）**——传进来的 width/height 是「期望框」，不是最终值：

  | 传入 | 来源图 | 实际摆放 |
  |---|---|---|
  | 60 × 30 | 600×300（2:1） | 60 × 30 |
  | 60 × 20 | 600×300（2:1） | **60 × 30**（高度按原图反算，不压扁） |
  | 40 × 80 | 200×400（1:2） | **30 × 60**（受高度上限约束，等比缩） |
  | 400 × 200 | 800×400（2:1） | **120 × 60**（受 `MAX_INLINE_IMAGE_WIDTH_MM` / `_HEIGHT_MM` 约束） |
  | 都不传 | 任意 | 按图片 DPI 折算物理尺寸，再受上面两个上限约束 |

  三条约束的用意：① 宽高比永远等于源图，避免记录里的框和原图比例不一致时被压扁；② 一切内联图片都夹在**版心**内（默认宽 146mm、高 60mm），超了就等比缩小 —— 行内图片一旦超过版心，Word 会把段落挤成两行、表格行高被顶开，整版就散了；③ 只给一维时反算另一维。

  ⚠️ 破坏性变更：老版本会把 width 和 height **原样**写入 `wp:extent`，因此「故意要一张非等比拉伸的图」在 V4 不再成立。需要精确控制尺寸的场合，请传与源图一致的比例，或改用浮动图片（`wp:anchor`）另行实现。

#### 3.6.1 编辑器画布版式（V4，`/parse` 输出新增字段）

`/parse` 的 `blocks` 除文字外还带版式信息，供在线编辑器还原 Word 观感。**这些字段只影响渲染，`/apply` 不读它们**（回写仍只认 `id/type/text/name/rows`），因此老前端不受影响。

- 顶层 `page`：`{width_mm, height_mm, margin_top_mm, margin_right_mm, margin_bottom_mm, margin_left_mm}`，来自 `w:sectPr` 的 `pgSz`/`pgMar`；缺失时按 A4 兜底。
- 段落/标题/列表项的 `fmt`：`{align, indent_left_mm, indent_right_mm, indent_first_mm（负值=悬挂）, space_before_mm, space_after_mm, line_height（倍数）, line_height_mm（固定值）}`。
- 段落/标题/列表项的 `runs`：`[{text, size_pt, bold, italic, underline, color, font}]`。**约束**：`runs.map(r => r.text).join('')` 必须等于 `text`；不等时服务端不下发 `runs`（画布退回纯文本渲染），避免「显示的字」和「回写的字」不一致。
- 表格：`col_width_mm[]`（`w:tblGrid`）、`fixed_layout`、`width_mm`、`align`；单元格：`grid_span`（`w:gridSpan`）、`v_merge`（`restart|continue`）、`row_span`（服务端算好的跨行数）、`hidden`（被纵向合并吞掉的格）、`shading`、`valign`。
- ⚠️ **`cells` 数组必须与 `w:tr/w:tc` 一一对应**（`/apply` 按下标写格）。被纵向合并吞掉的格仍然保留在数组里，只多一个 `hidden:true` 让前端跳过绘制 —— 绝不能从数组里删掉，否则后面所有格子整体错位。

- 二维码：`{"type":"qrcode","text":"任意文本","size":120}`，size 为成品在文档中的显示宽度（mm）；用 qrcode + Pillow 生成 PNG（高 box_size），再用 docxtpl InlineImage 插入。
- 富文本：`{"type":"html","content":"<b>粗</b><ul><li>..</li></ul>"}` 或 `{"type":"markdown","content":"**粗**"}`。
  - markdown 先用 `markdown` 库转 HTML；
  - HTML **白名单**标签：`b strong i em u s strike br p span ul ol li font color?`，仅允许 `color`（`style="color:#xxx"` 或 `color="red"`）、`bold/italic/underline`；其余标签剥离（不报错）；事件属性、`javascript:` 一律清除；
  - 转成 docxtpl `RichText`（`add` 分段，支持加粗/斜体/下划线/颜色、列表项前加 `• `/`1. `、`<br>` 换行、段落换行）。
- 普通字符串中的 `\n` 按 docxtpl 行为处理（换行符在 run 中生效）。

#### 3.6.2 出图端点 `POST /signature/render`、`POST /seal/render`（multipart，返回 image/png）

Go 服务在签章注入阶段（§4.5.1「签章进阶」）调用；也可单独用作出图预览。

`/signature/render` 字段：`signer_name`（2–20 字）、`font_id`（取自 `GET /fonts`）、`width_mm`（20–120，默认 60）、`with_timestamp`（true/false）、`timestamp`（ISO 8601 字面时间）、`timestamp_expr`（**日期表达式 JSON 文本**，如 `{"$date":"now","format":"%Y-%m-%d %H:%M"}`）、`timestamp_prefix`（默认 `服务器时间：`）。

- 时间优先级：`timestamp_expr` > `timestamp` > 容器当下时间；两者同时给 → `422 invalid_timestamp`。
- `timestamp_expr` 在**本次调用**求值，语义与 §4.5.1 日期表达式一致（默认 `Asia/Shanghai`）；表达式非法一律 422，不做静默降级。
- 给了 `timestamp_expr` 即视为要印时间，无需再传 `with_timestamp`（Go 侧仍会显式传 `true`）。
- `timestamp` 给了但解析不了 → 422（老版本会静默退回「现在」，等于把 `2026-13-45` 印成今天）。

`/seal/render` 字段：`unit_name`、`seal_name`、`shape`（circle|ellipse）、`radius_mm`、`star`。印章不印时间。

### 3.7 跨 run 预处理（M1 必须实测攻克）
- 用 lxml 操作 `word/document.xml`（以及 header/footer 部件）；逐段落（w:p，含表格单元格内段落）：把段落内所有 `w:r/w:t` 文本拼接，若拼接结果包含 `{{` `}}` `{%` `%}` `{#` `#}` 且这些标记跨 run 分布，则把该段落内全部文本合并进**第一个含文本的 run**（保留其 rPr），其余 run 的 `w:t` 清空（保留 run 与 rPr），避免破坏样式；
- 对 `{%tr ... %}` 这种落在表格单元格段落里的标签同样处理；
- 预处理必须幂等（对已规范模板再跑一遍不改变结果）；
- 输出诊断：每做一处合并，记一条 warning（含所在位置描述）；无法配对的标记记 error。

### 3.8 环境变量（render-service）

`PORT=8101`、`MAX_TEMPLATE_MB=10`、`MAX_DATA_MB=20`、`MAX_IMAGE_MB=5`、`RENDER_TIMEOUT_SECONDS=60`、`LO_CONCURRENCY=2`、`PREVIEW_TTL_MINUTES=30`、`PREVIEW_MAX_PAGES=20`、`IMAGE_ALLOW_PRIVATE=false`、`TMP_DIR=/tmp/docforge`、`MAX_INLINE_IMAGE_WIDTH_MM=146`、`MAX_INLINE_IMAGE_HEIGHT_MM=60`、`DEFAULT_IMAGE_DPI=96`。

### 3.9 依赖与工程

- `requirements.txt`：fastapi、uvicorn[standard]、python-multipart、docxtpl、jinja2、lxml、python-docx、qrcode[pil]、pillow、markdown、beautifulsoup4、html5lib、requests、pypdf（或 pdf2image）、pytest、httpx（测试用）。
- 代码分层：`app/main.py`（路由）、`preprocess.py`（跨 run）、`scanner.py`（扫描/schema/示例）、`richdata.py`（图片/二维码/富文本）、`office.py`（LibreOffice/pdftoppm 封装与 profile 隔离）、`security.py`（docx 校验/大小/URL 策略）、`config.py`、`models.py`（pydantic）。
- Dockerfile：`python:3.12-slim`，装 `libreoffice-writer`（或 libreoffice 核心包）、`poppler-utils`、`fonts-noto-cjk`、`fonts-noto-cjk-extra`，非 root 运行，uvicorn 0.0.0.0:8101。
- pytest 至少覆盖：① 跨 run 占位符（用从 code-audit-report 复制来的原始 `模板.docx` 作夹具，断言渲染结果无残留 `{{`）；② 整表循环（渲染 N 条 → 输出含 N 张表、附录编号段落 N 个）；③ 条件块；④ 二维码（断言 docx 内新增 image 部件）；⑤ 富文本（加粗/列表/颜色）；⑥ /scan 的 variables/schema 形状；⑦ /validate 缺字段报错路径；⑧ 预处理幂等。

## 4. Go 服务 HTTP 契约（chi，监听 8100，前缀 `/api/v1`）

### 4.1 静态资源与 SPA

- 内嵌根包 `docforge.WebFS`（`web/dist`）：`GET /assets/*` 等 dist 内文件按内容类型返回（长缓存）；
- 其余**非 `/api/`、非已注册静态文件**的 GET（含前端 history 路由 `/templates/...`）一律回退返回 `dist/index.html`（200）；
- `GET /` → index.html。

### 4.2 认证（本地账号密码 + 可选 OIDC，两条腿并存，两者永不互斥）

**users 模型**：id `u_<12hex>`、username 唯一（3-64 位，字母数字/下划线/连字符）、password_hash 可空（argon2id PHC 编码串；纯 OIDC 账号为空）、email、display_name、role（`admin`/`member`）、status（`pending`/`active`/`disabled`）、source（`local`/`oidc`）、oidc_sub 唯一可空、guide_seen、last_login_at、created_at、updated_at（见 §5）。

**会话**：登录成功种 HttpOnly cookie `df_session`（SameSite=Lax、Path=/、7 天有效；值为 `AUTH_SECRET` 签名的令牌，载荷含 user_id 与过期）；API 侧 `X-API-Key: df_xxxx` 与 cookie 二选一。每次请求校验用户仍为 active。

**本地账号密码（始终可用，不得被 OIDC 开关禁用）**
- `POST /api/v1/auth/login` `{username,password}` → 种 cookie，200 返回与 /me 相同的主体；
- 密码规则：**至少 8 位且必须同时含字母和数字**（前后端都校验，违规 422 并指明字段）；
- 登录限速：同账号连续失败 5 次锁定 5 分钟（423，响应给出剩余秒数）；同 IP 对登录/注册/setup 类接口限流 20 次/分钟（429）；错误信息统一"用户名或密码错误"（不透露账号是否存在）；**日志严禁打印密码或令牌**；
- `POST /api/v1/auth/logout`（清 cookie）；`GET /api/v1/auth/me`（形状见下）；`PUT /api/v1/auth/guide` `{seen}`；
- 自助注册：`ALLOW_REGISTER=true` 时开放 `POST /api/v1/auth/register` `{username,password,email?,display_name?}`，注册即 `pending` 需管理员审核；默认关闭时该端点 404；
- 首个管理员初始化：启动时 users 为空——若配置了 `ADMIN_USERNAME`+`ADMIN_PASSWORD` 则自动创建 active admin；否则系统处于未初始化状态：公开 `GET /api/v1/auth/setup-status` → `{initialized, oidc_enabled, registration_enabled}`，并开放一次性 `POST /api/v1/auth/setup` `{username,password,display_name?,email?}`（创建首个 admin 且直接登录，完成后永久 404）。

**OIDC（可选叠加，`OIDC_ENABLED=true` 才渲染入口/生效）**：Authorization Code + **PKCE(S256)** + state，标准 Discovery（从 `{OIDC_ISSUER}/.well-known/openid-configuration` 取端点，**禁止硬编码端点**）：
- `GET /api/v1/auth/oidc/start` → 302 授权地址（state 与 PKCE verifier 存短期签名 cookie，非登录态也可走，绑定流程复用）；
- `GET /api/v1/auth/oidc/callback?code&state` → 换 token、取 sub 与 **verified email**；
  - 关联顺序：oidc_sub 直配 → verified email 匹配本地账号（email 或 username 相同）并回填 oidc_sub；
  - 未找到：`OIDC_AUTO_PROVISION=active`（默认）自动建 source=oidc、role=member、status=active 账号（username 取 preferred_username/email 前缀，冲突加数字后缀）；`=pending` 建待审核；`=none` 拒绝并提示联系管理员；
  - 目标账号 disabled → 403；
- 已登录用户绑定/解绑：`GET /api/v1/auth/oidc/bind/start`（带会话，回调把 oidc_sub 绑到当前用户）；`POST /api/v1/auth/oidc/unbind`（仅当该用户 has_password 时允许，否则 400"请先设置登录密码"）；
- env：`OIDC_ENABLED`、`OIDC_ISSUER`（无默认值，必须由部署方填写）、`OIDC_CLIENT_ID`、`OIDC_CLIENT_SECRET`、`OIDC_REDIRECT_URL`（默认 `<EXTERNAL_BASE_URL>/api/v1/auth/oidc/callback`）、`OIDC_AUTO_PROVISION`（active/pending/none）。

**开发模式**：`AUTH_DEV=true` 时信任 `X-Dev-User: <name>` 头（自动 upsert 为 active admin 便于联调）；本地登录接口同时照常可用；生产必须 false。

**个人中心（登录态）**：
- `POST /api/v1/auth/password` `{old_password,new_password}`：改本人密码；无密码的 OIDC 账号 old_password 可空（用于首次设置密码）；新密码同样执行规则校验；
- `GET /api/v1/auth/me` 返回：
```json
{"user":{"id":"u_..","username":"admin","display_name":"...","email":"...","role":"admin",
 "status":"active","source":"local","oidc_bound":false,"has_password":true,"last_login_at":"..."},
 "auth_mode":"local|oidc|dev","oidc_enabled":true,"registration_enabled":false,"initialized":true,
 "guide_seen":false,"base_url":"http://localhost:8100"}
```
未登录 401；未激活(pending/disabled) 不放行并清除会话。

**用户管理（仅 admin，非 admin 一律 403）**
- `GET /api/v1/admin/users?status=&source=&q=&page=&page_size=` → `{items:[User],total,pending_count}`；
- `POST /api/v1/admin/users` `{username,password,display_name?,email?,role}`（创建即 active）；
- `POST /api/v1/admin/users/{id}/approve`（pending→active）；
- `POST /api/v1/admin/users/{id}/disable`、`/enable`；
- `POST /api/v1/admin/users/{id}/role` `{role}`；
- `POST /api/v1/admin/users/{id}/reset-password` `{new_password}`；
- 保护规则：不能禁用/降级/删除自己（400）；admin 操作写审计日志（内存表或日志即可，一期记操作人/动作/对象）。

**密码哈希**：argon2id（`golang.org/x/crypto/argon2` 的 IDKey，参数 time=2、memory=64MiB、threads=4、keyLen=32、salt 16 字节，标准 PHC 字符串存库）。

**限流**：API Key 按 `qps_limit` 做单实例令牌桶（内存），超限 429；认证类接口按账号/IP 限流如上。

### 4.3 模板

- `POST /api/v1/templates`（multipart，session）：字段 `file`、`code`（`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`，创建后不可改）、`name`、`category?`、`description?`、`visibility?`=private。
  服务端：校验 code 唯一（冲突 409）、调 render `/scan`（落库扫描结果；诊断含 error 则**不创建**，422 回传诊断）、文件存盘、生成首页缩略图（异步失败不阻断，thumbnail 为空即可）、版本号从 `1.0.0` 起。
- `GET /api/v1/templates?q=&category=&enabled=true|false&page=1&page_size=20` → `{items:[Template],total}`。
- `GET /api/v1/templates/{id}` → 模板详情 + `current_version`（含 variables/sample_json/json_schema/diagnostics/version）+ `versions[]`（id、version、是否当前、上传时间）+ `integration`（接入卡片所需信息，后端只给数据，代码示例由前端拼）。
- `GET /api/v1/templates/code/{code}` → 同上（按 code 解析；停用/不存在 → 404/423，generate 场景 fail-closed）。
- `POST /api/v1/templates/{id}/versions`（multipart `file`，`activate` 默认 true）→ 新版本号（语义化递增：同结构 patch+1，否则 minor+1；一期可直接 `1.0.{n}` 简单递增，允许前端不传）。
- `POST /api/v1/templates/{id}/versions/{vid}/activate`；
- `POST /api/v1/templates/{id}/disable`、`/enable`（停用后 generate/render 423 报错）。
- `GET /api/v1/templates/{id}/thumbnail?version_id=` → `image/png`（透传 render 预览首页，磁盘缓存到 `volumes/thumbnails/`）。
- `GET /api/v1/templates/{id}/versions/{vid}/download` → 原始 docx（session）。
- `GET /api/v1/wizard/blank-template.docx` → 内嵌 `BlankTemplateDOCX`（无需登录也可？保持需登录）。

Template 对象：
```json
{"id":"tpl_xxx","code":"audit-report","name":"代码审计报告","category":"安全交付","description":"...",
 "visibility":"public","enabled":true,"current_version_id":"ver_xxx","created_by":"...","created_at":"...",
 "updated_at":"...","current_version":{"id":"ver_xxx","version":"1.0.0","variables":[...],"sample_json":{...},
 "json_schema":{...},"diagnostics":[...],"file_size":18247,"created_at":"..."}}
```

### 4.4 投递即生成 `POST /api/v1/generate`（session 或 X-API-Key）

请求：
```json
{
  "template_code": "audit-report",
  "version": "1.0.0",            // 可选，缺省当前版本
  "data": { ... },               // 单份；与 records 二选一
  "records": [ { ... }, { ... } ],// 批量，多条
  "format": "docx",              // docx|pdf|both，默认 docx
  "filename": "{{项目编号}}_审计报告", // 可选；文件名规则（不含扩展名），仅取顶层变量
  "callback_url": "https://caller/on-rendered", // 可选
  "external_ref": "ORDER-123",   // 可选；幂等键
  "sync": true                   // 可选；缺省：单份且 JSON ≤ 1MB 自动同步
}
```
规则：
- 编号不存在/停用 → 404/423，不建任务；
- 幂等：同一 `api_key_id`（UI 调用按 user_id）+ `external_ref` 且任务未过保留期/未失败终结？（窗口期=保留期，状态非 failed）重复请求直接返回原任务，**不重复渲染**；
- 上限：`records ≤ MAX_RECORDS`（默认 1000）、总 JSON ≤ 20MB；超限 413/422；
- 校验调 render `/validate`，失败 422 同步返回（含字段路径）；
- 建 job + N 条 document（queued）；worker 领取；
- 同步模式：阻塞至该任务终结（上限 30s，超时则转异步并返回 queued，响应头/字段说明）；
- 异步响应 202：
```json
{ "job_id":"jb_20261018_0001", "status":"queued", "sync":false, "template_code":"audit-report",
  "external_ref":"ORDER-123",
  "status_url":"/api/v1/jobs/jb_20261018_0001",
  "download_url":"/api/v1/jobs/jb_20261018_0001/download",
  "signed_url":"/api/v1/s/<job-token>" }
```
- 同步单份成功 200：
```json
{ "job_id":"jb_...", "status":"succeeded", "sync":true,
  "documents":[ {"document_id":"dc_...","filename":"AP-001_审计报告.docx",
    "download_url":"/api/v1/documents/dc_.../download",
    "pdf_download_url":"/api/v1/documents/dc_.../download?format=pdf",
    "signed_url":"/api/v1/s/<doc-token>"} ] }
```
- 同步单份失败 422：标准错误信封，details 带字段路径。

**下载链接语义（重要）**：
- `download_url` 是**主链接**，永远指向这份产物实际存在的那个文件：有 docx 就是 docx；`format=pdf` 的任务没有 docx，它就等于 `.../download?format=pdf`。
- `pdf_download_url` **只在生成过 PDF 时出现**（`format=docx` 的产物没有这个字段），不要假设它总在。
- 请求不存在的格式返回 `404 format_not_available`（不是 `410 expired`）；`410 expired` 专指「生成过但文件已被清理 / 过了保留期」。

### 4.5 UI 渲染 `POST /api/v1/render`（session）
body `{"template_id":"tpl_..","version_id":"ver_..","data":{...}|"records":[...],"format":"docx|pdf|both","filename":"..."}`；
行为与 generate 相同（source=ui），区别是用 template_id。单份同步返回同 §4.4。

### 4.5.1 `data` 里的两种特殊值

模板扫描出 `sig_*` / `seal_*` 变量时，`data` 里放的是**资源 ID**（不是图片）；
其余变量若需要「出具当天的日期」，放一个日期表达式对象。两者都在渲染前由服务端解析。

#### 签章：传资源 ID，服务端按授权注入

```json
{
  "sig_审核人": "sig_a1b2c3d4e5f60718",
  "seal_公司公章": "seal_0f1e2d3c4b5a6978"
}
```

- 传有效 ID → 校验 `status=approved` 且**调用者持有有效授权**（或为所有者/管理员），
  通过后写入图片并**原子扣减一次授权次数**；不通过返回 `403 grant_denied` 并带原因。
- 传 `""` / `null` → 退化为「**变量名本身就是资源 ID**」：编辑器插入签章占位符时，
  变量名就是资源 ID（`{{ sig_a1b2c3... }}`），这条路径为存量模板保留。
- 传既不是 ID 也不是空的普通字符串 → 视为调用方想直接印这段文字，原样保留，不查库。
- 传图片对象（`{"type":"image","src":"data:image/png;base64,..."}`）→ 完全交给调用方，
  不校验授权、不扣次数。
- 幂等/补跑注意：重试同一条记录会再扣一次次数。需要「不重复扣」的场景，
  把当天已生成的产物直接下载，而不是重跑任务。
- 只用自己拥有的或别人授权给你的资源；不知道 ID 时用
  `GET /api/v1/seals/usable`、`GET /api/v1/signatures/usable` 取「我能用的」清单。

「资源 ID」的判定：`^(sig|seal)_[0-9a-zA-Z]{6,}$`。判这一下的原因是模板变量名和资源 ID
用的是同一套前缀，光看前缀分不清；判错的后果不对称——把 ID 当普通字符串会丢掉签章，
把普通字符串当 ID 会白查一次库并 422。

#### 签章进阶：传参数对象，**出图那一刻**现场生成（可带时间）

前面的形态里，签章资源存的是**一张已经出好的图**，签署时间在资源创建那一刻就被烤进
像素了：同一张图用在三份文件上，三份印的时间都一样、且都是制图那天。想在合同上印
「出具时间」而不是「制图时间」，老办法只能每次重建一个签名资源。

用一个带 `$sig` / `$seal` 标记的对象，把「引用哪份资源」和「怎么出图」分开写：

```json
{
  "sig_审核人": {
    "$sig": "sig_a1b2c3d4e5f60718",
    "font": "liu-jian-mao-cao",
    "with_timestamp": true,
    "timestamp": { "$date": "now", "format": "%Y-%m-%d %H:%M" },
    "timestamp_prefix": "签署时间：",
    "width_mm": 60
  }
}
```

- **授权口径完全不变**：`$sig` 指向的资源就是被校验、被扣次数的那一条。参数对象不能
  凭空获得权限——传个 `font` 就自己画一个签贴上去，是这一版刻意堵掉的洞。
- **字体/署名默认继承资源**：资源里有 `font_id`/`signer_name` 就不必重复写；写了则覆盖。
  所以同一份资源既当素材又当授权凭证，换字体不用重建资源。
- **时间在出图那一刻求值**：`timestamp` 里的日期表达式原样转交给 render-service，
  由它按 §4.5.1 日期表达式的语义（默认 `Asia/Shanghai`、支持 `offset_*`/`format`）
  在生成 PNG 时求值。每份产物印各自的出具时间。**Go 侧不做任何时间计算**，
  避免出现两套「北京时间怎么算」的答案。
- `timestamp` 也接受 ISO 8601 字面字符串（`"2026-10-25T09:30:00+08:00"`，补跑还原原始签署时间时用）。
  给了 `timestamp` 就等于要印时间，不必再写 `with_timestamp`。
- `timestamp_prefix` 默认 `服务器时间：`；公文场景通常要改成 `签署时间：`/`签署日期：` 或直接给空串。
- 只改 `width_mm`/`height_mm` **不会**触发现场出图（读原图就够），少一次往返。

| 字段 | 签名 | 印章 | 说明 |
|---|---|---|---|
| `$sig` / `$seal` | ✅ | ✅ | 资源 ID；空串 = 用变量名当 ID |
| `font` | ✅ | — | 字体 id，取自 `GET /fonts` |
| `signer_name` | ✅ | — | 2–20 字 |
| `with_timestamp` | ✅ | ❌ | `true` 时在签名下方印一行时间小字 |
| `timestamp` | ✅ | ❌ | ISO 8601 字符串 或 `{"$date":...}` 表达式 |
| `timestamp_prefix` | ✅ | ❌ | 时间前缀，默认 `服务器时间：` |
| `unit_name` / `seal_name` | — | ✅ | 印章上/下方的文字，缺省继承资源 |
| `shape` / `radius_mm` | — | ✅ | `circle|ellipse`；半径缺省 = `width_mm/2` |
| `width_mm` / `height_mm` | ✅ | ✅ | 摆放尺寸（mm），高度留空按原图比例反算 |

**严格性**：未知字段、类型不符、`sig_` 键配 `$seal` 标记、印章带时间、资源没记字体
又没在参数里给 —— 一律 `422 data_invalid`，`message` 里带变量名。渲染服务不可用时
同样报错，**绝不静默退回预置图**（用户写了 `with_timestamp` 却拿到一张没有时间的图，
是他自己看不出来的那种错）。

#### 日期：传生成规则，渲染那一刻现场算

```json
{
  "报告日期": { "$date": "today" },
  "有效期至":  { "$date": "today", "offset_days": 30 },
  "签发时间":  { "$date": "now", "format": "%Y年%m月%d日 %H:%M" },
  "结算月末":  { "$date": "today", "offset_months": 1, "last_day_of_month": true },
  "固定日期":  { "$date": "2026-01-01", "format": "%Y年%m月%d日" }
}
```

字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `$date` | ✅ | `today` \| `now` \| `YYYY-MM-DD` \| `YYYY-MM-DDTHH:MM[:SS][±HH:MM]` |
| `offset_days` | | 整数，可负，±36600 |
| `offset_months` | | 整数，可负，±1200；月末自动钳制（1/31 +1月 → 2/28） |
| `offset_years` | | 整数，可负，±100 |
| `last_day_of_month` | | `true` 时跳到当月最后一天（在 months/years 之后、days 之前执行） |
| `format` | | strftime，默认 `%Y-%m-%d`，长度 ≤64 |
| `tz` | | `Asia/Shanghai`（默认）\| `UTC` \| `±HH:MM` |

**为什么要有它**：报告日期必须是出具当天。让调用方自己算有三个坑——调用方时区可能不同、
批量任务跨零点会前后半批不同日、补跑历史任务会把原始日期覆盖成今天。
补跑要还原原始日期时，就传字面日期（`{"$date":"2026-01-01"}`）。

**严格性**：未知字段、拼错的 base、超范围的偏移一律 `422 data_invalid`，
错误信封的 `details[].path` 精确到字段路径。不做静默降级——把 `todya` 当普通字符串
印在报告日期栏里、而校验还是绿的，是最坏的结果。

**校验与渲染看到同一份数据**：`/validate` 也会先解析日期表达式再校验类型，
不存在「校验不过但能渲染」的分裂。

### 4.6 预览 / 校验（session，工作台用）

- `POST /api/v1/preview` body `{"template_id":"tpl_..","version_id":"ver_..?","data":{...},"max_pages":6}` → 透传 render 结果，但 `pages[].url` 改写为 `/api/v1/preview/{pv}/page/{n}`（本服务代理）。
- `GET /api/v1/preview/{pv}/page/{n}` → 代理回 render 的 PNG（pv 用一次性映射缓存到内存，TTL 30 分钟）。
- `POST /api/v1/validate` body `{"template_id":"tpl_..","version_id":"ver_..?","data":{...}}` → render 校验结果。

### 4.7 任务

- `GET /api/v1/jobs?page=&page_size=&status=&source=` → 分页列表。
- `GET /api/v1/jobs/{id}`：
```json
{ "id":"jb_..", "status":"running", "source":"batch", "template_id":"tpl_..", "template_name":"...",
  "template_code":"audit-report", "version":"1.0.0", "format":"docx", "filename_rule":"...",
  "total":3,"succeeded":2,"failed":0,"progress":0.67,
  "callback_url":"...","callback_status":"pending","callback_attempts":1,"external_ref":"...",
  "zip_download_url":"/api/v1/jobs/jb_../download", "signed_url":"/api/v1/s/<job-token>",
  "errors":[ {"record_index":1,"error":"...", "path":"items[0].name"} ],
  "documents":[ {"document_id":"dc_..","record_index":0,"filename":"...docx","status":"succeeded",
                 "download_url":"...","pdf_download_url":"...","signed_url":"...","error":""} ],
  "created_at":"...","started_at":"...","finished_at":"...","expires_at":"..." }
```
- `GET /api/v1/jobs/{id}/download` → zip（N 份；每份文件名按 filename_rule；`both` 时 docx/pdf 分两个子目录；失败条目写 `errors.json` 放 zip 根）。任务未终结时 409。
- `POST /api/v1/jobs/{id}/retry` body `{"only_failed":true}` → 重置（only_failed 时仅 failed 文档）为 queued，清空旧产物文件，返回 job 详情。

### 4.8 文档生成中心

- `GET /api/v1/documents?page=&page_size=&source=&status=&template_id=&q=&from=&to=` → `{items:[Document],total}`；
- `GET /api/v1/documents/{id}` → Document 详情，含 `input_data`（原始 JSON）、`operator`（用户名或 API Key 名称）、`duration_ms`、`expires_at`；
- `GET /api/v1/documents/{id}/download?format=docx|pdf`（session 或 API Key）→ 文件；`download_count` 自增；过期 410；
- `POST /api/v1/documents/{id}/regenerate` → 用原 input_data 新建 ui 任务，返回 §4.4 同步/异步响应；
- `POST /api/v1/signed-url` body `{"document_id":"dc_.."}` 或 `{"job_id":"jb_.."}` → `{"signed_url":"/api/v1/s/...","expires_at":"..."}`；
- `GET /api/v1/s/{token}` → 文件流（见 §4.10）。

### 4.9 API Key（session）

- `GET /api/v1/keys` → `[{"id":"ak_..","name":"扫描平台","prefix":"df_a1b2c3d4","qps_limit":5,"max_records":1000,"enabled":true,"last_used_at":"...","created_at":"..."}]`；
- `POST /api/v1/keys` body `{"name":"...","qps_limit":5,"max_records":1000}` → 201，**明文 key 仅此一次返回**：`{"id":"ak_..","key":"df_<40hex>","prefix":"df_xxxxxxxx",...}`；存储 sha256(key) 哈希；
- `POST /api/v1/keys/{id}/disable` | `/enable`；`DELETE /api/v1/keys/{id}`。

### 4.10 签名直链（HMAC）

- 密钥：`SIGNED_URL_SECRET`（未配置则启动时随机生成并日志告警：重启后旧链接失效）；
- payload（字符串）：`d:<document_id>:<exp_unix>` 或 `j:<job_id>:<exp_unix>`；
- token = `b64url(payload)` + `.` + `b64url(hmac_sha256(secret, payload))`；
- TTL：`SIGNED_URL_TTL_HOURS`（默认 24）；
- 校验：签名常量时间比较、未过期、产物存在且未过保留期；document 默认给 docx，`?format=pdf` 给 pdf；job 给 zip；
- 过期清理前签名自然失效（exp 与保留期双重保险）。

### 4.11 健康检查

`GET /api/v1/healthz` → `{"status":"ok","db":"ok","render":"ok"}`（render 不通时 render:"error"，整体 503）。

## 5. Go 服务内部实现约定

- 数据模型（GORM 自动迁移，jsonb 用 `datatypes.JSON` 或 PG jsonb；SQLite 下回退为 text）：
  - `users`（id、username 唯一、password_hash 可空、email、display_name、role admin/member、status pending/active/disabled、source local/oidc、oidc_sub 唯一可空、guide_seen、last_login_at、created_at、updated_at；见 §4.2）；
  - `templates`（id、code 唯一索引、name、category、description、visibility、enabled、current_version_id、created_by、created_at、updated_at）；
  - `template_versions`（id、template_id 索引、version、(template_id,version) 唯一、file_path、file_size、variables jsonb、sample_json jsonb、json_schema jsonb、diagnostics jsonb、uploaded_by、created_at）；
  - `api_keys`（id、user_id、name、key_hash 唯一、prefix、qps_limit、max_records、enabled、last_used_at、created_at）；
  - `jobs`（id 主键 string、template_id、version_id、created_by_user_id、api_key_id 可空、source、external_ref、idempotency_key 唯一索引（可空，可空字段用部分唯一索引：api_key_id+external_ref）、callback_url、callback_status、callback_attempts、status、total、succeeded、failed、format、filename_rule、zip_path、error_details jsonb、created_at、started_at、finished_at、expires_at）；
  - `documents`（id、job_id 索引、record_index、filename、docx_path、pdf_path、status、error、input_data jsonb、download_count、duration_ms、created_at、started_at、finished_at）；
  - sessions 表（token 哈希、user_id、created_at、expires_at）。
- 数据库：默认 PostgreSQL（`DATABASE_URL` 或 POSTGRES_HOST/PORT/USER/PASSWORD/DB 拼装）；**开发兜底**：`DATABASE_URL=sqlite:///./volumes/docforge.dev.db` 时用纯 Go 驱动 `github.com/glebarez/sqlite`（无 CGO），便于无 Docker 本机联调；docker 中必须 PG。
- worker：单 goroutine + 状态机，`queued → running → succeeded/partial/failed`；领取用事务 `SELECT ... FOR UPDATE SKIP LOCKED`（PG；SQLite 用事务串行）；逐条：调 render `/render`（docx，必要时再 /render format=pdf 或一次请求 format=pdf，both 时两次）、文件落 `volumes/jobs/{job_id}/{document_id}.docx|.pdf`、更新 document；失败隔离，错误写 documents.error 与 job.error_details；全部终结后打包 zip（archive/zip）；随后触发回调；
- 保留期清理：每小时 ticker，`RETENTION_DAYS`（默认 7）到期的 job/documents 删文件、状态置 failed/记录删除策略（一期：删文件 + 状态标记 expired 列表不再返回，或直接删记录；选**删文件+标记 expired**，文档列表默认隐藏 expired）；
- 回调：job 终结后若有 callback_url，POST JSON（PRD §11 形状：job_id/external_ref/status/total/succeeded/failed/downloads[]/errors[]，downloads 里给签名直链）；**SSRF 防护**：scheme 仅 http/https；DNS 解析后拒绝 127.0.0.0/8、10/8、172.16/12、192.168/16、169.254/16、::1、fc00::/7、IPv4-mapped IPv6 等内网/回环/链路本地；`CALLBACK_ALLOW_PRIVATE=true` 时放开但必须命中 `CALLBACK_HOST_ALLOWLIST`（逗号分隔，精确 host/后缀）；禁止跟随重定向（或仅允许 1 次且目标同校验）；超时 10s；失败指数退避 1s/4s/16s 共 3 次（异步，不阻塞 worker 领下一个任务），记录 callback_attempts/status。
- 文件名规则：`{{项目编号}}_审计报告` 只做**顶层变量**替换（正则 `{{\s*([\w一-龥]+)\s*}}`，取不到值用 record_index），再清洗 Windows/Unix 非法字符 `\\ / : * ? " < > |` 与控制字符，截断 120 字符；兜底 `{code}_{index:03d}`；扩展名按格式。
- 存储：`DATA_DIR`（默认 `./volumes`）下 `templates/{tpl_id}/{ver_id}/original.docx`、`jobs/{job_id}/*`、`thumbnails/*`、`tmp/*`。
- 内置模板播种：启动时若不存在 code=`audit-report` 的模板，用根包 `docforge.AuditTemplateDOCX` 建为 `public` 模板（code `audit-report`，name `代码审计报告`，version 1.0.0，sample_json 用 `AuditSampleJSON`，并调 render 扫描；若 render 不通则扫描结果留空、模板可见但标记待扫描，启动日志说明）。
- 所有外发/上传限制：模板 10MB、JSON 20MB、图片 5MB、records 1000；渲染 HTTP 调用超时（render 客户端：render 70s、scan 30s、preview 90s）。
- 工程：`go build ./...`、`go vet ./...`、`go test ./...` 必须通过；单测至少覆盖 HMAC 签发/校验/过期、SSRF 判定、文件名规则、幂等键逻辑、状态机流转（可用 sqlite 内存库）。

## 6. 环境变量总表（.env.example 由集成方按此生成）

Go 服务：`HTTP_ADDR=:8100`、`EXTERNAL_BASE_URL=http://localhost:8100`、`DATABASE_URL`（或 `POSTGRES_HOST/PORT/USER/PASSWORD/DB`）、`RENDER_SERVICE_URL=http://render:8101`、`DATA_DIR=/data`、`SIGNED_URL_SECRET=change-me`、`SIGNED_URL_TTL_HOURS=24`、`RETENTION_DAYS=7`、`AUTH_SECRET`（会话 cookie 签名，必填）、`AUTH_DEV=true`、`ADMIN_USERNAME`（可选）、`ADMIN_PASSWORD`（可选）、`ALLOW_REGISTER=false`、`OIDC_ENABLED=false`、`OIDC_ISSUER=`、`OIDC_CLIENT_ID`、`OIDC_CLIENT_SECRET`、`OIDC_REDIRECT_URL`（缺省用 EXTERNAL_BASE_URL 拼接）、`OIDC_AUTO_PROVISION=active`、`MAX_RECORDS=1000`、`MAX_JSON_MB=20`、`MAX_TEMPLATE_MB=10`、`CALLBACK_ALLOW_PRIVATE=false`、`CALLBACK_HOST_ALLOWLIST=`、`SEED_BUILTIN=true`。

## 7. 前端工程契约（web/）

- Vite 5 + React 18 + TypeScript + @arco-design/web-react + @arco-design/web-react/icon；react-router-dom v6；@tanstack/react-query v5；@monaco-editor/react（**必须配置本地 monaco-editor，禁止运行时从 CDN 拉取**：`loader.config({ monaco })` + Vite worker 配置，确保构建产物自包含、内网可断网运行）。
- `npm run build` 必须 TS 零报错产出 `web/dist`；`vite.config.ts` 开 dev server 代理 `/api` → `http://localhost:8100`。
- 视觉：Arco Light 专业控制台；主色 arcoblue；**全站禁止 emoji、禁止手绘 SVG 当主图标、禁止紫渐变与花哨插画**；图标只用 Arco Icon；字体栈 `PingFang SC, Microsoft YaHei, -apple-system, sans-serif`；对齐/间距/字号统一；表格分页、骨架屏 loading、按钮主次分明。
- 文案口径：品牌为「铸版 / ZHUBAN」——**全站禁止出现"公司/团队/我们团队"等字样**；产品名 docforge（代码标识符），界面名铸版。
- 所有请求同源 `/api/v1`，`credentials: 'include'`；401 → 跳 `/login?redirect=...`（登录后回跳）；403 → 独立 403 页面；react-query 管数据。
- 页面（全部可用，非骨架）：
  1. **登录页**（第一印象，大厂水准，克制专业）：独立全屏路由 `/login`，左侧品牌区（铸版标识、一句话定位）+ 右侧 Arco Card 表单；Arco Form 用户名/密码（前端校验：密码 ≥8 位且含字母+数字，回车提交，按钮 loading，失败 Arco Alert 展示后端中文错误，账号锁定显示剩余时间）；表单下方分隔线 +「使用 APSSO 登录」按钮（**仅当 setup-status/me 的 oidc_enabled=true 才渲染，未启用不得出现灰按钮**，点击跳 `/api/v1/auth/oidc/start`）；`registration_enabled=true` 时显示注册入口（注册成功提示等待管理员审核）；`initialized=false` 时整页改为**首次初始化向导**（创建管理员：用户名/密码/确认密码/显示名，POST /auth/setup 成功后直接进入）；dev 模式（AUTH_DEV）页面可额外提供一个"开发一键登录"区域（X-Dev-User 仅 dev 生效，后端可提供 dev-login 便捷端点，但**本地账号密码登录是主路径，始终可用**）。
  2. 控制台布局：固定侧边栏（docforge logo + 菜单：模板库 / 渲染工作台 / 文档生成中心 / API Key / 引导与帮助；**admin 角色额外显示「用户管理」，待审核 pending_count 用 Arco Badge 角标**）、顶部栏（显示名 + 角色 Tag（admin/member 不同色）+ 用户下拉：个人中心 / 退出登录）、面包屑；
  2.1. **个人中心**（抽屉/Modal）：基本信息（username/email/角色/来源 Tag）；修改密码（旧密码+新密码+确认；无密码 OIDC 账号隐藏旧密码走"首次设置密码"）；APSSO 绑定状态与「绑定/解绑」（绑定跳 /auth/oidc/bind/start；解绑需确认且仅在 has_password 时可用）；
  2.2. **用户管理**（仅 admin，路由 `/admin/users`）：Arco Table（用户名、显示名、邮箱、角色、状态、来源 local/oidc、APSSO 绑定、最后登录、创建时间、操作）；状态/来源/关键词筛选；操作：pending 行审核通过（approve）/拒绝（disable）、禁用/启用、角色切换、重置密码（弹窗输入新密码，同样校验）、新建用户（Modal，含初始密码/角色）；对自己的禁用/降级按钮置灰（后端亦 400）；
  3. 模板库：筛选（关键词/分类/启停）、表格或卡片（缩略图、名称、code、分类、状态、版本、操作）、上传向导（Steps：上传 docx → 扫描诊断（diagnostics 分级展示）→ 确认信息 code/名称/分类/可见性）、版本抽屉（列表/激活/回滚）、停用启用、接入卡片 Modal（Tabs：curl / Python / Go；用 EXTERNAL_BASE_URL（从一个 `GET /api/v1/bootstrap` 或 auth/me 扩展返回的 `base_url` 拼接；后端在 `/api/v1/auth/me` 里加 `base_url` 字段）+ 当前用户 key 占位 `df_在APIKey页面创建` + template_code + 示例 body；代码块一键复制；含轮询与回调示例）；
  4. 渲染工作台（路由带 template id）：三栏——左 模板信息+变量树（kind 用彩色 Tag）+「填充示例 JSON」；中 Monaco JSON（保存/校验，错误按 path 定位，JSON 语法错误直接标红）；右 预览（编辑后防抖 1.5s 调 /preview，页面图分页+放大，空态引导，水印提示）；顶栏 format 切换（docx/pdf）+「生成并下载」（调 /render，同步拿 signed_url 直接下载）；
  5. 文档生成中心：Arco Table（时间/来源 Tag/模板/文件名/记录数/进度 Progress/状态彩色 Tag/操作）、筛选（模板、状态、来源、时间 RangePicker）、行操作（下载 docx/pdf、复制签名直链、重试、查看输入 JSON Drawer +「用此数据再生成」）；批量任务行提供 zip 下载与失败明细 Modal（errors 列表）；轮询 running 任务（3s）；
  6. API Key：表格 + 创建 Modal（明文 key 只展示一次，Arco Alert 警告 + 一键复制）、限流字段、启停、删除、最后使用时间；
  7. 引导与帮助：重播首次三步 Tour（Arco `Tour`，若所用 Arco 版本无该组件则用 `driver.js`，在 README 注明）；模板制作向导（语法速查卡片 + 下载空白示例模板按钮 `/api/v1/wizard/blank-template.docx`）；各空状态用 Arco Empty + 下一步按钮；所有错误 Message/Alert 必须展示后端中文 message（含怎么改），禁止 console 式堆栈弹窗。

## 8. 验收口径（各自必须自检后再回报）

- 渲染服务：本机 `python -m pytest` 全绿（pdf 相关无 LibreOffice 可 skip）；用迁移模板 + 示例 JSON 真实渲染出 docx，解压校验：无残留 `{{`/`{%`、表格数 == items 数、附录编号段落（numPr）数 == items 数、富文本/换行/二维码图片部件存在；`samples/templates/audit-report.docx` 用原始 `模板.docx` 迁移而来（复制后在 docforge 内改，**禁止修改 code-audit-report 目录**）。
- Go：`go build ./...`、`go vet ./...`、`go test ./...` 全绿（在仓库根执行）；可用 sqlite 本机起服务跑通：首次初始化/本地账号登录（argon2id 密码校验、失败锁定）→ 建模板→渲染→文档列表→签名下载；OIDC 代码路径经代码评审（编译通过、Discovery 标准流程、PKCE/state 完整、邮箱关联/自动建号逻辑有单测）。
- 前端：`npm install` 与 `npm run build` 通过；dist 由 Go embed 后页面可打开；集成阶段由集成方用浏览器逐页截图验收。
- 禁止：在 code-audit-report 目录写任何文件、推送任何 Git 远端、在代码/配置里写真实密钥；`.env.example` 只放占位值。
