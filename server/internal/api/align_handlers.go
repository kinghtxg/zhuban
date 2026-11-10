package api

import (
	"net/http"
	"strings"
)

// 自由写作内容的体积上限（字节）。
//
// 放宽到 24MB 是因为截图走的是 data URI 内联：一张手机截图 base64 之后
// 就有 1~3MB，一篇带五六张图的报告轻松超 10MB。原来的 decodeJSON 上限
// 25MB 正好够，所以这里不需要另开一条读取通道，只做一次明确的越界判定。
const maxAlignContentBytes = 24 << 20

// handleTemplateAlign POST /templates/{id}/align
//
// 请求：{"content":"<html 或纯文本>","mime":"html"|"text"}
// 响应：渲染服务返回的对齐建议原样透传。
//
// 这条路上的定位很关键：它**只产出建议**，一个字节都不写模板、不渲染。
// 用户确认之后前端把选中的值拼成一个普通的 data 对象，还是走原来的
// /render。这样"智能识别"这个新能力对格式的风险是零——最坏的结果是建议
// 不对、用户手动改，而不是生成一份版面被撑坏的正式文档。
func (s *Server) handleTemplateAlign(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	// 取的是"读"权限而不是"编辑"权限：填表的人本来就能用这个模板，
	// 智能识别是他填表的辅助手段，不该要求他去改模板。
	_, ver, ok := s.loadTemplateAndCurrentVersion(w, r)
	if !ok {
		return
	}

	var body struct {
		Content string `json:"content"`
		Mime    string `json:"mime"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不合法", nil)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeErr(w, http.StatusBadRequest, "data_invalid", "内容不能为空", nil)
		return
	}
	if len(body.Content) > maxAlignContentBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			"内容过大（含图片请控制在 24MB 以内）", nil)
		return
	}
	// 只认这两个值。写错一个字母不静默退化成纯文本——那样会丢掉
	// 字体/字号这些识别依据，用户看到的是一份"莫名其妙对不准"的结果，
	// 却完全查不到原因。
	mime := strings.ToLower(strings.TrimSpace(body.Mime))
	if mime == "" {
		mime = "html"
	}
	if mime != "html" && mime != "text" {
		writeErr(w, http.StatusBadRequest, "invalid_param", "mime 只能是 html 或 text", nil)
		return
	}

	docx, err := readFileSafe(ver.FilePath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "模板文件不可用", nil)
		return
	}

	out, aerr := s.Renderer.Align(r.Context(), docx, body.Content, mime)
	if aerr != nil {
		s.renderErr(w, aerr)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
