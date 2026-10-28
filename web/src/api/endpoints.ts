// 后端接口函数（对齐 CONTRACT §4），页面里通过 react-query 调用
import { request, requestBlob } from './client';
import type {
  AdminSettings,
  AdminUserListResp,
  ApiKey,
  CreatedApiKey,
  DocumentDetail,
  DocumentRow,
  GenerateResp,
  GenerateHttpRequest,
  Job,
  MeResponse,
  PageResp,
  PreviewResp,
  Role,
  SetupStatus,
  SignedUrlResp,
  Template,
  TemplateAsset,
  TemplateDetail,
  User,
  UpdateInfo,
  ValidateResp,
  VariableMetaResp,
  VersionInfo,
} from '@/types/api';

// ---------- 版本与更新 ----------
// 两个都是公开接口：登录页也要能看见升级提示，否则自建用户永远不知道有新版本。
export const getVersion = () => request<VersionInfo>('/version');
/** force=true 会绕过服务端 6h 缓存；只在用户主动点「重新检查」时用。 */
export const getUpdates = (force = false) =>
  request<UpdateInfo>(`/updates${force ? '?force=1' : ''}`);

// ---------- 认证 ----------
export const getMe = () => request<MeResponse>('/auth/me');
export const getSetupStatus = () => request<SetupStatus>('/auth/setup-status');
export const login = (body: { username: string; password: string }) =>
  request<MeResponse>('/auth/login', { json: body });
export const register = (body: { username: string; password: string; email?: string; display_name?: string }) =>
  request<unknown>('/auth/register', { json: body });
export const setupAdmin = (body: { username: string; password: string; display_name?: string; email?: string }) =>
  request<MeResponse>('/auth/setup', { json: body });
export const devLogin = (username: string) =>
  request<MeResponse>('/auth/dev-login', { json: { username } });
export const logout = () => request('/auth/logout', { method: 'POST' });
export const markGuideSeen = () => request('/auth/guide', { method: 'PUT', json: { seen: true } });
export const changePassword = (body: { old_password?: string; new_password: string }) =>
  request<unknown>('/auth/password', { json: body });
export const unbindOidc = () => request<unknown>('/auth/oidc/unbind', { method: 'POST' });

// ---------- 用户管理（仅 admin） ----------
export interface AdminUserQuery {
  status?: string;
  source?: string;
  q?: string;
  page?: number;
  page_size?: number;
}
export const listAdminUsers = (q: AdminUserQuery) =>
  request<AdminUserListResp>('/admin/users', {
    query: { status: q.status, source: q.source, q: q.q, page: q.page, page_size: q.page_size },
  });
export const createAdminUser = (body: {
  username: string;
  password: string;
  display_name?: string;
  email?: string;
  role: Role;
}) => request<User>('/admin/users', { json: body });
export const approveUser = (id: string) => request<User>(`/admin/users/${id}/approve`, { method: 'POST' });
export const disableUser = (id: string) => request<User>(`/admin/users/${id}/disable`, { method: 'POST' });
export const enableUser = (id: string) => request<User>(`/admin/users/${id}/enable`, { method: 'POST' });
export const changeUserRole = (id: string, role: Role) =>
  request<User>(`/admin/users/${id}/role`, { method: 'POST', json: { role } });
export const resetUserPassword = (id: string, new_password: string) =>
  request<unknown>(`/admin/users/${id}/reset-password`, { method: 'POST', json: { new_password } });

// ---------- 模板 ----------
export interface TemplateListQuery {
  q?: string;
  category?: string;
  enabled?: boolean;
  page?: number;
  page_size?: number;
}
export const listTemplates = (q: TemplateListQuery) =>
  request<PageResp<Template>>('/templates', {
    query: {
      q: q.q,
      category: q.category,
      enabled: q.enabled === undefined ? undefined : String(q.enabled),
      page: q.page,
      page_size: q.page_size,
    },
  });
export const getTemplate = (id: string) => request<TemplateDetail>(`/templates/${id}`);
export interface CreateTemplateArgs {
  file: File;
  code: string;
  name: string;
  category?: string;
  description?: string;
  visibility?: 'private' | 'public';
}
export const createTemplate = (args: CreateTemplateArgs) => {
  const fd = new FormData();
  fd.append('file', args.file);
  fd.append('code', args.code);
  fd.append('name', args.name);
  if (args.category) fd.append('category', args.category);
  if (args.description) fd.append('description', args.description);
  if (args.visibility) fd.append('visibility', args.visibility);
  // 注意：返回的是扫描结果 + 创建后的模板，宽松收窄为 TemplateDetail
  return request<TemplateDetail>('/templates', { form: fd });
};
export const uploadVersion = (
  templateId: string,
  file: File,
  activate = true,
  changeSummary?: string,
) => {
  const fd = new FormData();
  fd.append('file', file);
  fd.append('activate', String(activate));
  // 只在有值时发送：后端把空串当成「没填」，发一个空字段过去没意义。
  if (changeSummary) fd.append('change_summary', changeSummary);
  return request<unknown>(`/templates/${templateId}/versions`, { form: fd });
};
export const activateVersion = (templateId: string, versionId: string) =>
  request<unknown>(`/templates/${templateId}/versions/${versionId}/activate`, { method: 'POST' });
export const disableTemplate = (id: string) => request(`/templates/${id}/disable`, { method: 'POST' });
export const enableTemplate = (id: string) => request(`/templates/${id}/enable`, { method: 'POST' });
/** 删除模板（后端软删：已生成的文档与生成记录仍可查；code 会被释放以便重建同名） */
export const deleteTemplate = (id: string) => request(`/templates/${id}`, { method: 'DELETE' });

// ---------- 工作台 ----------
export const preview = (body: { template_id: string; version_id?: string; data: unknown; max_pages?: number }) =>
  request<PreviewResp>('/preview', { json: body });
export const validateData = (body: { template_id: string; version_id?: string; data: unknown }) =>
  request<ValidateResp>('/validate', { json: body });
export const renderDoc = (body: {
  template_id: string;
  version_id?: string;
  data?: unknown;
  records?: unknown[];
  format?: 'docx' | 'pdf' | 'both';
  filename?: string;
}) => request<GenerateResp>('/render', { json: body });

// ---------- 任务 / 文档 ----------
export const getJob = (id: string) => request<Job>(`/jobs/${id}`);
export const retryJob = (id: string, onlyFailed: boolean) =>
  request<Job>(`/jobs/${id}/retry`, { method: 'POST', json: { only_failed: onlyFailed } });
export interface DocumentListQuery {
  page?: number;
  page_size?: number;
  source?: string;
  status?: string;
  template_id?: string;
  q?: string;
  from?: string;
  to?: string;
}
export const listDocuments = (q: DocumentListQuery) =>
  request<PageResp<DocumentRow>>('/documents', {
    query: {
      page: q.page,
      page_size: q.page_size,
      source: q.source,
      status: q.status,
      template_id: q.template_id,
      q: q.q,
      from: q.from,
      to: q.to,
    },
  });
export const getDocument = (id: string) => request<DocumentDetail>(`/documents/${id}`);
export const regenerateDocument = (id: string) =>
  request<GenerateResp>(`/documents/${id}/regenerate`, { method: 'POST' });
export const createSignedUrl = (body: { document_id?: string; job_id?: string }) =>
  request<SignedUrlResp>('/signed-url', { method: 'POST', json: body });

// ---------- API Key ----------
export const listKeys = () => request<ApiKey[]>('/keys');
export const createKey = (body: { name: string; qps_limit: number; max_records: number }) =>
  request<CreatedApiKey>('/keys', { json: body });
export const disableKey = (id: string) => request(`/keys/${id}/disable`, { method: 'POST' });
export const enableKey = (id: string) => request(`/keys/${id}/enable`, { method: 'POST' });
export const deleteKey = (id: string) => request(`/keys/${id}`, { method: 'DELETE' });

// ---------- 下载（Blob） ----------
export const downloadDocument = (id: string, format: 'docx' | 'pdf') =>
  requestBlob(`/documents/${id}/download`, { query: { format } });
export const downloadJobZip = (id: string) => requestBlob(`/jobs/${id}/download`);
export const downloadVersionFile = (templateId: string, versionId: string) =>
  requestBlob(`/templates/${templateId}/versions/${versionId}/download`);
export const downloadBlankTemplate = () => requestBlob('/wizard/blank-template.docx');

// ---------- V2：投递即生成（POST /generate，session 鉴权，不消耗 API Key） ----------
export const generate = (body: GenerateHttpRequest) =>
  request<GenerateResp>('/generate', { json: body });

// ---------- V2：投递任务视图（复用 /jobs 列表） ----------
export interface JobListQuery {
  page?: number;
  page_size?: number;
  status?: string;
  source?: string;
}
export const listJobs = (q: JobListQuery = {}) =>
  request<PageResp<Job>>('/jobs', {
    query: { page: q.page, page_size: q.page_size, status: q.status, source: q.source },
  });

// ---------- V2：模板资源（图片/logo）——后端同步开发，未就绪时 404/405 优雅降级 ----------
export const listAssets = (templateId: string) =>
  request<TemplateAsset[]>(`/templates/${templateId}/assets`);
export const uploadAsset = (templateId: string, file: File, name?: string, type?: string) => {
  const fd = new FormData();
  fd.append('file', file);
  if (name) fd.append('name', name);
  if (type) fd.append('type', type);
  return request<TemplateAsset>(`/templates/${templateId}/assets`, { form: fd });
};
export const deleteAsset = (templateId: string, assetId: string) =>
  request(`/templates/${templateId}/assets/${assetId}`, { method: 'DELETE' });
// ---------- V2：变量元数据（GET/PUT） ----------
export const getVariablesMeta = (templateId: string) =>
  request<VariableMetaResp>(`/templates/${templateId}/variables/meta`);
export const putVariablesMeta = (templateId: string, variables: unknown) =>
  request<VariableMetaResp>(`/templates/${templateId}/variables/meta`, { method: 'PUT', json: { variables } });

// ---------- V2：系统设置（admin，同步开发中） ----------
export const getAdminSettings = () => request<AdminSettings>('/admin/settings');

// ---------- V2：在线模板编辑器（render-service /parse /apply + Go editor handlers） ----------
import type {
  EditorApplyResp,
  EditorModelResp,
  EditorVersion,
  EditorVersionListResp,
} from '@/types/editor';

// GET 当前版本结构化块模型（Go 按 version_id 缓存 parse 结果）
export const getEditorModel = (templateId: string, signal?: AbortSignal) =>
  request<EditorModelResp>(`/templates/${templateId}/editor/model`, { signal });

// POST 块模型回写为临时 docx 并出预览（不落库）。model 为完整块数组。
export const applyEditorModel = (
  templateId: string,
  model: unknown[],
  baseVersionId: string | undefined,
  signal?: AbortSignal,
) => {
  const fd = new FormData();
  fd.append('model', JSON.stringify({ blocks: model }));
  if (baseVersionId) fd.append('base_version_id', baseVersionId);
  return request<EditorApplyResp>(`/templates/${templateId}/editor/apply`, { form: fd, signal });
};

// POST 存为新版本（base_version_id 乐观锁；409 返回 version_conflict）
export const saveEditorVersion = (
  templateId: string,
  model: unknown[],
  baseVersionId: string,
  changeSummary: string,
) => {
  const fd = new FormData();
  fd.append('model', JSON.stringify({ blocks: model }));
  fd.append('base_version_id', baseVersionId);
  if (changeSummary) fd.append('change_summary', changeSummary);
  return request<EditorVersion>(`/templates/${templateId}/editor/save`, { form: fd });
};

// GET 版本时间线
export const listEditorVersions = (templateId: string) =>
  request<EditorVersionListResp>(`/templates/${templateId}/versions`);

// POST 回滚到历史版本（复制为新版本并激活）
export const rollbackEditorVersion = (templateId: string, versionId: string) =>
  request<EditorVersion>(`/templates/${templateId}/versions/${versionId}/rollback`, { method: 'POST' });

// POST 导出当前编辑草稿（multipart model + format=docx|pdf），二进制 blob 直接下载，不落版本
export const exportEditor = (templateId: string, model: unknown[], format: 'docx' | 'pdf') => {
  const fd = new FormData();
  fd.append('model', JSON.stringify({ blocks: model }));
  fd.append('format', format);
  return requestBlob(`/templates/${templateId}/editor/export`, { method: 'POST', form: fd });
};

// ==================== V3：电子签章与审计（PRD V3 §6） ====================
import type {
  AdminSettingsV3,
  AuditLog,
  AuditVerifyResult,
  FontItem,
  ReviewQueueItem,
  Seal,
  Signature,
} from '@/types/api';

// ---------- 字体 ----------
// 签名生成用的内置字体（长得像手写的那些），来自渲染服务。
//
// 刻意不叫 listFonts：后台「字体管理」里也有一个 listFonts（/fonts，返回
// builtin/imported/system 三类）。两个同名会同时踩两个坑——
//   1. TS 里后者被前者遮蔽，FontManager 读 .system 直接编译失败；
//   2. 两处 queryKey 都是 ['fonts']，React Query 共用一份缓存，
//      先打开哪个页面，另一个页面就拿到形状不对的数据。
// 名字区分开，语义也就清楚了：这里是「签名能用的字形」，那里是「运维能管的字体」。
export const listSignatureFonts = () => request<{ fonts: FontItem[] }>('/render/fonts');

// ---------- 签名 CRUD ----------
export interface SealListQuery {
  page?: number;
  page_size?: number;
  status?: string;
  keyword?: string;
  source?: string;
}
export const listSignatures = (q: SealListQuery = {}) =>
  request<PageResp<Signature>>('/signatures', {
    query: { page: q.page, page_size: q.page_size, status: q.status, keyword: q.keyword, source: q.source },
  });
export const getSignature = (id: string) => request<Signature>(`/signatures/${id}`);
export const createSignature = (fd: FormData) => request<Signature>('/signatures', { method: 'POST', form: fd });
export const updateSignature = (id: string, fd: FormData) =>
  request<Signature>(`/signatures/${id}`, { method: 'PUT', form: fd });
export const deleteSignature = (id: string) => request(`/signatures/${id}`, { method: 'DELETE' });
export const previewSignature = (fd: FormData) =>
  requestBlob('/signatures/render-preview', { method: 'POST', form: fd });

// ---------- 印章 CRUD（与签名同构） ----------
export const listSeals = (q: SealListQuery = {}) =>
  request<PageResp<Seal>>('/seals', {
    query: { page: q.page, page_size: q.page_size, status: q.status, keyword: q.keyword, source: q.source },
  });
export const getSeal = (id: string) => request<Seal>(`/seals/${id}`);
export const createSeal = (fd: FormData) => request<Seal>('/seals', { method: 'POST', form: fd });
export const updateSeal = (id: string, fd: FormData) =>
  request<Seal>(`/seals/${id}`, { method: 'PUT', form: fd });
export const deleteSeal = (id: string) => request(`/seals/${id}`, { method: 'DELETE' });
export const previewSeal = (fd: FormData) =>
  requestBlob('/seals/render-preview', { method: 'POST', form: fd });

// ---------- 我可用的签章（自己拥有的 + 别人授权给我的） ----------
// 与 /signatures、/seals 的区别：那两个是「我上传过什么」，这个是「我能盖哪个」。
// 填数据选章必须用这个，否则被授权的人会看到空列表，误以为没权限。
export type UsableAsset = {
  id: string;
  kind: 'signature' | 'seal';
  name: string;
  width_mm: number;
  height_mm: number;
  owned: boolean;
  owner_name?: string;
  /** 0 = 不限次数 */
  remaining: number;
  image_url: string;
};

export const listUsableSignatures = () =>
  request<{ items: UsableAsset[]; total: number }>('/signatures/usable');
export const listUsableSeals = () =>
  request<{ items: UsableAsset[]; total: number }>('/seals/usable');

// ---------- 字体（admin 导入） ----------
export type FontList = {
  /** 内置签名风字体（有 id，可用于签名生成） */
  builtin: { id: string; name: string }[];
  /** 运维导入的字体（fontconfig 已识别） */
  imported: { file: string; family: string; style: string }[];
  /** 系统自带字体数量（只给个数，不铺列表） */
  system: number;
  builtin_error?: string;
  system_error?: string;
};

export const listFonts = () => request<FontList>('/fonts');
export const uploadFont = (file: File) => {
  const fd = new FormData();
  fd.append('file', file);
  return request<{ ok: boolean; name: string; size: number; reload: Record<string, unknown> }>(
    '/fonts',
    { form: fd },
  );
};
export const deleteFont = (name: string) =>
  request(`/fonts/${encodeURIComponent(name)}`, { method: 'DELETE' });

// ---------- 审核队列 ----------
export const listReviewQueue = (resource_type: 'signature' | 'seal' | '') =>
  request<PageResp<ReviewQueueItem>>('/review/queue', { query: resource_type ? { resource_type } : {} });
export const approveReview = (resource_type: string, id: string, note?: string) =>
  request(`/review/${resource_type}/${id}/approve`, { method: 'POST', json: { note: note || '' } });
export const rejectReview = (resource_type: string, id: string, note: string) =>
  request(`/review/${resource_type}/${id}/reject`, { method: 'POST', json: { note } });

// ---------- 审计日志 ----------
export interface AuditQuery {
  action?: string;
  actor?: string;
  object_type?: string;
  template_code?: string;
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
}
export const listAuditLogs = (q: AuditQuery = {}) =>
  request<PageResp<AuditLog>>('/admin/audit-logs', {
    query: {
      action: q.action,
      actor: q.actor,
      object_type: q.object_type,
      template_code: q.template_code,
      from: q.from,
      to: q.to,
      page: q.page,
      page_size: q.page_size,
    },
  });
export const verifyAuditChain = () => request<AuditVerifyResult>('/admin/audit-logs/verify');
export const exportAuditLogs = (from?: string, to?: string) =>
  requestBlob('/admin/audit-logs/export', { query: { from, to } });

// ---------- 系统设置 V3（PUT） ----------
export const updateAdminSettings = (patch: Partial<AdminSettingsV3>) =>
  request<AdminSettingsV3>('/admin/settings', { method: 'PUT', json: patch });

// ==================== V3.1：签章授权（PRD §10） ====================
import type { GrantCreateBody, GrantListResp, ResourceType, SealGrant } from '@/types/api';

export const listGrants = (resourceType: ResourceType, id: string) =>
  request<GrantListResp>(`/${resourceType === 'signature' ? 'signatures' : 'seals'}/${id}/grants`);
export const createGrants = (resourceType: ResourceType, id: string, body: GrantCreateBody) =>
  request<{ items: SealGrant[] }>(`/${resourceType === 'signature' ? 'signatures' : 'seals'}/${id}/grants`, {
    method: 'POST',
    json: body,
  });
export const renewGrant = (grantId: string, body: { add_uses?: number; max_uses?: number; valid_from?: string; valid_until?: string }) =>
  request(`/grants/${grantId}/renew`, { method: 'POST', json: body });
export const revokeGrant = (grantId: string, note?: string) =>
  request(`/grants/${grantId}/revoke`, { method: 'POST', json: { note } });

// ==================== V3.2：邮件投递（SMTP 只在服务端配） ====================
import type { MailSettings, MailSettingsPatch, MailTestBody } from '@/types/api';

export const getMailSettings = () => request<MailSettings>('/admin/mail/settings');
export const updateMailSettings = (patch: MailSettingsPatch) =>
  request<MailSettings>('/admin/mail/settings', { method: 'PUT', json: patch });
export const sendMailTest = (body: MailTestBody) =>
  request<{ ok: boolean; message: string }>('/admin/mail/test', { method: 'POST', json: body });
// 投递记录没有独立端点：它作为 action=mail.deliver / object_type=email
// 的审计事件，走 /admin/audit-logs 查询，与其它操作同一条链。
