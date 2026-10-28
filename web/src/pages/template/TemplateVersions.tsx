import { useState } from 'react';
import { Button, Card, Input, List, Message, Space, Tag, Upload } from '@arco-design/web-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from 'react-router-dom';
import { activateVersion, downloadVersionFile, getTemplate, uploadVersion } from '@/api/endpoints';
import type { VersionSummary } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';

// 版本来源的展示口径。和「状态」不是一回事：这一列说明的是「这个版本怎么来的」。
const SOURCE_TAG: Record<string, { label: string; color: string }> = {
  upload: { label: '上传', color: 'arcoblue' },
  edit: { label: '在线编辑', color: 'purple' },
  rollback: { label: '回滚', color: 'orange' },
};

// 上传人的展示口径。
// "system" 是 V3.3 之前 handleAddVersion 写死的值（所有版本都署这个名），
// 它不是用户也不是真实 ID。把历史数据如实标成「未知」，
// 比让一串 system 冒充人名更清楚。
function uploaderName(id?: string): string {
  if (!id || id === 'system') return '未知';
  return id;
}

// 版本历史页签：上传新版本 + 版本时间线（激活/下载）
export default function TemplateVersions() {
  const { id = '' } = useParams();
  const qc = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [summary, setSummary] = useState('');

  const { data: tpl, isLoading } = useQuery({
    queryKey: ['template', id],
    queryFn: () => getTemplate(id),
    enabled: !!id,
  });

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['template', id] });
    qc.invalidateQueries({ queryKey: ['templates'] });
  };

  const uploadMut = useMutation({
    mutationFn: () => uploadVersion(id, file!, true, summary.trim() || undefined),
    onSuccess: () => {
      Message.success('新版本已上传并激活');
      setFile(null);
      setSummary('');
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

  return (
    <div style={{ padding: '0 24px 24px' }}>
      <Card style={{ marginBottom: 16 }}>
        {/*
          这里**不能**给 Upload 传 children。Arco 的触发器逻辑是
          「有 children → 走自定义触发器分支」，直接忽略 drag 的整套样式
          （虚线框、50px 内边距、拖拽高亮全都不生效），于是这个控件在页面上
          只剩一行裸文字，看着完全不像可点区域。
          留空 children 才会渲染内置的拖拽区：加号图标 + 文案 + tip。
        */}
        <Upload
          drag
          accept=".docx"
          limit={1}
          multiple={false}
          // 受控 fileList：beforeUpload 返回 false 时 Arco 不会把文件收进内部列表，
          // 不接管的话选完文件控件里什么都不显示，用户以为没选上。
          fileList={
            file
              ? [{ uid: 'picked', name: file.name, status: 'done' as const, originFile: file }]
              : []
          }
          beforeUpload={(f: File) => {
            if (!f.name.toLowerCase().endsWith('.docx')) {
              Message.error('仅支持 .docx');
              return false;
            }
            setFile(f);
            return false;
          }}
          onRemove={() => setFile(null)}
          tip="仅支持 .docx，上传后自动激活；历史版本保留可回滚"
          style={{ marginBottom: 16 }}
        />
        {/*
          变更说明是「这一版改了什么」的唯一记录，而它以前根本没有入口——
          时间线上只有版本号和上传时间，隔两周没人说得清 1.0.3 改的是哪一处。
          放在按钮上方而不是塞进对话框：上传是一步动作，多开一层弹窗只会让人跳过它。
        */}
        <Input
          value={summary}
          onChange={setSummary}
          maxLength={200}
          showWordLimit
          placeholder="变更说明（可选）：这一版改了什么，例如「补充签署页」「修正明细表列宽」"
          style={{ marginBottom: 12 }}
        />
        {/*
          以前是 type=primary + long 的整行按钮：未选文件时 disabled，
          Arco 的禁用主按钮是淡蓝底 + 白字，整行 860px 铺开就是一条
          「浅蓝色空条」——不是按钮，是个色块。改回内容宽度并右对齐。
        */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 12 }}>
          <span style={{ color: 'var(--color-text-3)', fontSize: 12 }}>
            {file ? `待上传：${file.name}` : '尚未选择文件'}
          </span>
          <Button type="primary" disabled={!file} loading={uploadMut.isPending} onClick={() => uploadMut.mutate()}>
            上传并激活
          </Button>
        </div>
      </Card>

      <Card title="版本时间线">
        <List<VersionSummary>
          loading={isLoading}
          dataSource={tpl?.versions || []}
          render={(v: VersionSummary) => (
            <List.Item key={v.id}>
              <Space style={{ width: '100%', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div style={{ minWidth: 0 }}>
                  <Space>
                    <strong>{v.version}</strong>
                    {v.is_current && <Tag color="green">当前版本</Tag>}
                    {/* 来源要标出来：同一个版本号可能是上传来的、也可能是在线改出来的、
                        还可能是回滚产生的。不标的话，看到「1.0.3」不知道它是哪来的。 */}
                    <Tag size="small" color={SOURCE_TAG[v.source || 'upload']?.color || 'gray'}>
                      {SOURCE_TAG[v.source || 'upload']?.label || v.source}
                    </Tag>
                  </Space>
                  {/* 变更说明是「这一版改了什么」的唯一答案，有就显示在最显眼的位置 */}
                  {v.change_summary ? (
                    <div style={{ marginTop: 4, color: 'var(--color-text-2)', fontSize: 13 }}>
                      {v.change_summary}
                    </div>
                  ) : null}
                  <div style={{ color: 'var(--color-text-3)', fontSize: 12, marginTop: 4 }}>
                    上传于 {fmtTime(v.created_at)} · 上传人 {uploaderName(v.created_by)}
                    {v.file_size ? ` · ${(v.file_size / 1024).toFixed(0)} KB` : ''}
                  </div>
                </div>
                <Space>
                  {!v.is_current && (
                    <Button size="mini" onClick={() => act(() => activateVersion(id, v.id), `已切换到 ${v.version}`)}>
                      激活此版本
                    </Button>
                  )}
                  <Button
                    size="mini"
                    onClick={() =>
                      act(() => downloadVersionFile(id, v.id).then(({ blob, filename }) => {
                        const url = URL.createObjectURL(blob);
                        const a = document.createElement('a');
                        a.href = url;
                        a.download = filename || `template_${v.version}.docx`;
                        a.click();
                        URL.revokeObjectURL(url);
                      }), '已开始下载')
                    }
                  >
                    下载原文件
                  </Button>
                </Space>
              </Space>
            </List.Item>
          )}
        />
      </Card>
    </div>
  );
}
