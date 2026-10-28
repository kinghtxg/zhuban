import { useNavigate } from 'react-router-dom';
import { Button, Card, Empty, Table, Tag } from '@arco-design/web-react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { getTemplate, listTemplates } from '@/api/endpoints';
import { KIND_META, lookupMeta } from '@/constants/enums';
import PageContainer from '@/components/PageContainer';

// 全局变量与资源（PRD §4.5）：V2 先做「按模板分组」视图，跨模板复用留二期。
// 列表接口的 current_version 只带版本号，variables 需走详情 getTemplate；
// 这里用 useQueries 并发拉每个模板详情，读 current_version.variables。
export default function ResourcesPage() {
  const navigate = useNavigate();
  const { data, isLoading } = useQuery({
    queryKey: ['templates', { all: true }],
    queryFn: () => listTemplates({ page: 1, page_size: 200 }),
  });

  const tpls = data?.items || [];

  const details = useQueries({
    queries: tpls.map((t) => ({
      queryKey: ['template', t.id],
      queryFn: () => getTemplate(t.id),
      enabled: !!t.id,
      staleTime: 60_000,
    })),
  });

  return (
    <PageContainer
      title="变量与资源"
      subtitle="按模板分组查看变量清单；点击「编辑变量与资源」进入该模板的变量说明与资源库"
    >
      {tpls.length === 0 && !isLoading ? (
        <div className="empty-block empty-surface">
          <Empty description="还没有模板，上传一个 docx 即可自动扫描变量" />
          <Button type="primary" onClick={() => navigate('/templates')}>
            去模板库
          </Button>
        </div>
      ) : (
        tpls.map((t, i) => {
          const d = details[i];
          const vars = d?.data?.current_version?.variables || [];
          return (
            <Card
              key={t.id}
              title={`${t.name}（${t.code}）`}
              className="section"
              extra={
                <Button size="small" type="primary" onClick={() => navigate(`/templates/${t.id}/variables`)}>
                  编辑变量与资源
                </Button>
              }
            >
              <Table
                rowKey="path"
                size="small"
                loading={isLoading || !!d?.isLoading}
                pagination={false}
                data={vars}
                noDataElement={<div className="empty-block empty-block-desc">该模板未扫描到变量</div>}
                columns={[
                  { title: '名称', dataIndex: 'name', width: 220, render: (v: string) => <strong>{v}</strong> },
                  {
                    title: '类型',
                    dataIndex: 'kind',
                    width: 110,
                    render: (k: string) => {
                      const m = lookupMeta(KIND_META as Record<string, { label: string; color: string }>, k);
                      return <Tag color={m.color} size="small">{m.label}</Tag>;
                    },
                  },
                  { title: '路径', dataIndex: 'path', render: (v: string) => <code style={{ fontSize: 12 }}>{v}</code> },
                  {
                    title: '必填',
                    dataIndex: 'required',
                    width: 80,
                    render: (v: boolean) => (v ? <Tag color="red" size="small">必填</Tag> : <span>—</span>),
                  },
                ]}
              />
            </Card>
          );
        })
      )}
    </PageContainer>
  );
}
