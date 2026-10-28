// 显示层工具：时间本地化、文件大小、错误 message 提取
import type { ApiError } from '@/types/api';

export function fmtTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function fmtSize(bytes?: number): string {
  if (!bytes && bytes !== 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}

// 从错误中抽「给用户看的一句话 + 字段路径明细」
export function errorMessage(err: unknown): { summary: string; details: string[] } {
  if (err && typeof err === 'object' && 'name' in err && (err as ApiError).name === 'ApiError') {
    const e = err as ApiError;
    const details = e.details.map((d) => (d.path ? `${d.path}：${d.message}` : d.message));
    return { summary: e.message, details };
  }
  return { summary: '网络异常，请稍后重试', details: [] };
}
