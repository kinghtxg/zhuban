/**
 * 签名详情 —— 配置层
 * 通用实现见 SealAssetDetail（与印章详情共用同一份逻辑）
 */
import { deleteSignature, getSignature } from '@/api/endpoints';
import { fmtTime } from '@/utils/format';
import { SealAssetDetail } from './SealAssetDetail';

export default function SignatureDetail() {
  return (
    <SealAssetDetail
      config={{
        kind: 'signatures',
        queryKey: 'signature',
        basePath: '/seals-center/signatures',
        resourceType: 'signature',
        assetLabel: '签名',
        sourceLabels: { uploaded: '上传图', generated: '文字生成' },
        imageMaxHeight: 240,
        imageMaxWidth: '90%',
        getFn: getSignature,
        deleteFn: deleteSignature,
        paramRows: (s) => [
          { label: '来源', value: s.source === 'generated' ? '文字生成' : '上传图' },
          { label: '被签人', value: s.signer_name || '—' },
          { label: '尺寸', value: s.width_mm ? `${s.width_mm} × ${s.height_mm || '—'} mm` : '—' },
          { label: '被引用文档数', value: s.ref_count != null ? `${s.ref_count}` : '—' },
          { label: '创建时间', value: fmtTime(s.created_at) },
        ],
      }}
    />
  );
}
