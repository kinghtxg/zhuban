import { useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Drawer,
  Form,
  Input,
  Message,
  Popconfirm,
  Switch,
  Table,
  Tag,
  Upload,
} from '@arco-design/web-react';
import { IconRefresh } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from 'react-router-dom';
import {
  deleteAsset,
  getTemplate,
  getVariablesMeta,
  listAssets,
  putVariablesMeta,
  uploadAsset,
} from '@/api/endpoints';
import { KIND_META, lookupMeta } from '@/constants/enums';
import type { TemplateAsset, VariableMeta } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';

// 模板维度的变量与资源页签（PRD §4.5）
// 变量元数据 PUT /templates/{id}/variables/meta、资源 /assets 由后端同步开发；
// 未就绪时表格照常展示扫描出的只读变量，资源区给「未接入」说明，不白屏。
export default function TemplateVariables() {
  const { id = '' } = useParams();
  const qc = useQueryClient();

  const tplQuery = useQuery({
    queryKey: ['template', id],
    queryFn: () => getTemplate(id),
    enabled: !!id,
  });
  const metaQuery = useQuery({
    queryKey: ['variables-meta', id],
    queryFn: () => getVariablesMeta(id),
    enabled: !!id,
    retry: false,
  });
  const assetsQuery = useQuery({
    queryKey: ['assets', id],
    queryFn: () => listAssets(id),
    enabled: !!id,
    retry: false,
  });

  const [editing, setEditing] = useState<VariableMeta | null>(null);

  // 变量行：优先用可编辑 meta，否则用扫描出的只读变量
  const scanned = tplQuery.data?.current_version?.variables || [];
  const metaVars = metaQuery.data?.variables;
  const rows: VariableMeta[] =
    metaVars && metaVars.length > 0
      ? metaVars
      : scanned.map((v) => ({
          name: v.name,
          kind: v.kind,
          type: v.type,
          required: v.required,
        }));

  const saveMeta = async (next: VariableMeta) => {
    const merged = rows.map((r) => (r.name === next.name ? next : r));
    try {
      await putVariablesMeta(id, merged);
      Message.success('已保存变量说明');
      qc.invalidateQueries({ queryKey: ['variables-meta', id] });
      setEditing(null);
    } catch (err) {
      Message.error(`${errorMessage(err).summary}（变量元数据接口尚未就绪时，此处不可保存）`);
    }
  };

  const uploadMut = useMutation({
    mutationFn: (f: File) => uploadAsset(id, f, f.name, 'image'),
    onSuccess: () => {
      Message.success('资源已上传');
      qc.invalidateQueries({ queryKey: ['assets', id] });
    },
    onError: (err) => Message.error(`${errorMessage(err).summary}（资源库接口尚未就绪）`),
  });

  const delMut = useMutation({
    mutationFn: (aid: string) => deleteAsset(id, aid),
    onSuccess: () => {
      Message.success('已删除');
      qc.invalidateQueries({ queryKey: ['assets', id] });
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const assetError = assetsQuery.isError;

  return (
    <div style={{ padding: '0 24px 24px' }}>
      <Card title="变量表" style={{ marginBottom: 16 }}>
        <Table<VariableMeta>
          rowKey="name"
          size="small"
          loading={tplQuery.isLoading}
          pagination={false}
          data={rows}
          columns={[
            { title: '名称', dataIndex: 'name', width: 200, render: (v: string) => <strong>{v}</strong> },
            {
              title: '类型',
              dataIndex: 'kind',
              width: 100,
              render: (k: string) => {
                const m = lookupMeta(KIND_META as Record<string, { label: string; color: string }>, k);
                return <Tag color={m.color} size="small">{m.label}</Tag>;
              },
            },
            { title: '必填', dataIndex: 'required', width: 70, render: (v: boolean) => (v ? <Tag color="red" size="small">必填</Tag> : <span>—</span>) },
            { title: '默认值', dataIndex: 'default', width: 140, render: (v: unknown) => (v == null ? '—' : String(v)) },
            { title: '说明', dataIndex: 'description', render: (v: string) => v || <span style={{ color: 'var(--color-border-3)' }}>未填写</span> },
            {
              title: '操作',
              dataIndex: 'op',
              width: 90,
              render: (_: unknown, r: VariableMeta) => (
                <Button size="mini" type="text" onClick={() => setEditing(r)}>编辑</Button>
              ),
            },
          ]}
        />
      </Card>

      <Card
        title="资源库（图片 / logo）"
        extra={
          <Upload
            accept="image/*"
            showUploadList={false}
            limit={1}
            beforeUpload={(f: File) => {
              if (f.size > 5 * 1024 * 1024) {
                Message.error('图片不能超过 5MB');
                return false;
              }
              uploadMut.mutate(f);
              return false;
            }}
          >
            <Button type="primary" size="small" loading={uploadMut.isPending}>上传图片 / logo</Button>
          </Upload>
        }
      >
        {assetError ? (
          <Alert
            type="warning"
            action={<Button size="mini" icon={<IconRefresh />} onClick={() => assetsQuery.refetch()}>重试</Button>}
            content="资源库接口尚未就绪（后端开发中）。上传图片与 logo 暂不可用，页面其余功能不受影响。"
          />
        ) : (assetsQuery.data || []).length === 0 ? (
          <div style={{ color: 'var(--color-text-3)', padding: 16 }}>
            还没有资源。上传图片或 logo 后，可在模板里通过 {"{{ image_logo }}"} 占位符引用。
          </div>
        ) : (
          <Table<TemplateAsset>
            rowKey="id"
            size="small"
            loading={assetsQuery.isLoading}
            pagination={false}
            data={assetsQuery.data || []}
            columns={[
              {
                title: '缩略图',
                dataIndex: 'url',
                width: 90,
                render: (v: string) => (
                  <img src={v?.startsWith('http') ? v : `/api/v1${v}`} alt="" style={{ width: 48, height: 36, objectFit: 'cover', border: '1px solid var(--color-border-2)', borderRadius: 4 }}
                    onError={(e) => ((e.target as HTMLImageElement).style.display = 'none')} />
                ),
              },
              { title: '名称', dataIndex: 'name', width: 200 },
              { title: '用途', dataIndex: 'type', width: 120, render: (v: string) => v || '—' },
              { title: '上传时间', dataIndex: 'created_at', width: 160, render: (v: string) => fmtTime(v) },
              {
                title: '操作',
                dataIndex: 'op',
                width: 120,
                render: (_: unknown, r: TemplateAsset) => (
                  <Popconfirm title="确认删除该资源？" onConfirm={() => delMut.mutate(r.id)}>
                    <Button size="mini" status="danger" type="text">删除</Button>
                  </Popconfirm>
                ),
              },
            ]}
          />
        )}
      </Card>

      <Drawer
        title="编辑变量说明"
        visible={!!editing}
        onCancel={() => setEditing(null)}
        width={440}
        unmountOnExit
        footer={
          <Button type="primary" long onClick={() => editing && saveMeta(editing)}>保存</Button>
        }
      >
        {editing && (
          <Form layout="vertical">
            <Form.Item label="变量名">
              <Input value={editing.name} disabled />
            </Form.Item>
            <Form.Item label="是否必填">
              <Switch checked={!!editing.required} onChange={(v) => setEditing({ ...editing, required: v })} />
            </Form.Item>
            <Form.Item label="默认值">
              <Input
                value={editing.default == null ? '' : String(editing.default)}
                onChange={(v) => setEditing({ ...editing, default: v })}
                placeholder="缺省时显示的默认文案"
              />
            </Form.Item>
            <Form.Item label="说明文案">
              <Input.TextArea
                value={editing.description || ''}
                onChange={(v) => setEditing({ ...editing, description: v })}
                placeholder="给填报人看的字段说明"
              />
            </Form.Item>
          </Form>
        )}
      </Drawer>
    </div>
  );
}
