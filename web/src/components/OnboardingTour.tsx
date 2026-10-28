import { useCallback, useEffect, useRef } from 'react';
import { driver, type Driver } from 'driver.js';
import 'driver.js/dist/driver.css';

// Arco 2.66.18 未导出 Tour 组件，按契约 §7 改用 driver.js（README/回报中注明）。
// 三步引导：① 传模板 → ② 看扫描结果与 JSON 示例 → ③ 填数据生成。
type Control = { start: () => void };
let externalControl: Control | null = null;
export const tourControl: Control = {
  start: () => externalControl?.start(),
};

const STEP_DEFS = [
  {
    element: '#tour-top-templates',
    title: '第一步：模板中心',
    description: '顶部一级导航按业务域分组：模板中心 / 文档中心 / 集成中心。在「模板库」中上传 .docx 模板（{{变量}}、{% for %} 等占位符直接写在 Word 里）。',
    side: 'bottom' as const,
    align: 'start' as const,
  },
  {
    element: '#tour-upload-btn',
    title: '第二步：上传模板并查看扫描结果',
    description: '上传后自动扫描变量清单、生成 JSON 示例；被 Word 拆散的占位符会给出修复指引。',
    side: 'bottom' as const,
    align: 'start' as const,
  },
  {
    element: '#tour-card-play',
    title: '第三步：进入试运行',
    description: '在模板卡片点「试运行」，把示例 JSON 改成真实数据，右侧实时预览，确认后一键生成 docx / pdf。',
    side: 'bottom' as const,
    align: 'start' as const,
  },
];

export default function OnboardingTour({ onDone }: { onDone?: () => void }) {
  const ref = useRef<Driver | null>(null);
  const doneRef = useRef(onDone);
  doneRef.current = onDone;

  const start = useCallback(() => {
    // 只保留当前 DOM 中真实存在的锚点步骤，避免 driver 落空
    const steps = STEP_DEFS.filter((s) => document.querySelector(s.element));
    if (steps.length === 0) {
      doneRef.current?.();
      return;
    }
    ref.current?.destroy();
    const d = driver({
      animate: true,
      showProgress: true,
      nextBtnText: '下一步',
      prevBtnText: '上一步',
      doneBtnText: '完成',
      popoverClass: 'df-driver-popover',
      steps: steps.map((s) => ({
        element: s.element,
        popover: { title: s.title, description: s.description, side: s.side, align: s.align },
      })),
      onDestroyed: () => doneRef.current?.(),
    });
    ref.current = d;
    d.drive();
  }, []);

  useEffect(() => {
    externalControl = { start };
    return () => {
      ref.current?.destroy();
    };
  }, [start]);

  return null;
}
