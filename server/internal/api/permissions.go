package api

import (
	"net/http"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
)

// requireUser 取已登录主体，未登录直接写 401。
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) *auth.Principal {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return nil
	}
	return p
}

// canEditTemplate 判定该主体能否在线编辑/保存/回滚/上传资源（PRD §3.4）：
// admin 一律可；member 仅可编辑自己创建的模板或 public 模板；private 模板仅创建者。
func canEditTemplate(p *auth.Principal, t *models.Template) bool {
	if p == nil || p.User == nil {
		return false
	}
	if p.User.IsAdmin() {
		return true
	}
	if t.CreatedBy == p.User.ID {
		return true
	}
	// public 模板：成员可编辑（PRD §3.4 member 行）。
	if t.Visibility == "public" {
		return true
	}
	return false
}

// canDeleteTemplate 判定能否删除模板。
//
// 刻意比 canEditTemplate 严：编辑公开模板是协作（PRD §3.4 允许 member 改），
// 但删除是**不可逆的破坏性操作**——把「谁都能改」顺势推成「谁都能删」，
// 等于任何一个成员都能清空全公司的模板库。所以只留 owner 与 admin。
func canDeleteTemplate(p *auth.Principal, t *models.Template) bool {
	if p == nil || p.User == nil {
		return false
	}
	return p.User.IsAdmin() || t.CreatedBy == p.User.ID
}

// canViewTemplate 判定能否查看模板资源/版本时间线（登录用户对 public 或自己的可见）。
// 当前所有登录用户都能看到模板列表，这里与现有列表口径一致：登录即可。
func canViewTemplate(p *auth.Principal, t *models.Template) bool {
	return p != nil && p.User != nil
}

// canSeeJob 判定能否查看/下载一个渲染任务及其产物。
//
// 判据是「谁发起的」，而不是「谁有权渲染」：产物里装着调用方传进来的业务数据
// （姓名、证件号、审计结论），默认只有发起人与管理员能看。
//
// 为什么列表和详情必须用同一把尺子：任务 ID 是 jb_20261026_0016 这种
// 「日期+当日序号」，肉眼就能猜到下一个。如果列表过滤了而详情没过滤，
// 那层过滤只是把入口藏起来，改个 URL 照样能拿到别人的东西——等于没做隔离。
func canSeeJob(p *auth.Principal, job *models.Job) bool {
	if p == nil || p.User == nil || job == nil {
		return false
	}
	if p.User.IsAdmin() {
		return true
	}
	return job.CreatedByUserID != "" && job.CreatedByUserID == p.User.ID
}

// canSeeDocument 判定能否查看/下载一份文档。文档没有独立的归属字段，
// 归属跟着所属任务走——文档是任务的产物，不是独立创建的实体。
func canSeeDocument(p *auth.Principal, d *models.Document, job *models.Job) bool {
	if d == nil || job == nil || d.JobID != job.ID {
		return false
	}
	return canSeeJob(p, job)
}
