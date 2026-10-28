import { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Message, Pagination, Spin, Tag, Tooltip } from '@arco-design/web-react';
import { IconRefresh } from '@arco-design/web-react/icon';
import { preview } from '@/api/endpoints';
import type { PreviewResp } from '@/types/api';
import { errorMessage } from '@/utils/format';
import { sameOriginUrl } from '@/api/client';
import EmptyState from './EmptyState';

interface Props {
  templateId: string;
  versionId?: string;
  data: unknown; // 已解析的 JSON 对象（null 表示空）
  editKey: number; // 编辑版本号，触发防抖重算
}

// 预览面板：编辑后防抖 1.5s 调 /preview，按页展示 PNG
export default function PreviewPane({ templateId, versionId, data, editKey }: Props) {
  const [result, setResult] = useState<PreviewResp | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>('');
  const [page, setPage] = useState(1);
  const [zoom, setZoom] = useState(1);
  const [bump, setBump] = useState(0);

  const dataStr = useMemo(() => {
    if (data === null || data === undefined) return '';
    try {
      return JSON.stringify(data);
    } catch {
      return '';
    }
  }, [data]);

  useEffect(() => {
    if (!dataStr) {
      setResult(null);
      setError('');
      return;
    }
    setLoading(true);
    setError('');
    const t = setTimeout(() => {
      preview({ template_id: templateId, version_id: versionId, data, max_pages: 6 })
        .then((r) => {
          setResult(r);
          setPage(1);
        })
        .catch((err) => setError(errorMessage(err).summary))
        .finally(() => setLoading(false));
    }, 1500);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataStr, editKey, bump, templateId, versionId]);

  if (!dataStr) {
    return (
      <EmptyState
        description="JSON 为空"
        action={<div style={{ color: 'var(--color-text-3)', fontSize: 12 }}>先点击左侧「填充示例 JSON」，再改成你的真实数据。</div>}
      />
    );
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <Space>
          <Tooltip content="重新预览">
            <Button
              size="mini"
              icon={<IconRefresh />}
              onClick={() => setBump((k) => k + 1)}
            />
          </Tooltip>
          <Button.Group>
            <Button size="mini" onClick={() => setZoom((z) => Math.max(0.6, +(z - 0.2).toFixed(1)))}>
              缩小
            </Button>
            <Button size="mini" onClick={() => setZoom((z) => Math.min(2, +(z + 0.2).toFixed(1)))}>
              放大
            </Button>
          </Button.Group>
          {result && <Tag color="gray">缩放 {Math.round(zoom * 100)}%</Tag>}
        </Space>
      </div>

      {loading && (
        <Spin style={{ width: '100%', display: 'flex', justifyContent: 'center', padding: 40 }} />
      )}

      {error && !loading && (
        <Alert type="error" style={{ marginBottom: 8 }} content={`预览失败：${error}`} />
      )}

      {!loading && !error && result && (
        <>
          {result.pages.length === 0 ? (
            <EmptyState description="未生成预览页" />
          ) : (
            result.pages.slice(page - 1, page).map((p) => (
              <img
                key={p.page}
                className="df-preview-img"
                src={sameOriginUrl(p.url)}
                style={{ transform: `scale(${zoom})`, transformOrigin: 'top left' }}
                alt={`第 ${p.page} 页`}
              />
            ))
          )}
          {result.returned_pages > 0 && (
            <Pagination
              style={{ marginTop: 8 }}
              size="mini"
              current={page}
              total={result.page_count}
              pageSize={1}
              onChange={(p) => {
                setPage(p);
                Message.info(`预览最多返回 ${result.returned_pages} 页；正式生成不受限制`);
              }}
            />
          )}
          <Alert
            type="warning"
            style={{ marginTop: 8, fontSize: 12 }}
            content="预览页带「铸版预览」水印且限前几页；正式交付以 docx 在 Word 中打开为准。"
          />
        </>
      )}
    </div>
  );
}

function Space({ children }: { children: React.ReactNode }) {
  return <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>{children}</div>;
}
