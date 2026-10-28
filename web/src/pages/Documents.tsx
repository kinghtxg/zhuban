/**
 * 生成记录（文档中心）
 *
 * 布局规范：PageContainer（页头 + 工具栏 + 数据卡片）
 * 表格规范：
 *   - 操作列 fixed:right，避免表格横向滚动时操作入口被裁掉（此前的实际缺陷）
 *   - 操作层级：主「下载」下拉 → 次「更多」收纳 复制直链 / 查看 JSON / 重试
 */

import { usePagination } from '@/hooks/usePagination';
import { useMemo, useState } from 'react';
import {
  Alert,
  Button,
  DatePicker,
  Descriptions,
  Drawer,
  Dropdown,
  Input,
  Message,
  Modal,
  Progress,
  Select,
  Menu,
  Table,
  Tag,
} from '@arco-design/web-react';
import { IconDown, IconMore } from '@arco-design/web-react/icon';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createSignedUrl,
  downloadDocument,
  downloadJobZip,
  getDocument,
  getJob,
  listDocuments,
  listTemplates,
  regenerateDocument,
  retryJob,
} from '@/api/endpoints';
import { DOC_STATUS_META, JOB_STATUS_META, POLL_INTERVAL, SOURCE_DOT, SOURCE_META, lookupMeta } from '@/constants/enums';
import type { DocumentRow } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';
import PageContainer from '@/components/PageContainer';

export default function DocumentsPage() {
  const qc = useQueryClient();
  const [q, setQ] = useState('');
  const [source, setSource] = useState('');
  const [status, setStatus] = useState('');
  const [templateId, setTemplateId] = useState('');
  const [range, setRange] = useState<string[]>([]);
  const { page, setPage, pagination } = usePagination();
  const [detailId, setDetailId] = useState<string | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);

  const { data: tpls } = useQuery({ queryKey: ['templates', { all: true }], queryFn: () => listTemplates({ page: 1, page_size: 100 }) });

  const { data, isLoading } = useQuery({
    queryKey: ['documents', { q, source, status, templateId, range, page }],
    queryFn: () =>
      listDocuments({
        q: q || undefined,
        source: source || undefined,
        status: status || undefined,
        template_id: templateId || undefined,
        from: range?.[0],
        to: range?.[1],
        page,
        page_size: 20,
      }),
    refetchInterval: (query) => {
      const rows = (query.state.data as { items?: DocumentRow[] } | undefined)?.items || [];
      return rows.some((r) => r.status === 'queued' || r.status === 'running') ? POLL_INTERVAL : false;
    },
  });

  const download = async (row: DocumentRow, format: 'docx' | 'pdf') => {
    try {
      const { blob, filename } = await downloadDocument(row.id, format);
      triggerDownload(blob, filename || row.filename || `document.${format}`);
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  const copySigned = async (row: DocumentRow) => {
    try {
      const r = await createSignedUrl({ document_id: row.id });
      await navigator.clipboard.writeText(r.signed_url);
      Message.success(`签名直链已复制（24 小时有效）：${r.signed_url}`);
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  const retry = (row: DocumentRow) => {
    if (!row.job_id) return;
    Modal.confirm({
      title: '重试任务',
      content: '重试范围：仅失败条目 / 整任务重跑？',
      okText: '仅失败条目',
      cancelText: '整任务',
      onOk: () => doRetry(row.job_id!, true),
      onCancel: () => doRetry(row.job_id!, false),
    });
  };

  const doRetry = async (jid: string, onlyFailed: boolean) => {
    try {
      await retryJob(jid, onlyFailed);
      Message.success('已重新排队');
      qc.invalidateQueries({ queryKey: ['documents'] });
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  const regen = async (row: DocumentRow) => {
    try {
      await regenerateDocument(row.id);
      Message.success('已用原数据重新生成，去列表查看');
      qc.invalidateQueries({ queryKey: ['documents'] });
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  const columns = useMemo(
    () => [
      {
        title: '生成时间',
        dataIndex: 'created_at',
        width: 160,
        render: (v: string) => <span style={{ whiteSpace: 'nowrap' }}>{fmtTime(v)}</span>,
      },
      {
        title: '来源',
        dataIndex: 'source',
        width: 116,
        // 来源是「分类」，状态是「结论」。两列都用实心 Tag 时，一眼扫过去
        // 分不清哪个该先看，而这一页真正要回答的问题是「哪几份失败了」。
        // 所以来源降级成「小圆点 + 文字」：保留颜色区分，去掉胶囊底，
        // 视觉重量让给右边的实心状态标签。
        render: (s: DocumentRow['source']) => {
          const m = lookupMeta(SOURCE_META, s);
          return (
            <span className="source-cell">
              <i className="source-dot" style={{ background: SOURCE_DOT[m.color] || 'var(--color-text-3)' }} />
              {m.label}
            </span>
          );
        },
      },
      {
        title: '模板 / 版本',
        dataIndex: 'template',
        width: 176,
        render: (_: unknown, r: DocumentRow) => (
          <div>
            <div className="cell-strong">{r.template_name || r.template_code || '—'}</div>
            <div className="cell-sub">v{r.version || '—'}</div>
          </div>
        ),
      },
      {
        title: '文件名',
        dataIndex: 'filename',
        render: (v: string) => <span className="cell-strong">{v || '—'}</span>,
      },
      {
        title: '进度',
        dataIndex: 'progress',
        width: 150,
        render: (_: unknown, r: DocumentRow) => {
          // 单篇文档没有 total，以前这里直接显示「—」，于是同一列里混着
          // 「2/2 成功」和「—」两种东西——读起来像「这一列有的行不适用」，
          // 但其实是同一件事（产出了几份）。
          // 现在统一成「产出 / 总量」：单篇就是 1/1，排队中是 0/1，失败是 0/1。
          const total = r.total && r.total > 0 ? r.total : 1;
          const done = r.succeeded != null ? r.succeeded : r.status === 'succeeded' ? total : 0;
          const failed = r.failed || 0;
          const pct = Math.min(100, Math.round(((done + failed) / total) * 100));
          return (
            <div>
              <Progress
                percent={pct}
                size="small"
                status={r.status === 'failed' ? 'error' : undefined}
                formatText={(p) => `${p}%`}
              />
              <div className="cell-sub">
                {done}/{total} 成功
                {!!failed && <span className="cell-danger"> · {failed} 失败</span>}
              </div>
            </div>
          );
        },
      },
      {
        title: '状态',
        dataIndex: 'status',
        width: 88,
        render: (s: DocumentRow['status']) => {
          const meta =
            (JOB_STATUS_META as Record<string, { label: string; color: string }>)[s] ||
            (DOC_STATUS_META as Record<string, { label: string; color: string }>)[s] ||
            { label: s || '未知', color: 'gray' };
          return <Tag size="small" color={meta.color}>{meta.label}</Tag>;
        },
      },
      {
        title: '操作',
        dataIndex: 'actions',
        width: 172,
        fixed: 'right' as const,
        render: (_: unknown, r: DocumentRow) => (
          <div className="cell-actions">
            <Dropdown
              droplist={
                <Menu
                  onClickMenuItem={(key: string) => {
                    if (key === 'docx') download(r, 'docx');
                    else if (key === 'pdf') download(r, 'pdf');
                    else if (key === 'job' && r.job_id) setJobId(r.job_id);
                  }}
                >
                  <Menu.Item key="docx">下载 docx</Menu.Item>
                  <Menu.Item key="pdf">下载 pdf</Menu.Item>
                  {r.total && r.total > 1 && <Menu.Item key="job">失败明细 / 下载 zip</Menu.Item>}
                </Menu>
              }
            >
              <Button size="small">
                下载 <IconDown />
              </Button>
            </Dropdown>

            <Dropdown
              droplist={
                <Menu
                  onClickMenuItem={(key: string) => {
                    if (key === 'signed') copySigned(r);
                    else if (key === 'json') setDetailId(r.id);
                    else if (key === 'retry') retry(r);
                    else if (key === 'regen') regen(r);
                  }}
                >
                  <Menu.Item key="signed">复制签名直链</Menu.Item>
                  <Menu.Item key="json">查看输入 JSON</Menu.Item>
                  <Menu.Item key="regen">用原数据重生成</Menu.Item>
                  {(r.status === 'failed' || r.status === 'partial') && (
                    <Menu.Item key="retry">重试任务</Menu.Item>
                  )}
                </Menu>
              }
            >
              <Button size="small" icon={<IconMore />} aria-label="更多操作" />
            </Dropdown>
          </div>
        ),
      },
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return (
    <PageContainer
      title="生成记录"
      subtitle="所有来源（页面手动 / 同步 API / 投递即生成 / 批量）的产物统一在此查看与下载"
      toolbar={
        <>
          <Select
            placeholder="模板：全部"
            allowClear
            style={{ width: 200 }}
            onChange={(v) => {
              setTemplateId(v || '');
              setPage(1);
            }}
          >
            {(tpls?.items || []).map((t) => (
              <Select.Option key={t.id} value={t.id}>
                {t.name}（{t.code}）
              </Select.Option>
            ))}
          </Select>
          <Select
            placeholder="状态：全部"
            allowClear
            style={{ width: 130 }}
            onChange={(v) => {
              setStatus(v || '');
              setPage(1);
            }}
          >
            <Select.Option value="queued">排队中</Select.Option>
            <Select.Option value="running">渲染中</Select.Option>
            <Select.Option value="succeeded">成功</Select.Option>
            <Select.Option value="partial">部分失败</Select.Option>
            <Select.Option value="failed">失败</Select.Option>
          </Select>
          <Select
            placeholder="来源：全部"
            allowClear
            style={{ width: 140 }}
            onChange={(v) => {
              setSource(v || '');
              setPage(1);
            }}
          >
            {Object.entries(SOURCE_META).map(([k, m]) => (
              <Select.Option key={k} value={k}>
                {m.label}
              </Select.Option>
            ))}
          </Select>
          <DatePicker.RangePicker
            onChange={(v) => setRange((v || []).map((x) => (x ? new Date(x).toISOString() : '')))}
            style={{ width: 240 }}
          />
          <Input.Search
            placeholder="文件名 / 单号"
            style={{ width: 200 }}
            onSearch={(v) => {
              setQ(v);
              setPage(1);
            }}
          />
        </>
      }
    >
      <div className="data-card">
        <Table
          rowKey="id"
          loading={isLoading}
          columns={columns}
          data={data?.items || []}
          scroll={{ x: 1120 }}
          pagination={pagination(data?.total)}
          noDataElement={<div className="empty-block empty-block-desc">还没有生成记录，上传模板后即可开始生成</div>}
        />
      </div>

      <InputJsonDrawer documentId={detailId} onClose={() => setDetailId(null)} onRegen={regen} />
      <JobDetailDrawer jobId={jobId} onClose={() => setJobId(null)} />
    </PageContainer>
  );
}

// ---------- 单条输入 JSON Drawer ----------
function InputJsonDrawer({ documentId, onClose, onRegen }: { documentId: string | null; onClose: () => void; onRegen: (r: DocumentRow) => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ['document', documentId],
    queryFn: () => getDocument(documentId!),
    enabled: !!documentId,
  });
  return (
    <Drawer title="生成时的输入 JSON" visible={!!documentId} onCancel={onClose} width={560} unmountOnExit>
      {isLoading ? (
        <div>加载中…</div>
      ) : data ? (
        <>
          <Descriptions
            column={1}
            size="mini"
            data={[
              { label: '文件名', value: data.filename },
              { label: '操作人', value: data.operator || '—' },
              { label: '耗时', value: data.duration_ms ? `${data.duration_ms} ms` : '—' },
            ]}
            style={{ marginBottom: 12 }}
          />
          <pre className="df-codeblock" style={{ maxHeight: 400, overflow: 'auto' }}>
            {JSON.stringify(data.input_data || {}, null, 2)}
          </pre>
          <Button type="primary" long style={{ marginTop: 12 }} onClick={() => onRegen(data as DocumentRow)}>
            用此数据再生成
          </Button>
        </>
      ) : (
        <Alert type="error" content="加载失败" />
      )}
    </Drawer>
  );
}

// ---------- 批量任务详情 Drawer ----------
function JobDetailDrawer({ jobId, onClose }: { jobId: string | null; onClose: () => void }) {
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
    <Drawer title={`任务详情：${jobId || ''}`} visible={!!jobId} onCancel={onClose} width={560} unmountOnExit
      footer={data?.zip_download_url ? <Button type="primary" long onClick={downloadZip}>下载整包 zip</Button> : undefined}>
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
              { label: '创建', value: fmtTime(data.created_at) },
            ]}
          />
          {(data.errors || []).length > 0 && (
            <Alert
              style={{ margin: '12px 0' }}
              type="error"
              content={
                <div>
                  {data.errors!.map((e, i) => (
                    <div key={i}>
                      第 {e.record_index + 1} 条：{e.error}
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
              {
                title: '状态',
                dataIndex: 'status',
                render: (s: string) => {
                  const m = lookupMeta(DOC_STATUS_META, s);
                  return <Tag color={m.color}>{m.label}</Tag>;
                },
              },
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
