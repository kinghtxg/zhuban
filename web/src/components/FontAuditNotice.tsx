import { Button, Spin, Tag } from '@arco-design/web-react';
import {
  IconCheckCircleFill,
  IconExclamationCircleFill,
  IconInfoCircleFill,
  IconSettings,
} from '@arco-design/web-react/icon';
import type { CSSProperties } from 'react';
import { useQuery } from '@tanstack/react-query';
import { getMe } from '@/api/endpoints';
import type { FontAudit, FontAuditEntry } from '@/types/api';
import './font-audit.css';

interface Props {
  /** 体检报告；null / undefined 表示这个版本没有结论（老数据），此时什么都不显示 */
  audit?: FontAudit | null;
  loading?: boolean;
  /**
   * compact：上传向导里用。只列缺失项，不铺开「已装好的」那些。
   * 非 compact：模板详情页用，可以展开全部字体。
   */
  compact?: boolean;
  /** 传了就显示「去导入字体」按钮（管理员可见） */
  onManageFonts?: () => void;
  /** 传了就显示「重新检查」：导入字体后回来点一下，不用重新上传文件 */
  onRecheck?: () => void;
  /** 重新检查进行中：按钮转圈，但**不要**把已有结论换成 loading 占位 */
  rechecking?: boolean;
  style?: CSSProperties;
}

/** 中文族名走这个判断，决定「换字形」到底有多要紧 */
const SEVERE_HINT = '汉字可能显示不出来';

function partText(parts: string[]): string {
  return parts.length ? parts.join(' · ') : '模板';
}

/**
 * 中文 / 西文各用什么字体。
 *
 * 为什么单独一块：中英混排的段落里，汉字和西文**本来是两种字体**
 * （汉字 eastAsia、西文 ascii/hAnsi）。以前把四个槽位拉成一张不分角色的
 * 族名清单，用户看得见「仿宋_GB2312」「Times New Roman」两行，
 * 却不知道哪个管中文——这不是他要的答案。
 */
function RoleBlock({ audit }: { audit: FontAudit }) {
  const roles = audit.roles || {};
  const keys = ['eastAsia', 'latin'].filter((k) => roles[k] && roles[k].fonts.length);
  if (!keys.length) return null;
  return (
    <div className="fa-roles">
      {keys.map((k) => (
        <div className="fa-role-line" key={k}>
          <span className="fa-role-tag">{roles[k].label}</span>
          <span className="fa-role-fams">
            {roles[k].fonts.map((f) => (
              <span
                key={f.family}
                className={
                  f.status === 'missing' || f.status === 'fallback'
                    ? 'fa-role-fam is-bad'
                    : 'fa-role-fam'
                }
                title={
                  f.status === 'fallback' || f.status === 'missing'
                    ? `本机没有，导出会变成 ${f.resolved || '没有可替代的中文字体'}`
                    : `本机已装：${f.family}`
                }
              >
                {f.family}
                <em>{f.segments} 段</em>
              </span>
            ))}
          </span>
        </div>
      ))}
    </div>
  );
}

/** 逐段明细：这一段的中文是什么、西文是什么 */
function ParagraphTable({ audit }: { audit: FontAudit }) {
  const rows = (audit.paragraphs || []).slice(0, 20);
  if (!rows.length) return null;
  return (
    <details className="fa-paras">
      <summary>逐段看：每段的中文与西文各用哪支字体（示例 {rows.length} 段）</summary>
      <ul>
        {rows.map((p) => (
          <li key={p.part + p.index}>
            <span className="fa-para-text">{p.text || '（空段）'}</span>
            <span className="fa-para-f">
              <b>中</b>
              {p.cjk ? p.cjk.family : '—'}
            </span>
            <span className="fa-para-f">
              <b>西</b>
              {p.latin ? p.latin.family : '—'}
            </span>
          </li>
        ))}
      </ul>
    </details>
  );
}

/** 脏族名（WPS 把主题槽位的界面名写进 rFonts）单独说一句，不要混进缺失清单 */
function SuspectNotice({ audit }: { audit: FontAudit }) {
  const list = audit.suspects || [];
  if (!list.length) return null;
  return (
    <div className="fa-suspect">
      另有 {list.length} 个族名看着不像真字体（
      {list.map((s) => s.family).join('、')}
      ），已按括号前的名字判断，不影响结论。
    </div>
  );
}

/** 一行：族名 → 实际会用到的字体 + 影响范围 */
function Row({ f }: { f: FontAuditEntry }) {
  const danger = f.status === 'missing';
  return (
    <li className="fa-row">
      <span className="fa-name">{f.family}</span>
      <Tag size="small" color={danger ? 'red' : 'orange'}>
        {danger ? SEVERE_HINT : '字形会变'}
      </Tag>
      <span className="fa-arrow">→</span>
      <span className="fa-resolved">{f.resolved || '没有可替代的中文字体'}</span>
      <span className="fa-parts">{partText(f.parts)}</span>
    </li>
  );
}

/**
 * 模板用字体检提示。
 *
 * 这段话的目的是让用户在**还能改的时候**知道一件事：
 * 你模板里写的仿宋_GB2312，本机没有，导出的 PDF 里会显示成宋体。
 *
 * 所以文案不是「警告：缺少字体」，而是「缺哪几个 / 会变成什么样 / 你有两条路」。
 * 缺字体本身不阻塞上传，也不需要阻塞——多数情况下接受替换是可以的，
 * 只有公文、合同这类对字形有硬要求的场景才必须补齐。这个判断应该交给用户，
 * 我们要做的是把事实摆清楚。
 */
export default function FontAuditNotice({
  audit,
  loading,
  compact,
  onManageFonts,
  onRecheck,
  rechecking,
  style,
}: Props) {
  // 只有管理员能进字体管理。成员看到按钮点进去会被 403，不如直接不给。
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: getMe, retry: false, staleTime: 60_000 });
  const canManage = me?.user.role === 'admin';

  if (loading) {
    return (
      <div className="fa-panel" style={style}>
        <div className="fa-quiet">
          <Spin size={12} style={{ marginRight: 6 }} />
          正在检查模板里用到的字体…
        </div>
      </div>
    );
  }

  // 没有结论：老版本没有这个字段。**不要**用「未检查」占位——
  // 一句没有信息量的灰字只会让人以为系统有问题。
  if (!audit) return null;

  // 体检自身失败：如实说，并明确「不影响使用」，
  // 否则用户会把「检查没做成」当成「模板有问题」。
  if (!audit.ok) {
    return (
      <div className="fa-panel" style={style}>
        <div className="fa-quiet">
          本次没能完成字体检查{audit.error ? `（${audit.error}）` : ''}，不影响模板使用。
        </div>
      </div>
    );
  }

  const bad = audit.fonts.filter((f) => f.status === 'fallback' || f.status === 'missing');
  const total = audit.checked;

  if (bad.length === 0) {
    return (
      <div className="fa-panel" style={style}>
        <span className="fa-ok">
          <IconCheckCircleFill style={{ marginRight: 6 }} />
          字体检查通过：模板用到的 {total} 种字体本机都有，导出的字形与你在 Word 里看到的一致。
        </span>
        <RoleBlock audit={audit} />
        {!compact && <ParagraphTable audit={audit} />}
        <SuspectNotice audit={audit} />
      </div>
    );
  }

  const hasSevere = audit.severe.length > 0;
  const shown = compact ? bad.slice(0, 6) : bad;

  return (
    <div className={`fa-panel ${hasSevere ? 'is-danger' : 'is-warn'}`} style={style}>
      <div className="fa-head">
        {hasSevere ? <IconExclamationCircleFill /> : <IconInfoCircleFill />}
        有 {bad.length} 种字体本机没有，导出时会被替换
        <span className="fa-count">
          共检查 {total} 种
        </span>
      </div>

      <ul className="fa-list">
        {shown.map((f) => (
          <Row key={f.family} f={f} />
        ))}
      </ul>
      {compact && bad.length > shown.length && (
        <div className="fa-quiet" style={{ marginTop: 6 }}>
          还有 {bad.length - shown.length} 种，创建后可在模板详情里看全。
        </div>
      )}

      {/* 有缺失时同样要说清「中文是什么、西文是什么」：
          上面那张表只列坏的那几支，用户照样看不出汉字到底落到哪支上。 */}
      <RoleBlock audit={audit} />
      {!compact && <ParagraphTable audit={audit} />}
      <SuspectNotice audit={audit} />

      <div className="fa-foot">
        两条路，任选其一：
        <ol>
          <li>
            <b>补齐字体</b>：把字体文件（.ttf/.ttc/.otf）导入后台「字体管理」，导出就能还原成模板里写的字形。
          </li>
          <li>
            <b>接受替换</b>：上面列的就是导出后会用的字体。模板本身不用改，只是字形与你在 Word 里看到的不同。
          </li>
        </ol>
        {(onManageFonts || onRecheck) && (
          <div className="fa-act">
            {onManageFonts && canManage && (
              <Button size="mini" icon={<IconSettings />} onClick={onManageFonts}>
                去导入字体
              </Button>
            )}
            {onRecheck && (
              <Button size="mini" type="text" loading={rechecking} onClick={onRecheck} style={{ marginLeft: 8 }}>
                重新检查
              </Button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
