import { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Drawer, Input, Message, Space, Tag } from '@arco-design/web-react';
import { IconDelete, IconPlus } from '@arco-design/web-react/icon';
import type { TemplateVariable } from '@/types/api';

/** 图表数据载荷：与后端 chartdata.payload_schema() 一致 */
export interface ChartPayload {
  categories: string[];
  series: { name: string; values: number[] }[];
}

interface Row {
  cat: string;
  /** 单元格保留字符串形态：用户可能正在输入 "12." 这种中间态，过早转数字会吃掉输入 */
  values: string[];
}

interface Props {
  visible: boolean;
  chart: TemplateVariable | null;
  /** JSON 里当前已有的值；为空则用模板示例值兜底 */
  value: unknown;
  onClose: () => void;
  onSave: (name: string, payload: ChartPayload) => void;
}

/**
 * 图表数据编辑器。
 *
 * 只做一件事：把「图表的数值表」做成可编辑的网格，写回 data JSON 的同名键。
 * 刻意**不允许增删系列**——系列数由模板里那张图决定，增删会让新系列失去
 * 原有配色（落到默认样式），这正是用户说"不要改样式"时最不想要的结果。
 * 数据点（分类）可以自由增删，因为那不影响样式。
 */
export default function ChartDataDrawer({ visible, chart, value, onClose, onSave }: Props) {
  const [seriesNames, setSeriesNames] = useState<string[]>([]);
  const [rows, setRows] = useState<Row[]>([]);

  const sample = useMemo(() => readPayload(value) ?? readPayload(chart?.sample), [value, chart]);

  useEffect(() => {
    if (!visible) return;
    const src = sample;
    if (!src) {
      setSeriesNames([]);
      setRows([]);
      return;
    }
    setSeriesNames(src.series.map((s) => s.name));
    setRows(
      src.categories.map((cat, i) => ({
        cat,
        values: src.series.map((s) => {
          const v = s.values[i];
          return v === undefined || v === null ? '' : String(v);
        }),
      })),
    );
  }, [visible, sample]);

  const seriesCount = Math.max(seriesNames.length, sample?.series.length ?? 0);

  const errors = useMemo(() => {
    const out: string[] = [];
    rows.forEach((r, ri) => {
      r.values.forEach((v, vi) => {
        if (v.trim() === '') {
          out.push(`第 ${ri + 1} 行「${seriesNames[vi] || `系列${vi + 1}`}」为空`);
        } else if (Number.isNaN(Number(v.replace(/,/g, '')))) {
          out.push(`第 ${ri + 1} 行「${seriesNames[vi] || `系列${vi + 1}`}」的「${v}」不是数字`);
        }
      });
    });
    if (rows.length === 0) out.push('至少需要一个数据点');
    return out;
  }, [rows, seriesNames]);

  const setCell = (ri: number, vi: number, v: string) => {
    setRows((prev) =>
      prev.map((r, i) => (i === ri ? { ...r, values: r.values.map((x, j) => (j === vi ? v : x)) } : r)),
    );
  };

  const addRow = () => {
    setRows((prev) => [...prev, { cat: `新项${prev.length + 1}`, values: seriesCount > 0 ? Array(seriesCount).fill('0') : [] }]);
  };

  const removeRow = (ri: number) => setRows((prev) => prev.filter((_, i) => i !== ri));

  const submit = () => {
    if (!chart) return;
    if (errors.length > 0) {
      Message.warning(errors[0]);
      return;
    }
    onSave(chart.name, {
      categories: rows.map((r) => r.cat),
      series: seriesNames.map((name, vi) => ({
        name,
        values: rows.map((r) => Number(r.values[vi].replace(/,/g, ''))),
      })),
    });
  };

  const kindName = chart?.chart_kind_name || '图表';

  return (
    <Drawer
      visible={visible}
      onCancel={onClose}
      width={720}
      title={
        <Space size={8}>
          <span>编辑图表数据</span>
          <Tag size="small" color="gold">
            {kindName}
          </Tag>
          <span style={{ fontSize: 12, color: 'var(--app-text-caption)', fontWeight: 400 }}>
            {chart?.title || chart?.name}
          </span>
        </Space>
      }
      footer={
        <Space>
          <Button onClick={onClose}>取消</Button>
          <Button type="primary" onClick={submit} disabled={errors.length > 0}>
            写回数据
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        style={{ marginBottom: 14 }}
        content={
          <span style={{ fontSize: 13 }}>
            只替换<strong>数值与标签</strong>。图表的配色、线型、字体、图例位置、坐标轴等全部保持模板原样。
            系列数由模板决定（当前 <strong>{seriesCount}</strong> 个），不可增删；数据点可以自由增删。
          </span>
        }
      />

      <div className="chart-grid-wrap">
        <table className="chart-grid">
          <thead>
            <tr>
              <th style={{ width: 180 }}>分类 / 坐标轴标签</th>
              {Array.from({ length: seriesCount }).map((_, vi) => (
                <th key={vi}>
                  <Input
                    size="mini"
                    value={seriesNames[vi] ?? ''}
                    placeholder={`系列 ${vi + 1}`}
                    onChange={(v) =>
                      setSeriesNames((prev) => {
                        const next = [...prev];
                        while (next.length <= vi) next.push('');
                        next[vi] = v;
                        return next;
                      })
                    }
                  />
                  <div className="chart-grid-sub">系列名（图例文字）</div>
                </th>
              ))}
              <th style={{ width: 44 }} />
            </tr>
          </thead>
          <tbody>
            {rows.map((r, ri) => (
              <tr key={ri}>
                <td>
                  <Input
                    size="mini"
                    value={r.cat}
                    onChange={(v) => setRows((prev) => prev.map((x, i) => (i === ri ? { ...x, cat: v } : x)))}
                  />
                </td>
                {Array.from({ length: seriesCount }).map((_, vi) => (
                  <td key={vi}>
                    <Input
                      size="mini"
                      value={r.values[vi] ?? ''}
                      status={r.values[vi] !== undefined && r.values[vi] !== '' && Number.isNaN(Number(r.values[vi].replace(/,/g, ''))) ? 'error' : undefined}
                      onChange={(v) => setCell(ri, vi, v)}
                    />
                  </td>
                ))}
                <td>
                  <Button
                    size="mini"
                    type="text"
                    status="danger"
                    icon={<IconDelete />}
                    disabled={rows.length <= 1}
                    onClick={() => removeRow(ri)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <Button size="small" type="outline" icon={<IconPlus />} onClick={addRow} style={{ marginTop: 10 }}>
        添加数据点
      </Button>

      {errors.length > 0 && (
        <Alert
          type="warning"
          style={{ marginTop: 12 }}
          content={
            <span style={{ fontSize: 12 }}>
              {errors.slice(0, 4).map((e, i) => (
                <div key={i}>{e}</div>
              ))}
              {errors.length > 4 ? <div>……共 {errors.length} 处</div> : null}
            </span>
          }
        />
      )}

      <div className="chart-grid-hint">
        写回后会直接落到数据 JSON 的 <code>{chart?.name}</code> 键上，随后用左侧的
        「校验数据」「生成并下载」走正常流程即可。
      </div>
    </Drawer>
  );
}

/** 把 unknown 收敛成 ChartPayload；结构不对就返回 null（用示例值兜底） */
function readPayload(v: unknown): ChartPayload | null {
  if (!v || typeof v !== 'object') return null;
  const obj = v as Record<string, unknown>;
  const seriesRaw = obj.series;
  if (!Array.isArray(seriesRaw) || seriesRaw.length === 0) return null;
  const series = seriesRaw.map((s, i) => {
    const so = (s ?? {}) as Record<string, unknown>;
    const values = Array.isArray(so.values) ? so.values : [];
    return { name: String(so.name ?? `系列${i + 1}`), values: values as number[] };
  });
  const catsRaw = obj.categories;
  const categories = Array.isArray(catsRaw)
    ? catsRaw.map((c) => String(c))
    : series[0].values.map((_, i) => `项${i + 1}`);
  return { categories, series };
}
