package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"douyin-server/internal/bot"
	"douyin-server/internal/config"
	"douyin-server/internal/repo"
)

// Server 持有依赖与路由。
type Server struct {
	repo   *repo.Repo
	bots   *bot.Manager
	cfg    config.Config
	log    *slog.Logger
	mux    *http.ServeMux
	bootAt time.Time
}

// New 注册全部路由。bots 为 nil 时机器人接口返回未启用。
func New(r *repo.Repo, b *bot.Manager, cfg config.Config, log *slog.Logger) *Server {
	s := &Server{repo: r, bots: b, cfg: cfg, log: log, mux: http.NewServeMux(), bootAt: time.Now()}

	// 健康检查不进业务栈，探针要能在 DB 挂掉时仍然区分出存活与就绪。
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)

	s.mux.HandleFunc("GET /api/v1/persons", s.listPersons)
	s.mux.HandleFunc("POST /api/v1/persons", s.createPerson)
	s.mux.HandleFunc("GET /api/v1/persons/{id}", s.getPerson)
	s.mux.HandleFunc("PATCH /api/v1/persons/{id}", s.updatePerson)
	s.mux.HandleFunc("DELETE /api/v1/persons/{id}", s.deletePerson)
	s.mux.HandleFunc("GET /api/v1/persons/{id}/accounts", s.listAccounts)
	s.mux.HandleFunc("POST /api/v1/persons/{id}/accounts", s.bindAccount)

	// 615 主播管理里的批量操作：批量删除、合并账号、重复检测、设师傅、改快照
	s.mux.HandleFunc("POST /api/v1/persons/batch-delete", s.batchDeletePersons)
	s.mux.HandleFunc("POST /api/v1/persons/sync-615", s.syncFrom615)
	s.mux.HandleFunc("GET /api/v1/persons/duplicates", s.duplicatePersons)

	// 网页端主播 CSV 批量导入：先预览（提取姓名+ID 供勾选），再批量建档
	s.mux.HandleFunc("POST /api/v1/persons/import-anchors/preview", s.importAnchorsPreview)
	s.mux.HandleFunc("POST /api/v1/persons/import-anchors", s.importAnchors)
	s.mux.HandleFunc("POST /api/v1/persons/merge", s.mergePersons)
	s.mux.HandleFunc("PATCH /api/v1/persons/{id}/master", s.setMaster)
	s.mux.HandleFunc("POST /api/v1/persons/{id}/snapshot", s.saveSnapshot)

	s.mux.HandleFunc("GET /api/v1/metrics/dashboard", s.dashboard)
	s.mux.HandleFunc("GET /api/v1/metrics/daily", s.dailyMetrics)
	s.mux.HandleFunc("GET /api/v1/metrics/monthly", s.monthlyMetrics)
	s.mux.HandleFunc("GET /api/v1/metrics/yearly", s.yearlyMetrics)

	s.mux.HandleFunc("POST /api/v1/imports/snapshots", s.importSnapshots)
	s.mux.HandleFunc("POST /api/v1/imports/preview", s.previewImport)
	s.mux.HandleFunc("POST /api/v1/imports/csv", s.importCSV)
	s.mux.HandleFunc("POST /api/v1/imports/recompute", s.recompute)
	s.mux.HandleFunc("POST /api/v1/imports/purge", s.purgeData)
	s.mux.HandleFunc("GET /api/v1/imports/logs", s.importLogs)

	// 导出图片（SVG，样式对齐 615）
	s.mux.HandleFunc("GET /api/v1/exports/report.svg", s.exportReport)

	// 机器人：双通道状态、启停、推送开关、意图试玩
	s.mux.HandleFunc("GET /api/v1/bots/status", s.botStatus)
	s.mux.HandleFunc("POST /api/v1/bots/{name}/start", s.botStart)
	s.mux.HandleFunc("POST /api/v1/bots/{name}/stop", s.botStop)
	s.mux.HandleFunc("POST /api/v1/bots/push", s.botSetPush)
	s.mux.HandleFunc("GET /api/v1/bots/messages", s.botMessages)
	s.mux.HandleFunc("POST /api/v1/bots/parse", s.botParse)
	s.mux.HandleFunc("GET /api/v1/bots/{name}/detail", s.botDetail)
	s.mux.HandleFunc("POST /api/v1/bots/weixin/qrcode", s.weixinQRCode)
	s.mux.HandleFunc("GET /api/v1/bots/weixin/qrcode/status", s.weixinLoginStatus)
	s.mux.HandleFunc("POST /api/v1/bots/qq/credentials", s.qqCredentials)

	return s
}

// ServeStatic 把前端构建产物挂在根路径，用于单容器部署。
// 带 SPA 回退：找不到静态文件时返回 index.html，否则刷新子路由会 404。
func (s *Server) ServeStatic(dir string) {
	fs := http.FileServer(http.Dir(dir))
	s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if _, err := os.Stat(filepath.Join(dir, filepath.Clean(r.URL.Path))); err != nil {
				http.ServeFile(w, r, filepath.Join(dir, "index.html"))
				return
			}
		}
		fs.ServeHTTP(w, r)
	}))
}

// Handler 返回带日志、恢复、CORS 的完整中间件栈。
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = cors(h)
	h = recoverer(s.log, h)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"uptime": time.Since(s.bootAt).String(),
	})
}

// handleReady 会真的打一次数据库，DB 不就绪时不该接流量。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.repo.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "DB_DOWN", "数据库不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

// cors 允许本地前端跨域。生产是同一个域名下的反代，这里放开是为了开发方便。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer 兜住 panic，避免一个 handler 崩掉整个进程。
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("请求处理 panic", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "PANIC", "服务内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// isNotFound 统一判断领域层的"没找到"。
func isNotFound(err error) bool {
	return errors.Is(err, repo.ErrNotFound)
}
