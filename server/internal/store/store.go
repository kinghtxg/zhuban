// Package store 负责数据库连接（PG / sqlite 双驱动）、自动迁移与 ID 生成。
package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"docforge/server/internal/auditx"
	"docforge/server/internal/config"
	"docforge/server/internal/idgen"
	"docforge/server/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Open 根据配置打开数据库并自动迁移。
func Open(cfg *config.Config) (*gorm.DB, error) {
	gl := gormlogger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true, // worker 轮询空队列是正常现象，不刷日志
			Colorful:                  false,
		},
	)

	var (
		db  *gorm.DB
		err error
	)
	if cfg.IsSQLite() {
		p := cfg.SQLitePath()
		if dir := filepath.Dir(p); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		// sqlite 外键：开 foreign_keys；空闲连接数限制为 1 以配合事务串行。
		db, err = gorm.Open(sqlite.Open(p+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"),
			&gorm.Config{Logger: gl})
		if err == nil {
			var sqlDB *sql.DB
			if sqlDB, err = db.DB(); err == nil {
				sqlDB.SetMaxOpenConns(1)
				sqlDB.SetConnMaxLifetime(time.Hour)
			}
		}
	} else {
		db, err = gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{Logger: gl})
	}
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// 必须跑在 AutoMigrate **之前**：模型改成 column:oidc_sub 之后，
	// AutoMigrate 会直接新建一个空的 oidc_sub 列，此时再把旧列改名就撞名了。
	migrateOIDCSubColumn(db)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// V3：对 V2 历史审计日志（seq=0）按时间顺序引导哈希链，幂等。
	if err := (&auditx.Chain{DB: db}).BootstrapBackfill(); err != nil {
		log.Printf("[store] audit chain bootstrap skipped: %v", err)
	}
	return db, nil
}

// migrateOIDCSubColumn 把 GORM 自动生成错名的 o_id_c_sub 改成 oidc_sub。
//
// 背景：User.OIDCSub 早先没写 column tag，GORM 的命名策略把它转成了 o_id_c_sub
// （它只认 ID/URL/API 这类缩写，不认 OIDC）。而 OIDC 登录的查询条件全是手写的
// "oidc_sub = ?"，两边对不上。已部署的库里就躺着这一列，直接改 tag 会让
// AutoMigrate 新建一个空列、把已有的绑定关系留在旧列里没人读——所以先把旧列改名。
//
// 幂等：新列已存在、或旧列不存在，都直接返回。
func migrateOIDCSubColumn(db *gorm.DB) {
	if !db.Migrator().HasTable("users") {
		return
	}
	if !db.Migrator().HasColumn("users", "o_id_c_sub") {
		return
	}
	if db.Migrator().HasColumn("users", "oidc_sub") {
		return
	}
	if err := db.Exec("ALTER TABLE users RENAME COLUMN o_id_c_sub TO oidc_sub").Error; err == nil {
		log.Printf("[store] 已把 users.o_id_c_sub 重命名为 oidc_sub")
		return
	}
	// 很老的 SQLite（< 3.25）不支持 RENAME COLUMN：退化成加列 + 搬数据。
	// 旧列留着不碍事，它已经没有任何代码引用。
	if err := db.Exec("ALTER TABLE users ADD COLUMN oidc_sub varchar(255)").Error; err != nil {
		log.Printf("[store] 补 oidc_sub 列失败，OIDC 绑定可能不可用：%v", err)
		return
	}
	if err := db.Exec("UPDATE users SET oidc_sub = o_id_c_sub WHERE oidc_sub IS NULL AND o_id_c_sub IS NOT NULL").Error; err != nil {
		log.Printf("[store] 迁移 oidc_sub 数据失败：%v", err)
	}
	log.Printf("[store] 已按兼容方式补出 oidc_sub 列并搬运数据")
}

// RandHex 生成 n 字节随机 hex 字符串。
func RandHex(n int) string { return idgen.Hex(n) }

// NewTplID / NewVerID / NewAkID 生成实体 ID。
func NewTplID() string { return "tpl_" + RandHex(6) }
func NewVerID() string { return "ver_" + RandHex(6) }
func NewAkID() string { return "ak_" + RandHex(6) }

// NextDailyID 生成当日流水 ID：<prefix>YYYYMMDD_0001。
// 通过查询当天已存在的最大序号并 +1 实现；在事务内调用以避免并发冲突。
func NextDailyID(db *gorm.DB, prefix string) (string, error) {
	today := time.Now().UTC().Format("20060102")
	like := prefix + today + "_%"
	var maxID string
	if err := db.Model(&models.Job{}).
		Where("id LIKE ?", like).
		Select("id").Order("id DESC").Limit(1).Row().Scan(&maxID); err != nil && err != sql.ErrNoRows {
		// documents 表也存 dc_ 前缀；分别在各自表查询。
	}
	seq := 1
	if maxID != "" {
		var tail int
		if n, _ := fmt.Sscanf(maxID, prefix+today+"_%d", &tail); n == 1 {
			seq = tail + 1
		}
	}
	return fmt.Sprintf("%s%s_%04d", prefix, today, seq), nil
}

// NextJobID 生成 jb_ 当日流水 ID。
func NextJobID(db *gorm.DB) (string, error) { return NextDailyID(db, "jb_") }

// NextDocumentID 生成 dc_ 当日流水 ID。
func NextDocumentID(db *gorm.DB) (string, error) {
	today := time.Now().UTC().Format("20060102")
	like := "dc_" + today + "_%"
	var maxID string
	if err := db.Model(&models.Document{}).
		Where("id LIKE ?", like).
		Select("id").Order("id DESC").Limit(1).Row().Scan(&maxID); err != nil && err != sql.ErrNoRows {
		return "", err
	}
	seq := 1
	if maxID != "" {
		var tail int
		if n, _ := fmt.Sscanf(maxID, "dc_"+today+"_%d", &tail); n == 1 {
			seq = tail + 1
		}
	}
	return fmt.Sprintf("dc_%s_%04d", today, seq), nil
}
