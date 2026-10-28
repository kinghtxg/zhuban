import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, InputNumber, Message, Radio, Skeleton, Switch } from '@arco-design/web-react';
import { useMutation } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { createSeal, previewSeal } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';
import { checkerBg } from './shared';

export default function SealNew() {
  const navigate = useNavigate();
  const [mode, setMode] = useState<'generated' | 'upload'>('generated');
  const [name, setName] = useState('');
  const [unitName, setUnitName] = useState('');
  const [sealName, setSealName] = useState('');
  const [shape, setShape] = useState<'circle' | 'ellipse'>('circle');
  const [radius, setRadius] = useState(20);
  const [star, setStar] = useState(true);
  const [file, setFile] = useState<File | null>(null);
  const [uploadPreview, setUploadPreview] = useState('');

  const [previewUrl, setPreviewUrl] = useState('');
  const [previewLoading, setPreviewLoading] = useState(false);
  const lastUrl = useRef('');

  useEffect(() => {
    if (mode !== 'generated') return;
    if (!unitName && !sealName) return;
    setPreviewLoading(true);
    const t = setTimeout(async () => {
      try {
        const fd = new FormData();
        fd.append('unit_name', unitName);
        fd.append('seal_name', sealName);
        fd.append('shape', shape);
        fd.append('radius_mm', String(radius));
        fd.append('star', String(star));
        const { blob } = await previewSeal(fd);
        if (lastUrl.current) URL.revokeObjectURL(lastUrl.current);
        const u = URL.createObjectURL(blob);
        lastUrl.current = u;
        setPreviewUrl(u);
      } catch {
        // 预览失败静默
      } finally {
        setPreviewLoading(false);
      }
    }, 500);
    return () => clearTimeout(t);
  }, [mode, unitName, sealName, shape, radius, star]);

  const saveMut = useMutation({
    mutationFn: () => {
      const fd = new FormData();
      fd.append('name', name || sealName || '印章');
      if (mode === 'upload') {
        fd.append('source', 'upload');
        if (file) fd.append('file', file);
      } else {
        fd.append('source', 'generated');
        fd.append('unit_name', unitName);
        fd.append('seal_name', sealName);
        fd.append('shape', shape);
        fd.append('radius_mm', String(radius));
        fd.append('star', String(star));
      }
      return createSeal(fd);
    },
    onSuccess: (s) => {
      Message.success('已保存');
      navigate(`/seals-center/seals/${s.id}`);
    },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  const preview = mode === 'upload' ? uploadPreview : previewUrl;

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h2 className="df-page-title">新建印章</h2>
          <div className="df-page-sub">参数化生成红色圆/椭圆章，或上传印章图</div>
        </div>
      </div>

      <Radio.Group type="button" value={mode} onChange={(v) => setMode(v as 'generated' | 'upload')} style={{ marginBottom: 16 }}>
        <Radio value="generated">参数化生成</Radio>
        <Radio value="upload">上传印章图</Radio>
      </Radio.Group>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <Card title="参数">
          <Form layout="vertical">
            {/*
              这里原来和下面的「印章名称」同名，两个字段都叫「印章名称」——
              填表的人不知道该把「公章」写进哪一格，而这两格进的是完全不同的地方：
              这一格是资产在列表/选择器里显示的名字，下一格是画到五角星下方的那行字。
              名字改成「素材名称」并把用途写进 extra，两个字段就不会混了。
            */}
            <Form.Item label="素材名称" extra="仅在印章库里标识这份素材，不会画到图上">
              <Input value={name} onChange={setName} placeholder="如：公章-2026" />
            </Form.Item>
            {mode === 'generated' ? (
              <>
                <Form.Item label="单位名称（环形）">
                  <Input value={unitName} onChange={setUnitName} placeholder="如：示例科技有限公司" />
                </Form.Item>
                <Form.Item label="五角星下文字" extra="会画在五角星下方">
                  <Input value={sealName} onChange={setSealName} placeholder="如：合同专用章" />
                </Form.Item>
                <Form.Item label="形状">
                  <Radio.Group type="button" value={shape} onChange={(v) => setShape(v as 'circle' | 'ellipse')}>
                    <Radio value="circle">圆形</Radio>
                    <Radio value="ellipse">椭圆</Radio>
                  </Radio.Group>
                </Form.Item>
                <Form.Item label="半径（mm）">
                  <InputNumber value={radius} onChange={(v) => setRadius(Number(v) || 20)} style={{ width: 160 }} />
                </Form.Item>
                <Form.Item label="五角星">
                  <Switch checked={star} onChange={setStar} />
                </Form.Item>
              </>
            ) : (
              <Form.Item label="上传印章图（透明底 PNG，≤5MB）">
                <input type="file" accept="image/png" onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) { setFile(f); setUploadPreview(URL.createObjectURL(f)); }
                }} />
              </Form.Item>
            )}
          </Form>
        </Card>

        <Card title="实时预览">
          <div style={{ ...checkerBg, height: 260, borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            {previewLoading ? (
              <Skeleton animation />
            ) : preview ? (
              <img src={preview} alt="" style={{ maxHeight: 230, maxWidth: '80%', objectFit: 'contain' }} />
            ) : (
              <span style={{ color: 'var(--color-border-3)', fontSize: 13 }}>填写参数后实时预览红章</span>
            )}
          </div>
          <Alert style={{ marginTop: 12 }} type="info" content="若系统开启了印章审核，保存后印章将进入待审核队列。" />
          <Button type="primary" style={{ marginTop: 12 }} loading={saveMut.isPending} onClick={() => saveMut.mutate()}>
            保存
          </Button>
        </Card>
      </div>
    </div>
  );
}
