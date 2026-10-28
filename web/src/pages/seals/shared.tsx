import type { SealStatus } from '@/types/api';

// 签章图片 URL：优先后端返回的 image_url，否则按约定 GET /{kind}/{id}/image
export function sealImageUrl(kind: 'signatures' | 'seals', id: string, imageUrl?: string) {
  if (imageUrl) return imageUrl.startsWith('http') ? imageUrl : `/api/v1${imageUrl.startsWith('/') ? '' : '/'}${imageUrl}`;
  return `/api/v1/${kind}/${id}/image`;
}

export const SEAL_STATUS_META: Record<SealStatus, { label: string; color: string }> = {
  pending: { label: '待审核', color: 'orange' },
  approved: { label: '可用', color: 'green' },
  rejected: { label: '已驳回', color: 'red' },
  disabled: { label: '停用', color: 'gray' },
};

// 透明底棋盘格底衬
export const checkerBg: React.CSSProperties = {
  backgroundColor: '#fff',
  backgroundImage:
    'linear-gradient(45deg,var(--color-border-2) 25%,transparent 25%),linear-gradient(-45deg,var(--color-border-2) 25%,transparent 25%),linear-gradient(45deg,transparent 75%,var(--color-border-2) 75%),linear-gradient(-45deg,transparent 75%,var(--color-border-2) 75%)',
  backgroundSize: '16px 16px',
  backgroundPosition: '0 0,0 8px,8px -8px,-8px 0',
};

// V3.1：当前调用人可用性标签（owner/admin 隐式可用，由后端 usable 标注）
export function usableTag(usable?: boolean, grantRemaining?: number) {
  if (usable == null) return null;
  return usable
    ? grantRemaining != null
      ? `可用 · 余 ${grantRemaining} 次`
      : '可用'
    : '未授权';
}
