import { useState } from 'react';
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Form,
  Input,
  Message,
  Modal,
  Popconfirm,
  Space,
  Table,
  Tag,
} from '@arco-design/web-react';
import { IconCopy, IconPlus } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createKey, deleteKey, disableKey, enableKey, listKeys } from '@/api/endpoints';
import type { CreatedApiKey } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';
import PageContainer from '@/components/PageContainer';

export default function KeysPage() {
  const qc = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [qps, setQps] = useState(5);
  const [maxRecords, setMaxRecords] = useState(1000);
  const [createdKey, setCreatedKey] = useState<CreatedApiKey | null>(null);
  // 列表接口没有 q 参数，也一共就几百条，前端过滤足够。
  // 加这一格是因为「按名字找一把 Key」是最常做的动作，而原来只能靠肉眼扫。
  const [kw, setKw] = useState('');

  const { data, isLoading } = useQuery({ queryKey: ['keys'], queryFn: listKeys });

  const rows = (data || []).filter((k) => {
    const q = kw.trim().toLowerCase();
    if (!q) return true;
    return k.name.toLowerCase().includes(q) || k.prefix.toLowerCase().includes(q);
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['keys'] });

  const createMut = useMutation({
    mutationFn: () => createKey({ name: name.trim(), qps_limit: qps, max_records: maxRecords }),
    onSuccess: (k) => {
      setCreatedKey(k);
      setCreateOpen(false);
      setName('');
      refresh();
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const act = (fn: () => Promise<unknown>, ok: string) =>
    fn()
      .then(() => {
        Message.success(ok);
        refresh();
      })
      .catch((err) => Message.error(errorMessage(err).summary));

  const columns = [
    {
      title: '名称',
      dataIndex: 'name',
      width: 160,
      render: (v: string) => <span style={{ whiteSpace: 'nowrap' }}>{v}</span>,
    },
    { title: '前缀', dataIndex: 'prefix', width: 130, render: (v: string) => <Tag color="gray">{v}</Tag> },
    { title: 'QPS', dataIndex: 'qps_limit', width: 80 },
    { title: '单任务上限', dataIndex: 'max_records', width: 110, render: (v: number) => <span style={{ whiteSpace: 'nowrap' }}>{v}</span> },
    {
      title: '状态',
      dataIndex: 'enabled',
      width: 90,
      render: (v: boolean) => (v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
    },
    {
      title: '最后使用',
      dataIndex: 'last_used_at',
      width: 190,
      // 时间 + 来源 IP 两行。
      // 只有时间时，「这把 Key 是谁在跑」还得靠时间去审计日志里对；
      // IP 直接放在下面，一眼就能看出是不是自己那台机器。
      // 老数据没有 IP（加这一列之前写的），显示「IP 未知」而不是留白，
      // 否则看起来像接口没返回。
      render: (v: string, record: { last_used_ip?: string }) => (
        <div>
          <div style={{ whiteSpace: 'nowrap' }}>{fmtTime(v)}</div>
          <div className="cell-sub">
            {record.last_used_ip || (v ? 'IP 未知' : '尚未使用')}
          </div>
        </div>
      ),
    },
    { title: '创建时间', dataIndex: 'created_at', width: 166, render: (v: string) => <span style={{ whiteSpace: 'nowrap' }}>{fmtTime(v)}</span> },
    {
      title: '操作',
      dataIndex: 'actions',
      width: 200,
      render: (_: unknown, record: { id: string; enabled: boolean }) => (
        <Space>
          {record.enabled ? (
            <Button size="mini" onClick={() => act(() => disableKey(record.id), '已停用')}>
              停用
            </Button>
          ) : (
            <Button size="mini" onClick={() => act(() => enableKey(record.id), '已启用')}>
              启用
            </Button>
          )}
          <Popconfirm title="删除后该密钥立即失效，确认删除？" onConfirm={() => act(() => deleteKey(record.id), '已删除')}>
            <Button size="mini" status="danger">
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <PageContainer
      title="API Key"
      subtitle="服务端调用铸版 API 使用，请求头 X-API-Key；密钥明文仅创建时展示一次"
      extra={
        <Button type="primary" icon={<IconPlus />} onClick={() => setCreateOpen(true)}>
          创建 API Key
        </Button>
      }
    >
      <div className="data-card">
        <div className="table-toolbar">
          <Input.Search
            allowClear
            placeholder="按名称或前缀搜索"
            value={kw}
            onChange={setKw}
            style={{ width: 260 }}
          />
          <span className="cell-sub">
            {kw.trim() ? `匹配 ${rows.length} / ${data?.length || 0}` : `共 ${data?.length || 0} 个`}
          </span>
        </div>
        <Table
          rowKey="id"
          loading={isLoading}
          columns={columns}
          data={rows}
          pagination={false}
          scroll={{ x: 1100 }}
          noDataElement={
            <div className="empty-block empty-block-desc">
              {kw.trim() ? '没有匹配的 Key，换个关键词试试' : '还没有 API Key，创建后即可在服务端脚本中调用'}
            </div>
          }
        />
      </div>

      <Modal title="创建 API Key" visible={createOpen} onCancel={() => setCreateOpen(false)} footer={null} unmountOnExit>
        <Form layout="vertical">
          <Form.Item label="名称（用途标识）" required>
            <Input value={name} onChange={setName} placeholder="如 扫描平台 / 审计后端" />
          </Form.Item>
          <Form.Item label="QPS 限流">
            <Input type="number" value={String(qps)} onChange={(v) => setQps(Number(v) || 5)} />
          </Form.Item>
          <Form.Item label="单任务最大条数">
            <Input type="number" value={String(maxRecords)} onChange={(v) => setMaxRecords(Number(v) || 1000)} />
          </Form.Item>
          <Button type="primary" long loading={createMut.isPending} onClick={() => {
            if (!name.trim()) return Message.warning('请填写名称');
            createMut.mutate();
          }}>
            创建
          </Button>
        </Form>
      </Modal>

      <Drawer title="密钥已创建" visible={!!createdKey} onCancel={() => setCreatedKey(null)} width={520} unmountOnExit>
        <Alert type="warning" style={{ marginBottom: 16 }} content="密钥仅展示一次，关闭后无法再次查看，请立即复制并妥善保存。" />
        <Descriptions
          column={1}
          data={[
            { label: '名称', value: createdKey?.name },
            { label: '前缀', value: createdKey?.prefix },
          ]}
        />
        <div style={{ position: 'relative', marginTop: 12 }}>
          <div className="df-codeblock">{createdKey?.key}</div>
          <Button
            size="mini"
            style={{ position: 'absolute', top: 8, right: 8, color: '#fff', background: 'rgba(255,255,255,0.15)' }}
            icon={<IconCopy />}
            onClick={async () => {
              if (createdKey) {
                await navigator.clipboard.writeText(createdKey.key);
                Message.success('已复制');
              }
            }}
          >
            复制
          </Button>
        </div>
        <Button type="primary" long style={{ marginTop: 16 }} onClick={() => setCreatedKey(null)}>
          我已保存密钥
        </Button>
      </Drawer>
    </PageContainer>
  );
}
