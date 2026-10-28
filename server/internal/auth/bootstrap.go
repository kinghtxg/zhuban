// Package auth — bootstrap.go：启动时按配置创建首个管理员。
package auth

import (
	"log"

	"docforge/server/internal/config"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"

	"gorm.io/gorm"
)

// BootstrapAdmin 在 users 为空且配置了 ADMIN_USERNAME/PASSWORD 时建 active admin。
func BootstrapAdmin(db *gorm.DB, cfg *config.Config) {
	var cnt int64
	db.Model(&models.User{}).Count(&cnt)
	if cnt > 0 {
		return
	}
	if cfg.AdminUsername == "" || cfg.AdminPassword == "" {
		log.Printf("[auth] 系统未初始化：未配置 ADMIN_USERNAME/PASSWORD，请通过 /api/v1/auth/setup 创建首个管理员")
		return
	}
	hash, err := HashPassword(cfg.AdminPassword)
	if err != nil {
		log.Printf("[auth] 管理员密码哈希失败：%v", err)
		return
	}
	u := models.User{
		ID: "u_" + idgen.Hex(8), Username: cfg.AdminUsername, PasswordHash: hash,
		DisplayName: cfg.AdminUsername, Role: "admin", Status: "active", Source: "local",
	}
	if err := db.Create(&u).Error; err != nil {
		log.Printf("[auth] 创建内置管理员失败：%v", err)
		return
	}
	log.Printf("[auth] 内置管理员 %s 已创建（active/admin）", cfg.AdminUsername)
}

// EnsureAdminUsernames 把 ADMIN_USERNAMES 名单里的已存在账号提升为 admin。
//
// 为什么需要它：OIDC 自动开户写死 Role:"member"，所以从 APSSO 登录进来的人
// 默认只是成员；而「谁是管理员」是部署事实，不该靠一次性 SQL 改库
// （重装 / 换库 / 加副本都会丢，且没有任何地方记录过这个决定）。
//
// 幂等：已经是 admin 的跳过，不刷 updated_at，不产生审计噪音。
// 只提升不降级 —— 名单外的 admin 不会被自动打回成员，避免改配置时误伤。
func EnsureAdminUsernames(db *gorm.DB, cfg *config.Config) {
	if len(cfg.AdminUsernames) == 0 {
		return
	}
	for _, name := range cfg.AdminUsernames {
		var u models.User
		if err := db.First(&u, "username = ?", name).Error; err != nil {
			// 账号还没登录过（OIDC 首次登录才建号）→ 登录时再提升
			log.Printf("[auth] 管理员名单 %q 暂无对应账号，等该用户首次登录时提升", name)
			continue
		}
		if u.Role == "admin" && u.Status == "active" {
			continue
		}
		// 先记下原值再 Updates：GORM 的 Updates 会就地改写 u 的字段，
		// 更新完再读 u.Role 拿到的是新值，日志会写成「原角色 admin」——
		// 运维看到这句会以为什么也没发生，反而怀疑配置没生效。
		oldRole, oldStatus := u.Role, u.Status
		if err := db.Model(&u).Updates(map[string]interface{}{"role": "admin", "status": "active"}).Error; err != nil {
			log.Printf("[auth] 提升 %q 为管理员失败：%v", name, err)
			continue
		}
		log.Printf("[auth] 已按 ADMIN_USERNAMES 将 %q 提升为管理员（原角色 %s / 状态 %s）", name, oldRole, oldStatus)
	}
}

// PromoteIfListed 在单个用户登录后复查名单。作用于「首次登录才建号」的场景。
func PromoteIfListed(db *gorm.DB, cfg *config.Config, u *models.User) {
	if u == nil || u.Role == "admin" {
		return
	}
	for _, name := range cfg.AdminUsernames {
		if name != u.Username {
			continue
		}
		if err := db.Model(u).Updates(map[string]interface{}{"role": "admin", "status": "active"}).Error; err != nil {
			log.Printf("[auth] 登录后提升 %q 为管理员失败：%v", u.Username, err)
			return
		}
		u.Role = "admin"
		u.Status = "active"
		log.Printf("[auth] 已按 ADMIN_USERNAMES 将 %q 提升为管理员（登录时复查）", u.Username)
		return
	}
}
