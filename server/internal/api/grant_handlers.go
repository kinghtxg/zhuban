package api

import (
	"encoding/json"
	"net/http"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"

	"github.com/go-chi/chi/v5"
)

// grantInput 单个授予项。
type grantInput struct {
	GranteeType string     `json:"grantee_type"`
	GranteeID   string     `json:"grantee_id"`
	MaxUses     int        `json:"max_uses"`
	ValidFrom   *time.Time `json:"valid_from"`
	ValidUntil  *time.Time `json:"valid_until"`
	Note        string     `json:"note"`
}

// loadResourceForGrant 校验 owner/admin 权限，返回资源与类型。
func (s *Server) loadResourceForGrant(w http.ResponseWriter, r *http.Request, resourceType string) (*auth.Principal, *models.Signature, *models.Seal) {
	p := s.requireUser(w, r)
	if p == nil {
		return nil, nil, nil
	}
	id := chi.URLParam(r, "id")
	if resourceType == "seal" {
		var x models.Seal
		if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "印章不存在", nil)
			return nil, nil, nil
		}
		if !p.User.IsAdmin() && x.OwnerID != p.User.ID {
			writeErr(w, http.StatusForbidden, "forbidden", "仅 owner 或 admin 可授权", nil)
			return nil, nil, nil
		}
		return p, nil, &x
	}
	var x models.Signature
	if err := s.DB.First(&x, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "签名不存在", nil)
		return nil, nil, nil
	}
	if !p.User.IsAdmin() && x.OwnerID != p.User.ID {
		writeErr(w, http.StatusForbidden, "forbidden", "仅 owner 或 admin 可授权", nil)
		return nil, nil, nil
	}
	return p, &x, nil
}

// createGrants POST /signatures/{id}/grants 与 /seals/{id}/grants。
func (s *Server) createGrants(w http.ResponseWriter, r *http.Request, resourceType string) {
	p, sig, seal := s.loadResourceForGrant(w, r, resourceType)
	if p == nil {
		return
	}
	var req struct {
		Grantees []grantInput `json:"grantees"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "data_invalid", "请求体需为 {grantees:[...]}", nil)
		return
	}
	resID, resName := "", ""
	if resourceType == "seal" {
		resID, resName = seal.ID, seal.Name
	} else {
		resID, resName = sig.ID, sig.Name
	}
	created := []models.Grant{}
	for _, g := range req.Grantees {
		if g.GranteeType == "" {
			g.GranteeType = "user"
		}
		if g.GranteeID == "" {
			continue
		}
		// 快照 grantee_name。
		name := g.GranteeID
		if g.GranteeType == "user" {
			var u models.User
			if s.DB.First(&u, "id = ?", g.GranteeID).Error == nil {
				name = u.DisplayName
				if name == "" {
					name = u.Username
				}
			}
		} else {
			var ak models.APIKey
			if s.DB.First(&ak, "id = ?", g.GranteeID).Error == nil {
				name = ak.Name
			}
		}
		row := models.Grant{
			ID: "grt_" + randID(), ResourceType: resourceType, ResourceID: resID,
			GrantedBy: p.User.ID, GranteeType: g.GranteeType, GranteeID: g.GranteeID, GranteeName: name,
			MaxUses: g.MaxUses, ValidFrom: g.ValidFrom, ValidUntil: g.ValidUntil, Note: g.Note,
		}
		if err := s.DB.Create(&row).Error; err != nil {
			s.renderErr(w, err)
			return
		}
		evt := models.GrantEvent{
			ID: "gev_" + randID(), GrantID: row.ID, ResourceType: resourceType, ResourceID: resID,
			Action: "grant.create", ActorID: p.User.ID, CreatedAt: time.Now(),
		}
		b, _ := json.Marshal(map[string]interface{}{"grantee": name, "max_uses": g.MaxUses, "note": g.Note})
		evt.Detail = b
		_ = s.DB.Create(&evt).Error
		s.auditReq(r, p, resourceType+".grant.create", resourceType, resID, map[string]interface{}{
			"grant_id": row.ID, "grantee_id": g.GranteeID, "grantee_type": g.GranteeType,
			"max_uses": g.MaxUses, "resource_name": resName,
		})
		created = append(created, row)
	}
	out := make([]map[string]interface{}, 0, len(created))
	for i := range created {
		out = append(out, grantMap(&created[i]))
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"items": out})
}

func (s *Server) handleCreateSignatureGrants(w http.ResponseWriter, r *http.Request) {
	s.createGrants(w, r, "signature")
}
func (s *Server) handleCreateSealGrants(w http.ResponseWriter, r *http.Request) {
	s.createGrants(w, r, "seal")
}

// grantMap 带计算态 status 与剩余次数。
func grantMap(g *models.Grant) map[string]interface{} {
	now := time.Now()
	status := "active"
	switch {
	case g.ValidUntil != nil && now.After(*g.ValidUntil):
		status = "expired"
	case g.ValidFrom != nil && now.Before(*g.ValidFrom):
		status = "not_yet"
	case g.MaxUses > 0 && g.UsedCount >= g.MaxUses:
		status = "exhausted"
	}
	remaining := -1
	if g.MaxUses > 0 {
		remaining = g.MaxUses - g.UsedCount
		if remaining < 0 {
			remaining = 0
		}
	}
	return map[string]interface{}{
		"id": g.ID, "resource_type": g.ResourceType, "resource_id": g.ResourceID,
		"granted_by": g.GrantedBy, "grantee_type": g.GranteeType, "grantee_id": g.GranteeID,
		"grantee_name": g.GranteeName, "max_uses": g.MaxUses, "used_count": g.UsedCount,
		"remaining": remaining, "status": status,
		"valid_from": g.ValidFrom, "valid_until": g.ValidUntil, "note": g.Note,
		"created_at": g.CreatedAt,
	}
}

// listGrants GET /signatures/{id}/grants 与 /seals/{id}/grants。
func (s *Server) listGrants(w http.ResponseWriter, r *http.Request, resourceType string) {
	p, _, _ := s.loadResourceForGrant(w, r, resourceType)
	if p == nil {
		return
	}
	id := chi.URLParam(r, "id")
	var rows []models.Grant
	s.DB.Where("resource_type=? AND resource_id=?", resourceType, id).Order("created_at DESC").Find(&rows)
	out := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		out = append(out, grantMap(&rows[i]))
	}
	var events []models.GrantEvent
	s.DB.Where("resource_type=? AND resource_id=?", resourceType, id).Order("created_at ASC").Find(&events)
	evtOut := make([]map[string]interface{}, 0, len(events))
	for i := range events {
		evtOut = append(evtOut, map[string]interface{}{
			"id": events[i].ID, "grant_id": events[i].GrantID,
			"resource_type": events[i].ResourceType, "resource_id": events[i].ResourceID,
			"action": events[i].Action, "actor_id": events[i].ActorID,
			"detail": jsonOrNil(events[i].Detail), "created_at": events[i].CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out, "total": len(out), "events": evtOut,
	})
}

func (s *Server) handleListSignatureGrants(w http.ResponseWriter, r *http.Request) { s.listGrants(w, r, "signature") }
func (s *Server) handleListSealGrants(w http.ResponseWriter, r *http.Request)    { s.listGrants(w, r, "seal") }

// myGrants GET /grants?grantee_type=&grantee_id= 当前被授权人视角。
func (s *Server) handleMyGrants(w http.ResponseWriter, r *http.Request) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	gt := r.URL.Query().Get("grantee_type")
	gid := r.URL.Query().Get("grantee_id")
	if !p.User.IsAdmin() || gt == "" || gid == "" {
		// 非 admin：强制查自己。
		gt, gid = "user", p.User.ID
	}
	var rows []models.Grant
	s.DB.Where("grantee_type=? AND grantee_id=?", gt, gid).Order("created_at DESC").Find(&rows)
	out := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		out = append(out, grantMap(&rows[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// grantByID 取 grant 并校验 owner/admin。
func (s *Server) grantByID(w http.ResponseWriter, r *http.Request) (*auth.Principal, *models.Grant) {
	p := s.requireUser(w, r)
	if p == nil {
		return nil, nil
	}
	var g models.Grant
	if err := s.DB.First(&g, "id = ?", chi.URLParam(r, "id")).Error; err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "授权不存在", nil)
		return nil, nil
	}
	// 校验调用方是资源 owner 或 admin。
	if !p.User.IsAdmin() {
		owner := ""
		if g.ResourceType == "signature" {
			var x models.Signature
			if s.DB.First(&x, "id = ?", g.ResourceID).Error == nil {
				owner = x.OwnerID
			}
		} else {
			var x models.Seal
			if s.DB.First(&x, "id = ?", g.ResourceID).Error == nil {
				owner = x.OwnerID
			}
		}
		if owner != p.User.ID {
			writeErr(w, http.StatusForbidden, "forbidden", "仅资源 owner 或 admin 可操作授权", nil)
			return nil, nil
		}
	}
	return p, &g
}

// renewGrant POST /grants/{id}/renew。
func (s *Server) handleRenewGrant(w http.ResponseWriter, r *http.Request) {
	p, g := s.grantByID(w, r)
	if p == nil {
		return
	}
	var req struct {
		MaxUses    *int        `json:"max_uses"`
		ValidFrom  *time.Time `json:"valid_from"`
		ValidUntil *time.Time `json:"valid_until"`
		AddUses    int        `json:"add_uses"`
	}
	_ = decodeJSON(r, &req)
	upd := map[string]interface{}{}
	if req.MaxUses != nil {
		upd["max_uses"] = *req.MaxUses
	}
	if req.ValidFrom != nil {
		upd["valid_from"] = req.ValidFrom
	}
	if req.ValidUntil != nil {
		upd["valid_until"] = req.ValidUntil
	}
	if req.AddUses > 0 {
		upd["max_uses"] = g.MaxUses + req.AddUses
	}
	if err := s.DB.Model(g).Updates(upd).Error; err != nil {
		s.renderErr(w, err)
		return
	}
	evt := models.GrantEvent{
		ID: "gev_" + randID(), GrantID: g.ID, ResourceType: g.ResourceType, ResourceID: g.ResourceID,
		Action: "grant.renew", ActorID: p.User.ID, CreatedAt: time.Now(),
	}
	b, _ := json.Marshal(upd)
	evt.Detail = b
	_ = s.DB.Create(&evt).Error
	s.auditReq(r, p, g.ResourceType+".grant.renew", g.ResourceType, g.ResourceID, map[string]interface{}{"grant_id": g.ID})
	s.DB.First(g, "id=?", g.ID)
	writeJSON(w, http.StatusOK, grantMap(g))
}

// revokeGrant POST /grants/{id}/revoke。
func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	p, g := s.grantByID(w, r)
	if p == nil {
		return
	}
	note := r.FormValue("note")
	// 软删：直接删除行（status 列只存 active，撤销即删除）。
	s.DB.Delete(g)
	evt := models.GrantEvent{
		ID: "gev_" + randID(), GrantID: g.ID, ResourceType: g.ResourceType, ResourceID: g.ResourceID,
		Action: "grant.revoke", ActorID: p.User.ID, CreatedAt: time.Now(),
	}
	b, _ := json.Marshal(map[string]interface{}{"note": note})
	evt.Detail = b
	_ = s.DB.Create(&evt).Error
	s.auditReq(r, p, g.ResourceType+".grant.revoke", g.ResourceType, g.ResourceID, map[string]interface{}{"grant_id": g.ID, "note": note})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}
