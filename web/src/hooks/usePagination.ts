/**
 * usePagination —— 列表分页统一入口
 *
 * 重构背景：Documents / Deliveries / AuditLogs / UsersAdmin 四个列表页各自维护
 * `const [page, setPage] = useState(1)` 并手写完全相同的 pagination 字面量
 * （current / pageSize: 20 / total / onChange）。收敛到此处后，页面只需：
 *
 *   const { page, setPage, pagination } = usePagination();
 *   ...
 *   <Table pagination={pagination(data?.total)} />
 *
 * 筛选条件变化时调用 setPage(1) 回到首页，行为与重构前一致。
 */

import { useCallback, useState } from 'react';

/** 全站列表默认每页条数 */
export const DEFAULT_PAGE_SIZE = 20;

export interface TablePagination {
  current: number;
  pageSize: number;
  total: number;
  showTotal: boolean;
  onChange: (page: number) => void;
}

export function usePagination(pageSize: number = DEFAULT_PAGE_SIZE) {
  const [page, setPage] = useState(1);

  const pagination = useCallback(
    (total?: number | null): TablePagination => ({
      current: page,
      pageSize,
      total: total || 0,
      showTotal: true,
      onChange: setPage,
    }),
    [page, pageSize],
  );

  return { page, setPage, pagination };
}
