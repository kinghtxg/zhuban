import { useRef, type ReactNode } from 'react';
import { Button, Card, Input, Message, Select, Space, Switch, Tag, Tooltip } from '@arco-design/web-react';
import {
  IconArrowDown,
  IconArrowUp,
  IconDelete,
  IconPlus,
} from '@arco-design/web-react/icon';
import type { TemplateVariable } from '@/types/api';
import { KIND_META } from '@/constants/enums';
import { fileToDataUrl } from '@/utils/imagefile';
import './fill-form.css';

/**
 * 把「扫描出的变量清单」渲染成一张问卷。
 *
 * 为什么要有这一层：`/scan` 已经能识别出标量、枚举、循环、条件、图片、二维码、
 * 图表七种形态，但界面上唯一的填写入口是 Monaco JSON 编辑器——那是给写代码的
 * 人用的。业务人员看到 `{"详细信息":[{"项目":"","金额":""}]}` 只会关掉页面。
 *
 * 这一层是**纯受控**的：只读 values、只抛 onChange，不碰网络、不管提交。
 * 页面负责加载/提交/校验，这里只管「长什么样、改了怎么回写」。
 */

type Values = Record<string, unknown>;

export interface FillFormProps {
  variables: TemplateVariable[];
  values: Values;
  onChange: (next: Values) => void;
  /** path -> 错误文案。path 规则：顶层用 name，循环内用 `数组名[0].字段` */
  errors?: Record<string, string>;
  /**
   * 签章类变量（sig_/seal_）：本组件不自己画选择器，交给页面弹抽屉。
   *
   * 传 `apply` 而不是「回调 + 变量名」，是因为同一个变量可能出现在循环的第 3 项、
   * 甚至嵌套循环里。只传变量名的话，页面只能改顶层同名字段——看起来保存成功，
   * 实际改的是另一个位置，或者干脆没生效。apply 已经把「该写回哪里」闭在里面了。
   */
  onPickAsset?: (v: TemplateVariable, value: unknown, apply: (next: unknown) => void) => void;
  /** 图表变量：交给页面弹数据网格，理由同上 */
  onEditChart?: (v: TemplateVariable, value: unknown, apply: (next: unknown) => void) => void;
  /** 每个签章变量当前选中的资源名，用于把 ID 显示成人看得懂的名字 */
  assetLabel?: (v: TemplateVariable, value: unknown) => string;
}

// ---------------------------------------------------------------- 取值与回写

function asObject(v: unknown): Values {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Values) : {};
}

function asArray(v: unknown): Values[] {
  return Array.isArray(v) ? v.map(asObject) : [];
}

/**
 * 单字段的初始值。新增一项、以及「从零开始填」都走这里，保证两处一致。
 *
 * 这里的形状**必须和扫描器产出的 json_schema 对齐**（见 scanner.py）：
 *   image    -> {"type":"image","src":…}          schema: object, required [type, src]
 *   richtext -> {"type":"markdown","content":…}   schema: object, required [type, content]
 *   qrcode   -> {"type":"qrcode","text":…}        schema: object, required [type, text]
 * 填成字符串会被 /validate 判「应为对象」——接口 422，而表单上看起来一切正常。
 */
export function initialValueOf(v: TemplateVariable): unknown {
  if (v.kind === 'loop' || v.type === 'array') return [];
  if (v.kind === 'condition') return false;
  if (v.kind === 'qrcode') return { type: 'qrcode', text: '', size: 20 };
  if (v.kind === 'richtext') return { type: 'markdown', content: '' };
  if (v.kind === 'chart') return undefined; // 不填就别出现在数据里，交给模板原有数据
  if (v.kind === 'image') return '';
  return '';
}

/** 生成一个空的循环项（字段全部取初始值）。 */
export function blankItem(fields: TemplateVariable[]): Values {
  const out: Values = {};
  for (const f of fields) out[f.name] = initialValueOf(f);
  return out;
}

export function isSignatureVar(v: TemplateVariable): boolean {
  return v.name.startsWith('sig_') || v.name.startsWith('seal_');
}

/** 必填判定：空串 / 空数组 / 未选条件为 true 的，都算没填。 */
export function isBlank(v: TemplateVariable, val: unknown): boolean {
  if (v.kind === 'condition') return false; // 条件只要有个布尔值就算填了
  if (v.kind === 'loop') {
    // 只看 length 会把「点了一下添加一项、但一个字没填」当成已填：
    // 用户看到一行空白，系统说填好了，生成出来是一行空表格。
    const items = asArray(val);
    if (items.length === 0) return true;
    const fields = v.fields || [];
    return items.every((it) => fields.every((f) => isBlank(f, it[f.name])));
  }
  if (v.kind === 'qrcode') {
    const t = asObject(val).text;
    return typeof t !== 'string' || !t.trim();
  }
  if (v.kind === 'richtext') {
    // 富文本存的是 {type,content}，空内容才算没填
    const obj = asObject(val);
    const content = typeof val === 'string' ? val : obj.content;
    return typeof content !== 'string' || !content.trim();
  }
  if (v.kind === 'image') {
    if (isSignatureVar(v)) return !val;
    if (typeof val === 'string') return !val.trim();
    const src = asObject(val).src;
    return typeof src !== 'string' || !src.trim();
  }
  if (typeof val === 'string') return !val.trim();
  return val === null || val === undefined;
}

/**
 * 一张空表单。
 *
 * 循环刻意给「一项空行」而不是零项：零项在页面上只显示一个「添加一项」按钮，
 * 用户不知道这张表长什么样、要填哪几列。给一行空的好处是列名直接可见，
 * 不想要可以删（`isBlank` 会把「全空的一项」判成没填，所以不会被算作已填）。
 */
export function blankForm(variables: TemplateVariable[]): Values {
  const out: Values = {};
  for (const v of variables) {
    if (v.system_resolved) continue; // 服务端注入，不该出现在数据里
    if (v.kind === 'chart') continue; // 图表不填就用模板原有数据，键都不该出现
    if (v.kind === 'loop' && (v.fields || []).length) {
      out[v.name] = [blankItem(v.fields || [])];
    } else {
      out[v.name] = initialValueOf(v);
    }
  }
  return out;
}

// ---------------------------------------------------------------- 控件

function FieldLabel({ v }: { v: TemplateVariable }) {
  return (
    <div className="df-fill-label">
      <span className="df-fill-name">{v.name}</span>
      {v.required ? <span className="df-fill-req">*</span> : null}
      {/* 类型角标只给「不是普通输入框」的字段加：标量每个都挂一个「标量」tag
          等于给整张表加噪音，而这几种形态确实需要提前知道（要弹窗、要选资源）。 */}
      {v.kind !== 'scalar' ? (
        <Tag size="small" color="gray">
          {KIND_META[v.kind]?.label || v.kind}
        </Tag>
      ) : null}
    </div>
  );
}

function ErrLine({ msg }: { msg?: string }) {
  if (!msg) return null;
  return <div className="df-fill-err">{msg}</div>;
}

interface FieldProps {
  v: TemplateVariable;
  value: unknown;
  path: string;
  errors?: Record<string, string>;
  onChange: (val: unknown) => void;
  form: FillFormProps;
}

/** 标量 / 富文本 / 图片 / 条件：单个值。循环由 LoopBlock 单独处理。 */
function ScalarField({ v, value, path, errors, onChange, form }: FieldProps) {
  const err = errors?.[path];
  // 图片字段的隐藏文件选择器。必须在所有提前 return 之前声明，
  // 否则条件/签章分支一走到就少调用一个 hook，React 会直接抛错。
  const inputRef = useRef<HTMLInputElement>(null);

  if (v.kind === 'condition') {
    return (
      <div className="df-fill-row">
        <FieldLabel v={v} />
        <Space size={8}>
          <Switch checked={Boolean(value)} onChange={(c) => onChange(c)} />
          <span className="df-fill-hint">
            {Boolean(value) ? '包含这段内容' : '不包含（模板里这段会被去掉）'}
          </span>
        </Space>
        {v.expression ? <div className="df-fill-hint">条件：{v.expression}</div> : null}
      </div>
    );
  }

  if (v.kind === 'image' && isSignatureVar(v)) {
    const label = form.assetLabel?.(v, value) || '';
    return (
      <div className="df-fill-row">
        <FieldLabel v={v} />
        <Space size={8}>
          <Button size="small" onClick={() => form.onPickAsset?.(v, value, onChange)}>
            {label ? '换一个' : v.name.startsWith('seal_') ? '选择印章' : '选择签名'}
          </Button>
          <span className={label ? 'df-fill-hint' : 'df-fill-hint df-fill-missing'}>
            {label || '未选择'}
          </span>
        </Space>
        <ErrLine msg={err} />
      </div>
    );
  }

  if (v.kind === 'image') {
    const src = typeof value === 'string' ? value : String(asObject(value).src || '');
    const isLocal = src.startsWith('data:');
    return (
      <div className="df-fill-row">
        <FieldLabel v={v} />
        <Space size={8} wrap>
          <Button size="small" onClick={() => inputRef.current?.click()}>
            {src ? '换一张' : '选择图片'}
          </Button>
          {src ? (
            <Button size="small" type="text" status="danger" onClick={() => onChange('')}>
              清除
            </Button>
          ) : null}
          <span className={src ? 'df-fill-hint' : 'df-fill-hint df-fill-missing'}>
            {src ? (isLocal ? '已选本地图片' : src.slice(0, 48)) : '未选择图片'}
          </span>
        </Space>
        <input
          ref={inputRef}
          type="file"
          accept="image/*"
          style={{ display: 'none' }}
          onChange={(e) => {
            const f = e.target.files?.[0];
            e.target.value = ''; // 允许连续选同一个文件
            if (!f) return;
            fileToDataUrl(f)
              .then((dataUrl) =>
                onChange(
                  typeof value === 'object' && value !== null
                    ? { ...asObject(value), src: dataUrl }
                    : // 不给宽高：渲染服务会按图片自身 DPI 折算物理尺寸再钳制上限，
                      // 比这里瞎猜一个毫米数靠谱
                      { type: 'image', src: dataUrl },
                ),
              )
              .catch((err: unknown) => Message.error(err instanceof Error ? err.message : '图片读取失败'));
          }}
        />
        {src ? <img className="df-fill-thumb" src={src} alt="" /> : null}
        {/* 已经选了本地图就不再用地址框占位置；反过来，值本来就是 http 地址时
            让人能直接改地址——自动化调用常传外链。 */}
        {!isLocal ? (
          <Input
            placeholder="也可以填图片地址（https://…）"
            value={typeof value === 'string' ? value : src}
            onChange={(s) => onChange(s)}
            status={err ? 'error' : undefined}
          />
        ) : null}
        <ErrLine msg={err} />
      </div>
    );
  }

  if (v.kind === 'qrcode') {
    const text = String(asObject(value).text || '');
    return (
      <div className="df-fill-row">
        <FieldLabel v={v} />
        <Input
          placeholder="扫码后显示的内容（网址 / 编号 / 一段文字）"
          value={text}
          onChange={(s) =>
            onChange({
              type: 'qrcode',
              text: s,
              size: Number(asObject(value).size) || 20,
            })
          }
          status={err ? 'error' : undefined}
        />
        <ErrLine msg={err} />
      </div>
    );
  }

  if (v.kind === 'richtext') {
    // 值必须是 {type:'markdown', content:'…'}，不能是裸字符串（schema 要 object）。
    // 老草稿里可能存着字符串，这里顺手兼容一下，不然刷新页面就读不出来了。
    const obj = asObject(value);
    const content = typeof value === 'string' ? value : String(obj.content ?? '');
    const kind = typeof obj.type === 'string' && obj.type ? obj.type : 'markdown';
    return (
      <div className="df-fill-row">
        <FieldLabel v={v} />
        <Input.TextArea
          autoSize={{ minRows: 3, maxRows: 12 }}
          placeholder="可以分段填写，支持 **加粗** 等简单格式"
          value={content}
          onChange={(s) => onChange({ ...obj, type: kind, content: s })}
          status={err ? 'error' : undefined}
        />
        <ErrLine msg={err} />
      </div>
    );
  }

  // 枚举：扫描器已经把可选值带出来了，别让用户手敲。
  const opts = v.enum;
  if (Array.isArray(opts) && opts.length > 0) {
    return (
      <div className="df-fill-row">
        <FieldLabel v={v} />
        <Select
          placeholder="请选择"
          value={typeof value === 'string' && value ? value : undefined}
          onChange={(s) => onChange(s)}
          status={err ? 'error' : undefined}
        >
          {opts.map((o) => (
            <Select.Option key={o} value={o}>
              {o}
            </Select.Option>
          ))}
        </Select>
        <ErrLine msg={err} />
      </div>
    );
  }

  const wide = typeof value === 'string' && value.length > 40;
  return (
    <div className="df-fill-row">
      <FieldLabel v={v} />
      {wide ? (
        <Input.TextArea
          autoSize={{ minRows: 2, maxRows: 6 }}
          value={typeof value === 'string' ? value : ''}
          onChange={(s) => onChange(s)}
          status={err ? 'error' : undefined}
        />
      ) : (
        <Input
          placeholder="填写内容"
          value={typeof value === 'string' ? value : ''}
          onChange={(s) => onChange(s)}
          status={err ? 'error' : undefined}
        />
      )}
      <ErrLine msg={err} />
    </div>
  );
}

/** 循环块：每一项是一张卡片，可增删、可上下移；字段里若还有循环，递归下去。 */
function LoopBlock({ v, value, path, errors, onChange, form }: FieldProps) {
  const items = asArray(value);
  const fields = v.fields || [];
  const err = errors?.[path];

  const setItems = (next: Values[]) => onChange(next);

  const update = (i: number, name: string, val: unknown) => {
    const next = items.slice();
    next[i] = { ...next[i], [name]: val };
    setItems(next);
  };

  const add = () => setItems([...items, blankItem(fields)]);

  const remove = (i: number) => setItems(items.filter((_, k) => k !== i));

  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir;
    if (j < 0 || j >= items.length) return;
    const next = items.slice();
    [next[i], next[j]] = [next[j], next[i]];
    setItems(next);
  };

  return (
    <div className="df-fill-loop">
      <div className="df-fill-loop-head">
        <FieldLabel v={v} />
        <span className="df-fill-hint">
          共 {items.length} 项{items.length === 0 ? '，至少填一项' : ''}
        </span>
      </div>

      {items.map((it, i) => (
        <Card
          key={i}
          size="small"
          className="df-fill-item"
          title={
            <Space size={6}>
              <Tag size="small" color="arcoblue">
                第 {i + 1} 项
              </Tag>
            </Space>
          }
          extra={
            <Space size={2}>
              <Tooltip content="上移">
                <Button
                  size="mini"
                  type="text"
                  disabled={i === 0}
                  icon={<IconArrowUp />}
                  onClick={() => move(i, -1)}
                />
              </Tooltip>
              <Tooltip content="下移">
                <Button
                  size="mini"
                  type="text"
                  disabled={i === items.length - 1}
                  icon={<IconArrowDown />}
                  onClick={() => move(i, 1)}
                />
              </Tooltip>
              <Tooltip content="删除这一项">
                <Button
                  size="mini"
                  type="text"
                  status="danger"
                  icon={<IconDelete />}
                  onClick={() => remove(i)}
                />
              </Tooltip>
            </Space>
          }
        >
          {fields.map((f) => {
            const childPath = `${v.name}[${i}].${f.name}`;
            return f.kind === 'loop' || f.type === 'array' ? (
              <LoopBlock
                key={f.name}
                v={f}
                value={it[f.name]}
                path={childPath}
                errors={errors}
                onChange={(val) => update(i, f.name, val)}
                form={form}
              />
            ) : f.kind === 'chart' ? (
              <ChartField
                key={f.name}
                v={f}
                value={it[f.name]}
                path={childPath}
                errors={errors}
                onChange={(val) => update(i, f.name, val)}
                form={form}
              />
            ) : (
              <ScalarField
                key={f.name}
                v={f}
                value={it[f.name]}
                path={childPath}
                errors={errors}
                onChange={(val) => update(i, f.name, val)}
                form={form}
              />
            );
          })}
        </Card>
      ))}

      <Button
        long
        type="dashed"
        icon={<IconPlus />}
        onClick={add}
        style={{ marginTop: items.length ? 8 : 0 }}
      >
        添加一项
      </Button>
      <ErrLine msg={err} />
    </div>
  );
}

/** 图表：不在表单里直接编辑数值，交给已有的数据网格抽屉（它保证不增删系列）。 */
function ChartField({ v, value, path, errors, onChange, form }: FieldProps) {
  const err = errors?.[path];
  const has = value !== undefined && value !== null && value !== '';
  return (
    <div className="df-fill-row">
      <FieldLabel v={v} />
      <Space size={8}>
        <Button size="small" onClick={() => form.onEditChart?.(v, value, onChange)}>
          {has ? '修改图表数据' : '填写图表数据'}
        </Button>
        <span className={has ? 'df-fill-hint' : 'df-fill-hint df-fill-missing'}>
          {has ? '已填写（留空则用模板原有数据）' : '未填写，将沿用模板原有数据'}
        </span>
      </Space>
      {v.title ? <div className="df-fill-hint">图表：{v.title}</div> : null}
      <ErrLine msg={err} />
    </div>
  );
}

// ---------------------------------------------------------------- 入口组件

/** 分区小标题：连续的同类变量归到一节，避免长表单糊成一坨。 */
function sectionOf(v: TemplateVariable): string {
  if (v.kind === 'loop') return '明细列表';
  if (v.kind === 'condition') return '可选段落';
  if (v.kind === 'chart') return '图表数据';
  if (v.kind === 'image' || v.kind === 'richtext') return '正文与图片';
  return '基本信息';
}

/** 分区展示顺序：先填主干，再填附件与列表，最后才是可选段和图表。 */
const SECTION_ORDER = ['基本信息', '正文与图片', '明细列表', '可选段落', '图表数据'];

export default function FillForm(props: FillFormProps) {
  const { variables, values, onChange } = props;

  if (!variables.length) {
    return <div className="df-fill-hint">这份模板没有扫描出可填写的变量。</div>;
  }

  // 按 variables 的原始顺序渲染，只在「分区变化」时插一个小标题。
  // 扫描器已经把标量按文档出现顺序排好、循环跟在后面，所以这样既保持了
  // 「从上往下像在填一张表」的直觉，又不会把三种形态混在一屏里。
  // 先按分区归组，再按固定顺序渲染。
  //
  // 为什么不是「按文档顺序、变了就插标题」：能力演示模板的变量顺序是
  // 标题/二维码/图片/编号/单位/日期/… 交替出现，那样会得到
  // 基本信息-正文与图片-基本信息-正文与图片 连跳四次，看起来像排版坏了。
  // 归组之后每个分区只出现一次；组内仍按文档顺序，读起来还是跟着文档走。
  const groups = new Map<string, TemplateVariable[]>();
  for (const v of variables) {
    if (v.system_resolved) continue; // 服务端注入的签名变量不让人填
    const sec = sectionOf(v);
    const arr = groups.get(sec);
    if (arr) arr.push(v);
    else groups.set(sec, [v]);
  }

  const rows: ReactNode[] = [];
  for (const sec of SECTION_ORDER) {
    const list = groups.get(sec);
    if (!list || !list.length) continue;
    rows.push(
      // key 用分区名：归组后同名分区只出现一次
      <div className="df-fill-section" key={`sec-${sec}`}>
        {sec}
      </div>,
    );

    for (const v of list) {
      const setOne = (val: unknown) => onChange({ ...values, [v.name]: val });

      if (v.kind === 'loop' || v.type === 'array') {
        rows.push(
          <LoopBlock
            key={v.name}
            v={v}
            value={values[v.name]}
            path={v.name}
            errors={props.errors}
            onChange={setOne}
            form={props}
          />,
        );
      } else if (v.kind === 'chart') {
        rows.push(
          <ChartField
            key={v.name}
            v={v}
            value={values[v.name]}
            path={v.name}
            errors={props.errors}
            onChange={setOne}
            form={props}
          />,
        );
      } else {
        rows.push(
          <ScalarField
            key={v.name}
            v={v}
            value={values[v.name]}
            path={v.name}
            errors={props.errors}
            onChange={setOne}
            form={props}
          />,
        );
      }
    }
  }

  return <div className="df-fill-form">{rows}</div>;
}
