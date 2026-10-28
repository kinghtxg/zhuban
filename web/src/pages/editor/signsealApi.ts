// V3 签名/印章资源拉取：后端并行开发中，先按契约实现。
// 只动 pages/editor/ 内，复用统一 request（同源、带 cookie、401 处理）。
import { request } from '@/api/client';
import { sameOriginUrl } from '@/api/client';

export interface SealSealItem {
  id: string; // sig_xxx / seal_xxx
  name: string; // 显示名，如「张三-合同签署」「合同专用章」
  width_mm?: number;
  height_mm?: number;
  status?: string;
  // V3.1 授权：后端在 usable_by=me 视图下可选返回
  usable?: boolean;
  grant_remaining?: number | null; // null/undefined 表示不限次数
  is_owner?: boolean;
}

interface ListResp {
  items?: SealSealItem[];
  data?: SealSealItem[];
  list?: SealSealItem[];
}

export async function listApprovedSignals(): Promise<SealSealItem[]> {
  // V3.1：只列对当前调用人可用的资源（approved 且 owner/admin 或存在有效授权）
  const resp = await request<ListResp | SealSealItem[]>('/signatures', {
    query: { usable_by: 'me', page_size: 200 },
  });
  return normalize(resp);
}

export async function listApprovedSeals(): Promise<SealSealItem[]> {
  const resp = await request<ListResp | SealSealItem[]>('/seals', {
    query: { usable_by: 'me', page_size: 200 },
  });
  return normalize(resp);
}

function normalize(resp: ListResp | SealSealItem[]): SealSealItem[] {
  if (Array.isArray(resp)) return resp;
  return resp.items || resp.data || resp.list || [];
}

export function sealImageUrl(kind: 'signature' | 'seal', id: string): string {
  const base = kind === 'signature' ? 'signatures' : 'seals';
  return sameOriginUrl(`${base}/${encodeURIComponent(id)}/image`);
}

// 画布上反查显示名：插入时缓存，避免 BlockView 再发请求。
const nameCache = new Map<string, string>();
const cacheKey = (kind: 'signature' | 'seal', id: string) => `${kind}:${id}`;

export function cacheSealName(kind: 'signature' | 'seal', id: string, name: string) {
  nameCache.set(cacheKey(kind, id), name);
}
export function lookupSealName(kind: 'signature' | 'seal', id: string): string | undefined {
  return nameCache.get(cacheKey(kind, id));
}
