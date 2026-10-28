import { useState } from 'react';
import { Button, Empty, Message, Popconfirm, Skeleton, Tag, Upload } from '@arco-design/web-react';
import { IconDelete, IconUpload } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { deleteFont, listFonts, uploadFont } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';

/**
 * 字体管理（仅管理员）。
 *
 * 两类字体分开呈现，因为它们的用途不同、能删的也不同：
 *   - 内置签名风字体：用于「签名生成」时挑字形，是镜像的一部分，删不掉
 *   - 导入字体：给文档渲染用（LibreOffice 排版时按字体名查找），随传随删
 * 混在一个列表里会让人以为内置的也能删，点了报错才发现不行。
 */
export default function FontManager() {
  const qc = useQueryClient();
  const [uploading, setUploading] = useState(false);

  const { data, isLoading } = useQuery({ queryKey: ['fonts'], queryFn: listFonts });
  const refresh = () => qc.invalidateQueries({ queryKey: ['fonts'] });

  const delMut = useMutation({
    mutationFn: (name: string) => deleteFont(name),
    onSuccess: () => {
      Message.success('已删除，字体缓存已刷新');
      refresh();
    },
    onError: (e) => Message.error(errorMessage(e).summary),
  });

  const doUpload = async (file: File) => {
    setUploading(true);
    try {
      const r = await uploadFont(file);
      const reload = r.reload as { ok?: boolean; elapsed_ms?: number; errors?: string[] } | undefined;
      if (reload?.ok) {
        Message.success(`「${r.name}」已导入，字体缓存刷新完成（${reload.elapsed_ms ?? 0}ms）`);
      } else {
        // 文件确实已经落盘，只是这次没立刻生效 —— 文案必须说清楚，
        // 否则用户会以为上传失败、反复重传，最后目录里堆一堆同名 -2 -3 文件。
        Message.warning({
          content: `「${r.name}」已保存，但字体缓存刷新未成功，可稍后重试或重启渲染服务`,
          duration: 6000,
        });
      }
      refresh();
    } catch (e) {
      Message.error(errorMessage(e).summary);
    } finally {
      setUploading(false);
    }
  };

  const imported = data?.imported || [];

  return (
    <div>
      <div className="cell-sub" style={{ marginBottom: 12, lineHeight: 1.8 }}>
        导入的字体用于<strong>文档渲染</strong>：模板里写的字体名（如「仿宋_GB2312」「思源黑体」）
        必须在这里存在，导出时才不会被替换成别的字体。
        支持 .ttf / .ttc / .otf / .otc，单个不超过 32MB。
      </div>

      <Upload
        accept=".ttf,.ttc,.otf,.otc"
        showUploadList={false}
        // beforeUpload 返回 false 即拦下框架自身的 XHR，由我们手动调接口，
        // 这样能拿到 reload 结果并给出「已保存但缓存没刷新」这种中间态提示。
        beforeUpload={(file) => {
          void doUpload(file);
          return false;
        }}
      >
        {/*
          这里以前是 type="primary"。整页另一个 primary 是底部「保存」，
          两个蓝底按钮隔着半屏，用户第一眼分不清哪个是本页的主操作。
          字体导入是低频、可重复的动作，用次级样式即可，把 primary 留给「保存」。
        */}
        <Button type="outline" icon={<IconUpload />} loading={uploading}>
          选择字体文件导入
        </Button>
      </Upload>

      <div style={{ marginTop: 18 }}>
        <div className="cell-sub" style={{ marginBottom: 8 }}>
          已导入（{imported.length}）
        </div>
        {isLoading ? (
          <Skeleton text={{ rows: 3 }} animation />
        ) : imported.length === 0 ? (
          <Empty description="还没有导入字体，当前使用镜像自带的字体" />
        ) : (
          <div className="font-list">
            {imported.map((f) => (
              <div className="font-row" key={f.file}>
                <div className="font-row-main">
                  <div className="font-row-name" title={f.file}>
                    {f.family || f.file}
                  </div>
                  <div className="font-row-meta">
                    {f.file}
                    {f.style && <span> · {f.style}</span>}
                  </div>
                </div>
                <Popconfirm
                  title={`删除字体「${f.family || f.file}」？`}
                  content="删除后引用该字体的模板会回落到其他字体，已导出的文档不受影响。"
                  onOk={() => delMut.mutate(f.file)}
                >
                  <Button size="mini" status="danger" icon={<IconDelete />}>
                    删除
                  </Button>
                </Popconfirm>
              </div>
            ))}
          </div>
        )}
      </div>

      <div style={{ marginTop: 18 }}>
        <div className="cell-sub" style={{ marginBottom: 8 }}>
          内置签名字体（{data?.builtin?.length ?? 0}，镜像自带，不可删除）
        </div>
        <div className="font-tags">
          {(data?.builtin || []).map((f) => (
            <Tag key={f.id} size="small" color="arcoblue">
              {f.name}
            </Tag>
          ))}
          {data?.builtin_error && (
            <Tag size="small" color="red">读取失败：{data.builtin_error}</Tag>
          )}
        </div>
        {data?.system != null && data.system > 0 && (
          <div className="cell-sub" style={{ marginTop: 8 }}>
            另有 {data.system} 个系统字体（镜像自带，不在此管理）
          </div>
        )}
      </div>
    </div>
  );
}
