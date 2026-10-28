package api

import (
	"encoding/json"
	"net/http"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
	"docforge/server/internal/render"

	"github.com/go-chi/chi/v5"
)

// requireAdmin 返回当前 admin 主体；非 admin 直接写 403 并返回 nil。
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *auth.Principal {
	p := auth.PrincipalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录", nil)
		return nil
	}
	if !p.User.IsAdmin() {
		writeErr(w, http.StatusForbidden, "forbidden", "仅管理员可执行此操作", nil)
		return nil
	}
	return p
}

// handleListUsers 管理员用户列表。
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	q := s.DB.Model(&models.User{})
	if st := r.URL.Query().Get("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if src := r.URL.Query().Get("source"); src != "" {
		q = q.Where("source = ?", src)
	}
	if kw := r.URL.Query().Get("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("username LIKE ? OR display_name LIKE ? OR email LIKE ?", like, like, like)
	}
	pg := getPage(r)
	var total int64
	q.Count(&total)
	var users []models.User
	q.Order("created_at DESC").Offset((pg.Page - 1) * pg.PageSize).Limit(pg.PageSize).Find(&users)
	var pending int64
	s.DB.Model(&models.User{}).Where("status = ?", "pending").Count(&pending)
	items := make([]map[string]interface{}, 0, len(users))
	for i := range users {
		items = append(items, publicUserFull(&users[i]))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": total, "pending_count": pending})
}

// handleCreateUser 管理员新建用户（active）。
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	var body struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Role        string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	if msg := auth.ValidateUsername(body.Username); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "username", Message: msg}})
		return
	}
	if msg := auth.ValidatePasswordRule(body.Password); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "password", Message: msg}})
		return
	}
	role := body.Role
	if role != "admin" {
		role = "member"
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	u := models.User{
		ID: "u_" + randID(), Username: body.Username, PasswordHash: hash,
		DisplayName: orDefault(body.DisplayName, body.Username), Email: body.Email,
		Role: role, Status: "active", Source: "local",
	}
	if err := s.DB.Create(&u).Error; err != nil {
		writeErr(w, http.StatusConflict, "conflict", "用户名已被占用", nil)
		return
	}
	s.audit(p, "user.create", "user", u.ID, map[string]interface{}{"username": u.Username, "role": u.Role})
	writeJSON(w, http.StatusCreated, publicUserFull(&u))
}

// loadTargetUser 取待操作用户，禁止操作自己。
func (s *Server) loadTargetUser(w http.ResponseWriter, r *http.Request, self *models.User) (*models.User, bool) {
	id := chi.URLParam(r, "id")
	if id == self.ID {
		writeErr(w, http.StatusBadRequest, "bad_request", "不能对自己执行此操作", nil)
		return nil, false
	}
	var u models.User
	if err := s.DB.First(&u, "id = ?", id).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "用户不存在", nil)
		return nil, false
	}
	return &u, true
}

func (s *Server) handleApproveUser(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	u, ok := s.loadTargetUser(w, r, p.User)
	if !ok {
		return
	}
	s.DB.Model(u).Update("status", "active")
	s.audit(p, "user.approve", "user", u.ID, map[string]interface{}{"username": u.Username})
	writeJSON(w, http.StatusOK, publicUserFull(u))
}

func (s *Server) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	u, ok := s.loadTargetUser(w, r, p.User)
	if !ok {
		return
	}
	s.DB.Model(u).Update("status", "disabled")
	// 停用只改数据库状态的话，已经登录的浏览器还能继续用到 cookie 过期——
	// 每次认证都会查 status，所以下一跳就会被拦；但那枚 cookie 仍然是「有效凭证」，
	// 一旦账号被重新启用，旧的登录态会跟着活过来。这里直接清掉更干净。
	s.Auth.RevokeAllSessions(u.ID)
	s.audit(p, "user.disable", "user", u.ID, map[string]interface{}{"username": u.Username})
	writeJSON(w, http.StatusOK, publicUserFull(u))
}

func (s *Server) handleEnableUser(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	id := chi.URLParam(r, "id")
	var u models.User
	if err := s.DB.First(&u, "id = ?", id).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "用户不存在", nil)
		return
	}
	s.DB.Model(&u).Update("status", "active")
	s.audit(p, "user.enable", "user", u.ID, map[string]interface{}{"username": u.Username})
	writeJSON(w, http.StatusOK, publicUserFull(&u))
}

func (s *Server) handleSetRole(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	u, ok := s.loadTargetUser(w, r, p.User)
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Role != "admin" && body.Role != "member" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", "role 只能是 admin 或 member", nil)
		return
	}
	s.DB.Model(u).Update("role", body.Role)
	s.audit(p, "user.role", "user", u.ID, map[string]interface{}{"username": u.Username, "role": body.Role})
	writeJSON(w, http.StatusOK, publicUserFull(u))
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	p := s.requireAdmin(w, r)
	if p == nil {
		return
	}
	id := chi.URLParam(r, "id")
	var u models.User
	if err := s.DB.First(&u, "id = ?", id).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "用户不存在", nil)
		return
	}
	var body struct {
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体不是合法 JSON", nil)
		return
	}
	if msg := auth.ValidatePasswordRule(body.NewPassword); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, "data_invalid", msg, []render.FieldError{{Path: "new_password", Message: msg}})
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		s.renderErr(w, err)
		return
	}
	s.DB.Model(&u).Update("password_hash", hash)
	// 重置密码的语义是「这个账号可能已经泄露」。只换哈希不清会话，等于
	// 攻击者手上那枚 cookie 还能继续用到 7 天后——重置就成了半个动作。
	s.Auth.RevokeAllSessions(u.ID)
	s.audit(p, "user.reset_password", "user", u.ID, map[string]interface{}{"username": u.Username})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}
