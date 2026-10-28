import { useQuery } from '@tanstack/react-query';
import { getVersion } from '@/api/endpoints';

/**
 * 取当前构建版本。
 *
 * 用一个 session 级的缓存（staleTime: Infinity）：版本号在进程生命周期里是常量，
 * 每次切页面重新问一遍是纯浪费。同 key ['version'] 在更新提示那边也用过，
 * 两处共享同一份缓存。
 *
 * 拿不到时返回 undefined，调用方应当退化成「不显示版本」而不是显示 v0.0.0：
 * 一个写错的版本号比没有版本号更容易误导排查。
 */
export function useVersion() {
  const { data } = useQuery({
    queryKey: ['version'],
    queryFn: getVersion,
    staleTime: Infinity,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    retry: false,
  });
  return data;
}

/** 把版本对象格式化成界面上一行的显示文本。 */
export function formatVersion(v?: { version: string; dev: boolean; commit?: string }): string {
  if (!v) return '';
  if (v.dev) return 'dev';
  return `v${v.version}`;
}
