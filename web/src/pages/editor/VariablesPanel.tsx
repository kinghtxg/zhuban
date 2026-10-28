import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, Input, Message, Popconfirm, Tag, Upload } from '@arco-design/web-react';
import {
  deleteAsset,
  getVariablesMeta,
  listAssets,
  putVariablesMeta,
  uploadAsset,
} from '@/api/endpoints';
import type { TemplateAsset, VariableMeta, VariableMetaResp } from '@/types/api';
import { errorMessage } from '@/utils/format';

export default function VariablesPanel({ templateId }: { templateId: string }) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<VariableMeta[] | null>(null);

  const { data: meta, isLoading } = useQuery<VariableMetaResp>({
    queryKey: ['var-meta', templateId],
    queryFn: () => getVariablesMeta(templateId),
  });
  const { data: assets, isLoading: assetsLoading } = useQuery<TemplateAsset[]>({
    queryKey: ['assets', templateId],
    queryFn: () => listAssets(templateId),
  });

  const saveMut = useMutation({
    mutationFn: (variables: VariableMeta[]) => putVariablesMeta(templateId, variables),
    onSuccess: () => {
      Message.success('变量元数据已保存');
      setDraft(null);
      qc.invalidateQueries({ queryKey: ['var-meta', templateId] });
    },
    onError: (e) => Message.error(errorMessage(e).summary),
  });

  const delAssetMut = useMutation({
    mutationFn: (aid: string) => deleteAsset(templateId, aid),
    onSuccess: () => {
      Message.success('资源已删除');
      qc.invalidateQueries({ queryKey: ['assets', templateId] });
    },
    onError: (e) => Message.error(errorMessage(e).summary),
  });

  const vars = draft ?? meta?.variables ?? [];

  return (
    <div className="vars-panel">
      <div className="vars-panel-title">变量元数据</div>

      {/* 面板只有 320px 宽，4 列表格会把「名称」挤成竖排。
          改成两行式：首行 名称 + 类型 + 必填，次行 整宽说明输入框。 */}
      {isLoading ? (
        <div className="vars-panel-empty">加载中…</div>
      ) : vars.length === 0 ? (
        <div className="vars-panel-empty">该模板没有变量</div>
      ) : (
        vars.map((v) => (
          <div className="var-item" key={v.name}>
            <div className="var-item-head">
              <code className="var-item-name" title={v.name}>
                {v.name}
              </code>
              <Tag size="small">{kindLabel(v.kind)}</Tag>
              {v.required && (
                <Tag size="small" color="red">
                  必填
                </Tag>
              )}
            </div>
            <Input
              size="small"
              value={v.description ?? ''}
              onChange={(val) => {
                const next = vars.map((x) => (x.name === v.name ? { ...x, description: val } : x));
                setDraft(next);
              }}
              placeholder="填写说明（可选）"
            />
          </div>
        ))
      )}

      <Button
        type="primary"
        size="small"
        long
        style={{ marginTop: 10 }}
        loading={saveMut.isPending}
        disabled={!draft}
        onClick={() => saveMut.mutate(vars)}
      >
        保存变量说明
      </Button>

      <div className="vars-panel-title" style={{ marginTop: 16 }}>
        图片 / Logo 资源库
      </div>
      <Upload
        accept="image/png,image/jpeg,image/gif,image/webp"
        limit={1}
        showUploadList={false}
        beforeUpload={(f: File) => {
          if (f.size > 5 * 1024 * 1024) {
            Message.error('图片不能超过 5MB');
            return false;
          }
          uploadAsset(templateId, f, f.name)
            .then(() => {
              Message.success('已上传');
              qc.invalidateQueries({ queryKey: ['assets', templateId] });
            })
            .catch((e) => Message.error(errorMessage(e).summary));
          return false;
        }}
      >
        <Button size="small" long type="outline">
          上传图片（≤5MB）
        </Button>
      </Upload>

      <div style={{ marginTop: 10 }}>
        {(assets ?? []).map((a) => (
          <div className="asset-row" key={a.id}>
            <img className="asset-thumb" src={a.url} alt={a.name} />
            <div style={{ flex: 1, minWidth: 0 }}>
              <div className="asset-name" title={a.name}>
                {a.name}
              </div>
              <div className="asset-meta">
                {a.width_mm ? `${a.width_mm}×${a.height_mm}mm` : '原始尺寸'}
              </div>
            </div>
            <Popconfirm title="删除该资源？" onOk={() => delAssetMut.mutate(a.id)}>
              <Button size="mini" status="danger" type="text">
                删除
              </Button>
            </Popconfirm>
          </div>
        ))}
        {!assetsLoading && (assets ?? []).length === 0 && (
          <div className="vars-panel-empty">暂无资源</div>
        )}
      </div>
    </div>
  );
}

/** 变量类型的中文短标签：侧栏空间有限，不用完整枚举文案 */
function kindLabel(kind: string): string {
  const map: Record<string, string> = {
    scalar: '标量',
    loop: '循环',
    condition: '条件',
    image: '图片',
    qrcode: '二维码',
    richtext: '富文本',
    chart: '图表',
  };
  return map[kind] || kind;
}
