import { useState } from 'react';
import { Button, Empty, Input, Message, Modal, Table, Tabs } from '@arco-design/web-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { approveReview, listReviewQueue, rejectReview } from '@/api/endpoints';
import type { ReviewQueueItem } from '@/types/api';
import { errorMessage, fmtTime } from '@/utils/format';
import { checkerBg, sealImageUrl } from './shared';

const TabPane = Tabs.TabPane;

// 审核队列：签名待审 / 印章待审 两个 Tab，通过/驳回即时刷新。
export default function ReviewQueue() {
  const qc = useQueryClient();
  const [tab, setTab] = useState<'signature' | 'seal'>('signature');
  const [rejectTarget, setRejectTarget] = useState<ReviewQueueItem | null>(null);
  const [note, setNote] = useState('');

  const sigQ = useQuery({ queryKey: ['review-queue', 'signature'], queryFn: () => listReviewQueue('signature') });
  const sealQ = useQuery({ queryKey: ['review-queue', 'seal'], queryFn: () => listReviewQueue('seal') });
  const active = tab === 'signature' ? sigQ : sealQ;
  const rows = active.data?.items || [];

  // 计数兼容：优先 items.length（后端 total 可能为 0 或缺省），其次嵌套 pending，最后 total。
  const countOf = (q: typeof sigQ, kind: 'signature' | 'seal') => {
    const d = q.data as typeof sigQ.data & { pending?: { signature?: number; seal?: number } };
    return d?.items?.length ?? d?.pending?.[kind] ?? d?.total ?? 0;
  };
  const sigCount = countOf(sigQ, 'signature');
  const sealCount = countOf(sealQ, 'seal');

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['review-queue'] });
  };

  const approveMut = useMutation({
    mutationFn: (item: ReviewQueueItem) => approveReview(item.resource_type, item.id),
    onSuccess: () => { Message.success('已通过'); refresh(); },
    onError: (err) => Message.error(errorMessage(err).summary),
  });
  const rejectMut = useMutation({
    mutationFn: () => rejectReview(rejectTarget!.resource_type, rejectTarget!.id, note),
    onSuccess: () => { Message.success('已驳回'); setRejectTarget(null); setNote(''); refresh(); },
    onError: (err) => Message.error(errorMessage(err).summary),
  });

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h2 className="df-page-title">审核队列</h2>
          <div className="df-page-sub">待审核的签名与印章；通过后即可在模板中选用，驳回需填写意见</div>
        </div>
      </div>

      <Tabs activeTab={tab} onChange={(k) => setTab(k as 'signature' | 'seal')}>
        <TabPane key="signature" title={`签名待审(${sigCount})`} />
        <TabPane key="seal" title={`印章待审(${sealCount})`} />
      </Tabs>

      <Table<ReviewQueueItem>
        rowKey="id"
        loading={active.isLoading}
        data={rows}
        pagination={false}
        noDataElement={<Empty description="暂无待审核项" />}
        columns={[
          {
            title: '预览',
            dataIndex: 'image_url',
            width: 110,
            render: (_: unknown, r) => (
              <div style={{ ...checkerBg, width: 72, height: 48, borderRadius: 4, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                <img src={sealImageUrl(r.resource_type === 'signature' ? 'signatures' : 'seals', r.id, r.image_url)} alt="" style={{ maxHeight: 42, maxWidth: 64, objectFit: 'contain' }} />
              </div>
            ),
          },
          { title: '名称', dataIndex: 'name', render: (v: string) => <strong>{v}</strong> },
          { title: '创建人', dataIndex: 'owner_name', width: 140, render: (v: string) => v || '—' },
          { title: '提交时间', dataIndex: 'created_at', width: 180, render: (v: string) => fmtTime(v) },
          {
            title: '操作',
            dataIndex: 'op',
            width: 180,
            render: (_: unknown, r) => (
              <span style={{ display: 'flex', gap: 6 }}>
                <Button size="mini" type="primary" loading={approveMut.isPending} onClick={() => approveMut.mutate(r)}>通过</Button>
                <Button size="mini" status="danger" onClick={() => { setRejectTarget(r); setNote(''); }}>驳回</Button>
              </span>
            ),
          },
        ]}
      />

      <Modal
        title="驳回意见"
        visible={!!rejectTarget}
        onCancel={() => setRejectTarget(null)}
        onOk={() => { if (note.trim()) rejectMut.mutate(); }}
        okText="确认驳回"
        okButtonProps={{ status: 'danger', disabled: !note.trim() ? true : undefined }}
      >
        <div style={{ marginBottom: 8 }}>将驳回：{rejectTarget?.name}</div>
        <Input.TextArea value={note} onChange={setNote} placeholder="请填写驳回原因（必填）" rows={4} />
      </Modal>
    </div>
  );
}
