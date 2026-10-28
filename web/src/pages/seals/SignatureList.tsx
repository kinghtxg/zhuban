/**
 * 签名库列表 —— 配置层
 * 通用实现见 SealAssetList（与印章库共用同一份逻辑）
 */
import { deleteSignature, listSignatures } from '@/api/endpoints';
import { SealAssetList } from './SealAssetList';

export default function SignatureList() {
  return (
    <SealAssetList
      config={{
        kind: 'signatures',
        title: '签名库',
        subtitle: '组织级手写签名图片素材，跨模板复用；待审核签名不可用于生成',
        createLabel: '新建签名',
        emptyText: '还没有签名，上传手写图或用姓名生成',
        basePath: '/seals-center/signatures',
        queryKey: 'signatures',
        sourceLabels: { uploaded: '上传图', generated: '文字生成' },
        showSourceFilter: true,
        imageMaxWidth: '90%',
        listFn: listSignatures,
        deleteFn: deleteSignature,
      }}
    />
  );
}
