/**
 * 控制台外壳 —— 布局骨架
 * 规范基线：Arco Design（字节跳动）
 *
 * 布局结构（Arco Pro 后台标准）：
 *   Layout
 *   ├── Layout.Header   56px 顶部通栏：品牌 + 一级域导航（Menu horizontal）+ 用户区
 *   └── Layout
 *       ├── Layout.Sider  220px 二级菜单（Menu vertical），编辑器全屏模式不渲染
 *       └── Layout.Content 面包屑条 + 页面滚动区
 *
 * 规范约束：
 *   - 导航一律使用 Arco Menu 组件（自带键盘可访问性、选中态、折叠能力），
 *     不再手写 button / div 模拟菜单项
 *   - 面包屑使用 Arco Breadcrumb，分隔符由组件统一提供
 *   - 尺寸、间距、圆角一律取自 tokens.css，不在组件内写死像素与色值
 */

import { ReactNode, useEffect, useMemo, useState } from 'react';
import {
  Avatar,
  Badge,
  Breadcrumb,
  Dropdown,
  Layout,
  Menu,
} from '@arco-design/web-react';
import {
  IconDown,
  IconExport,
  IconQuestionCircle,
  IconSettings,
} from '@arco-design/web-react/icon';
import { useLocation, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import type { MeResponse } from '@/types/api';
import { listAdminUsers, listReviewQueue, logout, markGuideSeen } from '@/api/endpoints';
import { formatVersion, useVersion } from '@/hooks/useVersion';
import { DOMAINS, matchDomain, matchSubItem } from '@/constants/nav';
import OnboardingTour, { tourControl } from './OnboardingTour';
import PersonalCenter from './PersonalCenter';
import { BreadcrumbContext } from './BreadcrumbContext';

const { Header, Sider, Content } = Layout;

export default function AppLayout({ me, children }: { me: MeResponse; children: ReactNode }) {
  const location = useLocation();
  const navigate = useNavigate();
  const [profileOpen, setProfileOpen] = useState(false);
  const [extraCrumbs, setExtraCrumbs] = useState<string[]>([]);

  const isAdmin = me.user.role === 'admin';
  const pathname = location.pathname;
  const editorMode = pathname.startsWith('/editor');

  // 侧栏页脚要显示的版本号（构建期注入，见 /api/v1/version）
  const ver = formatVersion(useVersion());

  // admin：待审核用户数 → 管理中心角标
  const { data: adminData } = useQuery({
    queryKey: ['admin-users-pending'],
    queryFn: () => listAdminUsers({ page: 1, page_size: 1 }),
    enabled: isAdmin,
    retry: false,
  });
  const pendingCount = adminData?.pending_count || 0;

  // admin：签章审核待审总数（签名 + 印章）→ 签章中心角标
  const sigQ = useQuery({
    queryKey: ['review-queue', 'signature'],
    queryFn: () => listReviewQueue('signature'),
    enabled: isAdmin,
    retry: false,
  });
  const sealQ = useQuery({
    queryKey: ['review-queue', 'seal'],
    queryFn: () => listReviewQueue('seal'),
    enabled: isAdmin,
    retry: false,
  });
  // 计数兼容：items.length 为准（后端 total 可能为 0/缺省），回退 total
  const queueCount = (d: typeof sigQ.data) => d?.items?.length ?? d?.total ?? 0;
  const reviewPending = queueCount(sigQ.data) + queueCount(sealQ.data);

  // 首次登录自动播引导
  useEffect(() => {
    if (!me.guide_seen) {
      const t = setTimeout(() => tourControl.start(), 700);
      return () => clearTimeout(t);
    }
  }, [me.guide_seen]);

  // 切页时清空页面级面包屑段（由页面重新设置）
  useEffect(() => {
    setExtraCrumbs([]);
  }, [pathname]);

  const domain = matchDomain(pathname);
  const subItem = domain ? matchSubItem(domain, pathname) : undefined;

  const visibleDomains = useMemo(() => DOMAINS.filter((d) => !d.adminOnly || isAdmin), [isAdmin]);

  const handleLogout = async () => {
    try {
      await logout();
    } catch {
      // 忽略：前端直接跳登录
    }
    window.location.href = '/login';
  };

  const displayName = me.user.display_name || me.user.username;

  // 一级域点击 → 进入该域首个二级页面
  const goDomain = (key: string) => {
    const d = visibleDomains.find((x) => x.key === key);
    if (d) navigate(d.children[0].path);
  };

  return (
    <BreadcrumbContext.Provider value={{ crumbs: extraCrumbs, setCrumbs: setExtraCrumbs }}>
      <Layout className="app-shell">
        {/* ===== 顶部通栏：品牌 + 一级域导航 + 用户区 ===== */}
        <Header className="app-header">
          <div className="app-brand" onClick={() => navigate('/templates')}>
            <span className="brand-mark">铸</span>
            <span className="app-brand-name">铸版</span>
          </div>

          <Menu
            className="app-nav-top"
            mode="horizontal"
            selectedKeys={domain ? [domain.key] : []}
            onClickMenuItem={goDomain}
          >
            {visibleDomains.map((d) => (
              <Menu.Item key={d.key}>
                <span id={d.key === 'templates' ? 'tour-top-templates' : undefined}>
                  {d.label}
                  {d.key === 'admin' && pendingCount > 0 && (
                    <Badge count={pendingCount} dotStyle={{ marginLeft: 6 }} color="red" />
                  )}
                  {d.key === 'seals' && reviewPending > 0 && (
                    <Badge count={reviewPending} dotStyle={{ marginLeft: 6 }} color="orange" />
                  )}
                </span>
              </Menu.Item>
            ))}
          </Menu>

          <div className="app-header-right">
            <span className="app-header-action" onClick={() => navigate('/help')}>
              <IconQuestionCircle />
              帮助
            </span>
            <Dropdown
              trigger="hover"
              droplist={
                <Menu
                  onClickMenuItem={(key) => {
                    if (key === 'profile') setProfileOpen(true);
                    else if (key === 'logout') handleLogout();
                  }}
                >
                  <div className="app-user-menu-head">
                    <div className="app-user-menu-name">{displayName}</div>
                    <div className="app-user-menu-role">
                      {isAdmin ? '管理员 · 可管理用户与系统设置' : '成员 · 可创建模板与生成文档'}
                    </div>
                  </div>
                  <Menu.Item key="profile">
                    <IconSettings />
                    个人中心
                  </Menu.Item>
                  <Menu.Item key="logout">
                    <IconExport />
                    退出登录
                  </Menu.Item>
                </Menu>
              }
            >
              <span className="app-user-trigger">
                <Avatar size={28} style={{ background: 'var(--brand-gradient)' }}>
                  {displayName.slice(0, 1)}
                </Avatar>
                <span className="app-user-name">{displayName}</span>
                <IconDown className="app-user-caret" />
              </span>
            </Dropdown>
          </div>
        </Header>

        {/* ===== 主体：二级菜单 + 内容 ===== */}
        <Layout className="app-body">
          {!editorMode && (
            <Sider className="app-sider" width={220}>
              {domain && (
                <Menu
                  className="app-nav-side"
                  mode="vertical"
                  selectedKeys={subItem ? [subItem.path] : []}
                  onClickMenuItem={(path) => navigate(path)}
                >
                  {domain.children.map((c) => (
                    <Menu.Item key={c.path}>{c.label}</Menu.Item>
                  ))}
                </Menu>
              )}
              {/* 侧栏页脚：开源版不署具体公司，只放产品名与版本。
                  版本号取自 /api/v1/version（构建期注入），不用前端写死的常量——
                  写死的话每次发版都会忘改。 */}
              <div className="app-sider-foot">
                铸版 ZHUBAN{ver ? ` · ${ver}` : ''}
              </div>
            </Sider>
          )}

          <Content className="app-content">
            <div className="app-crumbbar">
              <Breadcrumb>
                <Breadcrumb.Item>{domain?.label || '铸版'}</Breadcrumb.Item>
                {subItem && <Breadcrumb.Item>{subItem.label}</Breadcrumb.Item>}
                {extraCrumbs.map((c) => (
                  <Breadcrumb.Item key={c}>{c}</Breadcrumb.Item>
                ))}
              </Breadcrumb>
            </div>
            <div className="app-scroll">{children}</div>
          </Content>
        </Layout>

        <OnboardingTour onDone={() => markGuideSeen().catch(() => undefined)} />
        <PersonalCenter visible={profileOpen} onClose={() => setProfileOpen(false)} me={me} />
      </Layout>
    </BreadcrumbContext.Provider>
  );
}
