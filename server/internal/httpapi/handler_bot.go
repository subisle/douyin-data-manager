package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"douyin-server/internal/bot"
	"douyin-server/internal/bot/transport/ilink"
	"douyin-server/internal/bot/transport/qq"
)

func (s *Server) botManager() (*bot.Manager, bool) {
	if s.bots == nil {
		return nil, false
	}
	return s.bots, true
}

// botStatus GET /api/v1/bots/status
func (s *Server) botStatus(w http.ResponseWriter, _ *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": m.Status(),
		"push":     m.PushEnabled(),
		"reminder": m.GetReminderTargets(),
	})
}

// botSetReminderTargets POST /api/v1/bots/push/targets {"groups":true,"private":false}
//
// 索要提醒的发送范围：群聊与私聊各自独立开关。
func (s *Server) botSetReminderTargets(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	var req struct {
		Groups  *bool `json:"groups"`
		Private *bool `json:"private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	// 未传的字段保持现值，避免前端漏字段把开关悄悄关掉
	cur := m.GetReminderTargets()
	groups, private := cur.Groups, cur.Private
	if req.Groups != nil {
		groups = *req.Groups
	}
	if req.Private != nil {
		private = *req.Private
	}
	m.SetReminderTargets(groups, private)
	writeJSON(w, http.StatusOK, map[string]any{"reminder": m.GetReminderTargets()})
}

// botStart POST /api/v1/bots/{name}/start
func (s *Server) botStart(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	name := r.PathValue("name")
	if name != "weixin" && name != "qq" {
		badRequest(w, "通道只能是 weixin 或 qq")
		return
	}
	if err := m.Start(r.Context(), name); err != nil {
		writeError(w, http.StatusBadRequest, "START_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nil)
}

// botStop POST /api/v1/bots/{name}/stop
func (s *Server) botStop(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	name := r.PathValue("name")
	if name != "weixin" && name != "qq" {
		badRequest(w, "通道只能是 weixin 或 qq")
		return
	}
	if err := m.Stop(name); err != nil {
		writeError(w, http.StatusBadRequest, "STOP_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nil)
}

// botSetPush POST /api/v1/bots/push  {"enabled": true}
func (s *Server) botSetPush(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	m.SetPush(req.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"push": req.Enabled})
}

// botRemind POST /api/v1/bots/remind —— 立即向所有活跃会话索要 CSV 文件。
// 每日 1 点的定时版走 Manager.StartReminderLoop，这里是手动触发同一套逻辑。
func (s *Server) botRemind(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	// 群发可能要几十秒（逐会话发），给足超时；范围按当前开关走
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	targets := m.GetReminderTargets()
	opt := bot.RemindOptions{Groups: targets.Groups, Private: targets.Private}
	writeJSON(w, http.StatusOK, map[string]any{
		"text":     bot.ReminderText(time.Now()),
		"channels": m.RemindAll(ctx, bot.ReminderText(time.Now()), opt),
	})
}

// botMessages GET /api/v1/bots/messages?limit=50
func (s *Server) botMessages(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	limit := queryInt(r.URL.Query().Get("limit"), 50)
	writeJSON(w, http.StatusOK, m.RecentMessages(limit))
}

// botDetail GET /api/v1/bots/{name}/detail —— 通道的详细状态
func (s *Server) botDetail(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	switch r.PathValue("name") {
	case "weixin":
		if t, ok := m.Raw("weixin").(*ilink.Transport); ok {
			writeJSON(w, http.StatusOK, t.Detail())
			return
		}
	case "qq":
		if t, ok := m.Raw("qq").(*qq.Transport); ok {
			writeJSON(w, http.StatusOK, t.Detail())
			return
		}
	default:
		badRequest(w, "通道只能是 weixin 或 qq")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "CHANNEL_NOT_MOUNTED", "该通道未挂载")
}

// weixinQRCode POST /api/v1/bots/weixin/qrcode —— 取登录二维码
func (s *Server) weixinQRCode(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	t, ok := m.Raw("weixin").(*ilink.Transport)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "CHANNEL_NOT_MOUNTED", "微信通道未挂载")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	qr, err := t.StartLogin(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "QRCODE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, qr)
}

// weixinLoginStatus GET /api/v1/bots/weixin/qrcode/status —— 轮询扫码结果
func (s *Server) weixinLoginStatus(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	t, ok := m.Raw("weixin").(*ilink.Transport)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "CHANNEL_NOT_MOUNTED", "微信通道未挂载")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	st, err := t.CheckLogin(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "LOGIN_STATUS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// qqCredentials POST /api/v1/bots/qq/credentials —— 运行时配置 QQ 凭证
//
// 前端填完 AppID/Secret 就立刻挂载可用，不必重启服务。
func (s *Server) qqCredentials(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	var req struct {
		AppID        string `json:"appId"`
		ClientSecret string `json:"clientSecret"`
		APIBase      string `json:"apiBase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.AppID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
		badRequest(w, "appId 与 clientSecret 都不能为空")
		return
	}

	// 覆盖挂载（先停旧的，避免两个连接同时跑）
	_ = m.Stop("qq")
	m.Register(qq.New(m, req.AppID, req.ClientSecret, req.APIBase))
	writeJSON(w, http.StatusOK, map[string]any{"mounted": true})
}

// botInject POST /api/v1/bots/inject —— 网页端假装自己在群里说话。
//
// multipart: text（可空）+ file（可多个）。走的是和微信/QQ 完全相同的
// Manager.Handle，所以日期口令、q 退出、改名流程在网页上端 adm行为一致。
// 存在的意义：不用真连一个 IM 账号也能验收整套导入链路。
func (s *Server) botInject(w http.ResponseWriter, r *http.Request) {
	m, ok := s.botManager()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		badRequest(w, "表单解析失败："+err.Error())
		return
	}

	text := strings.TrimSpace(r.FormValue("text"))
	conv := strings.TrimSpace(r.FormValue("conversation"))
	if conv == "" {
		conv = "web-console"
	}

	// 入站附件。内容与文件名一起给 Manager，导入流程只认 .csv。
	atts := make([]bot.Attachment, 0, 2)
	if r.MultipartForm != nil {
		for _, headers := range r.MultipartForm.File {
			for _, fh := range headers {
				if fh.Size > 20<<20 {
					badRequest(w, "单个文件不能超过 20MB")
					return
				}
				f, err := fh.Open()
				if err != nil {
					badRequest(w, "读取上传文件失败")
					return
				}
				data, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil {
					badRequest(w, "读取上传文件失败")
					return
				}
				payload := data // 闭包要抓副本，循环变量会被复用
				atts = append(atts, bot.Attachment{
					FileName: fh.Filename,
					Size:     fh.Size,
					Fetch: func(ctx context.Context) ([]byte, error) {
						return payload, nil
					},
				})
			}
		}
	}

	if text == "" && len(atts) == 0 {
		badRequest(w, "至少要有文字或一个文件")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	// 一次只喂一个附件：与 IM 通道一致（多文件得在群里一次次发），
	// 这样日期口令「还剩几个文件」的计数语义才和线上一致。
	if len(atts) > 1 {
		atts = atts[:1]
	}

	out, err := m.Handle(ctx, bot.Inbound{
		Channel:        "web",
		ConversationID: conv,
		SenderID:       "web",
		Text:           text,
		AtMe:           true,
		Attachments:    atts,
		ReceivedAt:     time.Now(),
	})
	if err != nil {
		internalError(w, err)
		return
	}

	type mediaOut struct {
		Name    string `json:"name"`
		Size    int    `json:"size"`
		DataURL string `json:"dataUrl,omitempty"`
	}
	images := make([]mediaOut, 0, 2)
	collect := func(name string, data []byte) {
		if len(data) == 0 {
			return
		}
		mo := mediaOut{Name: name, Size: len(data)}
		if strings.EqualFold(filepath.Ext(name), ".svg") {
			// SVG 是文本，前端直接塞进 <img> 就行，省一轮 base64
			mo.DataURL = "data:image/svg+xml;utf8," + string(data)
		} else {
			mo.DataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		}
		images = append(images, mo)
	}
	collect(out.ImageName, out.Image)
	for _, img := range out.Images {
		collect(img.Name, img.Data)
	}

	files := make([]mediaOut, 0, len(out.Files))
	for _, f := range out.Files {
		files = append(files, mediaOut{Name: f.Name, Size: len(f.Data)})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"text":         out.Text,
		"conversation": conv,
		"images":       images,
		"files":        files,
	})
}

// botParse POST /api/v1/bots/parse  {"text":"柚子 9月"}
//
// 只做意图解析、不真的发消息。用来验收"群里这句话会怎么被理解"，
// 不用连真实账号也能查——这也是本机没 MySQL/没凭据时唯一能验证的部分。
func (s *Server) botParse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Now    string `json:"now"` // 可选，ISO 日期，便于回放历史说法
		Handle bool   `json:"handle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Text == "" {
		badRequest(w, "text 不能为空")
		return
	}

	now := time.Now()
	if req.Now != "" {
		if t, err := time.ParseInLocation(isoDate, req.Now, time.Local); err == nil {
			now = t
		}
	}

	intent := bot.ParseIntent(req.Text, now)
	result := map[string]any{
		"input":  req.Text,
		"intent": intent,
	}

	// handle=true 时真的走一遍技能路由，产出回复（可能需要数据库）
	if req.Handle {
		m, ok := s.botManager()
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "BOTS_DISABLED", "机器人未启用")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		out, err := m.Handle(ctx, bot.Inbound{
			Channel: "web", ConversationID: "debug", SenderID: "web",
			Text: req.Text, AtMe: true, ReceivedAt: now,
		})
		if err != nil {
			internalError(w, err)
			return
		}
		result["reply"] = map[string]any{
			"text":      out.Text,
			"hasImage":  len(out.Image) > 0,
			"imageName": out.ImageName,
		}
	}

	writeJSON(w, http.StatusOK, result)
}
