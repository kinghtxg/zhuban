import { useEffect, useState } from 'react';
import { Button, Message, Popconfirm, Space, Spin, Tabs, Tag } from '@arco-design/web-react';
import { IconEdit, IconFile, IconPlayCircle } from '@arco-design/web-react/icon';
import { Outlet, useLocation, useNavigate, useParams } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { disableTemplate, enableTemplate, getTemplate } from '@/api/endpoints';
import { useBreadcrumb } from '@/components/BreadcrumbContext';
import EmptyState from '@/components/EmptyState';
import { errorMessage } from '@/utils/format';

const TabPane = Tabs.TabPane;

// 模板详情容器：页头 + 页签（概览/试运行/版本历史/变量与资源）
export default function TemplateDetail() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const qc = useQueryClient();
  const { setCrumbs } = useBreadcrumb();

  const { data: tpl, isLoading } = useQuery({
    queryKey: ['template', id],
    queryFn: () => getTemplate(id),
    enabled: !!id,
  });

  useEffect(() => {
    // 切页签时 AppLayout 会按 pathname 清空面包屑对象段，这里在每次 pathname 变化后都重设模板名。
    setCrumbs(tpl ? [tpl.name] : []);
  }, [tpl, setCrumbs, location.pathname]);

  if (isLoading) {
    return (
      <div className="df-page">
        <Spin style={{ width: '100%', display: 'flex', justifyContent: 'center', padding: 80 }} />
      </div>
    );
  }
  if (!tpl) {
    return (
      <div className="df-page">
        <EmptyState
          description="模板不存在或已停用"
          action={<Button onClick={() => navigate('/templates')}>返回模板库</Button>}
        />
      </div>
    );
  }

  const toggle = async () => {
    try {
      if (tpl.enabled) await disableTemplate(tpl.id);
      else await enableTemplate(tpl.id);
      Message.success('操作成功');
      qc.invalidateQueries({ queryKey: ['template', id] });
      qc.invalidateQueries({ queryKey: ['templates'] });
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  // 当前激活页签：显式取 /templates/:id/ 之后的第一段，只认四个合法页签，其余回退概览。
  const TAB_KEYS = ['overview', 'playground', 'versions', 'variables'];
  const seg = location.pathname.replace(/^\/templates\/[^/]+\//, '');
  const activeTab = TAB_KEYS.includes(seg) ? seg : 'overview';

  return (
    <div>
      {/* 页头 */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', padding: '16px 24px 0' }}>
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', minWidth: 0 }}>
          <Thumb id={tpl.id} />
          <div style={{ minWidth: 0 }}>
            <Space wrap>
              <span style={{ fontSize: 18, fontWeight: 700 }}>{tpl.name}</span>
              <Tag color={tpl.enabled ? 'green' : 'red'}>{tpl.enabled ? '启用中' : '已停用'}</Tag>
              <Tag color="gray">v{tpl.current_version?.version || '—'}</Tag>
            </Space>
            <div style={{ color: 'var(--color-text-3)', fontSize: 12, marginTop: 6 }}>
              code: <code>{tpl.code}</code>
              {tpl.category && <> · 分类：{tpl.category}</>}
              {tpl.created_by && <> · 创建人：{tpl.created_by}</>}
            </div>
          </div>
        </div>
        <Space>
          <Button icon={<IconEdit />} onClick={() => navigate(`/editor/${tpl.id}`)}>
            在线编辑
          </Button>
          <Button type="primary" icon={<IconPlayCircle />} onClick={() => navigate(`/templates/${tpl.id}/playground`)}>
            试运行
          </Button>
          <Popconfirm
            title={tpl.enabled ? '停用后该模板将不能再被渲染，历史产物仍可下载。' : '启用后可再次渲染。'}
            onConfirm={toggle}
          >
            <Button status={tpl.enabled ? 'warning' : 'success'}>
              {tpl.enabled ? '停用' : '启用'}
            </Button>
          </Popconfirm>
        </Space>
      </div>

      {/* 页签：key 绑定 id+activeTab，强制 Arco Tabs 在路由变化后重挂，避免受控高亮滞留 */}
      <Tabs key={`${id}-${activeTab}`} activeTab={activeTab} onChange={(k) => navigate(`/templates/${id}/${k === 'overview' ? '' : k}`)}
        style={{ padding: '0 24px', marginTop: 8 }}>
        <TabPane key="overview" title="概览" />
        <TabPane key="playground" title="试运行" />
        <TabPane key="versions" title="版本历史" />
        <TabPane key="variables" title="变量与资源" />
      </Tabs>

      <Outlet />
    </div>
  );
}

function Thumb({ id }: { id: string }) {
  // 缩略图由 LibreOffice 渲染页面图；未安装时接口 4xx，降级为文件图标而不是空白块
  const [failed, setFailed] = useState(false);
  return (
    <div className="thumb-box" style={{ width: 44, height: 56 }}>
      {failed ? (
        <span className="thumb-fallback">
          <IconFile />
        </span>
      ) : (
        <img src={`/api/v1/templates/${id}/thumbnail`} alt="" onError={() => setFailed(true)} />
      )}
    </div>
  );
}
