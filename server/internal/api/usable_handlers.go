package api

import (
	"net/http"
	"time"

	"docforge/server/internal/auth"
	"docforge/server/internal/models"
)

// usableAssets 返回「当前调用者真正能用的」签名/印章。
//
// 与 /signatures、/seals 的区别是可见范围：
//   列表接口   —— 只能看到 owner_id = 自己 的（管理视角）
//   本接口     —— 自己拥有的 + 别人授权给我的（使用视角）
//
// 为什么需要分开：填数据时要选的是「我能盖哪一个」，不是「我上传过哪些」。
// 用列表接口做选章，被授权的人会看到空列表，然后以为没权限——其实有，只是
// 那些章不归他所有。
//
// 判定口径与 sigauth.resolveOne 保持一致（approved + 未删 + 授权仍有效），
// 避免出现「这里列出来了但渲染时用不了」的双重标准。
func (s *Server) handleUsableAssets(w http.ResponseWriter, r *http.Request, resource string) {
	p := s.requireUser(w, r)
	if p == nil {
		return
	}
	now := time.Now()
	granteeType, granteeID, _ := grantCaller(p)

	usable := s.usableResourceIDs(resource, granteeType, granteeID, now)
	isAdmin := p.User.IsAdmin()

	var sigs []models.Signature
	var seals []models.Seal
	if resource == "seal" {
		q := s.DB.Where("status = ? AND deleted_at IS NULL", "approved")
		if !isAdmin {
			if len(usable) == 0 {
				q = q.Where("owner_id = ?", p.User.ID)
			} else {
				q = q.Where("owner_id = ? OR id IN ?", p.User.ID, usable)
			}
		}
		q.Order("created_at DESC").Limit(200).Find(&seals)
	} else {
		q := s.DB.Where("status = ? AND deleted_at IS NULL", "approved")
		if !isAdmin {
			if len(usable) == 0 {
				q = q.Where("owner_id = ?", p.User.ID)
			} else {
				q = q.Where("owner_id = ? OR id IN ?", p.User.ID, usable)
			}
		}
		q.Order("created_at DESC").Limit(200).Find(&sigs)
	}

	// 资源 -> 剩余可用次数（0 表示不限）。自己拥有的没有 grant，按不限处理。
	remaining := s.grantRemaining(resource, granteeType, granteeID, now)
	owners := s.displayNames(append(sigOwnerIDs(sigs), sealOwnerIDs(seals)...))

	items := make([]map[string]interface{}, 0, len(sigs)+len(seals))
	if resource == "seal" {
		for _, x := range seals {
			items = append(items, s.usableItem(x.ID, "seal", x.Name, x.OwnerID, x.WidthMM, x.HeightMM,
				owners, remaining[x.ID], p.User.ID))
		}
	} else {
		for _, x := range sigs {
			items = append(items, s.usableItem(x.ID, "signature", x.Name, x.OwnerID, x.WidthMM, x.HeightMM,
				owners, remaining[x.ID], p.User.ID))
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "total": len(items)})
}

func (s *Server) usableItem(id, kind, name, ownerID string, wmm, hmm float64,
	owners map[string]string, rem int, meID string) map[string]interface{} {
	item := map[string]interface{}{
		"id": id, "kind": kind, "name": name,
		"width_mm": wmm, "height_mm": hmm,
		"owned":     ownerID == meID,
		"owner_name": owners[ownerID],
		"remaining": rem, // 0 = 不限
	}
	if kind == "seal" {
		item["image_url"] = "/api/v1/seals/" + id + "/file"
	} else {
		item["image_url"] = "/api/v1/signatures/" + id + "/file"
	}
	return item
}

// usableResourceIDs 查出对方名下「授权仍有效」的资源 ID 集合。
func (s *Server) usableResourceIDs(resource, granteeType, granteeID string, now time.Time) []string {
	var ids []string
	if granteeID == "" {
		return ids
	}
	s.DB.Model(&models.Grant{}).
		Where("resource_type = ? AND grantee_type = ? AND grantee_id = ?", resource, granteeType, granteeID).
		Where("(valid_from IS NULL OR valid_from <= ?) AND (valid_until IS NULL OR valid_until >= ?)", now, now).
		Where("max_uses = 0 OR used_count < max_uses").
		Distinct().Pluck("resource_id", &ids)
	return ids
}

// grantRemaining 资源 -> 剩余次数（取该资源多条授权里最大的剩余量；0 = 不限）。
func (s *Server) grantRemaining(resource, granteeType, granteeID string, now time.Time) map[string]int {
	out := map[string]int{}
	if granteeID == "" {
		return out
	}
	var rows []models.Grant
	s.DB.Where("resource_type = ? AND grantee_type = ? AND grantee_id = ?", resource, granteeType, granteeID).
		Where("(valid_from IS NULL OR valid_from <= ?) AND (valid_until IS NULL OR valid_until >= ?)", now, now).
		Where("max_uses = 0 OR used_count < max_uses").
		Find(&rows)
	for _, g := range rows {
		left := 0
		if g.MaxUses > 0 {
			left = g.MaxUses - g.UsedCount
			if left < 0 {
				left = 0
			}
		}
		if cur, ok := out[g.ResourceID]; !ok || left == 0 || (cur != 0 && left > cur) {
			out[g.ResourceID] = left
		}
	}
	return out
}

// displayNames 批量取用户名，避免逐条查库。
func (s *Server) displayNames(ids []string) map[string]string {
	out := map[string]string{}
	uniq := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out
	}
	var users []models.User
	s.DB.Select("id", "username", "display_name").Where("id IN ?", uniq).Find(&users)
	for _, u := range users {
		if u.DisplayName != "" {
			out[u.ID] = u.DisplayName
		} else {
			out[u.ID] = u.Username
		}
	}
	return out
}

func sigOwnerIDs(rows []models.Signature) []string {
	out := make([]string, 0, len(rows))
	for _, x := range rows {
		out = append(out, x.OwnerID)
	}
	return out
}

func sealOwnerIDs(rows []models.Seal) []string {
	out := make([]string, 0, len(rows))
	for _, x := range rows {
		out = append(out, x.OwnerID)
	}
	return out
}

// canPreviewAsset 判断能否查看签章图片。
//
// 口径：资源所有者、管理员、以及**持有有效授权的人**都能看。
// 被授权者在生成时本来就会把这张图印进文档，拦住预览只有体验损失、没有安全收益；
// 而签章是视觉资产，只能凭名字选等于让人闭着眼睛盖章。
func (s *Server) canPreviewAsset(p *auth.Principal, resource, id, ownerID, status string) bool {
	if p == nil || p.User == nil {
		return false
	}
	if p.User.IsAdmin() || ownerID == p.User.ID {
		return true
	}
	if status != "approved" {
		return false
	}
	granteeType, granteeID, _ := grantCaller(p)
	if granteeID == "" {
		return false
	}
	ids := s.usableResourceIDs(resource, granteeType, granteeID, time.Now())
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
