import { useMemo, useState } from 'react';
import { Button, Empty, Message, Modal, Spin, Tag } from '@arco-design/web-react';
import { useQuery } from '@tanstack/react-query';
import { listUsableSeals, listUsableSignatures, type UsableAsset } from '@/api/endpoints';

interface Props {
  visible: boolean;
  /** 该变量要的是签名还是印章：由变量名前缀 sig_ / seal_ 决定 */
  kind: 'signature' | 'seal';
  /** 变量名，仅用于标题提示 */
  variableName: string;
  onClose: () => void;
  onSelect: (asset: UsableAsset) => void;
}

/**
 * 选签名 / 选印章。
 *
 * 刻意不用 /signatures、/seals 列表：那两个是「我上传过什么」（按 owner 过滤），
 * 而被授权的人要选的是「我能盖哪个」。用错接口的后果是列表为空、用户以为没权限，
 * 其实他有一条别人给的授权正等着用。
 *
 * 缩略图走 image_url，后端对「持有效授权的人」也放行——签章是视觉资产，
 * 只能凭名字选等于让人闭着眼睛盖章。
 */
export default function SignAssetPicker({ visible, kind, variableName, onClose, onSelect }: Props) {
  const [loadingId, setLoadingId] = useState<string | null>(null);
  const isSeal = kind === 'seal';

  const { data, isLoading } = useQuery({
    queryKey: ['usable-assets', kind],
    queryFn: isSeal ? listUsableSeals : listUsableSignatures,
    enabled: visible,
  });

  const items = data?.items || [];
  const title = useMemo(
    () => `${isSeal ? '选择印章' : '选择签名'} · ${variableName}`,
    [isSeal, variableName],
  );

  const pick = (a: UsableAsset) => {
    setLoadingId(a.id);
    try {
      onSelect(a);
      Message.success(`已选用「${a.name}」`);
      onClose();
    } finally {
      setLoadingId(null);
    }
  };

  return (
    <Modal
      title={title}
      visible={visible}
      onCancel={onClose}
      footer={null}
      style={{ width: 720 }}
      unmountOnExit
    >
      <div className="sign-picker-hint">
        只列出<strong>你可用</strong>的{isSeal ? '印章' : '签名'}：自己上传且已审批通过的，
        以及别人授权给你的。「剩余次数」为 0 表示不限次数。
      </div>
      {isLoading ? (
        <div className="sign-picker-loading">
          <Spin />
        </div>
      ) : items.length === 0 ? (
        <Empty
          description={
            isSeal
              ? '没有可用印章。请先到「签章中心 / 印章库」上传并等审核通过，或请所有者为你的账号授权。'
              : '没有可用签名。请先到「签章中心 / 签名库」创建并等审核通过，或请所有者为你的账号授权。'
          }
        />
      ) : (
        <div className="sign-picker-grid">
          {items.map((a) => (
            <div className="sign-picker-card" key={a.id}>
              <div className="sign-picker-thumb">
                {/* 用原生 img：签章是后端直出的 PNG，且带鉴权 Cookie，不需要额外处理 */}
                <img src={a.image_url} alt={a.name} />
              </div>
              <div className="sign-picker-name" title={a.name}>
                {a.name}
              </div>
              <div className="sign-picker-meta">
                {a.owned ? (
                  <Tag size="small" color="arcoblue">我的</Tag>
                ) : (
                  <Tag size="small" color="orange">{a.owner_name || '他人'}授权</Tag>
                )}
                <Tag size="small" color={a.remaining === 0 ? 'green' : 'gold'}>
                  {a.remaining === 0 ? '不限次数' : `剩 ${a.remaining} 次`}
                </Tag>
              </div>
              <div className="sign-picker-size">
                {a.width_mm}×{a.height_mm} mm
              </div>
              <Button
                size="mini"
                type="primary"
                long
                loading={loadingId === a.id}
                onClick={() => pick(a)}
              >
                选用
              </Button>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
