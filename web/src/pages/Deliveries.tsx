import { usePagination } from '@/hooks/usePagination';
import { useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Input,
  Message,
  Progress,
  Select,
  Space,
  Table,
  Tag,
} from '@arco-design/web-react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  downloadJobZip,
  getJob,
  listJobs,
  listTemplates,
  retryJob,
} from '@/api/endpoints';
import { JOB_STATUS_META, POLL_INTERVAL, lookupMeta } from '@/constants/enums';
import type { Job } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';
import PageContainer from '@/components/PageContainer';

const CB_META: Record<string, { label: string; color: string }> = {
  '': { label: '未配置', color: 'gray' },
  pending: { label: '回调中', color: 'arcoblue' },
  succeeded: { label: '回调成功', color: 'green' },
  failed: { label: '回调失败', color: 'red' },
};

// 投递任务视图（PRD §4.7）：复用 /jobs 接口，突出 external_ref / 进度 / 回调状态 / 失败明细
export default function DeliveriesPage() {
  const qc = useQueryClient();
  const [status, setStatus] = useState('');
  const [cbStatus, setCbStatus] = useState('');
  const [ref, setRef] = useState('');
  const { page, setPage, pagination } = usePagination();
  const [detailId, setDetailId] = useState<string | null>(null);

  const { data: tpls, isLoading: tplsLoading } = useQuery({
    queryKey: ['templates', { all: true }],
    queryFn: () => listTemplates({ page: 1, page_size: 200 }),
  });
  const tplMap = useMemo(() => {
    const m: Record<string, string> = {};
    (tpls?.items || []).forEach((t) => (m[t.id] = t.name));
    return m;
  }, [tpls]);

  const { data, isLoading } = useQuery({
    queryKey: ['jobs', { status, page }],
    queryFn: () => listJobs({ status: status || undefined, page, page_size: 20 }),
    refetchInterval: (q) => {
      const rows = (q.state.data as { items?: Job[] } | undefined)?.items || [];
      return rows.some((r) => r.status === 'queued' || r.status === 'running') ? POLL_INTERVAL : false;
    },
  });

  // 前端过滤：回调状态 / external_ref（后端列表暂不支持这两个查询参数）
  const rows = (data?.items || []).filter((j) => {
    if (cbStatus && (j.callback_status || '') !== cbStatus) return false;
    if (ref && !(j.external_ref || '').toLowerCase().includes(ref.toLowerCase())) return false;
    return true;
  });

  const retry = async (j: Job) => {
    try {
      await retryJob(j.id, true);
      Message.success('已重新排队（仅失败条目）');
      qc.invalidateQueries({ queryKey: ['jobs'] });
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  const downloadZip = async (j: Job) => {
    try {
      const { blob, filename } = await downloadJobZip(j.id);
      triggerDownload(blob, filename || `${j.id}.zip`);
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  return (
    <PageContainer
      title="投递任务"
      subtitle="外部系统 API 投递与批量任务：进度、回调状态与失败明细"
      toolbar={
        <>
          <Select
            placeholder="状态：全部"
            allowClear
            style={{ width: 140 }}
            onChange={(v) => {
              setStatus(v || '');
              setPage(1);
            }}
          >
            {Object.entries(JOB_STATUS_META).map(([k, m]) => (
              <Select.Option key={k} value={k}>{m.label}</Select.Option>
            ))}
          </Select>
          <Select
            placeholder="回调状态：全部"
            allowClear
            style={{ width: 150 }}
            onChange={(v) => setCbStatus(v || '')}
          >
            {Object.entries(CB_META).map(([k, m]) => (
              <Select.Option key={k} value={k}>{m.label}</Select.Option>
            ))}
          </Select>
          <Input.Search
            placeholder="external_ref 搜索"
            style={{ width: 220 }}
            onSearch={(v) => setRef(v)}
            allowClear
          />
        </>
      }
    >
      <div className="data-card">
        <Table<Job>
        rowKey="id"
        loading={isLoading}
        data={rows}
        scroll={{ x: 1360 }}
        pagination={pagination(data?.total)}
        noDataElement={<div className="empty-block empty-block-desc">还没有投递任务</div>}
        columns={[
          // 时间列是「排查」这个动作的入口：以前这张表只有任务 ID，
          // 出问题时要拿 ID 去别处对时间，而运维排查的第一步几乎总是「几点开始的」。
          {
            title: '时间',
            dataIndex: 'created_at',
            width: 160,
            render: (v: string) => <span style={{ whiteSpace: 'nowrap' }}>{fmtTime(v)}</span>,
          },
          { title: '任务ID', dataIndex: 'id', width: 170, render: (v: string) => <code style={{ fontSize: 12 }}>{v}</code> },
          {
            title: '模板',
            dataIndex: 'template_id',
            width: 140,
            render: (v: string) => {
              if (!v) return '—';
              const name = tplMap[v];
              if (name) return name;
              // 模板被删掉后这里只剩一个 tpl_xxxx 原始 ID，读的人不知道那是什么。
              // 但模板列表还没回来时也不能说「已删除」——那样每次进页面都会先闪一下
              // 「已删除模板」，再变成正常名字。
              return tplsLoading ? (
                <span className="cell-sub">{v.slice(0, 8)}…</span>
              ) : (
                <span className="cell-sub" title={v}>已删除模板 · {v.slice(0, 8)}</span>
              );
            },
          },
          { title: 'external_ref', dataIndex: 'external_ref', width: 160, render: (v: string) => v || '—' },
          {
            title: '进度',
            dataIndex: 'progress',
            width: 150,
            render: (_: unknown, r: Job) => {
              const pct = r.total ? Math.round(((r.succeeded + r.failed) / r.total) * 100) : 0;
              return (
                <div>
                  <Progress percent={pct} size="small" status={r.status === 'failed' ? 'error' : undefined} />
                  <div style={{ fontSize: 11, color: 'var(--color-text-3)' }}>{r.succeeded}/{r.total}（失败 {r.failed}）</div>
                </div>
              );
            },
          },
          {
            title: '状态',
            dataIndex: 'status',
            width: 100,
            render: (s: string) => {
              const m = lookupMeta(JOB_STATUS_META, s);
              return <Tag color={m.color}>{m.label}</Tag>;
            },
          },
          {
            title: '回调状态',
            dataIndex: 'callback_status',
            width: 110,
            render: (s: string) => {
              const m = CB_META[s || ''] || CB_META[''];
              return <Tag color={m.color} size="small">{m.label}</Tag>;
            },
          },
          {
            title: '失败明细',
            dataIndex: 'errors',
            width: 120,
            render: (_: unknown, r: Job) =>
              r.failed > 0 ? (
                <Button size="mini" type="text" onClick={() => setDetailId(r.id)}>{r.failed} 条失败</Button>
              ) : (
                <span>—</span>
              ),
          },
          {
            title: '操作',
            dataIndex: 'actions',
            width: 220,
            render: (_: unknown, r: Job) => (
              <Space size={4}>
                <Button size="mini" onClick={() => setDetailId(r.id)}>详情</Button>
                <Button size="mini" onClick={() => downloadZip(r)} disabled={['queued', 'running'].includes(r.status)}>
                  zip 下载
                </Button>
                {(r.status === 'failed' || r.status === 'partial') && (
                  <Button size="mini" status="warning" onClick={() => retry(r)}>重试</Button>
                )}
              </Space>
            ),
          },
        ]}
        />
      </div>

      <JobDrawer jobId={detailId} onClose={() => setDetailId(null)} />
    </PageContainer>
  );
}

function JobDrawer({ jobId, onClose }: { jobId: string | null; onClose: () => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ['job', jobId],
    queryFn: () => getJob(jobId!),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && ['queued', 'running'].includes(q.state.data.status) ? POLL_INTERVAL : false),
  });

  const downloadZip = async () => {
    if (!jobId) return;
    try {
      const { blob, filename } = await downloadJobZip(jobId);
      triggerDownload(blob, filename || `${jobId}.zip`);
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  return (
    <Drawer
      title={`投递任务：${jobId || ''}`}
      visible={!!jobId}
      onCancel={onClose}
      width={620}
      unmountOnExit
      footer={data?.zip_download_url ? <Button type="primary" long onClick={downloadZip}>下载整包 zip</Button> : undefined}
    >
      {isLoading ? (
        <div>加载中…</div>
      ) : data ? (
        <>
          <Descriptions
            column={1}
            size="mini"
            data={[
              { label: '状态', value: lookupMeta(JOB_STATUS_META, data.status).label },
              { label: '进度', value: `${data.succeeded}/${data.total} 成功，${data.failed} 失败` },
              { label: 'external_ref', value: data.external_ref || '—' },
              {
                label: '回调',
                value: data.callback_url
                  ? `${CB_META[data.callback_status || '']?.label || data.callback_status}（已尝试 ${data.callback_attempts || 0} 次）→ ${data.callback_url}`
                  : '未配置回调',
              },
              { label: '创建', value: fmtTime(data.created_at) },
            ]}
          />
          {(data.errors || []).length > 0 && (
            <Alert
              style={{ margin: '12px 0' }}
              type="error"
              content={
                <div>
                  {(data.errors || []).map((e, i) => (
                    <div key={i}>
                      第 {(e.record_index ?? 0) + 1} 条：{e.error}
                      {e.path ? `（${e.path}）` : ''}
                    </div>
                  ))}
                </div>
              }
            />
          )}
          <Table
            size="small"
            rowKey="document_id"
            pagination={false}
            data={data.documents || []}
            columns={[
              { title: '#', dataIndex: 'record_index', width: 50 },
              { title: '文件名', dataIndex: 'filename' },
              { title: '状态', dataIndex: 'status', render: (s: string) => <Tag size="small">{s}</Tag> },
              { title: '错误', dataIndex: 'error', render: (v: string) => v || '—' },
            ]}
          />
        </>
      ) : null}
    </Drawer>
  );
}

function triggerDownload(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}
