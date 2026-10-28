// 统一 fetch 封装：同源 /api/v1、credentials include、错误信封解包、401 处理。
import { ApiError, type ApiErrorBody } from '@/types/api';

const BASE = '/api/v1';

// 401 时由 App 注册的回调接管（跳登录页）
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH';
  query?: Record<string, string | number | boolean | undefined>;
  json?: unknown;
  form?: FormData;
  signal?: AbortSignal;
}

// 409 冲突体（save 时 base_version_id 不等于当前版本）
export interface ConflictBody {
  current_version_id?: string;
  current_version?: string;
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  const url = new URL(BASE + path, window.location.origin);
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== '') url.searchParams.set(k, String(v));
    }
  }
  return url.pathname + url.search;
}

async function parseError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody | null = null;
  const text = await res.text();
  if (text) {
    try {
      const parsed = JSON.parse(text) as { error?: ApiErrorBody };
      if (parsed && parsed.error) body = parsed.error;
    } catch {
      body = null;
    }
  }
  return new ApiError(res.status, body, `请求失败（HTTP ${res.status}）`);
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: opts.method || (opts.json || opts.form ? 'POST' : 'GET'),
    credentials: 'include',
    signal: opts.signal,
    headers: {},
  };
  if (opts.json !== undefined) {
    (init.headers as Record<string, string>)['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.json);
  } else if (opts.form) {
    init.body = opts.form;
  }

  const res = await fetch(buildUrl(path, opts.query), init);

  if (res.status === 401) {
    // 公开引导接口的 401 不级联：/me 与 setup-status 的未登录状态由 App 的 useQuery
    // error 自然渲染登录页；若对它们也触发 onUnauthorized（invalidate /me）会形成
    // 「401 → invalidate → 再请求 → 401」死循环。
    const isPublicAuth = path === '/auth/me' || path === '/auth/setup-status';
    if (!isPublicAuth && onUnauthorized) onUnauthorized();
    throw await parseError(res);
  }
  if (!res.ok) {
    throw await parseError(res);
  }
  if (res.status === 204) return undefined as T;
  const ct = res.headers.get('content-type') || '';
  if (ct.includes('application/json')) {
    return (await res.json()) as T;
  }
  return (await res.text()) as unknown as T;
}

// 下载类接口：返回 Blob，由调用方触发浏览器下载
export async function requestBlob(path: string, opts: RequestOptions = {}): Promise<{ blob: Blob; filename?: string }> {
  const init: RequestInit = {
    method: opts.method || (opts.json || opts.form ? 'POST' : 'GET'),
    credentials: 'include',
    signal: opts.signal,
  };
  if (opts.json !== undefined) {
    (init as Record<string, unknown>).headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(opts.json);
  } else if (opts.form) {
    init.body = opts.form;
  }
  const res = await fetch(buildUrl(path, opts.query), init);
  if (res.status === 401) {
    if (onUnauthorized) onUnauthorized();
  }
  if (!res.ok) throw await parseError(res);
  const cd = res.headers.get('content-disposition') || '';
  const m = cd.match(/filename\*?=(?:UTF-8''")?([^";]+)"?/i);
  return { blob: await res.blob(), filename: m?.[1] };
}

// 同源拼接工具（缩略图 / 预览页图 / 原始文件下载均为相对路径）
export function sameOriginUrl(path: string): string {
  if (!path) return path;
  if (path.startsWith('http')) return path;
  return path.startsWith('/') ? path : `${BASE}/${path}`;
}
