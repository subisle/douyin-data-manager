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

	if cfg.Bots.QQAppID != "" {
		bots.Register(qq.New(bots, cfg.Bots.QQAppID, cfg.Bots.QQClientSecret, cfg.Bots.QQAPIBase))
		log.Info("QQ 通道已挂载", "appId", cfg.Bots.QQAppID)
	} else {
		log.Info("QQ 通道未配置 AppID，可在网页里填写")
	}

	// 带凭据的通道开机自启（与 615 行为一致：起服务即连，不用手动点开始）。
	// 微信没 token 时 Start 会报未登录，忽略即可——扫码后自动进入会话。
	for _, name := range []string{"weixin", "qq"} {
		startCtx, startCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if err := bots.Start(startCtx, name); err != nil {
			log.Warn("通道自启失败（可稍后在机器人页手动启动）", "channel", name, "err", err)
		}
		startCancel()
	}

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
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("优雅停机失败", "err", err)
	}
	log.Info("已停机")
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
