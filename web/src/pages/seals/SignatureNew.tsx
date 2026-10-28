import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, InputNumber, Message, Radio, Select, Skeleton, Switch } from '@arco-design/web-react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { createSignature, listSignatureFonts, previewSignature } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';
import { checkerBg } from './shared';

// 新建签名：上传图 / 文字生成双态 Tab，右侧实时预览（500ms 防抖）。
export default function SignatureNew() {
  const navigate = useNavigate();
  const [mode, setMode] = useState<'upload' | 'generated'>('generated');

  // upload 态
  const [file, setFile] = useState<File | null>(null);
  const [uploadPreview, setUploadPreview] = useState('');

  // generated 态
  const [signerName, setSignerName] = useState('');
  const [fontId, setFontId] = useState('');
  const [withTs, setWithTs] = useState(true);
  const [composite, setComposite] = useState(false);
  const [widthMm, setWidthMm] = useState(60);
  const [name, setName] = useState('');

  const { data: fontsData } = useQuery({ queryKey: ['signature-fonts'], queryFn: listSignatureFonts, retry: false });
  const fonts = fontsData?.fonts || [];

  // 默认选第一个字体
  useEffect(() => {
    if (!fontId && fonts.length > 0) setFontId(fonts[0].id);
  }, [fonts, fontId]);

  const [previewUrl, setPreviewUrl] = useState('');
  const [previewLoading, setPreviewLoading] = useState(false);
  const lastUrl = useRef<string>('');

  // 防抖实时预览（generated 态）
  useEffect(() => {
    if (mode !== 'generated' || !signerName || !fontId) return;
    setPreviewLoading(true);
    const t = setTimeout(async () => {
      try {
        const fd = new FormData();
        fd.append('signer_name', signerName);
        fd.append('font_id', fontId);
        fd.append('with_timestamp', String(withTs));
        fd.append('composite', String(composite));
        fd.append('width_mm', String(widthMm));
        const { blob } = await previewSignature(fd);
        if (lastUrl.current) URL.revokeObjectURL(lastUrl.current);
        const u = URL.createObjectURL(blob);
        lastUrl.current = u;
        setPreviewUrl(u);
      } catch {
        // 预览失败静默，保存时再报错
      } finally {
        setPreviewLoading(false);
      }
    }, 500);
    return () => clearTimeout(t);
  }, [mode, signerName, fontId, withTs, composite, widthMm]);

  const saveMut = useMutation({
    mutationFn: () => {
      const fd = new FormData();
      fd.append('name', name || (mode === 'generated' ? `${signerName}-签名` : '上传签名'));
      if (mode === 'upload') {
        fd.append('source', 'upload');
        if (file) fd.append('file', file);
      } else {
        fd.append('source', 'generated');
        fd.append('signer_name', signerName);
        fd.append('font_id', fontId);
        fd.append('with_timestamp', String(withTs));
        fd.append('composite', String(composite));
        fd.append('width_mm', String(widthMm));
      }
      return createSignature(fd);
    },
    onSuccess: (s) => {
      Message.success('已保存');
      navigate(`/seals-center/signatures/${s.id}`);
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const preview = mode === 'upload' ? uploadPreview : previewUrl;

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h2 className="df-page-title">新建签名</h2>
          <div className="df-page-sub">上传透明底手写 PNG，或用姓名+手写字体在线生成</div>
        </div>
      </div>

      <Radio.Group
        type="button"
        value={mode}
        onChange={(v) => setMode(v as 'upload' | 'generated')}
        style={{ marginBottom: 16 }}
      >
        <Radio value="generated">文字生成签名</Radio>
        <Radio value="upload">上传签名图（透明底 PNG）</Radio>
      </Radio.Group>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <Card title="参数">
          <Form layout="vertical">
            <Form.Item label="签名名称">
              <Input value={name} onChange={setName} placeholder="如：张三-合同签署" />
            </Form.Item>

            {mode === 'generated' ? (
              <>
                <Form.Item label="被签人姓名">
                  <Input value={signerName} onChange={setSignerName} placeholder="如：张三" />
                </Form.Item>
                <Form.Item label="字体">
                  <Select value={fontId} onChange={setFontId} style={{ width: '100%' }} placeholder="选择手写体">
                    {fonts.map((f) => (
                      <Select.Option key={f.id} value={f.id}>{f.name}</Select.Option>
                    ))}
                  </Select>
                </Form.Item>
                <Form.Item label="带服务器时间">
                  <Switch checked={withTs} onChange={setWithTs} />
                </Form.Item>
                <Form.Item label="组合图（签名在上 / 时间在下）">
                  <Switch checked={composite} onChange={setComposite} disabled={!withTs} />
                </Form.Item>
                <Form.Item label="宽度（mm）">
                  <InputNumber value={widthMm} onChange={(v) => setWidthMm(Number(v) || 60)} style={{ width: 160 }} />
                </Form.Item>
              </>
            ) : (
              <Form.Item label="上传 PNG（透明底，≤5MB）">
                <input
                  type="file"
                  accept="image/png"
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    if (f) {
                      setFile(f);
                      setUploadPreview(URL.createObjectURL(f));
                    }
                  }}
                />
              </Form.Item>
            )}
          </Form>
        </Card>

        <Card title="实时预览">
          <div style={{ ...checkerBg, height: 220, borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            {previewLoading ? (
              <Skeleton animation />
            ) : preview ? (
              <img src={preview} alt="" style={{ maxHeight: 200, maxWidth: '90%', objectFit: 'contain' }} />
            ) : (
              <span style={{ color: 'var(--color-border-3)', fontSize: 13 }}>填写参数后实时预览</span>
            )}
          </div>
          <Alert
            style={{ marginTop: 12 }}
            type="info"
            content="若系统开启了签名审核，保存后签名将进入待审核队列，通过前不可用于生成。"
          />
          <Button type="primary" style={{ marginTop: 12 }} loading={saveMut.isPending} onClick={() => saveMut.mutate()}>
            保存
          </Button>
        </Card>
      </div>
    </div>
  );
}
