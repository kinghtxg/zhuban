# docforge Go 主服务 — 本机 sqlite 实测记录

> 环境：Windows，Go 1.25（工具链自动切到 go1.26.9 以兼容 x/crypto v0.58），PowerShell 5.1。
> render-service 由另一代理负责，本机未起真实服务；为完成端到端验证，仓库根放了一个最小 mock `mock_render.py`（实现契约 §3 的 `/scan /render /validate /preview /healthz`，返回固定假 docx 字节）。

## 启动命令

```powershell
# 1) mock 渲染服务（端口 18101，8100/8101 在本机被 Windows 保留端口占用）
cd C:\Users\41935\Doubao\chats\2026-10-17\new-chat\docforge
python mock_render.py

# 2) Go 主服务（sqlite 兜底，绝对路径避免 cwd 歧义）
$env:DATABASE_URL='sqlite:///C:/Users/41935/Doubao/chats/2026-10-17/new-chat/docforge/volumes/docforge.dev.db'
$env:DATA_DIR='C:/Users/41935/Doubao/chats/2026-10-17/new-chat/docforge/volumes'
$env:AUTH_DEV='true'; $env:AUTH_SECRET='test-secret'; $env:SIGNED_URL_SECRET='sign-secret'
$env:HTTP_ADDR=':18100'; $env:EXTERNAL_BASE_URL='http://127.0.0.1:18100'
$env:RENDER_SERVICE_URL='http://127.0.0.1:18101'; $env:SEED_BUILTIN='true'
go run ./server
```

启动日志要点：
- `[auth] 系统未初始化…`（未配 ADMIN_USERNAME/PASSWORD，走 setup/dev-login 路径）
- `[seed] 内置模板 audit-report 播种完成（version 1.0.0）`
- `[main] docforge 监听 :18100`

## 实测序列与响应

### 1. 健康检查
```
GET /api/v1/healthz
→ {"db":"ok","render":"ok","status":"ok"}
```
（mock render 未起时为 `render=error, status=degraded`，符合契约。）

### 2. 开发一键登录（AUTH_DEV=true，公开端点）
```
POST /api/v1/auth/dev-login  {"username":"dev_admin"}
→ {"auth_mode":"dev","user":{"id":"u_…","username":"dev_admin","display_name":"dev_admin","email":"dev@dev.local"}}
Set-Cookie: df_session=…; HttpOnly; SameSite=Lax
```

### 3. 当前用户
```
GET /api/v1/auth/me
→ {"auth_mode":"local","base_url":"http://127.0.0.1:18100","guide_seen":false,
   "initialized":true,"oidc_enabled":false,"registration_enabled":false,
   "user":{"id":"…","username":"dev_admin","role":"admin","status":"active",
           "source":"local","oidc_bound":false,"has_password":false,"last_login_at":null}}
```

### 4. 模板列表（内置播种）
```
GET /api/v1/templates
→ total=1, item: code=audit-report, name=代码审计报告, visibility=public,
  current_version_id=ver_…, enabled=true, created_by=system
```

### 5. 投递即生成（同步，单条 + JSON≤1MB）
```
POST /api/v1/generate
{"template_code":"audit-report","data":{"项目编号":"AP-001"},"format":"docx","external_ref":"T-1"}
→ {"job_id":"jb_20261017_0001","status":"succeeded","sync":true,
   "documents":[{"document_id":"dc_20261017_0001","filename":"audit-report_000.docx",
                 "download_url":"…/documents/dc_…/download",
                 "signed_url":"/api/v1/s/ZDpk…"}]}
```
worker 状态机 queued→succeeded，文件落盘 `volumes/jobs/jb_…/dc_….docx`。

### 6. 幂等重发（同 external_ref）
```
POST /api/v1/generate （同 body）
→ 直接返回既有 job_id=jb_20261017_0001（sync:false 形状），不重复建任务。
```

### 7. 文档生成中心列表
```
GET /api/v1/documents
→ total=1, 含 input_data(base64)/operator/duration_ms=8/expires_at/expired=false。
```

### 8. 任务 zip 下载（登录态）
```
GET /api/v1/jobs/jb_20261017_0001/download → 200, 194 bytes zip
```

### 9. 签名直链真下载（HMAC，无需登录）
```
GET /api/v1/s/ZDpkY18yMDI2MTAxN18wMDAxOjE3OTIzNDMxNTQ.04AX3_…
→ 200, Content-Type=docx, 落盘 downloaded.docx = 32 bytes "FAKE-DOCX-BYTES-from-mock-render"
```

## 哪些是真实 / 哪些是 mock

- **真实（Go 主服务全链路）**：配置加载、sqlite 建表与迁移、会话 cookie、dev-login/me、模板播种落盘、generate 管道（模板解析/版本/幂等/records 校验/同步判定）、worker 状态机与文件落盘、zip 打包、文档列表、HMAC 签名直链签发与校验、SSRF/文件名/argon2/限流等单测。
- **mock（render-service 尚未由另一代理提供）**：`/scan /render /validate /preview /healthz` 返回固定成功响应；真实 docx/pdf 字节由 Python 渲染服务产出后替换 `RENDER_SERVICE_URL` 即可对接，无需改 Go 代码。worker 单测用 httptest mock 完整覆盖 queued→succeeded/partial/failed 与 zip 产物存在性。

## 已知本机环境坑（不影响代码正确性）
- 端口 8100/8101 在本机被 Windows 保留端口占用（bind 10013），实测改用 18100/18101；容器/部署环境无此问题。
- `sqlite:///` 三斜杠 DSN 必须正确剥前缀，否则相对路径会解析到磁盘根；已在 `config.SQLitePath()` 修正。
