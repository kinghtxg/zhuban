import { Suspense, lazy, useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom';
import { Button, Result, Skeleton } from '@arco-design/web-react';
import { setUnauthorizedHandler } from '@/api/client';
import { getMe } from '@/api/endpoints';
import { ApiError } from '@/types/api';
import AppLayout from '@/components/AppLayout';
import LoginPage from '@/pages/Login';
import TemplatesPage from '@/pages/Templates';
// 模板试运行（Studio）与投递调试台（DeliverPlayground）里嵌着 Monaco 编辑器。
// 这两个页面改成按需加载：Monaco 加上它的 worker 是这个前端里最大的一块，
// 而它只服务「想手写数据 JSON」的场景。不这么拆的话，所有人打开登录页都要
// 先下完一个代码编辑器。
const StudioPage = lazy(() => import('@/pages/Studio'));
const DeliverPlayground = lazy(() => import('@/pages/integrations/DeliverPlayground'));
import DocumentsPage from '@/pages/Documents';
import DeliveriesPage from '@/pages/Deliveries';
import KeysPage from '@/pages/Keys';
import IntegrationDocs from '@/pages/integrations/IntegrationDocs';
import HelpPage from '@/pages/Help';
import UsersAdminPage from '@/pages/UsersAdmin';
import AdminSettingsPage from '@/pages/admin/AdminSettings';
import MailSettingsPage from '@/pages/admin/MailSettings';
import AuditLogsPage from '@/pages/admin/AuditLogs';
import ResourcesPage from '@/pages/Resources';
import SignatureList from '@/pages/seals/SignatureList';
import SignatureNew from '@/pages/seals/SignatureNew';
import SignatureDetail from '@/pages/seals/SignatureDetail';
import SealList from '@/pages/seals/SealList';
import SealNew from '@/pages/seals/SealNew';
import SealDetail from '@/pages/seals/SealDetail';
import ReviewQueue from '@/pages/seals/ReviewQueue';
import TemplateDetail from '@/pages/template/TemplateDetail';
import TemplateOverview from '@/pages/template/TemplateOverview';
import TemplateVersions from '@/pages/template/TemplateVersions';
import TemplateVariables from '@/pages/template/TemplateVariables';
const EditorPage = lazy(() => import('@/pages/editor/EditorPage'));
import '@/styles/editor.css';

// 旧路由 /studio/:templateId → 新试运行
function StudioRedirect() {
  const { templateId } = useParams();
  return <Navigate to={`/templates/${templateId}/playground`} replace />;
}

export default function App() {
  const qc = useQueryClient();
  const location = useLocation();

  // 401 → 失效缓存，触发重新拉 /me → 自动落到登录页（带 redirect）
  useEffect(() => {
    setUnauthorizedHandler(() => {
      qc.setQueryData(['me'], null);
      qc.invalidateQueries({ queryKey: ['me'] });
    });
  }, [qc]);

  const { data: me, isLoading, error } = useQuery({
    queryKey: ['me'],
    queryFn: getMe,
    retry: false,
  });

  if (isLoading) {
    return (
      <div style={{ padding: 80 }}>
        <Skeleton animation />
      </div>
    );
  }

  // 403：账号未激活/被禁用 → 独立无权限页
  if (error instanceof ApiError && error.status === 403) {
    return (
      <div style={{ padding: 120 }}>
        <Result
          status="403"
          title="暂无权限访问"
          subTitle={error.message || '账号未激活或已被禁用，请联系管理员审核。'}
          extra={<Button onClick={() => (window.location.href = '/api/v1/auth/logout')}>退出并重新登录</Button>}
        />
      </div>
    );
  }

  // 未登录
  if (!me) {
    if (location.pathname === '/login') return <LoginPage />;
    const q = encodeURIComponent(location.pathname + location.search);
    return <Navigate to={`/login?redirect=${q}`} replace />;
  }

  // 已登录访问 /login → 回首页
  if (location.pathname === '/login') return <Navigate to="/" replace />;

  // 在线编辑器：全屏工作区，不套控制台二级菜单壳（与 /login 同级渲染）
  if (location.pathname.startsWith('/editor/')) {
    return (
      <Suspense fallback={<PageLoading />}>
        <Routes>
          <Route path="/editor/:templateId" element={<EditorPage />} />
        </Routes>
      </Suspense>
    );
  }

  return (
    <AppLayout me={me}>
      <Suspense fallback={<PageLoading />}>
        <Routes>
          <Route path="/" element={<Navigate to="/templates" replace />} />

          {/* 模板中心 */}
          <Route path="/templates" element={<TemplatesPage />} />
          <Route path="/templates/:id" element={<TemplateDetail />}>
            <Route index element={<TemplateOverview />} />
            <Route path="playground" element={<StudioPage />} />
            <Route path="versions" element={<TemplateVersions />} />
            <Route path="variables" element={<TemplateVariables />} />
          </Route>
          <Route path="/resources" element={<ResourcesPage />} />

          {/* 文档中心 */}
          <Route path="/documents" element={<DocumentsPage />} />
          <Route path="/deliveries" element={<DeliveriesPage />} />

          {/* 签章中心 */}
          <Route path="/seals-center" element={<Navigate to="/seals-center/signatures" replace />} />
          <Route path="/seals-center/signatures" element={<SignatureList />} />
          <Route path="/seals-center/signatures/new" element={<SignatureNew />} />
          <Route path="/seals-center/signatures/:id" element={<SignatureDetail />} />
          <Route path="/seals-center/seals" element={<SealList />} />
          <Route path="/seals-center/seals/new" element={<SealNew />} />
          <Route path="/seals-center/seals/:id" element={<SealDetail />} />
          <Route path="/seals-center/review" element={<RequireAdmin role={me.user.role}><ReviewQueue /></RequireAdmin>} />

          {/* 集成中心 */}
          <Route path="/integrations/keys" element={<KeysPage />} />
          <Route path="/integrations/deliver" element={<DeliverPlayground />} />
          <Route path="/integrations/docs" element={<IntegrationDocs />} />

          {/* 全局 */}
          <Route path="/help" element={<HelpPage />} />

          {/* 管理中心（仅 admin） */}
          <Route path="/admin/users" element={<RequireAdmin role={me.user.role}><UsersAdminPage currentUserId={me.user.id} /></RequireAdmin>} />
          <Route path="/admin/audit-logs" element={<RequireAdmin role={me.user.role}><AuditLogsPage /></RequireAdmin>} />
          <Route path="/admin/mail" element={<RequireAdmin role={me.user.role}><MailSettingsPage /></RequireAdmin>} />
          <Route path="/admin/settings" element={<RequireAdmin role={me.user.role}><AdminSettingsPage /></RequireAdmin>} />

          {/* 新旧重定向 */}
          <Route path="/studio" element={<Navigate to="/templates" replace />} />
          <Route path="/studio/:templateId" element={<StudioRedirect />} />
          <Route path="/keys" element={<Navigate to="/integrations/keys" replace />} />

          <Route path="*" element={<Navigate to="/templates" replace />} />
        </Routes>
      </Suspense>
    </AppLayout>
  );
}

// 路由守卫：仅管理员可访问；非管理员展示独立 403 页
function RequireAdmin({ role, children }: { role: string; children: React.ReactNode }) {
  const navigate = useNavigate();
  if (role !== 'admin') {
    return (
      <div style={{ padding: 80 }}>
        <Result
          status="403"
          title="暂无权限访问"
          subTitle="管理中心仅管理员可访问。"
          extra={<Button type="primary" onClick={() => navigate('/templates')}>返回首页</Button>}
        />
      </div>
    );
  }
  return <>{children}</>;
}

// PageLoading 是按需加载分块的占位。
// 用骨架而不是转圈：分块还在下载时，页面结构先出现，视觉上不会「先空一下再跳出来」。
function PageLoading() {
  return (
    <div style={{ padding: 24 }}>
      <Skeleton animation text={{ rows: 6 }} />
    </div>
  );
}
