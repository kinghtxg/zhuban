import { useRef, useState } from 'react';
import {
  Alert,
  Button,
  Form,
  Input,
  Message,
  Modal,
  Radio,
  Steps,
  Tag,
  Upload,
} from '@arco-design/web-react';
import { IconUpload } from '@arco-design/web-react/icon';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { createTemplate } from '@/api/endpoints';
import { SEVERITY_META, lookupMeta } from '@/constants/enums';
import type { Diagnostic, TemplateDetail, Visibility } from '@/types/api';
import { errorMessage } from '@/utils/format';

const Step = Steps.Step;
const MAX_MB = 10;

interface Props {
  visible: boolean;
  onClose: () => void;
  onCreated?: (tpl: TemplateDetail) => void;
}

// 上传向导：Step1 选 docx → Step2 填 code/名称/分类/可见性 → Step3 提交并展示扫描诊断
// 说明：契约 §4.3 规定扫描在创建模板时由后端完成（error 级诊断拒绝创建），故扫描结果在提交后展示。
export default function UploadWizard({ visible, onClose, onCreated }: Props) {
  const qc = useQueryClient();
  const [current, setCurrent] = useState(0);
  const [file, setFile] = useState<File | null>(null);
  const [code, setCode] = useState('');
  const [name, setName] = useState('');
  const [category, setCategory] = useState('');
  const [description, setDescription] = useState('');
  const [visibility, setVisibility] = useState<Visibility>('private');
  const [created, setCreated] = useState<TemplateDetail | null>(null);
  const [diags, setDiags] = useState<Diagnostic[]>([]);
  const uploadRef = useRef<HTMLDivElement>(null);

  const mutation = useMutation({
    mutationFn: () =>
      createTemplate({
        file: file!,
        code: code.trim(),
        name: name.trim(),
        category: category.trim() || undefined,
        description: description.trim() || undefined,
        visibility,
      }),
    onSuccess: (tpl) => {
      setCreated(tpl);
      setDiags(tpl.current_version?.diagnostics || []);
      qc.invalidateQueries({ queryKey: ['templates'] });
      setCurrent(2);
      Message.success('模板创建成功');
      onCreated?.(tpl);
    },
    onError: (err) => {
      // 422 回传诊断：解析 details 展示
      const { summary, details } = errorMessage(err);
      setDiags(
        details.map((d) => {
          const m = d.split('：');
          return { severity: 'error' as const, message: m.length > 1 ? m.slice(1).join('：') : d, location: m[0] };
        }),
      );
      setCurrent(2);
      Message.error({ content: `创建失败：${summary}`, duration: 5000 });
    },
  });

  const reset = () => {
    setCurrent(0);
    setFile(null);
    setCode('');
    setName('');
    setCategory('');
    setDescription('');
    setVisibility('private');
    setCreated(null);
    setDiags([]);
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  return (
    <Modal
      title="上传 Word 模板"
      visible={visible}
      onCancel={handleClose}
      footer={
        current === 0 ? (
          <Button type="primary" disabled={!file} onClick={() => setCurrent(1)}>
            下一步：填写信息
          </Button>
        ) : current === 1 ? (
          <>
            <Button onClick={() => setCurrent(0)}>上一步</Button>
            <Button type="primary" loading={mutation.isPending} onClick={() => mutation.mutate()}>
              上传并扫描
            </Button>
          </>
        ) : (
          <Button type="primary" onClick={handleClose}>
            完成
          </Button>
        )
      }
      style={{ width: 720 }}
      unmountOnExit
    >
      <Steps current={current} style={{ marginBottom: 20 }}>
        <Step title="上传文件" />
        <Step title="填写信息" />
        <Step title="扫描结果" />
      </Steps>

      {current === 0 && (
        <div ref={uploadRef}>
          <Upload
            drag
            accept=".docx"
            limit={1}
            multiple={false}
            beforeUpload={(f: File) => {
              if (!f.name.toLowerCase().endsWith('.docx')) {
                Message.error('只允许 .docx 文件（Word 模板），请先另存为 .docx');
                return false;
              }
              if (f.size > MAX_MB * 1024 * 1024) {
                Message.error(`文件不能超过 ${MAX_MB}MB`);
                return false;
              }
              setFile(f);
              return false; // 拦截自动上传，由向导统一提交
            }}
            onRemove={() => setFile(null)}
            tip={`支持 .docx，最大 ${MAX_MB}MB；含宏的 .docm 会被拒绝`}
          >
            <div style={{ padding: '20px 0' }}>
              <IconUpload style={{ fontSize: 40, color: 'rgb(var(--primary-6))' }} />
              <div style={{ marginTop: 8 }}>点击或拖拽 Word 模板到此处</div>
              <div style={{ color: 'var(--color-text-3)', fontSize: 12, marginTop: 4 }}>
                {'模板里直接写 {{ 变量 }}、{% for %} 等占位符'}
              </div>
            </div>
          </Upload>
          {file && (
            <Alert style={{ marginTop: 12 }} type="success" content={`已选择：${file.name}（${(file.size / 1024).toFixed(1)} KB）`} />
          )}
        </div>
      )}

      {current === 1 && (
        <Form layout="vertical">
          <Form.Item label="模板编号 code（API 调用用，创建后不可改）" required>
            <Input
              value={code}
              onChange={setCode}
              placeholder="如 audit-report，仅小写字母/数字/中划线"
              maxLength={64}
            />
          </Form.Item>
          <Form.Item label="名称" required>
            <Input value={name} onChange={setName} placeholder="如 代码审计报告" maxLength={64} />
          </Form.Item>
          <Form.Item label="分类">
            <Input value={category} onChange={setCategory} placeholder="如 安全交付（可选）" maxLength={32} />
          </Form.Item>
          <Form.Item label="描述">
            <Input.TextArea value={description} onChange={setDescription} placeholder="这份模板用来出什么报告（可选）" />
          </Form.Item>
          <Form.Item label="可见性">
            <Radio.Group value={visibility} onChange={(v) => setVisibility(v as Visibility)}>
              <Radio value="private">私有（仅自己可用）</Radio>
              <Radio value="public">公开（其他调用方可用）</Radio>
            </Radio.Group>
          </Form.Item>
        </Form>
      )}

      {current === 2 && (
        <div>
          {created ? (
            <Alert type="success" style={{ marginBottom: 12 }} content={`模板「${created.name}」创建成功，初始版本 ${created.current_version?.version || '1.0.0'}。`} />
          ) : (
            <Alert type="error" style={{ marginBottom: 12 }} content="模板未创建：请根据下面的诊断修复 Word 模板后重新上传。常见做法：在 Word 中把被拆坏的占位符删掉，用纯文本重新输入一遍（不要套用样式刷）。" />
          )}
          {diags.length === 0 ? (
            <Alert type="info" showIcon content="未发现语法诊断问题。" />
          ) : (
            diags.map((d, i) => {
              const sev = lookupMeta(SEVERITY_META, d.severity);
              return (
              <Alert
                key={i}
                style={{ marginBottom: 8 }}
                type={d.severity === 'error' ? 'error' : d.severity === 'warning' ? 'warning' : 'info'}
                icon={null}
                content={
                  <div>
                    <Tag color={sev.color} size="small">
                      {sev.label}
                    </Tag>{' '}
                    {d.message}
                    {d.location && <span style={{ color: 'var(--color-text-3)' }}>（位置：{d.location}）</span>}
                  </div>
                }
              />
              );
            })
          )}
        </div>
      )}
    </Modal>
  );
}
