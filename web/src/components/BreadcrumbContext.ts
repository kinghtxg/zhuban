import { createContext, useContext } from 'react';

// 页面可向外壳面包屑追加「具体对象名」一段（如模板名 / 试运行 / 版本历史）。
// 值为字符串数组，AppLayout 追加到 一级域 / 二级页 之后。
export const BreadcrumbContext = createContext<{
  crumbs: string[];
  setCrumbs: (c: string[]) => void;
}>({ crumbs: [], setCrumbs: () => undefined });

export function useBreadcrumb() {
  return useContext(BreadcrumbContext);
}
