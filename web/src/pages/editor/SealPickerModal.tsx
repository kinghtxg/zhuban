import { useEffect, useMemo, useState } from 'react';
import { Button, Empty, Input, Modal, Spin, Tag, Message } from '@arco-design/web-react';
import {
  listApprovedSeals,
  listApprovedSignals,
  sealImageUrl,
  type SealSealItem,
} from './signsealApi';

interface Props {
  kind: 'signature' | 'seal';
  onClose: () => void;
  onSelect: (item: SealSealItem) => void;
}

// 插入签名/印章占位：选 approved 资源，缩略图网格（透明底用浅灰格子衬底）。
export default function SealPickerModal({ kind, onClose, onSelect }: Props) {
  const [items, setItems] = useState<SealSealItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [kw, setKw] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    (kind === 'signature' ? listApprovedSignals() : listApprovedSeals())
      .then((list) => {
        if (!cancelled) setItems(list);
      })
      .catch((e) => {
        if (!cancelled) Message.error(e?.message || '加载签章列表失败');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [kind]);

  const filtered = useMemo(() => {
    const k = kw.trim().toLowerCase();
    if (!k) return items;
    return items.filter((it) => it.name.toLowerCase().includes(k) || it.id.toLowerCase().includes(k));
  }, [items, kw]);

  const title = kind === 'signature' ? '插入签名' : '插入印章';
  const centerPath = kind === 'signature' ? '/seals-center/signatures' : '/seals-center/seals';

  return (
    <Modal
      title={title}
      visible
      onCancel={onClose}
      footer={null}
      style={{ width: 640 }}
    >
      <Input.Search
        allowClear
        value={kw}
        onChange={setKw}
        placeholder="搜索名称或 ID"
        style={{ marginBottom: 12 }}
      />
      {loading ? (
        <div style={{ textAlign: 'center', padding: 40 }}><Spin /></div>
      ) : filtered.length === 0 ? (
        <Empty
          description={
            <span>
              暂无对你可用的{kind === 'signature' ? '签名' : '印章'}（仅显示已审核通过且你有使用权限的资源）
              <br />
              <a href={centerPath} target="_blank" rel="noreferrer">
                前往签章中心创建 →
              </a>
            </span>
          }
        />
      ) : (
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(3, 1fr)',
            gap: 12,
            maxHeight: 380,
            overflow: 'auto',
          }}
        >
          {filtered.map((it) => (
            <button
              key={it.id}
              onClick={() => onSelect(it)}
              style={{
                border: '1px solid var(--color-border-2)',
                borderRadius: 6,
                padding: 8,
                background: '#fff',
                cursor: 'pointer',
                textAlign: 'center',
              }}
            >
              <div
                className="df-checker"
                style={{ height: 72, display: 'flex', alignItems: 'center', justifyContent: 'center' }}
              >
                <img
                  src={sealImageUrl(kind, it.id)}
                  alt={it.name}
                  style={{ maxHeight: 64, maxWidth: '90%', objectFit: 'contain' }}
                />
              </div>
              <div style={{ fontSize: 12, marginTop: 6, color: '#1d2129' }}>{it.name}</div>
              <Tag size="small" color={it.is_owner ? 'blue' : 'green'}>
                {it.is_owner
                  ? '我的资源'
                  : it.grant_remaining === null || it.grant_remaining === undefined
                    ? '不限次数'
                    : `剩余 ${it.grant_remaining} 次`}
              </Tag>
            </button>
          ))}
        </div>
      )}
      <div style={{ marginTop: 12, textAlign: 'right' }}>
        <Button onClick={onClose}>取消</Button>
      </div>
    </Modal>
  );
}
