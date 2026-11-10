/**
 * 签章值的读取工具。
 *
 * 值有两种合法形态：
 *   "sig_xxxx"                                          老形态：直接引用资源
 *   {"$sig":"sig_xxxx","with_timestamp":true,...}        参数对象：出图时现场生成
 *
 * 只认字符串会让参数对象在界面上显示成「未选择」——用户明明填了，界面说没填。
 * 单独放一个文件是因为工作台（Studio）和录入页（FillForm）都要用；从 Studio 里
 * import 会顺带把 Monaco 编辑器拉进录入页的包，那是几百 KB 的无谓开销。
 */

/** 从数据里的值取出资源 ID。 */
export function assetIdOf(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    const id = o.$sig ?? o.$seal;
    if (typeof id === 'string') return id;
  }
  return '';
}

/** 值里是否带了现场出图参数（时间/字体/署名）。用来在界面上标一下，别让人忘了自己开过。 */
export function renderParamsOf(v: unknown): string[] {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return [];
  const o = v as Record<string, unknown>;
  const out: string[] = [];
  if (o.with_timestamp || o.timestamp) out.push('带签署时间');
  if (o.font) out.push('指定字体');
  if (o.signer_name) out.push('指定署名');
  if (o.width_mm || o.height_mm) out.push('指定尺寸');
  return out;
}

/**
 * 换签名时只换资源，保留现场出图参数。
 *
 * 直接覆盖成 `"sig_new"` 会把用户之前开过的「印上签署时间」一起清掉——
 * 这种丢失在界面上看不见，生成出来才发现时间没了。
 */
export function replaceAsset(prev: unknown, assetId: string): unknown {
  if (prev && typeof prev === 'object' && !Array.isArray(prev) && ('$sig' in prev || '$seal' in prev)) {
    const marker = '$seal' in prev ? '$seal' : '$sig';
    return { ...(prev as Record<string, unknown>), [marker]: assetId };
  }
  return assetId;
}
