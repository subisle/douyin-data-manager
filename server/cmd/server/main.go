// Command server 是 3328 分支的后端入口：一个二进制，一个端口。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"douyin-server/internal/bot"
	"douyin-server/internal/bot/transport/ilink"
	"douyin-server/internal/bot/transport/qq"
	"douyin-server/internal/config"
	"douyin-server/internal/httpapi"
	"douyin-server/internal/migrate"
	"douyin-server/internal/repo"
	"douyin-server/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("配置加载失败", "err", err)
		os.Exit(1)
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	db, err := store.Open(cfg.DB)
	if err != nil {
		log.Error("数据库连接失败", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("关闭数据库连接失败", "err", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if cfg.AutoMigrate {
		applied, err := migrate.Run(ctx, db)
		if err != nil {
			log.Error("数据库迁移失败", "err", err)
			os.Exit(1)
		}
		if len(applied) > 0 {
			log.Info("已应用迁移", "files", applied)
		}
	}

	// 机器人管理器：两个通道各自 Register，共享同一个 Agent
	r := repo.New(db)
	bots := bot.NewManager(r)

	wx, err := ilink.New(bots, cfg.Bots.WeixinBaseURL, cfg.Bots.WeixinToken)
	if err != nil {
		log.Warn("微信通道初始化失败", "err", err)
	} else {
		bots.Register(wx)
		if cfg.Bots.WeixinToken != "" {
			log.Info("微信通道已挂载（含 token）")
		} else {
			log.Info("微信通道已挂载（待扫码登录）")
		}
	}

	// 凭据来源优先级：**库里保存的（网页填的）> 环境变量**。
	// env 是首次部署的缺省值，但它在容器里是静态的：网页更新过凭据后，
	// env 里的旧密钥会反过来压制新值（实测：启动拿旧 secret，平台返回
	// 100016 invalid appid or secret，而网页手填的却是好的）。
	// 密钥不写进代码：进 Git 就是事故，换机器人也不用重新编译。
	qqCred := r.GetQQCredentials(ctx)
	if qqCred.AppID == "" {
		qqCred = repo.QQCredentials{
			AppID:        cfg.Bots.QQAppID,
			ClientSecret: cfg.Bots.QQClientSecret,
			APIBase:      cfg.Bots.QQAPIBase,
		}
	}
	if qqCred.AppID != "" {
		bots.Register(qq.New(bots, qqCred.AppID, qqCred.ClientSecret, qqCred.APIBase))
		log.Info("QQ 通道已挂载", "appId", qqCred.AppID)
	} else {
		log.Info("QQ 通道未配置 AppID，可在网页里填写")
	}

	// 带凭据的通道开机自启（与 615 行为一致：起服务即连，不用手动点开始）。
	// 微信没 token 时 Start 会报未登录，忽略即可——扫码后自动进入会话。
	for _, name := range []string{"weixin", "qq"} {
		startCtx, startCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := bots.Start(startCtx, name)
		startCancel()

		// QQ 刚起时可能撞上平台侧旧会话未释放，退避重试（30s/60s/120s）。
		// 间隔太短没用——服务端会话超时是分钟级的。
		// 微信未登录是常态（要扫码），重试没意义，不重试。
		for attempt, backoff := 1, 30*time.Second; err != nil && name == "qq" && attempt < 4; attempt, backoff = attempt+1, backoff*2 {
			log.Warn("QQ 通道自启失败，退避重试", "attempt", attempt, "backoff", backoff.String(), "err", err)
			time.Sleep(backoff)
			retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			err = bots.Start(retryCtx, name)
			retryCancel()
		}
		if err != nil {
			log.Warn("通道自启失败（可稍后在机器人页手动启动）", "channel", name, "err", err)
		}
	}

	// 指标自愈：补算「快照有、指标缺」的日子（导入撞上掉线会留下这种半成品，
	// 日榜会静默变成全 0 空榜）。启动 1 分钟后扫一次，之后每 6 小时一次。
	go metricRepairLoop(ctx, r, log)

	// 每天 1 点向活跃会话索要 CSV 文件（机器人页可开关、可手动触发）
	bots.StartReminderLoop(context.WithoutCancel(ctx))

	srv := httpapi.New(r, bots, cfg, log)

	// 单容器部署时，前端产物交给同一个端口托管，省一层反代。
	if cfg.WebDir != "" {
		if _, err := os.Stat(cfg.WebDir); err != nil {
			log.Warn("WEB_DIR 不可用，跳过静态托管", "dir", cfg.WebDir, "err", err)
		} else {
			srv.ServeStatic(cfg.WebDir)
			log.Info("已托管前端静态文件", "dir", cfg.WebDir)
		}
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("服务启动", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Error("服务异常终止", "err", err)
		os.Exit(1)
	case sig := <-stop:
		log.Info("收到退出信号，开始优雅停机", "signal", sig.String())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	// 先断机器人通道：粗暴切断 WebSocket 会让平台侧旧会话挂到超时，
	// 服务重启后立刻重连会被拒（QQ 实测 100016 invalid appid or secret）。
	if err := bots.StopAll(); err != nil {
		log.Warn("停止机器人通道", "err", err)
	}
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("优雅停机失败", "err", err)
	}
	log.Info("已停机")
}

// metricRepairLoop 定期补算缺指标的日子。
//
// 导入流程本身会重算，但撞上数据库掉线就会留下「快照进了、指标没算」的半成品
// （实测 2026-09-29 就是这样）。日榜以月榜名册 LEFT JOIN 出图，这种日子不会报错，
// 只会默默发一张全是 0 的空榜——比报错更难发现，所以定期兜一遍。
func metricRepairLoop(ctx context.Context, r *repo.Repo, log *slog.Logger) {
	const lookback = 7
	repair := func() {
		for i := lookback; i >= 0; i-- {
			if ctx.Err() != nil {
				return
			}
			day := time.Now().AddDate(0, 0, -i)
			if !r.DailyMissingMetric(ctx, day) {
				continue
			}
			log.Warn("发现缺指标的日期，补算", "date", day.Format("2006-01-02"))
			rc, cancel := context.WithTimeout(ctx, 5*time.Minute)
			n, err := r.RecomputeAll(rc, day, day)
			cancel()
			if err != nil {
				log.Warn("补算失败", "date", day.Format("2006-01-02"), "err", err)
				continue
			}
			log.Info("补算完成", "date", day.Format("2006-01-02"), "persons", n)
		}
	}

	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			repair()
		case <-ticker.C:
			repair()
		}
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
