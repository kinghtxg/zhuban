import { usePagination } from '@/hooks/usePagination';
import { useState } from 'react';
import { Alert, Button, DatePicker, Descriptions, Input, Message, Select, Table, Tag } from '@arco-design/web-react';
import { IconDownload, IconRefresh } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { exportAuditLogs, listAuditLogs, verifyAuditChain } from '@/api/endpoints';
import type { AuditLog } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';
import PageContainer from '@/components/PageContainer';

const { RangePicker } = DatePicker;

export default function AuditLogs() {
  const qc = useQueryClient();
  const [action, setAction] = useState('');
  const [actor, setActor] = useState('');
  const [objectType, setObjectType] = useState('');
  const [tplCode, setTplCode] = useState('');
  const [range, setRange] = useState<string[]>([]);
  const { page, pagination } = usePagination();

  const { data, isLoading } = useQuery({
    queryKey: ['audit-logs', { action, actor, objectType, tplCode, range, page }],
    queryFn: () =>
      listAuditLogs({
        action: action || undefined,
        actor: actor || undefined,
        object_type: objectType || undefined,
        template_code: tplCode || undefined,
        from: range?.[0],
        to: range?.[1],
        page,
        page_size: 20,
      }),
  });

  const verifyQ = useQuery({ queryKey: ['audit-verify'], queryFn: verifyAuditChain, retry: false });

  const exportMut = useMutation({
    mutationFn: () => exportAuditLogs(range?.[0], range?.[1]),
    onSuccess: ({ blob, filename }) => {
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename || `audit-${Date.now()}.csv`;
      a.click();
      URL.revokeObjectURL(url);
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const v = verifyQ.data;

  return (
    <PageContainer
      title="审计日志"
      subtitle="防篡改哈希链审计追溯；只增不改，可校验、可导出"
      extra={
        <>
          <Button icon={<IconRefresh />} onClick={() => { verifyQ.refetch(); qc.invalidateQueries({ queryKey: ['audit-logs'] }); }}>
            完整性校验
          </Button>
          <Button type="primary" icon={<IconDownload />} loading={exportMut.isPending} onClick={() => exportMut.mutate()}>
            导出 CSV
          </Button>
        </>
      }
    >
      {v && !v.ok && (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          content={`第 ${v.broken_at_seq} 条哈希不一致，疑似被篡改。expected=${v.expected_hash} actual=${v.actual_hash}`}
        />
      )}
      {v && v.ok && (
        <Alert
          style={{ marginBottom: 12 }}
          type="success"
          content={`链路完整，共 ${v.checked} 条记录${v.last_verified_at ? `，最近校验 ${fmtTime(v.last_verified_at)}` : ''}`}
        />
      )}

      <div className="page-toolbar">
        <Input placeholder="操作类型 action" style={{ width: 170 }} onChange={setAction} allowClear />
        <Input placeholder="调用人 actor" style={{ width: 150 }} onChange={setActor} allowClear />
        <Select
          placeholder="对象类型"
          style={{ width: 170 }}
          value={objectType || undefined}
          onChange={(v) => setObjectType(v || '')}
          allowClear
        >
          {OBJECT_TYPES.map((t) => (
            <Select.Option key={t.value} value={t.value}>
              {t.label}
            </Select.Option>
          ))}
        </Select>
        <Input placeholder="模板 code" style={{ width: 150 }} onChange={setTplCode} allowClear />
        <RangePicker showTime onOk={(d) => setRange(d as string[])} />
        <Button
          size="default"
          onClick={() => {
            setAction(MAIL_ACTION);
            setObjectType('email');
          }}
        >
          只看邮件投递
        </Button>
      </div>

      <div className="data-card">
        <Table<AuditLog>
        rowKey="id"
        loading={isLoading}
        data={data?.items || []}
        expandedRowRender={(r: AuditLog) => <AuditDetail row={r} />}
        pagination={pagination(data?.total)}
        columns={[
          { title: '时间', dataIndex: 'occurred_at', width: 170, render: (v: string) => fmtTime(v) },
          {
            title: '调用人',
            dataIndex: 'actor_name',
            width: 120,
            // 回退到 actor_user_id：原先写的是 r.actor_id，字段名根本不存在，
            // 所以只要 name 为空就一律显示「—」，看不出是谁做的。
            render: (v: string, r: AuditLog) => v || r.actor_user_id || actorTypeLabel(r.actor_type),
          },
          {
            title: '动作',
            dataIndex: 'action',
            width: 170,
            render: (v: string) => (
              <Tag size="small" color={v === MAIL_ACTION ? 'purple' : undefined}>
                {v}
              </Tag>
            ),
          },
          {
            title: '对象',
            dataIndex: 'object_id',
            width: 150,
            render: (v: string, r) => `${r.object_type}/${v}`,
          },
          {
            title: '摘要',
            dataIndex: 'detail',
            ellipsis: true,
            render: (_: unknown, r: AuditLog) => <DetailSummary row={r} />,
          },
          { title: '模板code', dataIndex: 'template_code', width: 110, render: (v: string) => v || '—' },
          { title: 'IP', dataIndex: 'source_ip', width: 120, render: (v: string) => v || '—' },
        ]}
        />
      </div>
    </PageContainer>
  );
}

const MAIL_ACTION = 'mail.deliver';

const ACTOR_TYPE_LABEL: Record<string, string> = {
  system: '系统',
  user: '用户',
  api_key: 'API Key',
};
const actorTypeLabel = (t?: string) => (t ? ACTOR_TYPE_LABEL[t] || t : '—');

const OBJECT_TYPES = [
  { value: 'email', label: 'email（邮件投递）' },
  { value: 'job', label: 'job（任务）' },
  { value: 'document', label: 'document（文档）' },
  { value: 'template', label: 'template（模板）' },
  { value: 'signature', label: 'signature（签名）' },
  { value: 'seal', label: 'seal（印章）' },
  { value: 'settings', label: 'settings（设置）' },
  { value: 'user', label: 'user（用户）' },
];

/** 列表里的一行摘要：邮件投递显示收件人+结果，其余沿用原样。 */
function DetailSummary({ row }: { row: AuditLog }) {
  const d = (row.detail || {}) as Record<string, unknown>;
  if (row.action === MAIL_ACTION) {
    const to = Array.isArray(d.to) ? (d.to as string[]).join('、') : '—';
    const ok = d.result === 'sent';
    return (
      <span>
        <Tag size="small" color={ok ? 'green' : 'red'}>
          {ok ? '已送达' : '失败'}
        </Tag>{' '}
        <span className="cell-sub">
          {to}
          {typeof d.subject === 'string' && d.subject ? ` · ${d.subject}` : ''}
        </span>
      </span>
    );
  }
  const s = JSON.stringify(d);
  return <span className="cell-sub">{s === '{}' ? '—' : s}</span>;
}

/** 展开行：邮件投递按字段拆开看，别的类型仍然给原始 JSON（不丢信息）。 */
function AuditDetail({ row }: { row: AuditLog }) {
  const d = (row.detail || {}) as Record<string, unknown>;
  if (row.action !== MAIL_ACTION) {
    return (
      <Descriptions
        column={1}
        size="mini"
        data={[
          { label: 'endpoint', value: row.endpoint || '—' },
          { label: 'detail', value: <pre style={{ margin: 0, fontSize: 12 }}>{JSON.stringify(row.detail, null, 2)}</pre> },
        ]}
      />
    );
  }

  const arr = (v: unknown) => (Array.isArray(v) ? (v as string[]).join('、') : '—');
  const ok = d.result === 'sent';
  const atts = Object.keys(d)
    .filter((k) => k.startsWith('attachment_'))
    .sort()
    .map((k) => String(d[k]));

  return (
    <Descriptions
      column={2}
      size="mini"
      labelStyle={{ minWidth: 88, whiteSpace: 'nowrap' }}
      data={[
        { label: '投递结果', value: <Tag size="small" color={ok ? 'green' : 'red'}>{String(d.result ?? '—')}</Tag> },
        { label: '尝试次数', value: String(d.attempts ?? '—') },
        { label: '收件人', value: arr(d.to) },
        { label: '抄送', value: arr(d.cc) },
        { label: '密送', value: arr(d.bcc) },
        { label: '来源', value: String(d.source ?? '—') },
        { label: '主题', value: String(d.subject || '—') },
        { label: '附件', value: atts.length ? `${atts.length} 个：${atts.join('、')}` : '无' },
        { label: '附件大小', value: d.bytes != null ? `${(Number(d.bytes) / 1048576).toFixed(2)} MB` : '—' },
        { label: '失败原因', value: d.error ? <span style={{ color: 'var(--color-danger-6)' }}>{String(d.error)}</span> : '—' },
        { label: 'endpoint', value: row.endpoint || '—' },
        {
          label: 'detail（原始）',
          span: 2,
          value: <pre style={{ margin: 0, fontSize: 12 }}>{JSON.stringify(row.detail, null, 2)}</pre>,
        },
      ]}
    />
  );
}
