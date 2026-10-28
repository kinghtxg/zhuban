/**
 * 模板库
 *
 * 布局规范：PageContainer（页头 + 工具栏 + 卡片网格）
 * 卡片操作层级：主操作「试运行」，次操作「编辑」，「更多」收纳低频动作。
 * 缩略图依赖 LibreOffice 渲染页面图；不可用时显示占位图标，不留空灰块。
 */

import { useState } from 'react';
import { Button, Dropdown, Empty, Input, Menu, Message, Modal, Select, Skeleton, Tag } from '@arco-design/web-react';
import { IconFile, IconMore, IconPlus } from '@arco-design/web-react/icon';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { deleteTemplate, disableTemplate, enableTemplate, listTemplates } from '@/api/endpoints';
import type { Template } from '@/types/api';
import { errorMessage } from '@/utils/format';
import UploadWizard from '@/components/UploadWizard';
import PageContainer from '@/components/PageContainer';

export default function TemplatesPage() {
  const qc = useQueryClient();
  const [q, setQ] = useState('');
  const [enabled, setEnabled] = useState<boolean | undefined>(undefined);
  const [wizardOpen, setWizardOpen] = useState(false);

  const { data, isLoading } = useQuery({
    queryKey: ['templates', { q, enabled }],
    queryFn: () => listTemplates({ q: q || undefined, enabled, page: 1, page_size: 100 }),
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['templates'] });

  const toggleMut = useMutation({
    mutationFn: (t: Template) => (t.enabled ? disableTemplate(t.id) : enableTemplate(t.id)),
    onSuccess: () => {
      Message.success('操作成功');
      refresh();
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  /**
   * 删除模板：不用 Popconfirm 而是 Modal.confirm，因为这里需要说清一件事——
   * 删的是「模板」不是「已经生成出来的文档」。文案讲明白，用户才敢点。
   */
  const confirmDelete = (t: Template) => {
    Modal.confirm({
      title: `删除模板「${t.name}」？`,
      content: (
        <div style={{ lineHeight: 1.8, fontSize: 13 }}>
          <div>删除后模板不再出现在列表里，也无法再用它生成新文档。</div>
          <div style={{ color: 'var(--app-text-caption)' }}>
            已生成的文档与生成记录<strong>不受影响</strong>，仍可下载。编号{' '}
            <code>{t.code}</code> 会释放，之后可以重建同名模板。
          </div>
        </div>
      ),
      okText: '删除',
      okButtonProps: { status: 'danger' },
      cancelText: '取消',
      onOk: async () => {
        try {
          await deleteTemplate(t.id);
          Message.success('已删除');
          refresh();
        } catch (err) {
          // 有未完成的任务时后端返回 409 job_running，原样把原因告诉用户
          Message.error(errorMessage(err).summary);
        }
      },
    });
  };

  const cards = data?.items || [];

  return (
    <PageContainer
      title="模板库"
      subtitle={`共 ${data?.total ?? 0} 个模板 · 上传 Word 模板，系统自动扫描变量并生成 JSON 示例`}
      extra={
        <Button id="tour-upload-btn" type="primary" icon={<IconPlus />} onClick={() => setWizardOpen(true)}>
          上传模板
        </Button>
      }
      toolbar={
        <>
          <Input.Search
            placeholder="按名称 / code 搜索"
            style={{ width: 240 }}
            onSearch={(v) => setQ(v)}
            allowClear
          />
          <Select
            placeholder="状态：全部"
            allowClear
            style={{ width: 140 }}
            onChange={(v) => setEnabled(v === undefined || v === '' ? undefined : v === 'true')}
          >
            <Select.Option value="true">启用中</Select.Option>
            <Select.Option value="false">已停用</Select.Option>
          </Select>
        </>
      }
    >
      {isLoading ? (
        <Skeleton animation />
      ) : cards.length === 0 ? (
        <div className="empty-block empty-surface">
          <Empty description="还没有模板，上传一个 docx 即可开始生成文档" />
          <Button type="primary" icon={<IconPlus />} onClick={() => setWizardOpen(true)}>
            上传模板
          </Button>
        </div>
      ) : (
        <div className="card-grid">
          {cards.map((t, i) => (
            <TemplateCard
              key={t.id}
              tpl={t}
              tourAnchor={i === 0}
              onToggle={() => toggleMut.mutate(t)}
              onDelete={() => confirmDelete(t)}
            />
          ))}
          <div className="grid-card-add" onClick={() => setWizardOpen(true)}>
            <IconPlus className="grid-card-add-icon" />
            上传新模板
          </div>
        </div>
      )}

      <UploadWizard visible={wizardOpen} onClose={() => setWizardOpen(false)} onCreated={() => refresh()} />
    </PageContainer>
  );
}

function TemplateCard({
  tpl,
  tourAnchor,
  onToggle,
  onDelete,
}: {
  tpl: Template;
  tourAnchor: boolean;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const navigate = useNavigate();

  const moreMenu = (
    <Menu
      onClickMenuItem={(key) => {
        if (key === 'detail') navigate(`/templates/${tpl.id}`);
        else if (key === 'versions') navigate(`/templates/${tpl.id}/versions`);
        else if (key === 'toggle') onToggle();
        else if (key === 'delete') onDelete();
        else if (key === 'download') {
          window.open(`/api/v1/templates/${tpl.id}/versions/${tpl.current_version_id}/download`, '_blank');
        }
      }}
    >
      <Menu.Item key="detail">接入信息</Menu.Item>
      <Menu.Item key="versions">版本历史</Menu.Item>
      <Menu.Item key="toggle">{tpl.enabled ? '停用模板' : '启用模板'}</Menu.Item>
      <Menu.Item key="download">下载源文件</Menu.Item>
      {/* Arco 的 Menu 没有 Divider 子组件（只有 Item / SubMenu / ItemGroup），
          这里用一条自绘分隔线：危险动作跟上面隔开，避免误点。 */}
      <div className="menu-divider" role="separator" />
      <Menu.Item key="delete" style={{ color: 'rgb(var(--danger-6))' }}>
        删除模板
      </Menu.Item>
    </Menu>
  );

  return (
    <div className="grid-card">
      <div className="grid-card-head">
        <TemplateThumb id={tpl.id} />
        <div className="grid-card-main">
          {/*
            标题做成可点的按钮：卡片上最自然的动作就是「点名字进详情」，
            而这里原来只能从右侧「更多 → 详情」绕进去，等于把主路径藏进了菜单。
            用真 button 而不是给 div 加 onClick：键盘 Tab 能到、回车能触发、
            读屏软件会念成按钮，这几点用 div 都要自己补。
          */}
          <button
            type="button"
            className="grid-card-title"
            title={tpl.name}
            onClick={() => navigate(`/templates/${tpl.id}`)}
          >
            {tpl.name}
          </button>
          <div className="cell-sub">{tpl.code}</div>
          <div className="grid-card-meta">
            {tpl.category && <Tag size="small">{tpl.category}</Tag>}
            <Tag size="small" color="gray">
              v{tpl.current_version?.version || '—'}
            </Tag>
            {tpl.enabled ? (
              <Tag size="small" color="green">
                启用
              </Tag>
            ) : (
              <Tag size="small" color="red">
                停用
              </Tag>
            )}
          </div>
        </div>
      </div>

      <div className="grid-card-actions">
        <Button
          id={tourAnchor ? 'tour-card-play' : undefined}
          size="small"
          type="primary"
          onClick={() => navigate(`/templates/${tpl.id}/playground`)}
        >
          试运行
        </Button>
        <Button size="small" onClick={() => navigate(`/editor/${tpl.id}`)}>
          编辑
        </Button>
        <div className="grid-card-actions-end">
          <Dropdown droplist={moreMenu} position="br">
            <Button size="small" icon={<IconMore />} aria-label="更多操作" />
          </Dropdown>
        </div>
      </div>
    </div>
  );
}

/**
 * 缩略图：拿不到图时降级为文件图标。
 *
 * 图标是**一直渲染在底层**的，图片盖在上面：图片成功加载就把它挡住，加载失败
 * 或返回 204（无缩略图，HTTP 语义上是"没有内容"）时它自己什么都不画，
 * 底下那个图标就露出来了。
 *
 * 之前是「先渲染 img，onError 之后才换成图标」。204 响应下 onError 会不会触发
 * 取决于浏览器实现，结果就是列表里一片灰底空框——既不像图片失败，也不像没有图。
 * 换成叠层之后不再依赖任何事件，渲染结果和网络结果无关。
 */
function TemplateThumb({ id }: { id: string }) {
  const [failed, setFailed] = useState(false);
  return (
    <div className="thumb-box">
      <span className="thumb-fallback">
        <IconFile />
      </span>
      {!failed && (
        <img
          src={`/api/v1/templates/${id}/thumbnail`}
          alt=""
          loading="lazy"
          decoding="async"
          onError={() => setFailed(true)}
        />
      )}
    </div>
  );
}
