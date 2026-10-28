/**
 * 印章库列表 —— 配置层
 * 通用实现见 SealAssetList（与签名库共用同一份逻辑）
 */
import { deleteSeal, listSeals } from '@/api/endpoints';
import { SealAssetList } from './SealAssetList';

export default function SealList() {
  return (
    <SealAssetList
      config={{
        kind: 'seals',
        title: '印章库',
        subtitle: '组织级红色印章素材，参数化生成或上传图，跨模板复用',
        createLabel: '新建印章',
        emptyText: '还没有印章，上传印章图或参数化生成',
        basePath: '/seals-center/seals',
        queryKey: 'seals',
        sourceLabels: { uploaded: '上传图', generated: '参数生成' },
        showSourceFilter: false,
        imageMaxWidth: 120,
        listFn: listSeals,
        deleteFn: deleteSeal,
      }}
    />
  );
}
