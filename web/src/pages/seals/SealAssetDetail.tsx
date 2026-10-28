/**
 * SealAssetDetail —— 签名/印章共用的资源详情
 *
 * 重构背景：SignatureDetail.tsx 与 SealDetail.tsx 曾逐行同构（预览卡 + 参数卡 +
 * 审核留痕卡 + 授权抽屉），差异仅在 API、路由前缀、参数行与图片来源文案。
 * 「参数」行因字段不同由各页面以 paramRows 注入，其余全部共用。
 *
 * 样式见 styles/seal-asset.css。
 */

import { useEffect, useState, type ReactNode } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Message,
  Popconfirm,
  Skeleton,
  Space,
  Tag,
} from '@arco-design/web-react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router-dom';
import { useBreadcrumb } from '@/components/BreadcrumbContext';
import EmptyState from '@/components/EmptyState';
import { errorMessage, fmtTime } from '@/utils/format';
import type { SealAssetBase } from './SealAssetList';
import { SEAL_STATUS_META, checkerBg, sealImageUrl } from './shared';
import GrantDrawer from './GrantDrawer';

export interface SealAssetDetailConfig<T extends SealAssetBase> {
  kind: 'signatures' | 'seals';
  /** react-query 缓存键：signature | seal */
  queryKey: string;
  /** 列表路由前缀 */
  basePath: string;
  /** 授权资源类型 */
  resourceType: 'signature' | 'seal';
  /** 资源中文名，用于文案（签名/印章） */
  assetLabel: string;
  sourceLabels: { uploaded: string; generated: string };
  /** 预览图最大尺寸 */
  imageMaxHeight?: number;
  imageMaxWidth?: number | string;
  getFn: (id: string) => Promise<T>;
  deleteFn: (id: string) => Promise<unknown>;
  /** 差异化参数行 */
  paramRows: (item: T) => { label: string; value: ReactNode }[];
}

export function SealAssetDetail<T extends SealAssetBase>({
  config,
}: {
  config: SealAssetDetailConfig<T>;
}) {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const [grantOpen, setGrantOpen] = useState(false);
  const { setCrumbs } = useBreadcrumb();

  const { data: item, isLoading } = useQuery({
    queryKey: [config.queryKey, id],
    queryFn: () => config.getFn(id),
    enabled: !!id,
  });

  useEffect(() => {
    setCrumbs(item?.name ? [item.name] : []);
  }, [item?.name, setCrumbs]);

  if (isLoading) {
    return (
      <div className="df-page">
        <Skeleton animation />
      </div>
    );
  }

  if (!item) {
    return (
      <div className="df-page">
        <EmptyState
          description={`${config.assetLabel}不存在`}
          action={<Button onClick={() => navigate(config.basePath)}>返回{config.assetLabel}库</Button>}
        />
      </div>
    );
  }

  const st = SEAL_STATUS_META[item.status] || { label: item.status, color: 'gray' };

  const handleDelete = async () => {
    try {
      await config.deleteFn(id);
      Message.success('已删除');
      navigate(config.basePath);
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h1 className="df-page-title">
            {item.name}{' '}
            <Tag color={st.color} size="small">
              {st.label}
            </Tag>
          </h1>
        </div>
        <Space>
          <Button type="primary" onClick={() => setGrantOpen(true)}>
            授权管理
          </Button>
          <Button onClick={() => navigate(config.basePath)}>返回列表</Button>
          <Popconfirm
            title={`确认删除该${config.assetLabel}？（软删，审计保留）`}
            onConfirm={handleDelete}
          >
            <Button status="danger">删除</Button>
          </Popconfirm>
        </Space>
      </div>

      <div className="seal-detail-grid">
        <Card title="预览">
          <div className="seal-detail-preview" style={checkerBg}>
            <img
              src={sealImageUrl(config.kind, item.id, item.image_url)}
              alt={item.name}
              style={{
                maxHeight: config.imageMaxHeight ?? 240,
                maxWidth: config.imageMaxWidth ?? '90%',
              }}
            />
          </div>
        </Card>

        <Card title="参数">
          <Descriptions column={1} size="medium" data={config.paramRows(item)} />
        </Card>
      </div>

      <Card title="审核留痕" className="seal-detail-section">
        {item.status === 'pending' ? (
          <Alert type="warning" content={`该${config.assetLabel}待审核。`} />
        ) : item.reviewed_at ? (
          <div>
            <Tag color={item.status === 'approved' ? 'green' : 'red'} size="small">
              {item.status === 'approved' ? '审核通过' : '已驳回'}
            </Tag>
            <div className="seal-detail-meta">{fmtTime(item.reviewed_at)}</div>
            {item.review_note && <div className="seal-detail-note">意见：{item.review_note}</div>}
          </div>
        ) : (
          <span className="seal-detail-muted">该{config.assetLabel}免审核，创建即可用。</span>
        )}
      </Card>

      <GrantDrawer
        visible={grantOpen}
        onClose={() => setGrantOpen(false)}
        resourceType={config.resourceType}
        resourceId={item.id}
      />
    </div>
  );
}
