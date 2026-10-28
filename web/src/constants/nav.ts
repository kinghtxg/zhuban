// 控制台信息架构：顶部一级域 + 左侧二级菜单（PRD V2 §3.1/§3.2）
// 路由前缀 → 一级域；一级域 → 二级菜单。adminOnly 的域对 member 不渲染菜单且路由 403。

export interface SubNavItem {
  path: string; // 二级菜单目标路径（精确前缀匹配）
  label: string;
}

export interface Domain {
  key: string;
  label: string;
  prefix: string; // pathname 前缀命中该域
  adminOnly?: boolean;
  children: SubNavItem[];
}

export const DOMAINS: Domain[] = [
  {
    key: 'templates',
    label: '模板中心',
    prefix: '/templates',
    children: [
      { path: '/templates', label: '模板库' },
      { path: '/resources', label: '变量与资源' },
    ],
  },
  {
    key: 'documents',
    label: '文档中心',
    prefix: '/documents',
    children: [
      { path: '/documents', label: '生成记录' },
      { path: '/deliveries', label: '投递任务' },
    ],
  },
  {
    key: 'seals',
    label: '签章中心',
    prefix: '/seals-center',
    children: [
      { path: '/seals-center/signatures', label: '签名库' },
      { path: '/seals-center/seals', label: '印章库' },
      { path: '/seals-center/review', label: '审核队列' },
    ],
  },
  {
    key: 'integrations',
    label: '集成中心',
    prefix: '/integrations',
    children: [
      { path: '/integrations/keys', label: 'API Key' },
      { path: '/integrations/deliver', label: '投递即生成' },
      { path: '/integrations/docs', label: '回调与接入文档' },
    ],
  },
  {
    key: 'admin',
    label: '管理中心',
    prefix: '/admin',
    adminOnly: true,
    children: [
      { path: '/admin/users', label: '用户管理' },
      { path: '/admin/audit-logs', label: '审计日志' },
      { path: '/admin/mail', label: '邮件服务' },
      { path: '/admin/settings', label: '系统设置' },
    ],
  },
];

// 根据 pathname 命中一级域：先按二级菜单 children 路径参与匹配（/deliveries、/resources
// 等挂在某域下但不等于该域 prefix 的路径也能命中），再回退到域 prefix。
// /editor 归模板中心但全屏不显示二级菜单。
export function matchDomain(pathname: string): Domain | undefined {
  if (pathname.startsWith('/editor')) return DOMAINS[0];

  // 1) children 前缀匹配（最长优先）
  let best: Domain | undefined;
  let bestLen = -1;
  for (const d of DOMAINS) {
    for (const c of d.children) {
      if (pathname.startsWith(c.path) && c.path.length > bestLen) {
        best = d;
        bestLen = c.path.length;
      }
    }
  }
  if (best) return best;

  // 2) 域 prefix 回退
  for (const d of DOMAINS) {
    if (pathname.startsWith(d.prefix)) return d;
  }
  return undefined;
}

// 二级菜单当前高亮项：取 children 中与 pathname 最长前缀匹配的那个
export function matchSubItem(domain: Domain, pathname: string): SubNavItem | undefined {
  let best: SubNavItem | undefined;
  let bestLen = -1;
  for (const c of domain.children) {
    if (pathname.startsWith(c.path) && c.path.length > bestLen) {
      best = c;
      bestLen = c.path.length;
    }
  }
  return best;
}
