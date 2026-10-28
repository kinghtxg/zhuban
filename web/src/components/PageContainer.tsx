/**
 * PageContainer —— 页面骨架（布局规范唯一入口）
 * 规范基线：Arco Design · Arco Pro
 *
 * 全站页面的结构固定为三段，顺序不可颠倒：
 *   1. 页头：主标题 + 副标题（左） / 主操作按钮（右）
 *   2. 工具栏：搜索、筛选、批量操作 —— 紧贴页头下方
 *   3. 内容：数据卡片 / 卡片网格 / 空状态
 *
 * 为什么强制统一：
 *   此前每个页面各自写 <div className="df-page"> + 手搓标题行 + 内联 margin，
 *   导致标题到内容的间距、按钮位置在页面间各不相同（有的主操作在右上、
 *   有的在标题下方），是「看起来乱」的直接来源。
 *
 * 使用约束：
 *   - 主操作只放 extra，且放最右侧（大厂后台一致约定）
 *   - 筛选条件只放 toolbar，不要塞进 extra
 *   - 页面正文不要再自己写 padding / 页头，交给本组件
 */

import { ReactNode } from 'react';

export interface PageContainerProps {
  /** 页面主标题 */
  title: ReactNode;
  /** 标题下的一行说明，用于交代「这个页面能干什么」 */
  subtitle?: ReactNode;
  /** 页头右侧主操作：新建 / 上传等 */
  extra?: ReactNode;
  /** 页头下方的筛选工具栏 */
  toolbar?: ReactNode;
  children?: ReactNode;
}

export default function PageContainer({
  title,
  subtitle,
  extra,
  toolbar,
  children,
}: PageContainerProps) {
  return (
    <div className="df-page">
      <div className="page-head">
        <div className="page-head-main">
          <h1 className="df-page-title">{title}</h1>
          {subtitle ? <div className="df-page-sub">{subtitle}</div> : null}
        </div>
        {extra ? <div className="page-head-actions">{extra}</div> : null}
      </div>

      {toolbar ? <div className="page-toolbar">{toolbar}</div> : null}

      {children}
    </div>
  );
}
