import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Button, Spin, Tooltip } from '@arco-design/web-react';
import {
  IconArrowUp,
  IconFullscreen,
  IconMinus,
  IconPlus,
  IconQuestionCircle,
  IconRefresh,
} from '@arco-design/web-react/icon';
import { preview } from '@/api/endpoints';
import type { PreviewResp } from '@/types/api';
import { errorMessage } from '@/utils/format';
import { sameOriginUrl } from '@/api/client';
import EmptyState from './EmptyState';

interface Props {
  templateId: string;
  versionId?: string;
  data: unknown; // 已解析的 JSON 对象（null 表示空）
  editKey: number; // 编辑版本号，触发防抖重算
}

/**
 * 一次要多少页。
 *
 * 翻页式预览只要 1 页，所以以前写 6（其实多要了）。连续滚动**必须**一次
 * 拿到足够的页：翻到第 5 页发现后面是"没有下一页了"，那就不叫滚动预览了。
 * 上限仍由服务端 `PREVIEW_MAX_PAGES` 兜底，这里只是"想要多少"。
 */
const FETCH_PAGES = 20;

const ZOOM_MIN = 0.5;
const ZOOM_MAX = 3;
const ZOOM_STEP = 0.1;
/** 判定"当前在第几页"的判定线：页顶进入滚动区顶部这个距离内就算翻到了 */
const SPY_OFFSET = 64;

/**
 * 实时预览：连续滚动（对齐百度文库那种"一张张纸往下滑"的读法）。
 *
 * 三个决定这一版形态的事实：
 *
 * 1. **翻页式读长文档很别扭**。以前一次只显示一页、底下挂分页器，用户为了
 *    确认"第三章的表格有没有串行"要点七八次。文档预览的默认读法就是往下滑，
 *    所以这里一次性把页铺开、连续滚。
 * 2. **编辑时不能闪白**。防抖重算期间如果把旧图清掉换成 spinner，用户每敲几个字
 *    画面就白一下。改成保留旧页、整体压暗，只在标题栏转小圈。
 * 3. **缩放的基准是"适应宽度"而不是物理 100%**。预览列在 Studio 里只有 420px，
 *    按 A4 实际像素（794px）显示会直接横向溢出；文库的默认也是"铺满阅读区"。
 */
export default function PreviewPane({ templateId, versionId, data, editKey }: Props) {
  const [result, setResult] = useState<PreviewResp | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>('');
  const [zoom, setZoom] = useState(1); // 1 = 适应宽度
  const [baseWidth, setBaseWidth] = useState(0);
  const [current, setCurrent] = useState(1);
  const [bump, setBump] = useState(0);

  const scrollRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const pagesRef = useRef<(HTMLElement | null)[]>([]);
  /** 是否已经成功加载过一版预览（用来决定"要不要把滚动条拉回顶部"） */
  const loadedOnce = useRef(false);

  const dataStr = useMemo(() => {
    if (data === null || data === undefined) return '';
    try {
      return JSON.stringify(data);
    } catch {
      return '';
    }
  }, [data]);

  useEffect(() => {
    if (!dataStr) {
      setResult(null);
      setError('');
      return;
    }
    setLoading(true);
    setError('');
    const t = setTimeout(() => {
      preview({ template_id: templateId, version_id: versionId, data, max_pages: FETCH_PAGES })
        .then((r) => {
          setResult(r);
          // 只在**第一次**加载时把滚动条拉回顶部。之后每次重算都回顶部的话，
          // 用户正在看第 6 页，改一个字段就被拽回第 1 页——连续滚动预览最忌这个。
          if (!loadedOnce.current && scrollRef.current) {
            scrollRef.current.scrollTop = 0;
            loadedOnce.current = true;
          }
        })
        .catch((err) => setError(errorMessage(err).summary))
        .finally(() => setLoading(false));
    }, 1500);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataStr, editKey, bump, templateId, versionId]);

  // 「适应宽度」的基准 = 纸张列表的宽度。
  //
  // 量的是 `.df-pv-list` 而不是外层滚动区/舞台：那两个盒子里还含着舞台内边距和
  // 右侧页码轨，按它们算，纸会宽出 30 多像素、把轨道压住。列表是 flex 项，
  // 宽度由布局算法定死，不会被"纸比它宽"反向撑开——所以不会出现
  // 纸宽↑→列表↑→基准↑ 的自激循环（这正是刚才用 flex 居中时踩到的坑）。
  // ResizeObserver 而不是 window resize：左栏收展、窗口不动的场景也要跟着变。
  useEffect(() => {
    const el = listRef.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const measure = () => setBaseWidth(el.clientWidth || 0);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [result]);

  const pages = result?.pages ?? [];
  const goTo = useCallback((n: number) => {
    const el = pagesRef.current[n - 1];
    const sc = scrollRef.current;
    if (!el || !sc) return;
    const top =
      el.getBoundingClientRect().top - sc.getBoundingClientRect().top + sc.scrollTop - 8;
    sc.scrollTo({ top, behavior: 'smooth' });
  }, []);

  // 滚动时算"当前第几页"。用 getBoundingClientRect 差值而不是 offsetTop：
  // offsetTop 的参照系是最近的定位祖先，样式一改就悄悄偏掉。
  useEffect(() => {
    const sc = scrollRef.current;
    if (!sc) return;
    let raf = 0;
    const onScroll = () => {
      if (raf) return;
      raf = requestAnimationFrame(() => {
        raf = 0;
        const scTop = sc.getBoundingClientRect().top + SPY_OFFSET;
        let idx = 1;
        for (let i = 0; i < pagesRef.current.length; i++) {
          const el = pagesRef.current[i];
          if (el && el.getBoundingClientRect().top <= scTop) idx = i + 1;
        }
        setCurrent(idx);
      });
    };
    sc.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      sc.removeEventListener('scroll', onScroll);
      if (raf) cancelAnimationFrame(raf);
    };
  }, [pages.length]);

  if (!dataStr) {
    return (
      <div className="df-pv">
        <div className="df-pv-empty">
          <EmptyState
            description="JSON 为空"
            action={
              <div style={{ color: 'var(--color-text-3)', fontSize: 12 }}>
                先点击左侧「填充示例 JSON」，再改成你的真实数据。
              </div>
            }
          />
        </div>
      </div>
    );
  }

  const pageWidth = Math.max(180, Math.round((baseWidth || 380) * zoom));
  const total = result?.page_count ?? 0;
  const truncated = Boolean(result && result.returned_pages > 0 && total > result.returned_pages);

  return (
    <div className="df-pv">
      <div className="df-pv-bar">
        <div className="df-pv-bar-left">
          <Tooltip content="重新预览">
            <Button size="mini" type="text" icon={<IconRefresh />} onClick={() => setBump((k) => k + 1)} />
          </Tooltip>
          <div className="df-pv-zoom">
            <Button
              size="mini"
              type="text"
              icon={<IconMinus />}
              disabled={zoom <= ZOOM_MIN}
              onClick={() => setZoom((z) => Math.max(ZOOM_MIN, +(z - ZOOM_STEP).toFixed(2)))}
            />
            <Tooltip content="当前缩放（100% = 适应阅读区宽度）">
              <span className="df-pv-zoom-val">{Math.round(zoom * 100)}%</span>
            </Tooltip>
            <Button
              size="mini"
              type="text"
              icon={<IconPlus />}
              disabled={zoom >= ZOOM_MAX}
              onClick={() => setZoom((z) => Math.min(ZOOM_MAX, +(z + ZOOM_STEP).toFixed(2)))}
            />
          </div>
          <Tooltip content="按阅读区宽度铺满">
            <Button
              size="mini"
              type="text"
              icon={<IconFullscreen />}
              onClick={() => setZoom(1)}
            />
          </Tooltip>
          {loading && <Spin size={12} />}
        </div>
        <div className="df-pv-bar-right">
          {total > 0 && (
            <span className="df-pv-pageno">
              {Math.min(current, pages.length || 1)} / {total}
            </span>
          )}
          <Tooltip
            content="预览页带「铸版预览」水印，且最多生成前 20 页；正式生成不受限制。"
          >
            <IconQuestionCircle className="df-pv-hint" />
          </Tooltip>
          <Tooltip content="回到第一页">
            <Button
              size="mini"
              type="text"
              icon={<IconArrowUp />}
              onClick={() => goTo(1)}
            />
          </Tooltip>
        </div>
      </div>

      {error && !loading && <div className="df-pv-error">预览失败：{error}</div>}

      <div className="df-pv-scroll" ref={scrollRef}>
        {pages.length === 0 && !loading && !error && <EmptyState description="未生成预览页" />}
        {pages.length > 0 && (
          <div className={`df-pv-stage${loading ? ' is-busy' : ''}`}>
            <div className="df-pv-list" ref={listRef}>
              {pages.map((p, i) => (
                <figure
                  key={p.page}
                  className={`df-pv-page${current === i + 1 ? ' is-current' : ''}`}
                  ref={(el) => {
                    pagesRef.current[i] = el;
                  }}
                  style={{ width: pageWidth }}
                >
                  <img
                    className="df-pv-img"
                    src={sameOriginUrl(p.url)}
                    loading="lazy"
                    alt={`第 ${p.page} 页`}
                  />
                  <figcaption className="df-pv-cap">第 {p.page} 页</figcaption>
                </figure>
              ))}
              <div className="df-pv-end">
                {truncated
                  ? `预览只生成前 ${result?.returned_pages} 页（全文 ${total} 页），正式生成不受限制`
                  : '已到最后一页'}
              </div>
            </div>
            {pages.length > 1 && (
              <nav className="df-pv-rail" aria-label="页码导航">
                {pages.map((p, i) => (
                  <button
                    key={p.page}
                    type="button"
                    className={`df-pv-dot${current === i + 1 ? ' is-active' : ''}`}
                    title={`第 ${p.page} 页`}
                    aria-label={`第 ${p.page} 页`}
                    onClick={() => goTo(i + 1)}
                  />
                ))}
              </nav>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
