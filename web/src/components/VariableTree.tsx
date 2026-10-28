import { Tag, Tree } from '@arco-design/web-react';
import { KIND_META } from '@/constants/enums';
import type { TemplateVariable } from '@/types/api';

interface Props {
  variables: TemplateVariable[];
}

// 变量清单树：按 kind 分组（标量/循环/条件/图片/二维码/富文本），循环展开 item 字段子树
export default function VariableTree({ variables }: Props) {
  const treeData = variables.map((v) => {
    const meta = KIND_META[v.kind];
    if (v.kind === 'loop' && v.fields) {
      return {
        key: v.path,
        title: (
          <span>
            <Tag color={meta.color} size="small">
              {meta.label}
            </Tag>{' '}
            {v.name}
          </span>
        ),
        children: v.fields.map((f) => ({
          key: f.path,
          title: (
            <span>
              <Tag color={KIND_META[f.kind]?.color || 'gray'} size="small">
                {KIND_META[f.kind]?.label || f.kind}
              </Tag>{' '}
              {f.name}
              <span style={{ color: 'var(--color-border-3)', fontSize: 12 }}>
                {' '}
                {f.required ? '必填' : ''}
              </span>
            </span>
          ),
        })),
      };
    }
    return {
      key: v.path,
      title: (
        <span>
          <Tag color={meta.color} size="small">
            {meta.label}
          </Tag>{' '}
          {v.name}
          {v.required ? <span style={{ color: 'rgb(var(--danger-6))' }}> *</span> : null}
        </span>
      ),
    };
  });

  if (variables.length === 0) {
    return <div style={{ color: 'var(--color-text-3)', fontSize: 13 }}>模板未扫描出变量。</div>;
  }
  return <Tree treeData={treeData} expandedKeys={treeData.map((n) => n.key)} />;
}
