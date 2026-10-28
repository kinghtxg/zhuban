// Package fname 实现契约 §5 的文件名规则：
// 顶层变量替换 + 非法字符清洗 + 截断 + 兜底命名。
package fname

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var tplVar = regexp.MustCompile(`\{\{\s*([\w一-龥]+)\s*\}\}`)

var illegal = strings.NewReplacer(
	"\\", "_", "/", "_", ":", "_", "*", "_", "?", "_",
	"\"", "_", "<", "_", ">", "_", "|", "_",
)

// Render 依据规则生成不含扩展名的文件名主体。
//   - rule:   文件名规则（如 "{{项目编号}}_审计报告"），可为空
//   - record: 该条记录数据（只取顶层变量）
//   - index:  记录序号
//   - code:   模板编号（兜底用）
func Render(rule string, record map[string]interface{}, index int, code string) string {
	out := rule
	switch {
	case strings.TrimSpace(out) != "":
		// 顶层变量替换；取不到值用 record_index 占位。
		out = tplVar.ReplaceAllStringFunc(out, func(m string) string {
			name := strings.TrimSpace(tplVar.FindStringSubmatch(m)[1])
			if record != nil {
				if v, ok := record[name]; ok && v != nil {
					s := fmt.Sprintf("%v", v)
					if s != "" && s != "<nil>" {
						return s
					}
				}
			}
			return strconv.Itoa(index)
		})
	}

	out = clean(out)
	if out == "" {
		out = fmt.Sprintf("%s_%03d", code, index)
	}
	// 截断到 120 字符（按 rune 截断避免截断多字节中文）。
	r := []rune(out)
	if len(r) > 120 {
		out = strings.TrimSpace(string(r[:120]))
	}
	return out
}

// clean 清洗 Windows/Unix 非法字符与控制字符。
func clean(s string) string {
	var b strings.Builder
	for _, ch := range s {
		if ch < 0x20 || ch == 0x7f {
			continue
		}
		b.WriteRune(ch)
	}
	out := illegal.Replace(b.String())
	return strings.TrimSpace(strings.Trim(out, ". "))
}

// WithExt 给文件名拼接扩展名。
func WithExt(base, format string) string {
	switch format {
	case "pdf":
		return base + ".pdf"
	case "both", "docx", "":
		return base + ".docx"
	default:
		return base + "." + format
	}
}
