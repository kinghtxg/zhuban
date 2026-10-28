// 枚举 → 中文文案 + Arco Tag 颜色映射（全站统一口径）
import type { DocumentStatus, JobStatus, Severity, Source, VarKind } from '@/types/api';

// 来源是「分类」不是「状态」，所以这里刻意避开状态三色（green / orange / red）。
// auto_api 原来用的 magenta 在 Arco 里偏品红，和 failed 的 red 在同一行里几乎分不开，
// 用户会把它读成「这一条失败了」。换成 gold：它是这套配色里唯一没被状态语义占用的色。
export const SOURCE_META: Record<Source, { label: string; color: string }> = {
  ui: { label: '页面手动', color: 'arcoblue' },
  sync_api: { label: '同步 API', color: 'purple' },
  auto_api: { label: '投递即生成', color: 'gold' },
  batch: { label: '批量任务', color: 'cyan' },
};

// Arco 的色板名 → CSS 变量。用变量而不是十六进制：深浅色主题会自动跟着变，
// 写死 #165DFF 这类值在暗色主题下会偏亮、在浅色下又不够。
export const SOURCE_DOT: Record<string, string> = {
  arcoblue: 'rgb(var(--arcoblue-6))',
  purple: 'rgb(var(--purple-6))',
  cyan: 'rgb(var(--cyan-6))',
  gold: 'rgb(var(--gold-6))',
};

export const JOB_STATUS_META: Record<JobStatus, { label: string; color: string }> = {
  queued: { label: '排队中', color: 'arcoblue' },
  running: { label: '渲染中', color: 'arcoblue' },
  succeeded: { label: '成功', color: 'green' },
  partial: { label: '部分失败', color: 'orange' },
  failed: { label: '失败', color: 'red' },
};

export const DOC_STATUS_META: Record<DocumentStatus, { label: string; color: string }> = {
  queued: { label: '排队中', color: 'arcoblue' },
  running: { label: '渲染中', color: 'arcoblue' },
  succeeded: { label: '成功', color: 'green' },
  failed: { label: '失败', color: 'red' },
};

export const SEVERITY_META: Record<Severity, { label: string; color: string }> = {
  error: { label: '错误', color: 'red' },
  warning: { label: '警告', color: 'orange' },
  info: { label: '提示', color: 'arcoblue' },
};

export const KIND_META: Record<VarKind, { label: string; color: string }> = {
  scalar: { label: '标量', color: 'arcoblue' },
  loop: { label: '循环', color: 'green' },
  condition: { label: '条件', color: 'orange' },
  image: { label: '图片', color: 'magenta' },
  qrcode: { label: '二维码', color: 'purple' },
  richtext: { label: '富文本', color: 'cyan' },
  chart: { label: '图表', color: 'gold' },
};

export const VISIBILITY_META = {
  private: { label: '私有', color: 'gray' },
  public: { label: '公开', color: 'green' },
} as const;

// 轮询：running/queued 的任务与文档列表 3s 刷新
export const POLL_INTERVAL = 3000;

// code 正则（CONTRACT §4.3）
export const CODE_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/;

// 安全查找：后端返回未知枚举值时绝不抛错，灰色 Tag 显示原始值
export function lookupMeta(
  map: Record<string, { label: string; color: string }>,
  key: unknown,
): { label: string; color: string } {
  const k = typeof key === 'string' ? key : '';
  const found = map[k];
  if (found) return found;
  return { label: k || '未知', color: 'gray' };
}
