# 02 · API 调用手册

> 适用对象：需要从业务系统**程序化投递数据、批量生成文档**的后端/集成工程师。
> 所有接口前缀：`/api/v1`。本文口径与 `server/internal/api/api.go`、`generate_handlers.go` 一致；如有出入以代码为准。
>
> 归铸版 ZHUBAN 项目所有 · Apache-2.0

---

## 目录

1. [认证方式](#1-认证方式)
2. [创建与管理 API Key](#2-创建与管理-api-key)
3. [投递即生成：POST /generate](#3-投递即生成post-generate)
4. [UI 渲染：POST /render](#4-ui-渲染post-render)
5. [查询任务与下载产物](#5-查询任务与下载产物)
6. [签名直链下载](#6-签名直链下载)
7. [错误码表](#7-错误码表)
8. [curl 与 Python 可复制示例](#8-curl-与-python-可复制示例)
9. [限流与大小限制](#9-限流与大小限制)

---

## 1. 认证方式

二选一：

| 方式 | 用途 | 怎么带 |
| --- | --- | --- |
| 会话 Cookie `df_session` | 浏览器里人工操作、UI 调用 | 登录后浏览器自动带（HttpOnly、7 天有效） |
| `X-API-Key` 请求头 | 业务系统程序化调用 | 每个请求加 `X-API-Key: df_xxxx` |

- API Key 格式为 `df_` 开头 + 40 位 hex，**明文只在创建时展示一次**，之后只存哈希。
- 两者都不带 → 401 `unauthorized`。
- Key 被停用/删除/绑定用户未激活 → 401 `unauthorized`（错误信息见响应 `error.message`）。

## 2. 创建与管理 API Key

路径：**集成中心 → API Key**（`/integrations/keys`）。

后端接口（需登录态 session）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/keys` | 列出我的 Key（不含明文，只给前缀） |
| POST | `/api/v1/keys` | 创建 `{"name":"扫描平台","qps_limit":5,"max_records":1000}` |
| POST | `/api/v1/keys/{id}/disable` | 停用 |
| POST | `/api/v1/keys/{id}/enable` | 启用 |
| DELETE | `/api/v1/keys/{id}` | 删除 |

创建成功响应（**明文 key 仅此一次**）：

```json
{
  "id": "ak_xxx",
  "key": "df_9f86d081884c7d659a2feaa0c55ad015a3bf4f1b",
  "prefix": "df_9f86d081",
  "qps_limit": 5,
  "max_records": 1000
}
```

> 拿到明文后立刻存到你方配置中心，不要写进代码仓库、不要提交进 git。

## 3. 投递即生成：POST /generate

这是业务系统最主要的入口。

### 3.1 请求体字段

```json
{
  "template_code": "audit-report",
  "template_id": "tpl_xxx",
  "version": "1.0.0",
  "version_id": "ver_xxx",
  "data": { },
  "records": [ { }, { } ],
  "format": "docx",
  "filename": "{{项目编号}}_审计报告",
  "callback_url": "https://your.host/on-rendered",
  "external_ref": "ORDER-123",
  "sync": true
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `template_code` | 二选一 | 模板编号，如 `audit-report` |
| `template_id` | 二选一 | 模板 id；与 `template_code` 二选一，`template_code` 优先 |
| `version` | 否 | 指定版本号，缺省用当前线上版本 |
| `version_id` | 否 | 指定版本 id，优先级高于 `version` |
| `data` | 二选一 | 单份数据对象；与 `records` 二选一 |
| `records` | 二选一 | 批量数据数组（多条）；最多 `max_records` 条 |
| `format` | 否 | `docx`（默认）/ `pdf` / `both`；`both` 表示同时产出 docx 与 pdf |
| `filename` | 否 | 文件名规则（不含扩展名），仅替换顶层变量，如 `{{项目编号}}_审计报告` |
| `callback_url` | 否 | 任务终结后回调该地址（需命中白名单，防 SSRF） |
| `external_ref` | 否 | **幂等键**；同一调用方 + 同一 `external_ref` 重复提交直接返回原任务，不重复渲染、不重复扣签章次数 |
| `sync` | 否 | `true` 时同步等待；单份且 JSON ≤ 1MB 默认即同步 |

### 3.2 响应

**同步成功（200，单份）：**

```json
{
  "job_id": "jb_20261018_0001",
  "status": "succeeded",
  "sync": true,
  "documents": [
    {
      "document_id": "dc_20261018_0001",
      "filename": "AP-2026-001_审计报告.docx",
      "download_url": "/api/v1/documents/dc_20261018_0001/download",
      "pdf_download_url": "/api/v1/documents/dc_20261018_0001/download?format=pdf",
      "signed_url": "/api/v1/s/<token>"
    }
  ]
}
```

**异步已受理（202，批量或大数据）：**

```json
{
  "job_id": "jb_20261018_0002",
  "status": "queued",
  "sync": false,
  "template_code": "audit-report",
  "external_ref": "ORDER-456",
  "status_url": "/api/v1/jobs/jb_20261018_0002",
  "download_url": "/api/v1/jobs/jb_20261018_0002/download",
  "signed_url": "/api/v1/s/<job-token>"
}
```

**失败（422，数据校验不通过）：**

```json
{
  "error": {
    "code": "data_invalid",
    "message": "第 1 条数据校验未通过",
    "details": [ { "path": "items[0].mitigation", "message": "字段缺失" } ]
  }
}
```

### 3.3 幂等

- 同一 `X-API-Key`（或同一登录用户）+ 同一 `external_ref`，且原任务未失败、未过保留期 → 直接返回原任务结果，**不重复渲染、不重复消耗签章授权次数**。
- 这是防重投的关键：业务侧重试时务必带上同一个 `external_ref`。

## 4. UI 渲染：POST /render

浏览器工作台"生成并下载"走的就是这个接口。与 `/generate` 的区别：

- 走登录态 session（不接受 X-API-Key）；
- 用 `template_id` 定位模板；
- `source` 记为 `ui`；
- 行为与 `/generate` 一致，单份同步返回同上。

业务系统程序化调用请一律用 `/generate`。

## 5. 查询任务与下载产物

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/jobs?page=&page_size=&status=&source=` | 任务分页列表 |
| GET | `/api/v1/jobs/{id}` | 任务详情（进度、成功/失败数、documents 列表） |
| GET | `/api/v1/jobs/{id}/download` | 任务 zip（批量；`both` 时 docx/pdf 分子目录；失败条目写 errors.json） |
| POST | `/api/v1/jobs/{id}/retry` | 重试（body `{"only_failed":true}` 只重跑失败条） |
| GET | `/api/v1/documents?page=&page_size=&status=&template_id=` | 文档列表 |
| GET | `/api/v1/documents/{id}` | 文档详情（含原始 input_data、操作人、耗时） |
| GET | `/api/v1/documents/{id}/download?format=docx\|pdf` | 下载单份（session 或 API Key） |
| POST | `/api/v1/documents/{id}/regenerate` | 用原数据再生成一次 |

产物有保留期（默认 `RETENTION_DAYS=7`），过期后文件被清理、接口返回 410 `expired`。

## 6. 签名直链下载

任务/文档响应里的 `signed_url` 是一条 **HMAC 签名的免登录直链**：

```
GET /api/v1/s/{token}
```

- **无需再带 API Key / Cookie**，token 自带凭证，可直接发给下游或嵌入站内。
- 默认有效期由 `SIGNED_URL_TTL_HOURS` 控制（默认 24 小时），过期自动失效。
- 单份文档默认给 docx，要 pdf 加 `?format=pdf`；任务级给 zip 包。
- 仅 PDF 的文档，直链必须显式带 `?format=pdf`，否则 410。
- 也可用 `POST /api/v1/signed-url` body `{"document_id":"dc_.."}` 或 `{"job_id":"jb_.."}` 主动换取一条新直链。

## 7. 错误码表

所有错误统一信封：

```json
{ "error": { "code": "...", "message": "中文、可执行的修复指引", "details": [ {"path":"...","message":"..."} ] } }
```

| HTTP | code | 含义 | 怎么改 |
| --- | --- | --- | --- |
| 400 | `data_invalid` | 请求体不是合法 JSON / 缺 data 或 records | 检查 JSON 语法；补上 `data` 或 `records` |
| 401 | `unauthorized` | 未登录 / API Key 无效、停用、删除 | 检查 `X-API-Key` 是否正确、是否被停用 |
| 403 | `grant_denied` | 引用了未授权的签名/印章（V3） | 见下表"签章授权拒绝" |
| 404 | `not_found` | 模板编号 / 模板 / 版本不存在 | 核对 `template_code` 拼写 |
| 410 | `expired` | 产物已过保留期被清理 | 重新生成 |
| 413 | `payload_too_large` | JSON 超限或记录数超限 | 拆任务、减小数据 |
| 422 | `data_invalid` | 数据校验失败（缺字段/类型不符/枚举不符） | 按 `details[].path` 修 JSON |
| 422 | `template_invalid` | Word 里占位符被样式拆坏 | 回 Word 纯文本重输占位符 |
| 422 | `signature_unavailable` | 引用的签名/印章未通过审核（pending/rejected/disabled） | 等管理员审核，或换已通过的素材 |
| 423 | `template_disabled` | 模板已停用 | 在模板库重新启用 |
| 429 | （认证类限流） | 登录/注册失败过多或 Key 超 QPS | 退避重试 |
| 502 | `conversion_failed` | LibreOffice 转 PDF 失败 | 重试；仍失败联系管理员 |
| 500 | `internal_error` | 服务内部错误 | 稍后重试，带 `X-Request-ID` 报管理员 |

### 签章授权拒绝（403 `grant_denied`）的 reason

响应 `message` 里会带原因，常见 `reason`：

| reason | 含义 | 处理 |
| --- | --- | --- |
| `unauthorized` | 当前用户/Key 对该签名或印章**没有任何有效授权** | 联系 owner/admin 授予授权 |
| `exhausted` | 授权次数已用完（`used_count >= max_uses`） | 请 owner/admin 续期加次数 |
| `expired` | 授权已过 `valid_until` | 请 owner/admin 延长有效期 |
| `not_yet` | 授权 `valid_from` 尚未到点 | 等到生效时间，或改授权窗口 |

> 审核与授权是两道独立关卡：先过审核（422 `signature_unavailable`），再过授权（403 `grant_denied`）。详见《03 · 签章与授权使用手册》。

## 8. curl 与 Python 可复制示例

### 8.1 curl：单份同步生成 docx

```bash
curl -X POST 'http://localhost:18100/api/v1/generate' \
  -H 'X-API-Key: df_你的key' \
  -H 'Content-Type: application/json' \
  -d '{
    "template_code": "audit-report",
    "external_ref": "ORDER-123",
    "format": "docx",
    "data": {
      "被测件名称": "示例业务系统后端服务",
      "被测件版本": "v2.1.0",
      "项目编号": "AP-2026-001",
      "测试人员": "张三",
      "测试时间": "2026-10-18",
      "items": [
        { "name": "GB_6.2.1.1 密码安全", "缺陷等级": "高", "次数": 3,
          "description": "密码实现不合规", "缺陷说明": "src/a.go:42",
          "mitigation": { "type": "markdown", "content": "- 采用国密" } }
      ]
    }
  }'
```

同步返回后，拿 `documents[0].signed_url` 直接下载。

### 8.2 curl：异步批量 + 回调

```bash
curl -X POST 'http://localhost:18100/api/v1/generate' \
  -H 'X-API-Key: df_你的key' \
  -H 'Content-Type: application/json' \
  -d '{
    "template_code": "audit-report",
    "records": [
      { "项目编号": "AP-001", "被测件名称": "系统A" },
      { "项目编号": "AP-002", "被测件名称": "系统B" }
    ],
    "callback_url": "https://your.host/on-rendered"
  }'
```

返回 202 后，用 `status_url` 轮询，或等 `callback_url` 收到回调。

### 8.3 Python：同步生成并下载

```python
import requests

BASE = "http://localhost:18100"
HEADERS = {"X-API-Key": "df_你的key"}

data = {
    "被测件名称": "示例业务系统后端服务",
    "被测件版本": "v2.1.0",
    "项目编号": "AP-2026-001",
    "测试人员": "张三",
    "测试时间": "2026-10-18",
    "items": [
        {"name": "GB_6.2.1.1 密码安全", "缺陷等级": "高", "次数": 3,
         "description": "密码实现不合规", "缺陷说明": "src/a.go:42",
         "mitigation": {"type": "markdown", "content": "- 采用国密"}}
    ],
}

r = requests.post(
    f"{BASE}/api/v1/generate",
    headers=HEADERS,
    json={
        "template_code": "audit-report",
        "external_ref": "ORDER-123",
        "format": "both",
        "data": data,
    },
    timeout=60,
)
r.raise_for_status()
resp = r.json()

# 同步成功：documents[0].signed_url 直接下载（免 Key）
for doc in resp["documents"]:
    url = BASE + doc["signed_url"]
    content = requests.get(url).content
    with open(doc["filename"], "wb") as f:
        f.write(content)
    print("已下载", doc["filename"])
```

## 9. 限流与大小限制

| 项 | 默认 | 说明 |
| --- | --- | --- |
| 单任务记录数 `records` | ≤ 1000（`max_records`） | 超限 413 |
| JSON 总大小 | ≤ 20MB（`MAX_JSON_MB`） | 超限 413 |
| 模板文件 | ≤ 10MB | 上传时校验 |
| 单张图片 | ≤ 5MB（`MAX_IMAGE_MB`） | 外链图 |
| API Key QPS | 创建时设定（默认 5） | 超了 429，令牌桶 |
| 同步等待上限 | 30 秒 | 超时转异步，返回 queued |
| 产物保留期 | 7 天（`RETENTION_DAYS`） | 过期清理，410 |
| 签名直链有效期 | 24 小时（`SIGNED_URL_TTL_HOURS`） | 过期失效 |

> 以上数值由管理员在环境变量/系统设置里配置，具体以「管理中心 → 系统设置」与 `GET /api/v1/admin/settings` 返回为准。

---

程序化投递通过上面的 HTTP 接口完成；控制台「模板详情」页提供"接入示例"（curl 与 JSON 样例，可直接复制），API Key 在「集成中心 → API 密钥」中创建并保管（明文仅展示一次）。

> 下一篇：《03 · 签章与授权使用手册》。
