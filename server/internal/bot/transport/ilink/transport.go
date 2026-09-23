package ilink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"douyin-server/internal/bot"
)

// Handler 处理入站消息。由 bot.Manager 实现。
type Handler interface {
	Handle(ctx context.Context, in bot.Inbound) (bot.Outbound, error)
}

// sessionCtx 是回复一条消息需要的信息。
// 615 把它存在 this.contexts 里（按 conversationId 索引），这里照做——
// 因为 iLink 发消息必须带上原始消息的 context_token。
type sessionCtx struct {
	toUserID     string
	groupId      string
	contextToken string
}

// Phase 登录/运行阶段，与 615 的 WeixinBotStatus.phase 对齐。
type Phase string

const (
	PhaseDisconnected   Phase = "disconnected"
	PhaseAwaitingScan   Phase = "awaiting_scan"
	PhaseScanned        Phase = "scanned"
	PhaseRunning        Phase = "running"
	PhaseStopped        Phase = "stopped"
	PhaseSessionExpired Phase = "session_expired"
	PhaseError          Phase = "error"
)

// Transport 实现 bot.Transport。一个实例对应一个微信账号。
type Transport struct {
	handler Handler
	client  *Client

	mu        sync.RWMutex
	running   bool
	connected bool
	phase     Phase
	note      string

	qrCode    string
	qrCodeURL string
	qrExpires time.Time
	nickname  string

	cursor   string
	received int
	sent     int
	lastMsg  string
	lastPoll time.Time

	sessions map[string]sessionCtx
	inbox    chan bot.Inbound
	cancel   context.CancelFunc
	started  bool
}

// New 构造 Transport。token 为空则只能走扫码登录。
func New(handler Handler, baseURL, token string) (*Transport, error) {
	client, err := NewClient(baseURL, token)
	if err != nil {
		return nil, err
	}
	return &Transport{
		handler:  handler,
		client:   client,
		phase:    PhaseDisconnected,
		sessions: map[string]sessionCtx{},
		inbox:    make(chan bot.Inbound, 32),
	}, nil
}

// Name 实现 bot.Transport。
func (t *Transport) Name() string { return "weixin" }

// Receive 实现 bot.Transport。
func (t *Transport) Receive() <-chan bot.Inbound { return t.inbox }

// Close 实现 bot.Transport。
func (t *Transport) Close() error {
	t.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	t.running = false
	t.connected = false
	t.phase = PhaseStopped
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// Start 启动长轮询。没有 token 时直接报错，避免启动一个永远收不到消息的空壳。
func (t *Transport) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	if t.client.token == "" {
		t.phase = PhaseAwaitingScan
		t.note = "尚未登录，请先扫码"
		t.mu.Unlock()
		return errors.New("微信未登录：请先扫码连接")
	}
	// 同 QQ 通道：不能用 r.Context()，start 接口返回后它会被取消，
	// 长轮询会静默死亡。
	runCtx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.running = true
	t.connected = true
	t.phase = PhaseRunning
	t.note = ""
	t.mu.Unlock()

	go t.pollLoop(runCtx)
	return nil
}

// pollLoop 长轮询收消息。超时时间跟服务端下发的 longpolling_timeout_ms 走。
func (t *Transport) pollLoop(ctx context.Context) {
	timeout := 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		updates, err := t.client.GetUpdates(ctx, t.currentCursor(), timeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var expired *SessionExpiredError
			if errors.As(err, &expired) {
				t.setPhase(PhaseSessionExpired, expired.Error())
				return
			}
			t.setNote(err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		// 服务端建议的超时：留 3 秒余量，并夹在 5~65 秒之间
		if updates.LongPollingTimeoutMS > 0 {
			suggested := time.Duration(updates.LongPollingTimeoutMS)*time.Millisecond + 3*time.Second
			if suggested < 5*time.Second {
				suggested = 5 * time.Second
			}
			if suggested > 65*time.Second {
				suggested = 65 * time.Second
			}
			timeout = suggested
			t.client.http.Timeout = timeout
		}

		for _, msg := range updates.Msgs {
			if msg.MessageType != 1 {
				continue // 只处理入站消息
			}
			t.handleMessage(ctx, msg)
		}

		if updates.GetUpdatesBuf != "" {
			t.setCursor(updates.GetUpdatesBuf)
		}
		t.markPolled()
	}
}

// handleMessage 转成 bot.Inbound 交给 Agent，再把回复发回去。
func (t *Transport) handleMessage(ctx context.Context, msg InboundMessage) {
	fromUserID := msg.FromUserID
	if fromUserID == "" {
		return
	}
	conversationID := msg.ConversationID()

	// 记下回复所需的信息
	if msg.ContextToken != "" {
		t.mu.Lock()
		t.sessions[conversationID] = sessionCtx{
			toUserID:     fromUserID,
			groupId:      msg.GroupID,
			contextToken: msg.ContextToken,
		}
		t.mu.Unlock()
	}

	text := msg.Text()
	if text == "" {
		return
	}
	t.countReceived(text)

	out, err := t.handler.Handle(ctx, bot.Inbound{
		Channel:        t.Name(),
		ConversationID: conversationID,
		SenderID:       fromUserID,
		Text:           text,
		AtMe:           true,
		ReceivedAt:     time.Now(),
	})
	if err != nil {
		t.setNote("处理消息失败: " + err.Error())
		return
	}

	if err := t.Send(ctx, out); err != nil {
		t.setNote("发送失败: " + err.Error())
	}
}

// Send 实现 bot.Transport。文字直发；图片先走 getuploadurl + CDN 上传
// （与 615 的 weixin-bot-media.js 同款流程），再以 type=2 的 item 发出。
func (t *Transport) Send(ctx context.Context, out bot.Outbound) error {
	t.mu.RLock()
	sc, ok := t.sessions[out.ConversationID]
	t.mu.RUnlock()
	if !ok {
		return fmt.Errorf("找不到会话 %s 的回复上下文，无法发送", out.ConversationID)
	}

	images := make([]bot.OutboundImage, 0, len(out.Images)+1)
	images = append(images, out.Images...)
	if len(out.Image) > 0 {
		images = append(images, bot.OutboundImage{Data: out.Image, Name: out.ImageName})
	}

	if len(images) == 0 {
		if out.Text == "" {
			return nil
		}
		if err := t.client.SendText(ctx, sc.toUserID, sc.groupId, sc.contextToken, out.Text); err != nil {
			return err
		}
	} else {
		// 先发文字说明，再逐张上传发送图片
		if out.Text != "" {
			if err := t.client.SendText(ctx, sc.toUserID, sc.groupId, sc.contextToken, out.Text); err != nil {
				return err
			}
		}
		for i, img := range images {
			name := img.Name
			if name == "" {
				name = fmt.Sprintf("image-%d.png", i+1)
			}
			item, err := t.client.uploadImage(ctx, img.Data, sc.toUserID, name)
			if err != nil {
				return fmt.Errorf("上传 %s 失败: %w", name, err)
			}
			if err := t.client.SendItems(ctx, sc.toUserID, sc.groupId, sc.contextToken, []outItem{item}); err != nil {
				return fmt.Errorf("发送 %s 失败: %w", name, err)
			}
		}
	}

	t.mu.Lock()
	t.sent++
	t.mu.Unlock()
	return nil
}

/* ------------------------------- 扫码登录 ------------------------------- */

// QRCode 登录二维码。
type QRCode struct {
	QRCode    string    `json:"qrcode"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// StartLogin 取一张登录二维码。
func (t *Transport) StartLogin(ctx context.Context) (*QRCode, error) {
	out, err := t.client.GetBotQRCode(ctx, "3")
	if err != nil {
		return nil, err
	}

	qr := &QRCode{
		QRCode: firstOf(out, "qrcode", "qr_code", "ticket"),
		URL:    firstOf(out, "qrcode_url", "qr_code_url", "url", "qrcode_img"),
	}
	t.mu.Lock()
	t.qrCode = qr.QRCode
	t.qrCodeURL = qr.URL
	t.phase = PhaseAwaitingScan
	t.note = "等待扫码"
	t.mu.Unlock()

	return qr, nil
}

// LoginStatus 扫码结果。
type LoginStatus struct {
	Phase    Phase  `json:"phase"`
	Scanned  bool   `json:"scanned"`
	LoggedIn bool   `json:"loggedIn"`
	Nickname string `json:"nickname,omitempty"`
	Note     string `json:"note,omitempty"`
}

// CheckLogin 查一次扫码状态。扫完并确认后会把 token 写进客户端。
func (t *Transport) CheckLogin(ctx context.Context) (*LoginStatus, error) {
	t.mu.RLock()
	qrCode := t.qrCode
	t.mu.RUnlock()
	if qrCode == "" {
		return nil, errors.New("没有进行中的登录，请先取二维码")
	}

	st, err := t.client.GetQRCodeStatus(ctx, qrCode)
	if err != nil {
		return nil, err
	}

	res := &LoginStatus{Phase: PhaseAwaitingScan}
	switch st.Status {
	case "confirmed", "success", "logined", "logged_in":
		if st.Token == "" {
			return nil, errors.New("扫码已确认但未返回 token")
		}
		t.client.SetToken(st.Token)
		if st.BaseURL != "" {
			if c, err := NewClient(st.BaseURL, st.Token); err == nil {
				t.client = c
			}
		}
		res.Phase = PhaseRunning
		res.Scanned = true
		res.LoggedIn = true
		res.Nickname = st.Nickname
		t.mu.Lock()
		t.phase = PhaseRunning
		t.connected = true
		t.nickname = st.Nickname
		t.note = ""
		t.mu.Unlock()
	case "scanned", "scaned":
		res.Scanned = true
		res.Note = "已扫码，请在手机上确认"
		t.setPhase(PhaseScanned, res.Note)
	case "expired", "timeout":
		res.Note = "二维码已过期，请刷新"
		t.setPhase(PhaseDisconnected, res.Note)
	default:
		res.Note = "等待扫码"
	}
	return res, nil
}

// Token 返回当前 token（供持久化）。
func (t *Transport) Token() string { return t.client.token }

/* -------------------------------- 状态 -------------------------------- */

// Detail 给前端展示的详细信息。
type Detail struct {
	Phase     Phase     `json:"phase"`
	Note      string    `json:"note,omitempty"`
	QRCodeURL string    `json:"qrCodeUrl,omitempty"`
	Nickname  string    `json:"nickname,omitempty"`
	Received  int       `json:"received"`
	Sent      int       `json:"sent"`
	LastMsg   string    `json:"lastMessage,omitempty"`
	LastPoll  time.Time `json:"lastPollAt,omitempty"`
	LoggedIn  bool      `json:"loggedIn"`
}

// Connected 实时连接状态（是否在长轮询中）。Manager 汇总 /bots/status 用。
func (t *Transport) Connected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.phase == PhaseRunning
}

// Detail 返回当前状态。
func (t *Transport) Detail() Detail {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Detail{
		Phase:     t.phase,
		Note:      t.note,
		QRCodeURL: t.qrCodeURL,
		Nickname:  t.nickname,
		Received:  t.received,
		Sent:      t.sent,
		LastMsg:   t.lastMsg,
		LastPoll:  t.lastPoll,
		LoggedIn:  t.client.token != "",
	}
}

func (t *Transport) currentCursor() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cursor
}

func (t *Transport) setCursor(v string) {
	t.mu.Lock()
	t.cursor = v
	t.mu.Unlock()
}

func (t *Transport) setPhase(p Phase, note string) {
	t.mu.Lock()
	t.phase = p
	t.note = note
	if p != PhaseRunning {
		t.connected = false
	}
	t.mu.Unlock()
}

func (t *Transport) setNote(note string) {
	t.mu.Lock()
	t.note = note
	t.mu.Unlock()
}

func (t *Transport) markPolled() {
	t.mu.Lock()
	t.lastPoll = time.Now()
	t.mu.Unlock()
}

func (t *Transport) countReceived(text string) {
	t.mu.Lock()
	t.received++
	t.lastMsg = text
	t.mu.Unlock()
}
