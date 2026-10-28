/**
 * 印章详情 —— 配置层
 * 通用实现见 SealAssetDetail（与签名详情共用同一份逻辑）
 */
import type { ReactNode } from 'react';
import { deleteSeal, getSeal } from '@/api/endpoints';
import { fmtTime } from '@/utils/format';
import { SealAssetDetail } from './SealAssetDetail';

export default function SealDetail() {
  return (
    <SealAssetDetail
      config={{
        kind: 'seals',
        queryKey: 'seal',
        basePath: '/seals-center/seals',
        resourceType: 'seal',
        assetLabel: '印章',
        sourceLabels: { uploaded: '上传图', generated: '参数生成' },
        imageMaxHeight: 230,
        imageMaxWidth: '70%',
        getFn: getSeal,
        deleteFn: deleteSeal,
        paramRows: (s) => {
          const generated = s.source === 'generated';
          const rows: { label: string; value: ReactNode }[] = [
            { label: '来源', value: generated ? '参数生成' : '上传图' },
          ];
          // 上传的印章没有几何参数：这几行以前一律显示「—」，看着像「数据丢了」，
          // 但上传图本来就没有半径/形状/星这些概念，后端也没存。
          // 不出这几行，比出一排「—」更诚实。
          if (generated) {
            rows.push(
              { label: '单位名称', value: s.unit_name || '—' },
              { label: '印章名称', value: s.seal_name || '—' },
              { label: '形状', value: s.shape === 'ellipse' ? '椭圆' : '圆形' },
              // 尺寸与半径都从 width_mm 来：后端创建时把表单里的半径换算成
              // width = radius*2 存下来，所以这两个值永远一致。
              // 椭圆还有高度，圆形固定为 0（不是 40×0 这种怪值）。
              {
                label: '尺寸',
                value:
                  s.shape === 'ellipse' && s.height_mm
                    ? `${s.width_mm ?? '—'} × ${s.height_mm} mm`
                    : `${s.width_mm ?? '—'} mm（直径）`,
              },
              { label: '半径', value: s.radius_mm ? `${s.radius_mm} mm` : '—' },
              { label: '五角星', value: s.star === false ? '无' : '有' },
            );
          }
          rows.push(
            { label: '被引用文档数', value: s.ref_count != null ? `${s.ref_count}` : '—' },
            { label: '创建时间', value: fmtTime(s.created_at) },
          );
          return rows;
        },
      }}
    />
  );
}
