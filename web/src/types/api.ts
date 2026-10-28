// docforge 后端响应类型 —— 严格对齐 docs/CONTRACT.md §1/§2/§4。
// 不使用 any；后端 JSON 用 unknown/Record 承接，业务处再收窄。

// ---------- 枚举（CONTRACT §1） ----------
export type Source = 'ui' | 'sync_api' | 'auto_api' | 'batch';
export type JobStatus = 'queued' | 'running' | 'succeeded' | 'partial' | 'failed';
export type DocumentStatus = 'queued' | 'running' | 'succeeded' | 'failed';
export type CallbackStatus = '' | 'pending' | 'succeeded' | 'failed';
export type Format = 'docx' | 'pdf' | 'both';
export type VarKind = 'scalar' | 'loop' | 'condition' | 'image' | 'qrcode' | 'richtext' | 'chart';
export type Visibility = 'private' | 'public';
export type Severity = 'error' | 'warning' | 'info';
export type AuthMode = 'dev' | 'oidc';

// ---------- 错误信封（CONTRACT §2） ----------
export interface ApiErrorDetail {
  path?: string;
  message: string;
}
export interface ApiErrorBody {
  code: string;
  message: string;
  details?: ApiErrorDetail[];
  request_id?: string;
}
export class ApiError extends Error {
  code: string;
  details: ApiErrorDetail[];
  request_id?: string;
  status: number;
  constructor(status: number, body: ApiErrorBody | null, fallback: string) {
    super(body?.message || fallback);
    this.name = 'ApiError';
    this.code = body?.code || `http_${status}`;
    this.details = body?.details || [];
    this.request_id = body?.request_id;
    this.status = status;
  }
}

// ---------- 认证（CONTRACT §4.2：本地账号密码 + 可选 OIDC） ----------
export type Role = 'admin' | 'member';
export type UserStatus = 'pending' | 'active' | 'disabled';
export type UserSource = 'local' | 'oidc';

export interface User {
  id: string;
  username: string;
  display_name?: string;
  email?: string;
  role: Role;
  status: UserStatus;
  source: UserSource;
  oidc_bound?: boolean;
  has_password?: boolean;
  last_login_at?: string;
  created_at?: string;
}

export interface MeResponse {
  user: User;
  auth_mode: 'local' | 'oidc' | 'dev';
  oidc_enabled: boolean;
  registration_enabled: boolean;
  initialized: boolean;
  guide_seen: boolean;
  base_url?: string;
}

export interface SetupStatus {
  initialized: boolean;
  oidc_enabled: boolean;
  registration_enabled: boolean;
  dev_login_enabled?: boolean;
}

export interface AdminUserListResp {
  items: User[];
  total: number;
  pending_count: number;
}

// ---------- 模板 ----------
export interface TemplateVariable {
  name: string;
  kind: VarKind;
  type?: string;
  sample?: unknown;
  path: string;
  required?: boolean;
  // loop 专用
  iterable?: string;
  loop_var?: string;
  sample_size?: number;
  fields?: TemplateVariable[];
  // condition 专用
  expression?: string;
  // chart 专用（chartdata 扫描产物）：图表类型/标题/结构，供前端渲染数据表格
  chart_kind?: string;
  chart_kind_name?: string;
  title?: string;
  series_count?: number;
  point_count?: number;
}
export interface Diagnostic {
  severity: Severity;
  message: string;
  location?: string;
}
export interface TemplateVersion {
  id: string;
  version: string;
  variables?: TemplateVariable[];
  sample_json?: Record<string, unknown>;
  json_schema?: Record<string, unknown>;
  diagnostics?: Diagnostic[];
  file_size?: number;
  uploaded_by?: string;
  created_at?: string;
}
export interface Template {
  id: string;
  code: string;
  name: string;
  category?: string;
  description?: string;
  visibility: Visibility;
  enabled: boolean;
  current_version_id?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
  current_version?: TemplateVersion;
}
// 版本时间线的一行。
// 字段名按后端 versionMap 的实际输出：上传人叫 created_by，不叫 uploaded_by。
// 以前这里写成 uploaded_by，页面上「上传人」就永远是空的——后端明明有值。
export interface VersionSummary {
  id: string;
  version: string;
  is_current?: boolean;
  created_at?: string;
  /** upload=上传文件 / edit=在线编辑 / rollback=回滚 */
  source?: string;
  /** 人读的变更摘要（在线编辑与回滚会写，上传时可选填） */
  change_summary?: string;
  created_by?: string;
  file_size?: number;
  /** 兼容旧字段名，不再使用 */
  uploaded_by?: string;
}
// integration：后端只给数据，代码示例由前端拼（CONTRACT §4.3/§7）
export interface IntegrationInfo {
  template_code: string;
  version: string;
  sample_json?: Record<string, unknown>;
  filename_rule?: string;
}
export interface TemplateDetail extends Template {
  versions?: VersionSummary[];
  integration?: IntegrationInfo;
}
export interface PageResp<T> {
  items: T[];
  total: number;
}

// ---------- 预览 / 校验 ----------
export interface PreviewPage {
  page: number;
  url: string;
  width: number;
  height: number;
}
export interface PreviewResp {
  preview_id: string;
  page_count: number;
  returned_pages: number;
  pages: PreviewPage[];
}
export interface ValidateIssue {
  path?: string;
  message: string;
}
export interface ValidateResp {
  valid: boolean;
  errors: ValidateIssue[];
  warnings?: ValidateIssue[];
}

// ---------- 生成 / 渲染响应 ----------
export interface GeneratedDocument {
  document_id: string;
  filename: string;
  download_url?: string;
  pdf_download_url?: string;
  signed_url?: string;
}
export interface GenerateResp {
  job_id: string;
  status: JobStatus;
  sync: boolean;
  template_code?: string;
  external_ref?: string;
  status_url?: string;
  download_url?: string;
  signed_url?: string;
  documents?: GeneratedDocument[];
  /** 以下三个字段只在本次请求带了 email/emails 时出现 */
  email_status?: 'pending' | 'sent' | 'failed' | 'skipped' | 'disabled';
  email_error?: string;
  email_attempts?: number;
}

// ---------- 任务 / 文档 ----------
export interface JobError {
  record_index: number;
  error: string;
  path?: string;
}
export interface JobDocument {
  document_id: string;
  record_index: number;
  filename: string;
  status: DocumentStatus;
  download_url?: string;
  signed_url?: string;
  error?: string;
}
export interface Job {
  id: string;
  status: JobStatus;
  source: Source;
  template_id: string;
  template_name?: string;
  template_code?: string;
  version?: string;
  format?: Format;
  filename_rule?: string;
  total: number;
  succeeded: number;
  failed: number;
  progress?: number;
  callback_url?: string;
  callback_status?: CallbackStatus;
  callback_attempts?: number;
  external_ref?: string;
  zip_download_url?: string;
  signed_url?: string;
  errors?: JobError[];
  documents?: JobDocument[];
  created_at: string;
  started_at?: string;
  finished_at?: string;
  expires_at?: string;
}
// 文档生成中心列表行：后端可能直接给 job 级聚合字段
export interface DocumentRow {
  id: string;
  job_id?: string;
  source: Source;
  template_id?: string;
  template_name?: string;
  template_code?: string;
  version?: string;
  filename: string;
  status: DocumentStatus | JobStatus;
  record_index?: number;
  total?: number;
  succeeded?: number;
  failed?: number;
  format?: Format;
  operator?: string;
  error?: string;
  created_at: string;
  expires_at?: string;
}
export interface DocumentDetail extends DocumentRow {
  input_data?: Record<string, unknown>;
  duration_ms?: number;
}

// ---------- API Key ----------
export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  qps_limit: number;
  max_records: number;
  enabled: boolean;
  last_used_at?: string;
  /** 最近一次调用的来源 IP（与审计的 source_ip 同一判据）；加这一列之前的老数据没有 */
  last_used_ip?: string;
  created_at: string;
}
export interface CreatedApiKey extends ApiKey {
  key: string;
}

// ---------- 签名直链 ----------
export interface SignedUrlResp {
  signed_url: string;
  expires_at: string;
}

// ---------- V2：模板资源（图片/logo） ----------
// 后端契约 §9 / §7.2：GET/POST/DELETE /api/v1/templates/{id}/assets
export interface TemplateAsset {
  id: string;
  name: string;
  type?: string; // image | qrcode 背景图
  url: string;
  width_mm?: number;
  height_mm?: number;
  created_by?: string;
  created_at?: string;
}

// ---------- V2：变量元数据（可编辑） ----------
// 后端契约 §7.2：GET/PUT /api/v1/templates/{id}/variables/meta
export interface VariableMeta {
  name: string;
  kind: VarKind;
  type?: string;
  required?: boolean;
  default?: unknown;
  description?: string;
}
export interface VariableMetaResp {
  variables: VariableMeta[];
}

// ---------- V2：投递即生成 /generate 响应（复用契约 §4.4） ----------
export interface GenerateHttpRequest {
  template_code?: string;
  template_id?: string;
  version?: string;
  data?: unknown;
  records?: unknown[];
  format?: 'docx' | 'pdf' | 'both';
  filename?: string;
  external_ref?: string;
  sync?: boolean;
  /** 整任务一封；与 emails 二选一 */
  email?: GenerateEmailSpec;
  /** 每条记录各一封，长度必须等于 records/data 的记录数 */
  emails?: GenerateEmailSpec[];
}

/** POST /generate 的邮件选项。注意：**不含任何 SMTP 凭据**，那些只在服务端配。 */
export interface GenerateEmailSpec {
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject?: string;
  body?: string;
  attach?: 'auto' | 'docx' | 'pdf' | 'both' | 'zip' | 'none';
  include_link?: boolean;
}

// ---------- V2：系统设置（admin） ----------
// 后端 GET /api/v1/admin/settings 同步开发中；字段宽松收窄，未就绪时前端优雅空态。
export interface AdminSettings {
  retention_days?: number;
  signed_url_ttl_hours?: number;
  max_records?: number;
  max_json_mb?: number;
  callback_allowlist?: string;
  oidc_enabled?: boolean;
  render_health?: 'ok' | 'error' | string;
  [key: string]: unknown;
}

// ---------- V3：电子签章（PRD V3 §3/§6） ----------
export type SealStatus = 'pending' | 'approved' | 'rejected' | 'disabled';
export type SealSource = 'upload' | 'generated';
export type ResourceType = 'signature' | 'seal';

export interface Signature {
  id: string;
  name: string;
  owner_id: string;
  owner_name?: string;
  source: SealSource;
  signer_name?: string;
  font_id?: string;
  params?: Record<string, unknown>;
  width_mm?: number;
  height_mm?: number;
  status: SealStatus;
  reviewed_by?: string;
  reviewed_at?: string;
  review_note?: string;
  created_at: string;
  updated_at: string;
  // 图片访问约定：GET /signatures/{id}/image；后端若直接返回 image_url 也兼容
  image_url?: string;
  ref_count?: number; // 被引用文档数（详情接口返回）
  // V3.1：当前调用人是否可用 / 剩余授权次数
  usable?: boolean;
  grant_remaining?: number;
}

export interface Seal {
  id: string;
  name: string;
  owner_id: string;
  owner_name?: string;
  source: SealSource;
  unit_name?: string;
  seal_name?: string;
  shape?: 'circle' | 'ellipse';
  radius_mm?: number;
  star?: boolean;
  width_mm?: number;
  height_mm?: number;
  status: SealStatus;
  reviewed_by?: string;
  reviewed_at?: string;
  review_note?: string;
  created_at: string;
  updated_at: string;
  image_url?: string;
  ref_count?: number;
  usable?: boolean;
  grant_remaining?: number;
}

export interface ReviewRecord {
  id: string;
  resource_type: ResourceType;
  resource_id: string;
  action: 'submit' | 'approve' | 'reject' | 'disable' | 'enable';
  operator_id: string;
  operator_name?: string;
  note?: string;
  created_at: string;
}

// 审核队列项：合并签名/印章，带缩略图与创建人
export interface ReviewQueueItem {
  id: string;
  resource_type: ResourceType;
  name: string;
  owner_name?: string;
  created_at: string;
  image_url?: string;
}

export interface FontItem {
  id: string;
  name: string;
  family?: string;
  style?: string;
}

export interface AuditLog {
  id: string;
  seq: number;
  actor_type: 'user' | 'api_key' | 'system';
  /** 后端字段名是 actor_user_id；早期这里错写成 actor_id，导致调用人列永远是「—」 */
  actor_user_id: string;
  actor_name: string;
  action: string;
  object_type: string;
  object_id: string;
  template_code?: string;
  external_ref?: string;
  signature_id?: string;
  seal_id?: string;
  source_ip?: string;
  endpoint?: string;
  detail?: Record<string, unknown>;
  occurred_at: string;
  self_hash?: string;
}

export interface AuditVerifyResult {
  ok: boolean;
  checked: number;
  broken_at_seq?: number;
  expected_hash?: string;
  actual_hash?: string;
  last_verified_at?: string;
}

// 系统设置 V3 扩展字段
export interface AdminSettingsV3 extends AdminSettings {
  review_signatures_enabled?: boolean;
  review_seals_enabled?: boolean;
  seal_default_width_mm?: number;
  signature_default_width_mm?: number;
}

// ---------- V3.1：签章授权（PRD §3.7/§3.8/§10） ----------
export type GranteeType = 'user' | 'api_key';
export type GrantCalcStatus = 'active' | 'exhausted' | 'expired' | 'revoked' | 'not_started';

export interface SealGrant {
  id: string;
  resource_type: ResourceType;
  resource_id: string;
  granted_by?: string;
  grantee_type: GranteeType;
  grantee_id: string;
  grantee_name?: string;
  max_uses: number; // 0 = 不限次数
  used_count: number;
  valid_from?: string;
  valid_until?: string;
  note?: string;
  status: GrantCalcStatus; // 后端计算态
  created_at: string;
  updated_at?: string;
}

export interface GrantEvent {
  id: string;
  grant_id?: string;
  action: string; // grant.create / renew / update / revoke / use / denied
  operator_name?: string;
  note?: string;
  detail?: Record<string, unknown>;
  created_at: string;
}

export interface GrantListResp {
  items: SealGrant[];
  total: number;
  events?: GrantEvent[];
}

export interface GranteeInput {
  grantee_type: GranteeType;
  grantee_id: string;
}
export interface GrantCreateBody {
  grantees: GranteeInput[];
  max_uses: number;
  valid_from?: string;
  valid_until?: string;
  note?: string;
}

/* ==================== V3.2：邮件投递 ==================== */

/** 服务端 SMTP 配置。密码只出不进：读接口只回 smtp_password_set。 */
export interface MailSettings {
  smtp_host: string;
  smtp_port: number;
  smtp_user: string;
  smtp_from: string;
  smtp_tls: 'tls' | 'starttls' | 'none';
  mail_allowed_domains: string;
  smtp_password_set: boolean;
  /** 未配置 SMTP 时为 true，用于前端给出明确提示 */
  configured: boolean;
}

export interface MailSettingsPatch {
  smtp_host?: string;
  smtp_port?: number;
  smtp_user?: string;
  smtp_pass?: string;
  smtp_from?: string;
  smtp_tls?: 'tls' | 'starttls' | 'none';
  mail_allowed_domains?: string;
}

export interface MailTestBody {
  to: string;
  subject?: string;
  body?: string;
}

/** 邮件投递不再有独立记录表：见审计日志 action=mail.deliver / object_type=email。 */

/* ==================== 版本与更新检查 ==================== */

/** GET /version：构建期注入的版本三元组。 */
export interface VersionInfo {
  version: string;
  commit: string;
  build_time: string;
  /** 本地构建（未注入版本号）。此时版本号没有可比性，不弹更新提示。 */
  dev: boolean;
  update_check: boolean;
}

/**
 * GET /updates：GitHub 上有没有更高的版本。
 *
 * 注意三态，别压成两种：
 *   has_update=true            有新版本
 *   has_update=false, stale=否 查过了，已经是最新
 *   has_update=false, stale=是 没查成（网络不通/被限流），不知道是不是最新
 * 第三种若当成第二种，用户会以为更新检查在工作，其实一直是哑的。
 */
export interface UpdateInfo {
  current: string;
  latest?: string;
  has_update: boolean;
  dev: boolean;
  /** UPDATE_CHECK=false：这台机器不检查更新 */
  disabled?: boolean;
  url?: string;
  name?: string;
  notes?: string;
  published_at?: string;
  checked_at: string;
  stale?: boolean;
}
