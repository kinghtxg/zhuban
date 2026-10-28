/**
 * SealAssetList —— 签名/印章共用的资源列表
 *
 * 重构背景：SignatureList.tsx 与 SealList.tsx 曾逐行同构（搜索框 + 状态筛选 +
 * 骨架屏 + 空态 + 卡片网格 + 删除 Dropdown），差异仅在标题、API、路由前缀和
 * 个别文案。此处收敛为一份泛型实现，两个页面退化为配置。
 *
 * 样式见 styles/seal-asset.css；颜色与间距全部取自 tokens。
 */

import { useState } from 'react';
import {
  Button,
  Dropdown,
  Empty,
  Input,
  Menu,
  Message,
  Popconfirm,
  Select,
  Skeleton,
  Tag,
} from '@arco-design/web-react';
import { IconDown, IconPlus } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import type { SealSource, SealStatus } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';
import PageContainer from '@/components/PageContainer';
import { SEAL_STATUS_META, checkerBg, sealImageUrl, usableTag } from './shared';
import '@/styles/seal-asset.css';

/** 签名 / 印章共有的字段集合 —— 列表与详情通用部分只需这些 */
export interface SealAssetBase {
  id: string;
  name: string;
  status: SealStatus;
  source: SealSource;
  created_at: string;
  image_url?: string;
  usable?: boolean;
  grant_remaining?: number;
  /** 以下为详情页「审核留痕 / 引用数」共用字段 */
  reviewed_at?: string;
  review_note?: string;
  ref_count?: number;
}

/** 列表查询参数（与 endpoints 中的 SealListQuery 字段一致） */
export interface SealAssetQuery {
  keyword?: string;
  status?: string;
  source?: string;
  page?: number;
  page_size?: number;
}

export interface SealAssetListConfig<T extends SealAssetBase> {
  /** 资源类型，用于拼接图片地址 /api/v1/{kind}/{id}/image */
  kind: 'signatures' | 'seals';
  title: string;
  subtitle: string;
  createLabel: string;
  emptyText: string;
  /** 列表路由前缀，如 /seals-center/signatures */
  basePath: string;
  /** react-query 缓存键：signatures | seals */
  queryKey: string;
  /** 来源文案：上传图 / 文字生成(签名) 或 参数生成(印章) */
  sourceLabels: { uploaded: string; generated: string };
  /** 是否展示「来源」筛选（签名有，印章无） */
  showSourceFilter?: boolean;
  /** 卡片内图片最大宽度 */
  imageMaxWidth?: number | string;
  listFn: (q: SealAssetQuery) => Promise<{ items: T[]; total?: number }>;
  deleteFn: (id: string) => Promise<unknown>;
}

export function SealAssetList<T extends SealAssetBase>({
  config,
}: {
  config: SealAssetListConfig<T>;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [q, setQ] = useState('');
  const [status, setStatus] = useState('');
  const [source, setSource] = useState('');

  const { data, isLoading } = useQuery({
    queryKey: [config.queryKey, { q, status, source }],
    queryFn: () =>
      config.listFn({
        keyword: q || undefined,
        status: status || undefined,
        source: source || undefined,
        page: 1,
        page_size: 100,
      }),
  });

  const delMut = useMutation({
    mutationFn: (id: string) => config.deleteFn(id),
    onSuccess: () => {
      Message.success('已删除');
      qc.invalidateQueries({ queryKey: [config.queryKey] });
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const cards = data?.items || [];

  return (
    <PageContainer
      title={config.title}
      subtitle={config.subtitle}
      extra={
        <Button type="primary" icon={<IconPlus />} onClick={() => navigate(`${config.basePath}/new`)}>
          {config.createLabel}
        </Button>
      }
      toolbar={
        <>
          <Input.Search
            placeholder="按名称搜索"
            style={{ width: 240 }}
            onSearch={(v) => setQ(v)}
            allowClear
          />
          <Select
            placeholder="状态：全部"
            allowClear
            style={{ width: 140 }}
            onChange={(v) => setStatus(v || '')}
          >
            {Object.entries(SEAL_STATUS_META).map(([k, m]) => (
              <Select.Option key={k} value={k}>
                {m.label}
              </Select.Option>
            ))}
          </Select>
          {config.showSourceFilter && (
            <Select
              placeholder="来源：全部"
              allowClear
              style={{ width: 140 }}
              onChange={(v) => setSource(v || '')}
            >
              <Select.Option value="upload">{config.sourceLabels.uploaded}</Select.Option>
              <Select.Option value="generated">{config.sourceLabels.generated}</Select.Option>
            </Select>
          )}
        </>
      }
    >
      {isLoading ? (
        <Skeleton animation />
      ) : cards.length === 0 ? (
        <div className="empty-block empty-surface">
          <Empty description={config.emptyText} />
          <Button type="primary" icon={<IconPlus />} onClick={() => navigate(`${config.basePath}/new`)}>
            {config.createLabel}
          </Button>
        </div>
      ) : (
        <div className="card-grid">
          {cards.map((item) => (
            <AssetCard
              key={item.id}
              item={item}
              config={config}
              onDelete={() => delMut.mutate(item.id)}
            />
          ))}
        </div>
      )}
    </PageContainer>
  );
}

function AssetCard<T extends SealAssetBase>({
  item,
  config,
  onDelete,
}: {
  item: T;
  config: SealAssetListConfig<T>;
  onDelete: () => void;
}) {
  const navigate = useNavigate();
  const st = SEAL_STATUS_META[item.status] || { label: item.status, color: 'gray' };
  const detailPath = `${config.basePath}/${item.id}`;
  const usable = usableTag(item.usable, item.grant_remaining);

  return (
    <div className="seal-asset-card">
      <div className="seal-asset-thumb" style={checkerBg}>
        <img
          src={sealImageUrl(config.kind, item.id, item.image_url)}
          alt={item.name}
          style={{ maxWidth: config.imageMaxWidth ?? '90%' }}
          onError={(e) => ((e.target as HTMLImageElement).style.opacity = '0.25')}
        />
      </div>

      <div className="seal-asset-row">
        <strong className="seal-asset-name">{item.name}</strong>
        <Tag color={st.color} size="small">
          {st.label}
        </Tag>
      </div>

      <div className="seal-asset-meta">
        {item.source === 'generated' ? config.sourceLabels.generated : config.sourceLabels.uploaded}
        {' · '}
        {fmtTime(item.created_at).slice(0, 10)}
      </div>

      {usable && (
        <div className="seal-asset-usable">
          <Tag size="small" color={item.usable ? 'green' : 'gray'}>
            {usable}
          </Tag>
        </div>
      )}

      <div className="seal-asset-actions">
        <Button size="mini" type="primary" onClick={() => navigate(detailPath)}>
          详情
        </Button>
        <Button size="mini" onClick={() => navigate(detailPath)}>
          编辑
        </Button>
        <Dropdown
          position="br"
          droplist={
            <Menu onClickMenuItem={(k) => k === 'del' && onDelete()}>
              <Menu.Item key="del">
                <Popconfirm title="确认删除？（软删，审计保留）" onOk={onDelete}>
                  <span className="seal-asset-danger">删除</span>
                </Popconfirm>
              </Menu.Item>
            </Menu>
          }
        >
          <Button size="mini">
            更多 <IconDown />
          </Button>
        </Dropdown>
      </div>
    </div>
  );
}
