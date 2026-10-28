package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"docforge/server/internal/render"

	"github.com/go-chi/chi/v5"
)

// 字体上传：管理员把 .ttf/.ttc/.otf/.otc 放进共享目录，再让渲染服务刷新
// fontconfig 缓存，LibreOffice 就能在文档里用上这个字体。
//
// 为什么必须落在「Go 可写、render 只读」的共享目录：
// 镜像里的 /usr/share/fonts/custom 是仓库字体的只读挂载，动态导入写不进去。
// 所以导入走另一条挂载（宿主 ./volumes/fonts → render 容器的
// /usr/share/fonts/imported），既不动既有字体，也能单独删掉导入的。
const (
	maxFontMB     = 32
	maxFontCount  = 200
	fontNameRe    = `^[A-Za-z0-9][A-Za-z0-9 ._\-]*$`
	fontExtsAllow = ".ttf,.ttc,.otf,.otc"
)

var fontNameCheck = regexp.MustCompile(fontNameRe)

// fontMagicOK 校验字体文件魔数，挡住「改个扩展名就传上来」的情况。
//
// 这不是为了防御恶意字体（那是 freetype 的战场，且管理员上传本就属于可信操作），
// 而是为了在第一时间告诉运维「你这个文件不是字体」——比让它静静躺在目录里、
// 等某次渲染才发现缺字体要好得多。
func fontMagicOK(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch {
	case head[0] == 0x00 && head[1] == 0x01 && head[2] == 0x00 && head[3] == 0x00: // TrueType
		return true
	case string(head[:4]) == "OTTO": // CFF
		return true
	case string(head[:4]) == "ttcf": // TrueType Collection
		return true
	case string(head[:4]) == "true", string(head[:4]) == "typ1": // 老 Mac
		return true
	}
	return false
}

func (s *Server) fontDir() string { return filepath.Join(s.Cfg.DataDir, "fonts") }

// handleUploadFont POST /fonts（admin）
func (s *Server) handleUploadFont(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	if err := r.ParseMultipartForm(int64(maxFontMB) << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("上传内容过大或格式不是 multipart（单字体上限 %dMB）", maxFontMB), nil)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "缺少 file 字段", nil)
		return
	}
	defer file.Close()

	if hdr.Size > int64(maxFontMB)<<20 {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("字体文件超过 %dMB", maxFontMB), nil)
		return
	}

	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if !strings.Contains(fontExtsAllow, ext) || ext == "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid",
			"只支持 "+fontExtsAllow+" 格式", []render.FieldError{{Path: "file", Message: "不支持的扩展名 " + ext}})
		return
	}

	// hdr.Filename 已经过标准库 filepath.Base 处理（part.FileName 的文档行为），
	// ../ 在到达这里之前就被剥掉了。这里再 Base 一次是纵深防御——万一以后换掉
	// multipart 解析（或自己解析），这一行仍然兜住。
	base := filepath.Base(hdr.Filename)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if !fontNameCheck.MatchString(stem) {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid",
			"字体文件名只能包含字母、数字、空格、点、下划线和连字符", nil)
		return
	}

	dir := s.fontDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "字体目录不可用："+err.Error(), nil)
		return
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) >= maxFontCount {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid",
			fmt.Sprintf("已导入字体数量达到上限（%d 个），请先删除不用的", maxFontCount), nil)
		return
	}

	// 先读头部校验魔数，再整体落盘（避免把非字体文件写进去）
	head := make([]byte, 8)
	n, rerr := io.ReadFull(file, head)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "读取文件失败", nil)
		return
	}
	if !fontMagicOK(head[:n]) {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid",
			"不是有效的字体文件（未通过魔数校验）", []render.FieldError{{Path: "file", Message: "魔数不符"}})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "读取文件失败", nil)
		return
	}

	// 不覆盖同名文件：重名时加 -2/-3 后缀。静默覆盖会让运维以为换成功了，
	// 实际旧的还在（或反过来），排查时非常难对上。
	target := uniquePath(dir, stem, ext)
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "写入字体失败："+err.Error(), nil)
		return
	}
	written, cerr := io.Copy(dst, io.LimitReader(file, int64(maxFontMB)<<20))
	dst.Close()
	if cerr != nil {
		os.Remove(target)
		writeErr(w, http.StatusInternalServerError, "internal_error", "写入字体失败："+cerr.Error(), nil)
		return
	}

	name := filepath.Base(target)
	// 落盘即成功。刷新失败不撤销文件——文件已经在，只是这次没立刻生效，
	// 返回里带 reload 结果让前端如实提示（撤销会让用户以为上传失败、反复重传）。
	reload := map[string]interface{}{"ok": false, "hint": "字体已保存，但刷新缓存失败，可稍后重试或重启渲染服务"}
	if res, rerr := s.Renderer.ReloadFonts(r.Context()); rerr != nil {
		reload["error"] = rerr.Error()
	} else {
		reload = map[string]interface{}{"ok": res.OK, "elapsed_ms": res.ElapsedMS, "errors": res.Errors}
	}

	s.auditReq(r, p, "font.upload", "font", name, map[string]interface{}{
		"size": written, "ext": ext,
	})
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"ok": true, "name": name, "size": written, "reload": reload,
	})
}

func uniquePath(dir, stem, ext string) string {
	cand := filepath.Join(dir, stem+ext)
	if _, err := os.Stat(cand); os.IsNotExist(err) {
		return cand
	}
	for i := 2; i < 500; i++ {
		cand = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, os.Getpid(), ext))
}

// handleListFonts GET /fonts
//
// 两个来源合并成一个列表：内置签名风字体（有 id，可用于签名生成）
// + fontconfig 已识别的字体（文档渲染可用）。前端只需要一份"系统里有什么字体"。
func (s *Server) handleListFonts(w http.ResponseWriter, r *http.Request) {
	// 与上传/删除同一权限：这个接口回答的是「这台机器上装了哪些字体」，
	// 属于部署信息。不能写但能看只是半个门——而且前端只有后台字体管理页用它，
	// 没有非管理员的使用场景。签名生成用的内置字形走 /render/fonts，不受影响。
	if s.requireAdmin(w, r) == nil {
		return
	}
	builtin, berr := s.Renderer.Fonts(r.Context())
	installed, ierr := s.Renderer.InstalledFonts(r.Context())

	builtinItems := make([]map[string]interface{}, 0, len(builtin))
	for _, f := range builtin {
		builtinItems = append(builtinItems, map[string]interface{}{
			"id": f.ID, "name": f.Name,
		})
	}

	imported := make([]map[string]interface{}, 0)
	others := make([]map[string]interface{}, 0)
	for _, f := range installed {
		item := map[string]interface{}{
			"file": f.File, "family": f.Family, "style": f.Style,
		}
		if f.Imported {
			imported = append(imported, item)
		} else {
			others = append(others, item)
		}
	}
	sort.Slice(imported, func(i, j int) bool {
		return imported[i]["file"].(string) < imported[j]["file"].(string)
	})

	resp := map[string]interface{}{
		"builtin":  builtinItems,
		"imported": imported,
		"system":   len(others),
	}
	if berr != nil {
		resp["builtin_error"] = berr.Error()
	}
	if ierr != nil {
		resp["system_error"] = ierr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDeleteFont DELETE /fonts/{name}（admin）
// 只允许删导入目录里的字体；镜像自带的系统字体删不掉（也不该删）。
func (s *Server) handleDeleteFont(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "字体名非法", nil)
		return
	}
	target := filepath.Join(s.fontDir(), name)
	if _, err := os.Stat(target); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "字体不存在", nil)
		return
	}
	if err := os.Remove(target); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "删除失败："+err.Error(), nil)
		return
	}
	reload := map[string]interface{}{"ok": false}
	if res, rerr := s.Renderer.ReloadFonts(r.Context()); rerr == nil {
		reload = map[string]interface{}{"ok": res.OK, "elapsed_ms": res.ElapsedMS}
	}
	s.auditReq(r, p, "font.delete", "font", name, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "reload": reload})
}
