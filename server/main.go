// Package main 是 docforge Go 主服务入口：装配配置、DB、渲染客户端、签名、
// 认证、worker、HTTP 路由并启动监听。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docforge/server/internal/api"
	"docforge/server/internal/auth"
	"docforge/server/internal/callback"
	"docforge/server/internal/clientip"
	"docforge/server/internal/config"
	"docforge/server/internal/mailer"
	"docforge/server/internal/netguard"
	"docforge/server/internal/render"
	"docforge/server/internal/seed"
	"docforge/server/internal/sign"
	"docforge/server/internal/storage"
	"docforge/server/internal/store"
	"docforge/server/internal/version"
	"docforge/server/internal/worker"

	"github.com/go-chi/chi/v5"
)

func main() {
	// 版本日志放最前面：出问题时「线上跑的到底是哪个版本」永远是第一个要回答的问题，
	// 而这条日志必须在任何可能失败的操作之前打出来（连不上库就没有第二行日志了）。
	v := version.Current()
	if v.Dev {
		log.Printf("[main] 铸版 ZHUBAN dev 构建（未注入版本号，更新检查不参与比较）")
	} else {
		log.Printf("[main] 铸版 ZHUBAN v%s（commit %s，构建于 %s）", v.Version, v.Commit, v.BuildTime)
	}

	cfg := config.Load()

	// 密钥：未配置则随机生成并告警。
	signedSecret := cfg.SignedURLSecret
	if signedSecret == "" {
		signedSecret = sign.GenerateRandomSecret()
		log.Printf("[main] WARN SIGNED_URL_SECRET 未配置，已随机生成（重启后旧签名链接失效）")
	}
	authSecret := cfg.AuthSecret
	if authSecret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		authSecret = hex.EncodeToString(b)
		log.Printf("[main] WARN AUTH_SECRET 未配置，已随机生成（重启后旧会话全部失效）")
	}
	cfg.AuthSecret = authSecret
	if cfg.AuthDev {
		log.Printf("[main] WARN AUTH_DEV=true：信任 X-Dev-User 头，仅限本机联调，生产环境务必关闭")
	}

	printConfig(cfg)

	db, err := store.Open(cfg)
	if err != nil {
		log.Fatalf("[main] 打开数据库失败：%v", err)
	}

	// 首个管理员
	auth.BootstrapAdmin(db, cfg)
	// 名单驱动的管理员（OIDC 建号默认是 member，这里按配置兜住）
	auth.EnsureAdminUsernames(db, cfg)

	st := storage.New(cfg.DataDir)
	rd := render.New(cfg.RenderServiceURL, cfg.ScanTimeout, cfg.RenderTimeout, cfg.PreviewTimeout)
	sg := sign.New(signedSecret, cfg.SignedURLTTL)
	guard := netguard.New(cfg.CallbackAllowPrivate, cfg.CallbackHostAllow)
	mgr := auth.New(db, cfg)

	// 可信代理名单写错必须让启动失败：静默忽略等于「限流与审计 IP 静默失效」，
	// 而这种失效在线上没有任何征兆——日志照写，只是写的都是同一个地址。
	ips, err := clientip.New(cfg.TrustedProxies)
	if err != nil {
		log.Fatalf("[main] TRUSTED_PROXIES 配置有误：%v", err)
	}
	// 与限流、审计共用同一套来源 IP 判据：API Key 的「最近使用 IP」
	// 必须和审计里那一列对得上，否则排查时会以为是两个不同的东西。
	mgr.IPs = ips

	oidcMgr, err := auth.NewOIDC(mgr)
	if err != nil {
		log.Printf("[main] WARN OIDC Discovery 失败：%v（OIDC 入口不可用，本地登录不受影响）", err)
		oidcMgr = nil
	}
	sender := callback.New(db, cfg, sg, guard)
	mail := mailer.New(db, cfg)

	// 内置模板播种。
	//
	// **必须异步**：seed.Run 在 render 服务没就绪时会一直重试到 60 秒。
	// 同步调用意味着「容器起来了但端口不监听」——首次 docker compose up 时
	// 用户打开域名看到的是连接被拒，而那时 render 往往只是还没编译完镜像。
	// 另外它要访问的 /scan 与安装向导毫无关系，没理由让向导等它。
	if cfg.SeedBuiltin {
		go seed.Run(context.Background(), db, st, rd)
	}

	// worker
	wk := worker.New(db, cfg, st, rd, sg, sender, mail)
	ctx, cancel := context.WithCancel(context.Background())
	go wk.Run(ctx)

	srv := api.New(db, cfg, st, rd, sg, mgr, oidcMgr, sender, mail, ips)
	// 未初始化时把安装页释放到数据目录；已初始化时什么都不做（也不会被服务）。
	// 失败只告警：数据目录只读的部署仍可从内嵌资源直出安装页。
	if err := srv.PrepareInstallPage(); err != nil {
		log.Printf("[main] WARN 安装页释放失败：%v（仍可从内嵌资源访问 /install）", err)
	}
	r := chi.NewRouter()
	srv.Mount(r)

	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: r, ReadHeaderTimeout: 15 * time.Second}

	go func() {
		log.Printf("[main] 铸版 ZHUBAN 监听 %s（base_url=%s）", cfg.HTTPAddr, cfg.ExternalBaseURL)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[main] HTTP 服务退出：%v", err)
		}
	}()

	// 优雅退出
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	cancel()
	shutCtx, shutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdown()
	_ = httpSrv.Shutdown(shutCtx)
	log.Printf("[main] 已退出")
}

func printConfig(cfg *config.Config) {
	log.Printf("[main] 配置：addr=%s db=%s render=%s data=%s retention=%dd oidc=%v register=%v auth_dev=%v",
		cfg.HTTPAddr, maskedDB(cfg), cfg.RenderServiceURL, cfg.DataDir, cfg.RetentionDays,
		cfg.OIDCEnabled, cfg.AllowRegister, cfg.AuthDev)
}

func maskedDB(cfg *config.Config) string {
	if cfg.IsSQLite() {
		return "sqlite:" + cfg.SQLitePath()
	}
	return "postgres"
}
