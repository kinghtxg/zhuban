package mailer

import (
	"fmt"
	"regexp"
	"strings"
)

// Attach 模式：带哪些附件。
const (
	AttachAuto = "auto" // 跟随任务 format（docx 就 docx，both 就两份）
	AttachDocx = "docx"
	AttachPDF  = "pdf"
	AttachBoth = "both"
	AttachZip  = "zip" // 只带整包 zip（多份产物时更省事）
	AttachNone = "none"
)

// EmailSpec 一次邮件投递的规格。API 入参与落库 JSON 是同一份结构，
// 避免「入参一种写法、库里另一种」导致回放时对不上。
type EmailSpec struct {
	To      []string `json:"to"`
	CC      []string `json:"cc,omitempty"`
	BCC     []string `json:"bcc,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Body    string   `json:"body,omitempty"`
	// Attach 见 Attach* 常量，空串等同 auto。
	Attach string `json:"attach,omitempty"`
	// IncludeLink 正文末尾附一条签名下载直链。nil 时按 true 处理。
	IncludeLink *bool `json:"include_link,omitempty"`
}

// WantsLink 是否要正文带下载直链。
func (e EmailSpec) WantsLink() bool { return e.IncludeLink == nil || *e.IncludeLink }

// Empty 这份规格是否等于「不发」。
func (e EmailSpec) Empty() bool {
	return len(e.To) == 0 && len(e.CC) == 0 && len(e.BCC) == 0
}

// Validate 校验规格本身（不碰配置与收件人白名单，那要等发送时才知道）。
// 只拦一定会出错或一定被滥用的写法。
func (e EmailSpec) Validate() error {
	if e.Empty() {
		return fmt.Errorf("email.to 不能为空")
	}
	if len([]rune(e.Subject)) > 200 {
		return fmt.Errorf("email.subject 过长（上限 200 字）")
	}
	if len([]rune(e.Body)) > 8000 {
		return fmt.Errorf("email.body 过长（上限 8000 字）")
	}
	switch strings.ToLower(strings.TrimSpace(e.Attach)) {
	case "", AttachAuto, AttachDocx, AttachPDF, AttachBoth, AttachZip, AttachNone:
	default:
		return fmt.Errorf("email.attach 取值非法：%s（允许 auto|docx|pdf|both|zip|none）", e.Attach)
	}
	for _, a := range append(append(append([]string{}, e.To...), e.CC...), e.BCC...) {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("email 收件人含空值")
		}
		if !strings.Contains(a, "@") {
			return fmt.Errorf("email 收件人地址不合法：%s", a)
		}
	}
	return nil
}

// placeholderRe 匹配 {{ name }} / {{name}} / {{ a.b }}。
//
// 刻意不支持 Jinja 的过滤器、循环、if：邮件主题正文只需要「把变量填进去」，
// 引入完整模板引擎会让调用方能写的东西远超预期（也远超我们愿意维护的）。
var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*)\s*\}\}`)

// Render 把 subject/body 里的 `{{ 变量 }}` 按数据填好。
//
// 变量找不到时**置空**而不是保留 `{{ 变量 }}` 原样：把占位符字面印进客户邮件
// 是最难解释的事故。找不到的变量名一并返回，方便调用方自查。
func Render(spec EmailSpec, data map[string]interface{}) (EmailSpec, []string) {
	out := spec
	var missing []string
	seen := map[string]bool{}
	repl := func(src string) string {
		return placeholderRe.ReplaceAllStringFunc(src, func(m string) string {
			path := placeholderRe.FindStringSubmatch(m)[1]
			v, ok := lookup(data, strings.Split(path, "."))
			if !ok {
				if !seen[path] {
					seen[path] = true
					missing = append(missing, path)
				}
				return ""
			}
			return fmt.Sprint(v)
		})
	}
	out.Subject = repl(spec.Subject)
	out.Body = repl(spec.Body)
	// 收件人也允许填变量——「把这份报告发给数据里的负责人邮箱」是常见用法。
	for i, a := range out.To {
		out.To[i] = repl(a)
	}
	for i, a := range out.CC {
		out.CC[i] = repl(a)
	}
	for i, a := range out.BCC {
		out.BCC[i] = repl(a)
	}
	return out, missing
}

// lookup 按点号路径取值，支持 map 与数组下标（orders.0.name）。
func lookup(data map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = data
	for _, p := range path {
		switch t := cur.(type) {
		case map[string]interface{}:
			v, ok := t[p]
			if !ok {
				return nil, false
			}
			cur = v
		case []interface{}:
			var idx int
			if _, err := fmt.Sscanf(p, "%d", &idx); err != nil || idx < 0 || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	if cur == nil {
		return nil, false
	}
	return cur, true
}
