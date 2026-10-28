// 前端共享校验规则（与后端 CONTRACT §4.2 一致）
// 密码：至少 8 位且同时含字母和数字
export function validatePassword(pw: string): string | null {
  if (pw.length < 8) return '密码至少 8 位';
  if (!/[A-Za-z]/.test(pw)) return '密码必须包含字母';
  if (!/\d/.test(pw)) return '密码必须包含数字';
  return null;
}

// username：3-64 位，字母数字/下划线/连字符
export function validateUsername(name: string): string | null {
  if (!/^[A-Za-z0-9_-]{3,64}$/.test(name)) return '用户名需 3-64 位，仅限字母、数字、下划线、连字符';
  return null;
}
