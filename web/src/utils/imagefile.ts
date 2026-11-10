/**
 * 本地图片 -> data: URI。
 *
 * 为什么要这么一个东西：图片变量的值最终是
 *   {"type":"image","src":"<url>"}
 * 渲染服务用 security.download_image 去取，而它接受 data: URI。于是「让用户
 * 从本机选一张图」不需要新增上传接口、不需要把内网地址暴露给渲染服务，
 * 也不用担心 SSRF 白名单——图片跟着数据一起走。
 *
 * 代价是 JSON 会变大，所以这里做了两件事：限制最长边、必要时重新编码。
 * 上限取 1.5MB 而不是服务端的 5MB：base64 会膨胀 4/3，再留一点余量，
 * 让「选了张手机原图」不会变成 422 payload_too_large。
 */

const MAX_SIDE = 1400;
const MAX_BYTES = 1.5 * 1024 * 1024;

function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(String(fr.result || ''));
    fr.onerror = () => reject(new Error('读取文件失败'));
    fr.readAsDataURL(file);
  });
}

export async function fileToDataUrl(file: File): Promise<string> {
  if (!file.type.startsWith('image/')) throw new Error('请选择图片文件（png/jpg/webp 等）');

  // 小图直接用原文件：重新编码只会掉质量，不会变小
  if (file.size <= MAX_BYTES) {
    const raw = await readAsDataUrl(file);
    // 尺寸可能仍然很大（一张 200KB 的 6000px 长图，物理尺寸会算成两米宽），
    // 所以还要看像素：超过最长边就缩放。
    const dim = await sizeOf(raw);
    if (Math.max(dim.w, dim.h) <= MAX_SIDE) return raw;
    return shrink(raw, dim);
  }

  const raw = await readAsDataUrl(file);
  return shrink(raw, await sizeOf(raw));
}

function sizeOf(src: string): Promise<{ w: number; h: number }> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve({ w: img.naturalWidth, h: img.naturalHeight });
    img.onerror = () => reject(new Error('这张图片无法解析'));
    img.src = src;
  });
}

async function shrink(src: string, dim: { w: number; h: number }): Promise<string> {
  const k = Math.min(1, MAX_SIDE / Math.max(dim.w, dim.h));
  const w = Math.max(1, Math.round(dim.w * k));
  const h = Math.max(1, Math.round(dim.h * k));

  const img = await new Promise<HTMLImageElement>((resolve, reject) => {
    const el = new Image();
    el.onload = () => resolve(el);
    el.onerror = () => reject(new Error('这张图片无法解析'));
    el.src = src;
  });

  const canvas = document.createElement('canvas');
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext('2d');
  if (!ctx) return src;
  ctx.drawImage(img, 0, 0, w, h);

  // PNG 多半是 logo / 印章，transparent 背景压成 JPEG 会变黑底，宁可大一点
  const isPng = src.startsWith('data:image/png');
  const out = isPng ? canvas.toDataURL('image/png') : canvas.toDataURL('image/jpeg', 0.86);

  if (out.length > 4 * 1024 * 1024) throw new Error('图片太大，请先压缩到 2MB 以内');
  return out;
}
