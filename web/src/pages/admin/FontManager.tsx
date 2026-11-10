import { useRef, useState } from 'react';
import { Button, Empty, Message, Popconfirm, Progress, Skeleton, Tag, Upload } from '@arco-design/web-react';
import { IconDelete, IconUpload } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { deleteFont, listFonts, uploadFont } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';

const ALLOWED_EXT = ['.ttf', '.ttc', '.otf', '.otc'];
const MAX_FONT_MB = 32;

/** 批量导入时每个文件的结局。批量最容易出的事不是"全失败"，
 *  而是"成功了几个、重复了几个、有一个失败"——只报总数等于没说。 */
type ImportItem = {
  name: string;
  status: 'ok' | 'dup' | 'warn' | 'fail';
  detail: string;
};

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
  const [progress, setProgress] = useState<{ done: number; total: number; name: string } | null>(null);
  const [results, setResults] = useState<ImportItem[]>([]);
  // 框架对多选/多拖的每个文件各调一次 beforeUpload，拿不到"这一批"。
  // 用一个 80ms 的缓冲把它们攒成一批，进度和汇总才是"一次导入"而不是 N 次。
  const pending = useRef<File[]>([]);
  const timer = useRef<number | null>(null);
  const uploading = progress !== null;

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

  const doUploadMany = async (files: File[]) => {
    // 已有字体按文件名排重。重复导入同一个字体不会覆盖，服务端会改名另存成 -2，
    // 结果是列表里出现两行看起来一样的字体——所以这里先在客户端拦一道。
    const existing = new Set((data?.imported || []).map((f) => f.file));
    const items: ImportItem[] = [];
    setResults([]);
    setProgress({ done: 0, total: files.length, name: files[0]?.name || '' });

    for (let i = 0; i < files.length; i += 1) {
      const file = files[i];
      setProgress({ done: i, total: files.length, name: file.name });

      if (existing.has(file.name)) {
        items.push({ name: file.name, status: 'dup', detail: '库里已有同名文件，本次跳过' });
        setResults([...items]);
        continue;
      }
      try {
        // 逐个串行上传：每次上传都会触发一次 fc-cache，
        // 并发上传只是让多个 fc-cache 互相抢同一份缓存目录。
        const r = await uploadFont(file);
        const reload = r.reload as { ok?: boolean; elapsed_ms?: number; errors?: string[] } | undefined;
        if (r.name && r.name !== file.name) {
          items.push({
            name: file.name,
            status: 'dup',
            detail: `已存在同名文件，这次另存为 ${r.name}；多余的可在下方列表删掉`,
          });
        } else if (!reload?.ok) {
          // 文件确实已经落盘，只是这次没立刻生效 —— 文案必须说清楚，
          // 否则用户会以为上传失败、反复重传，最后目录里堆一堆同名 -2 -3 文件。
          items.push({
            name: file.name,
            status: 'warn',
            detail: '已保存，但字体缓存刷新未成功，可稍后重试或重启渲染服务',
          });
        } else {
          existing.add(r.name || file.name);
          items.push({
            name: file.name,
            status: 'ok',
            detail: reload?.elapsed_ms ? `已导入（缓存刷新 ${reload.elapsed_ms}ms）` : '已导入',
          });
        }
      } catch (e) {
        items.push({ name: file.name, status: 'fail', detail: errorMessage(e).summary });
      }
      setResults([...items]);
    }

    setProgress(null);
    refresh();

    const ok = items.filter((i) => i.status === 'ok').length;
    const dup = items.filter((i) => i.status === 'dup').length;
    const bad = items.filter((i) => i.status === 'fail' || i.status === 'warn').length;
    if (items.length === 1 && bad === 0 && dup === 0) {
      Message.success(`「${items[0].name}」${items[0].detail}`);
    } else if (bad > 0) {
      Message.error({
        content: `导入完成：成功 ${ok} 个，重复跳过 ${dup} 个，失败或未生效 ${bad} 个（失败原因见导入结果）`,
        duration: 6000,
      });
    } else {
      Message.success(`导入完成：成功 ${ok} 个${dup ? `，重复跳过 ${dup} 个` : ''}`);
    }
  };

  const enqueue = (file: File) => {
    const lower = file.name.toLowerCase();
    if (!ALLOWED_EXT.some((ext) => lower.endsWith(ext))) {
      Message.error(`「${file.name}」不是支持的字体格式（${ALLOWED_EXT.join(' / ')}）`);
      return;
    }
    if (file.size > MAX_FONT_MB * 1024 * 1024) {
      Message.error(`「${file.name}」超过 ${MAX_FONT_MB}MB`);
      return;
    }
    pending.current.push(file);
    if (timer.current) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      const batch = pending.current.slice();
      pending.current = [];
      timer.current = null;
      if (batch.length) void doUploadMany(batch);
    }, 80);
  };

  const imported = data?.imported || [];
  const problems = results.filter((r) => r.status !== 'ok');
  const okCount = results.length - problems.length;

  return (
    <div>
      <div className="cell-sub" style={{ marginBottom: 12, lineHeight: 1.8 }}>
        导入的字体用于<strong>文档渲染</strong>：模板里写的字体名（如「仿宋_GB2312」「思源黑体」）
        必须在这里存在，导出时才不会被替换成别的字体。
        支持 .ttf / .ttc / .otf / .otc，单个不超过 32MB，<strong>可一次多选或整批拖入</strong>。
        字体文件里带的族名就是匹配依据，与文件名无关。
      </div>

      <Upload
        accept=".ttf,.ttc,.otf,.otc"
        multiple
        drag
        showUploadList={false}
        // beforeUpload 返回 false 即拦下框架自身的 XHR，由我们手动调接口，
        // 这样能拿到 reload 结果并给出「已保存但缓存没刷新」这种中间态提示，
        // 也能逐个文件汇总"哪个重复了、哪个失败了"。
        beforeUpload={(file) => {
          enqueue(file);
          return false;
        }}
      >
        {/*
          这里以前是 type="primary"。整页另一个 primary 是底部「保存」，
          两个蓝底按钮隔着半屏，用户第一眼分不清哪个是本页的主操作。
          字体导入是低频、可重复的动作，用次级样式即可，把 primary 留给「保存」。
        */}
        <Button type="outline" icon={<IconUpload />} loading={uploading}>
          {uploading ? '正在导入…' : '选择字体文件（可多选）'}
        </Button>
      </Upload>

      {progress && (
        <div className="font-import-progress">
          <span style={{ whiteSpace: 'nowrap' }}>
            {progress.done + 1}/{progress.total}
          </span>
          <Progress
            className="font-import-progress-bar"
            percent={Math.round((progress.done / Math.max(progress.total, 1)) * 100)}
            size="small"
            showText={false}
          />
          <span style={{ maxWidth: 220, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {progress.name}
          </span>
        </div>
      )}

      {!progress && results.length > 0 && results.length === okCount && (
        <div className="font-import-progress">
          <Tag size="small" color="green">全部成功</Tag>
          <span>共导入 {okCount} 个字体，缓存已刷新</span>
        </div>
      )}

      {!progress && problems.length > 0 && (
        <div className="font-import-result">
          {okCount > 0 && (
            <div className="font-import-row">
              <Tag size="small" color="green">成功 {okCount}</Tag>
              <span className="font-import-row-why">其余文件已导入，缓存已刷新</span>
            </div>
          )}
          {problems.map((r) => (
            <div className="font-import-row" key={r.name + r.status}>
              <Tag size="small" color={r.status === 'fail' ? 'red' : 'orange'}>
                {r.status === 'fail' ? '失败' : r.status === 'dup' ? '跳过' : '未生效'}
              </Tag>
              <span className="font-import-row-name" title={r.name}>{r.name}</span>
              <span className="font-import-row-why">{r.detail}</span>
            </div>
          ))}
        </div>
      )}

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
