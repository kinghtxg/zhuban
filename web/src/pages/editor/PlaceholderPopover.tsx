import { useState } from 'react';
import { Button, Input, Modal, Radio, Space, Tag } from '@arco-design/web-react';
import type { EditBlock } from '@/types/editor';

interface Props {
  block: EditBlock;
  onClose: () => void;
  onWrap: (kind: 'for' | 'if', a: string, b: string) => void;
  onInsertPlaceholder: (kind: string, varName: string) => void;
  onPickSignature: () => void;
  onPickSeal: () => void;
}

// 选中块后的浮起操作：插入占位符 / 包裹段级循环或条件 / 删除块
export default function PlaceholderPopover({ block, onClose, onWrap, onInsertPlaceholder, onPickSignature, onPickSeal }: Props) {
  const [open, setOpen] = useState(true);
  const [mode, setMode] = useState<'scalar' | 'image' | 'qrcode' | 'rich' | 'for' | 'if' | null>(null);
  const [name, setName] = useState('');
  const [iterVar, setIterVar] = useState('item');
  const [iterObj, setIterObj] = useState('items');
  const [expr, setExpr] = useState('');

  const isTable = block.type === 'table';
  const isUnsupported = block.type === 'unsupported';

  const close = () => {
    setOpen(false);
    onClose();
  };

  const submit = () => {
    if (mode === 'for') {
      onWrap('for', iterVar.trim() || 'item', iterObj.trim() || 'items');
    } else if (mode === 'if') {
      onWrap('if', expr.trim(), '');
    } else if (mode) {
      onInsertPlaceholder(mode, name);
    }
    close();
  };

  return (
    <Modal
      title="块操作"
      visible={open}
      onCancel={close}
      footer={null}
      onOk={submit}
    >
      <div style={{ marginBottom: 10, fontSize: 13, color: 'var(--app-text-body)' }}>
        当前块：<Tag>{block.type}{block.type === 'heading' ? ` h${block.level}` : ''}</Tag>
        {isTable && <span style={{ marginLeft: 8, fontSize: 12 }}>表格行增删在悬停行时操作；列增删请回 Word。</span>}
      </div>

      <Radio.Group
        style={{ marginBottom: 12, display: 'flex', flexWrap: 'wrap', gap: 4 }}
        value={mode as string}
        onChange={(v) => {
          const val = v as string;
          if (val === 'sig') {
            setOpen(false);
            onClose();
            onPickSignature();
            return;
          }
          if (val === 'seal') {
            setOpen(false);
            onClose();
            onPickSeal();
            return;
          }
          setMode(v as typeof mode);
        }}
        options={[
          { label: '插入标量占位符', value: 'scalar' },
          { label: '插入图片', value: 'image' },
          { label: '插入二维码', value: 'qrcode' },
          { label: '插入富文本', value: 'rich' },
          { label: '插入签名', value: 'sig' },
          { label: '插入印章', value: 'seal' },
          { label: '包裹循环', value: 'for' },
          { label: '包裹条件', value: 'if' },
        ]}
        direction="vertical"
      />

      {(mode === 'scalar' || mode === 'image' || mode === 'qrcode' || mode === 'rich') && (
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--app-text-caption)', marginBottom: 4 }}>变量名</div>
          <Input
            value={name}
            onChange={setName}
            placeholder={mode === 'scalar' ? '如 项目编号' : mode === 'image' ? '如 logo（自动加 image_ 前缀）' : '如 报告二维码'}
          />
        </div>
      )}

      {mode === 'for' && (
        <Space style={{ marginBottom: 12 }}>
          <div>
            <div style={{ fontSize: 12, color: 'var(--app-text-caption)' }}>循环变量</div>
            <Input value={iterVar} onChange={setIterVar} style={{ width: 120 }} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--app-text-caption)' }}>可迭代对象</div>
            <Input value={iterObj} onChange={setIterObj} style={{ width: 120 }} />
          </div>
        </Space>
      )}

      {mode === 'if' && (
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--app-text-caption)', marginBottom: 4 }}>条件表达式（jinja）</div>
          <Input value={expr} onChange={setExpr} placeholder="如 not loop.last 或 has_cover" />
        </div>
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
        <Button status="danger" disabled={isUnsupported} onClick={() => { onClose(); }}>
          删除块
        </Button>
        <Space>
          <Button onClick={close}>取消</Button>
          <Button type="primary" onClick={submit}>执行</Button>
        </Space>
      </div>
    </Modal>
  );
}
