import { useMemo, useState } from 'react';
import {
  Alert,
  Button,
  DatePicker,
  Descriptions,
  Drawer,
  Input,
  InputNumber,
  Message,
  Modal,
  Popconfirm,
  Switch,
  Table,
  Tabs,
  Tag,
  Transfer,
} from '@arco-design/web-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createGrants,
  listAdminUsers,
  listGrants,
  listKeys,
  renewGrant,
  revokeGrant,
} from '@/api/endpoints';
import type { GrantEvent, GranteeType, ResourceType, SealGrant } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';

const TabPane = Tabs.TabPane;

const GRANT_STATUS_META: Record<string, { label: string; color: string }> = {
  active: { label: '生效中', color: 'green' },
  exhausted: { label: '已用完', color: 'gray' },
  expired: { label: '已过期', color: 'gray' },
  revoked: { label: '已吊销', color: 'red' },
  not_started: { label: '未生效', color: 'orange' },
};

// 策略徽标：次数 + 期限组合
function policyBadge(g: SealGrant) {
  const parts: string[] = [];
  if (g.max_uses === 1) parts.push('单次');
  else if (g.max_uses > 0) parts.push(`限 ${g.max_uses} 次`);
  else parts.push('不限次数');
  if (g.valid_until) parts.push(`至 ${fmtTime(g.valid_until).slice(0, 16)}`);
  return parts.join(' · ');
}

export default function GrantDrawer({
  visible,
  onClose,
  resourceType,
  resourceId,
}: {
  visible: boolean;
  onClose: () => void;
  resourceType: ResourceType;
  resourceId: string;
}) {
  const qc = useQueryClient();
  const [tab, setTab] = useState('list');
  const [createOpen, setCreateOpen] = useState(false);
  const [renewTarget, setRenewTarget] = useState<SealGrant | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['grants', resourceType, resourceId],
    queryFn: () => listGrants(resourceType, resourceId),
    enabled: visible && !!resourceId,
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['grants', resourceType, resourceId] });

  const revokeMut = useMutation({
    mutationFn: (g: SealGrant) => revokeGrant(g.id),
    onSuccess: () => { Message.success('已吊销'); refresh(); },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const rows = data?.items || [];
  const events = data?.events || [];

  return (
    <Drawer title="授权管理" visible={visible} onCancel={onClose} width={720} unmountOnExit footer={null}>
      <Alert
        style={{ marginBottom: 12 }}
        type="info"
        content="未授权的用户/API Key 无法在文档中使用该资源；资源创建者本人与管理员始终可用。"
      />
      <div style={{ marginBottom: 12 }}>
        <Button type="primary" onClick={() => setCreateOpen(true)}>+ 授予授权</Button>
      </div>

      <Tabs activeTab={tab} onChange={setTab}>
        <TabPane key="list" title={`授权列表(${rows.length})`}>
          <Table<SealGrant>
            rowKey="id"
            loading={isLoading}
            data={rows}
            pagination={false}
            columns={[
              {
                title: '被授权人',
                dataIndex: 'grantee_name',
                width: 160,
                render: (v: string, r) => (
                  <span>
                    <Tag color={r.grantee_type === 'user' ? 'arcoblue' : 'purple'} size="small">
                      {r.grantee_type === 'user' ? '用户' : 'API Key'}
                    </Tag>{' '}
                    {v || r.grantee_id}
                  </span>
                ),
              },
              { title: '策略', dataIndex: 'policy', render: (_: unknown, r) => <Tag size="small">{policyBadge(r)}</Tag> },
              {
                title: '剩余',
                dataIndex: 'used_count',
                width: 110,
                render: (_: unknown, r) =>
                  r.max_uses === 0 ? '不限' : `${r.max_uses - r.used_count}/${r.max_uses}`,
              },
              {
                title: '有效期',
                dataIndex: 'valid_until',
                width: 150,
                render: (_: unknown, r) => (r.valid_from || r.valid_until ? `${fmtTime(r.valid_from || '').slice(0, 16)} ~ ${fmtTime(r.valid_until || '').slice(0, 16)}` : '长期'),
              },
              {
                title: '状态',
                dataIndex: 'status',
                width: 90,
                render: (s: string) => {
                  const m = GRANT_STATUS_META[s] || { label: s, color: 'gray' };
                  return <Tag color={m.color} size="small">{m.label}</Tag>;
                },
              },
              {
                title: '操作',
                dataIndex: 'op',
                width: 130,
                render: (_: unknown, r) => (
                  <span style={{ display: 'flex', gap: 4 }}>
                    <Button size="mini" onClick={() => setRenewTarget(r)}>续期</Button>
                    <Popconfirm title="确认吊销该授权？" onConfirm={() => revokeMut.mutate(r)}>
                      <Button size="mini" status="danger">吊销</Button>
                    </Popconfirm>
                  </span>
                ),
              },
            ]}
          />
        </TabPane>

        <TabPane key="events" title="授权事件时间线">
          {events.length === 0 ? (
            <span style={{ color: 'var(--color-text-3)' }}>暂无事件流水</span>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {events.map((e: GrantEvent) => (
                <div key={e.id} style={{ borderLeft: '3px solid rgb(var(--primary-6))', paddingLeft: 12 }}>
                  <div>
                    <Tag size="small" color="arcoblue">{e.action}</Tag>
                    <span style={{ marginLeft: 8, color: 'var(--color-text-3)', fontSize: 12 }}>{e.operator_name || '—'} · {fmtTime(e.created_at)}</span>
                  </div>
                  {e.note && <div style={{ marginTop: 4 }}>{e.note}</div>}
                  {e.detail && (
                    <pre style={{ margin: '4px 0 0', fontSize: 12, background: '#f6f7fa', padding: 8, borderRadius: 6 }}>
                      {JSON.stringify(e.detail, null, 2)}
                    </pre>
                  )}
                </div>
              ))}
            </div>
          )}
        </TabPane>
      </Tabs>

      <GrantCreateModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onDone={() => { setCreateOpen(false); refresh(); }}
        resourceType={resourceType}
        resourceId={resourceId}
      />

      <RenewModal
        grant={renewTarget}
        onClose={() => setRenewTarget(null)}
        onDone={() => { setRenewTarget(null); refresh(); }}
      />
    </Drawer>
  );
}

// 授予授权 Modal：双 Transfer 多选用户 + Key，策略三选一/可组合
function GrantCreateModal({
  open,
  onClose,
  onDone,
  resourceType,
  resourceId,
}: {
  open: boolean;
  onClose: () => void;
  onDone: () => void;
  resourceType: ResourceType;
  resourceId: string;
}) {
  const usersQ = useQuery({ queryKey: ['admin-users', { all: true }], queryFn: () => listAdminUsers({ page: 1, page_size: 200 }), enabled: open });
  const keysQ = useQuery({ queryKey: ['keys'], queryFn: listKeys, enabled: open });

  const [selUsers, setSelUsers] = useState<string[]>([]);
  const [selKeys, setSelKeys] = useState<string[]>([]);
  const [single, setSingle] = useState(false);
  const [limited, setLimited] = useState(false);
  const [maxUses, setMaxUses] = useState(10);
  const [withWindow, setWithWindow] = useState(false);
  const [range, setRange] = useState<string[]>([]);
  const [note, setNote] = useState('');

  const userData = useMemo(
    () => (usersQ.data?.items || []).map((u) => ({ key: u.id, value: `${u.display_name || u.username}（${u.username}）` })),
    [usersQ.data],
  );
  const keyData = useMemo(
    () => (keysQ.data || []).map((k) => ({ key: k.id, value: `${k.name}（${k.prefix}…）` })),
    [keysQ.data],
  );

  const submitMut = useMutation({
    mutationFn: () => {
      const grantees: { grantee_type: GranteeType; grantee_id: string }[] = [
        ...selUsers.map((id) => ({ grantee_type: 'user' as GranteeType, grantee_id: id })),
        ...selKeys.map((id) => ({ grantee_type: 'api_key' as GranteeType, grantee_id: id })),
      ];
      const iso = (s?: string) => (s ? new Date(s.replace(' ', 'T')).toISOString() : undefined);
      const body = {
        grantees,
        max_uses: single ? 1 : limited ? maxUses : 0,
        valid_from: withWindow ? iso(range[0]) : undefined,
        valid_until: withWindow ? iso(range[1]) : undefined,
        note: note || undefined,
      };
      return createGrants(resourceType, resourceId, body);
    },
    onSuccess: () => { Message.success('已授予'); onDone(); },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const canSubmit = (selUsers.length > 0 || selKeys.length > 0) && !(single && limited);

  return (
    <Modal title="授予授权" visible={open} onCancel={onClose} onOk={() => { if (canSubmit) submitMut.mutate(); }}
      okText="授予" okButtonProps={{ disabled: !canSubmit }} style={{ width: 760 }}>
      <Descriptions column={1} size="mini" style={{ marginBottom: 12 }} />
      <div style={{ marginBottom: 8, fontWeight: 600 }}>选择被授权用户</div>
      <Transfer
        dataSource={userData}
        targetKeys={selUsers}
        onChange={(keys) => setSelUsers(keys as string[])}
        showSearch
        style={{ marginBottom: 8 }}
      />
      <div style={{ margin: '12px 0 8px', fontWeight: 600 }}>选择被授权 API Key（不显示明文）</div>
      <Transfer
        dataSource={keyData}
        targetKeys={selKeys}
        onChange={(keys) => setSelKeys(keys as string[])}
        showSearch
      />

      <div style={{ margin: '16px 0 8px', fontWeight: 600 }}>授权策略</div>
      <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'center' }}>
        <span><Switch checked={single} onChange={(v) => { setSingle(v); if (v) { setLimited(false); } }} size="small" /> 单次授权</span>
        <span>
          <Switch checked={limited} onChange={(v) => { setLimited(v); if (v) setSingle(false); }} size="small" /> 次数{' '}
          <InputNumber value={maxUses} onChange={(v) => setMaxUses(Number(v) || 1)} style={{ width: 90, marginLeft: 6 }} disabled={!limited} />
        </span>
        <span><Switch checked={withWindow} onChange={setWithWindow} size="small" /> 期限（起止）</span>
      </div>
      {withWindow && (
        <div style={{ marginTop: 10 }}>
          <DatePicker.RangePicker
            showTime={{ format: 'HH:mm' }}
            format="YYYY-MM-DD HH:mm"
            style={{ width: '100%' }}
            onChange={(dateStrings) => setRange((dateStrings as string[]) || [])}
          />
        </div>
      )}
      <div style={{ marginTop: 10 }}>
        <Input.TextArea placeholder="说明（可选）" value={note} onChange={setNote} rows={2} />
      </div>
    </Modal>
  );
}

// 续期 Modal：追加 N 次 / 调整窗口
function RenewModal({ grant, onClose, onDone }: { grant: SealGrant | null; onClose: () => void; onDone: () => void }) {
  const [addUses, setAddUses] = useState<number | undefined>();
  const [validUntil, setValidUntil] = useState('');
  const renewMut = useMutation({
    mutationFn: () =>
      renewGrant(grant!.id, {
        add_uses: addUses,
        valid_until: validUntil || undefined,
      }),
    onSuccess: () => { Message.success('已续期'); onDone(); },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  if (!grant) return null;
  return (
    <Modal title={`续期授权：${grant.grantee_name || grant.grantee_id}`} visible onCancel={onClose} onOk={() => renewMut.mutate()}
      okText="保存">
      <div style={{ marginBottom: 8 }}>当前：{grant.max_uses === 0 ? '不限次数' : `已用 ${grant.used_count}/${grant.max_uses}`}</div>
      <div style={{ marginBottom: 8 }}>
        追加次数：<InputNumber value={addUses} onChange={(v) => setAddUses(Number(v) || undefined)} style={{ width: 120 }} />
      </div>
      <div>
        调整截止时间（ISO 字符串，留空不改）：<Input value={validUntil} onChange={setValidUntil} placeholder="2027-01-01T00:00:00Z" />
      </div>
    </Modal>
  );
}
