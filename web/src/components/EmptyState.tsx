import { ReactNode } from 'react';
import { Empty } from '@arco-design/web-react';

// Arco Empty 不接受 children，统一用这个容器放动作按钮
export default function EmptyState({ description, action }: { description: string; action?: ReactNode }) {
  return (
    <div style={{ padding: 40, textAlign: 'center' }}>
      <Empty description={description} />
      {action && <div style={{ marginTop: 8 }}>{action}</div>}
    </div>
  );
}
