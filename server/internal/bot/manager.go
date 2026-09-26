package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"douyin-server/internal/domain"
	"douyin-server/internal/render"
	"douyin-server/internal/repo"
)

// Attachment 消息里的文件附件（CSV 导入用）。
type Attachment struct {
	URL      string // 下载直链（QQ 事件自带 rkey 鉴权参数，无需额外头）
	FileName string
	Size     int64
	// Fetch 优先于 URL：微信 iLink 的文件要走 CDN 下载 + AES 解密，
	// 拿不到一个裸 URL，只能给闭包。没有 Fetch 时按 URL 下载。
	Fetch func(ctx context.Context) ([]byte, error)
}

// Inbound 收到的消息。两个通道（微信 / QQ）统一成这个结构。
type Inbound struct {
	Channel        string // weixin / qq
	ConversationID string // 群 ID 或用户 ID
	SenderID       string
	Text           string
	AtMe           bool
	Attachments    []Attachment
	ReceivedAt     time.Time
}

// Outbound 要发出去的消息。bot 回复以图为主，文字只是补充。
type Outbound struct {
	ConversationID string
	Text           string
	Image          []byte
	ImageName      string
	// Images 多图（日报按性别各一张：女团样式1 + 男团样式2）。
	// 通道按顺序逐张发送；Image 字段保留给单图场景，两者可并存。
	Images []OutboundImage
	// Files 要发回去的文件（CSV 导出等）。两个通道都支持：
	// 微信走 type=4 file_item 上传，QQ 走 /files 的 file_type=4。
	Files []OutboundFile
}

// OutboundImage 一张待发送的图片。
type OutboundImage struct {
	Data []byte
	Name string
}

// OutboundFile 一个待发送的文件（非图片）。
type OutboundFile struct {
	Data []byte
	Name string
}

// Transport 是 IM 通道的抽象。微信 iLink 与 QQ 开放平台各实现一份，
// 共享同一个 Agent（意图解析 + 技能路由），这样两端行为必然一致。
type Transport interface {
	Name() string
	Start(ctx context.Context) error
	Send(ctx context.Context, out Outbound) error
	Receive() <-chan Inbound
	Close() error
}

// ChannelStatus 一个通道的运行状态。
type ChannelStatus struct {
	Name      string `json:"name"`
	Running   bool   `json:"running"`
	Connected bool   `json:"connected"`
	Note      string `json:"note,omitempty"`
}

// Manager 持有两个通道并驱动技能路由。
type Manager struct {
	repo *repo.Repo

	mu         sync.RWMutex
	channels   map[string]*channelState
	push       bool
	log        []LoggedMessage
	pending    map[string]*pendingImport // 导入日期口令，key 是会话 ID
	pendingOps map[string]*pendingOp     // 改名/改号对话，key 是会话 ID
}

// IsQuitCommand 是否「退出当前流程」口令。
//
// 只认孤立的 q/Q/quit 与「退出」「取消」，必须是整条消息——
// 群里有主播艺名是单字母的可能，所以要求完全相等才算。
func IsQuitCommand(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "q", "quit", "退出", "退出。", "算了", "取消", "cancel", "c":
		return true
	}
	return false
}

// cancelConversation 清空该会话正在进行的一切：导入日期口令 + 改名/改号待办。
//
// 什么都没在进行时也要回一句话——用户发了 q 而机器人安静如鸡，
// 看起来跟掉线一模一样。
func (m *Manager) cancelConversation(conv string) string {
	m.mu.Lock()
	pending := m.pending[conv]
	if pending != nil {
		delete(m.pending, conv)
	}
	hasOp := false
	if m.pendingOps != nil {
		if _, ok := m.pendingOps[conv]; ok {
			delete(m.pendingOps, conv)
			hasOp = true
		}
	}
	m.mu.Unlock()

	switch {
	case pending != nil && hasOp:
		return fmt.Sprintf("已退出：取消了「%s」的导入口令，同时放弃了未完成的改名/改号。",
			friendlyDate(pending.date.Format("2006-01-02")))
	case pending != nil:
		return fmt.Sprintf("已退出：取消了「%s」的导入口令。直接发 CSV 就按昨天导入。",
			friendlyDate(pending.date.Format("2006-01-02")))
	case hasOp:
		return "已退出：放弃了未完成的改名/改号。需要的话重新发「改名」或「改号」。"
	default:
		return "当前没有进行中的流程。直接发 CSV 导入昨天的数据；想指定日期就先发「9.1」再传文件。"
	}
}

// pendingImport 615 同款：先发「9.11」记住日期，10 分钟内连传的 CSV 都进该日。
// 最多 2 个文件（音浪 + 时长各一），用满即失效，防止很久后误导入旧日期。
type pendingImport struct {
	date      time.Time
	kinds     map[string]bool
	remaining int
	expiresAt time.Time
}

const (
	pendingImportTTL      = 10 * time.Minute
	pendingImportMaxFiles = 2
)

type channelState struct {
	transport Transport
	running   bool
	connected bool
	note      string
}

// LoggedMessage 消息日志的一行，供前端查看。
type LoggedMessage struct {
	At       time.Time `json:"at"`
	Channel  string    `json:"channel"`
	Dir      string    `json:"dir"` // in / out
	From     string    `json:"from"`
	Text     string    `json:"text"`
	Intent   string    `json:"intent,omitempty"`
	HasImage bool      `json:"hasImage"`
}

// NewManager 构造。通道需要调用 Register 挂载真实实现。
func NewManager(r *repo.Repo) *Manager {
	m := &Manager{
		repo: r,
		channels: map[string]*channelState{
			"weixin": {note: "微信 iLink 适配器待接入"},
			"qq":     {note: "QQ 开放平台适配器待接入"},
		},
		log:     []LoggedMessage{},
		pending: map[string]*pendingImport{},
	}
	// 推送开关持久化在 app_setting：容器天天重启，内存态撑不到凌晨 1 点
	if r != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if enabled, err := r.GetPushEnabled(ctx); err == nil {
			m.push = enabled
		}
	}
	return m
}

// Register 挂载一个通道实现。
func (m *Manager) Register(t Transport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[t.Name()] = &channelState{transport: t}
}

// Raw 返回通道的具体实现，供上层查详情或走扫码登录流程。
// 返回 any 是有意的：两个通道的状态结构不同，由调用方断言。
func (m *Manager) Raw(name string) any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if st, ok := m.channels[name]; ok {
		return st.transport
	}
	return nil
}

// Start 启动通道。没有真实适配器时只标记运行态，避免 /status 说谎。
func (m *Manager) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	st, ok := m.channels[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("未知通道: %s", name)
	}
	if st.transport == nil {
		m.mu.Lock()
		st.running = true
		st.note = "运行（适配器待接入，仅本地意图解析可用）"
		m.mu.Unlock()
		return nil
	}
	if err := st.transport.Start(ctx); err != nil {
		return err
	}
	// connected 不在这里标 true：连接是异步建立的（WS 握手/长轮询首轮），
	// Status() 会实时问通道要，标早了 /status 就说谎。
	m.mu.Lock()
	st.running = true
	st.note = ""
	m.mu.Unlock()
	return nil
}

// Stop 停止通道。
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	st, ok := m.channels[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("未知通道: %s", name)
	}
	if st.transport != nil {
		if err := st.transport.Close(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	st.running = false
	st.connected = false
	m.mu.Unlock()
	return nil
}

// Status 返回所有通道状态。
//
// connected 优先问通道本身（WS/长轮询是异步建立的，Start 返回时往往还没连上），
// 通道没实现 Connected() 的才退回 Start 时的快照。
func (m *Manager) Status() []ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ChannelStatus, 0, len(m.channels))
	for name, st := range m.channels {
		connected := st.connected
		if cc, ok := st.transport.(interface{ Connected() bool }); ok {
			connected = cc.Connected()
		}
		out = append(out, ChannelStatus{Name: name, Running: st.running, Connected: connected, Note: st.note})
	}
	return out
}

// PushEnabled 日报推送开关。
func (m *Manager) PushEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.push
}

// SetPush 切换每日索要提醒，并持久化到 app_setting。
func (m *Manager) SetPush(enabled bool) {
	m.mu.Lock()
	m.push = enabled
	m.mu.Unlock()
	if m.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.repo.SetPushEnabled(ctx, enabled); err != nil {
			// 持久化失败只影响重启后的默认值，不回滚内存态
			m.appendLog(LoggedMessage{At: time.Now(), Channel: "all", Dir: "out",
				From: "系统", Text: "提醒开关持久化失败：" + err.Error(), Intent: "remind"})
		}
	}
}

// reminderHour 每天几点自动向活跃会话索要 CSV 文件。
const reminderHour = 1

// ReminderText 定时/手动索要文件时发的文案。
// X 日 = 昨天：数据是 T+1 的，1 号凌晨 1 点要的是上月末那天的文件。
func ReminderText(now time.Time) string {
	return fmt.Sprintf("请发送%s的音浪文件即可", friendlyDate(now.AddDate(0, 0, -1).Format("2006-01-02")))
}

// StartReminderLoop 每天 reminderHour 点向所有活跃会话索要 CSV 文件。
// 只在「提醒开关」开启时发送（机器人页可开关，持久化）；重启后循环重建，不丢调度。
func (m *Manager) StartReminderLoop(ctx context.Context) {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), reminderHour, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(next.Sub(now)):
			}
			if !m.PushEnabled() {
				continue // 开关关着：跳过本次，明天再看
			}
			res := m.RemindAll(ctx, ReminderText(time.Now()))
			m.appendLog(LoggedMessage{At: time.Now(), Channel: "all", Dir: "out",
				From: "每日提醒", Text: ReminderText(time.Now()) + "\n（" + strings.Join(res, "；") + "）",
				Intent: "remind"})
		}
	}()
}

// Broadcaster 由支持「向所有活跃会话群发」的通道实现。
// 定义在 bot 包（transport 反过来 import bot，这里不能直接引子包）。
type Broadcaster interface {
	// RemindAll 群发文本，返回成功/失败条数。
	RemindAll(ctx context.Context, text string) (sent, failed int)
}

// RemindAll 立即向所有有会话上下文的群/用户发一条文本，
// 返回各通道的发送结果（供网页「立即索要」按钮展示）。
func (m *Manager) RemindAll(ctx context.Context, text string) []string {
	m.mu.RLock()
	ts := make([]Transport, 0, len(m.channels))
	for _, st := range m.channels {
		if st.transport != nil {
			ts = append(ts, st.transport)
		}
	}
	m.mu.RUnlock()

	out := make([]string, 0, len(ts))
	for _, t := range ts {
		b, ok := t.(Broadcaster)
		if !ok {
			continue
		}
		sent, failed := b.RemindAll(ctx, text)
		label := t.Name()
		switch label {
		case "weixin":
			label = "微信"
		case "qq":
			label = "QQ"
		}
		out = append(out, fmt.Sprintf("%s 成功 %d / 失败 %d", label, sent, failed))
		m.appendLog(LoggedMessage{At: time.Now(), Channel: t.Name(), Dir: "out",
			From: "全部会话", Text: text, Intent: "remind"})
	}
	return out
}

// RecentMessages 返回最近的消息（新的在前）。
func (m *Manager) RecentMessages(limit int) []LoggedMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > len(m.log) {
		limit = len(m.log)
	}
	out := make([]LoggedMessage, 0, limit)
	for i := len(m.log) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.log[i])
	}
	return out
}

func (m *Manager) appendLog(msg LoggedMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, msg)
	if len(m.log) > 200 {
		m.log = m.log[len(m.log)-200:]
	}
}

// Handle 处理一条入站消息并给出回复。
//
// 这是 Agent 的入口：解析意图 → 路由技能 → 产出文字与图。
// 目前日报技能能出真实图（复用 internal/render），其余先回文字。
func (m *Manager) Handle(ctx context.Context, in Inbound) (Outbound, error) {
	now := time.Now()

	// 有文件附件：CSV 导入流程优先于文字意图（615 同款顺序）。
	if len(in.Attachments) > 0 {
		out, err := m.handleInboundFile(ctx, in, now)
		intent := "import_csv"
		if err != nil {
			// 出错也必须回复：用户发了文件石沉大海比报错更糟。
			// 细节进日志，回复给一句人话。
			intent = "import_error"
			out.Text = "处理失败：" + err.Error()
		}
		m.appendLog(LoggedMessage{
			At: now, Channel: in.Channel, Dir: "in",
			From: in.SenderID, Text: strings.TrimSpace(in.Text + " [文件]"), Intent: intent,
		})
		m.appendLog(LoggedMessage{
			At: time.Now(), Channel: in.Channel, Dir: "out",
			From: in.ConversationID, Text: out.Text, Intent: intent,
		})
		return out, nil
	}

	// q / 退出：放弃本会话当前进行的一切。放在意图解析之前——
	// 用户在流程里敲 q 就是想跳出来，不该被当成艺名或日期去解析。
	if IsQuitCommand(in.Text) {
		out := Outbound{ConversationID: in.ConversationID, Text: m.cancelConversation(in.ConversationID)}
		m.appendLog(LoggedMessage{At: now, Channel: in.Channel, Dir: "in",
			From: in.SenderID, Text: in.Text, Intent: string(IntentQuit)})
		m.appendLog(LoggedMessage{At: time.Now(), Channel: in.Channel, Dir: "out",
			From: in.ConversationID, Text: out.Text, Intent: string(IntentQuit)})
		return out, nil
	}

	intent := ParseIntent(in.Text, now)

	out := Outbound{ConversationID: in.ConversationID}

	// 改名/改号流程：进行中的对话优先消费消息；然后是入口关键词；
	// 最后裸抖音号（命中库内账号）触发改名。
	if reply, handled := m.handlePendingOp(ctx, in.ConversationID, in.Text); handled {
		out.Text = reply
		m.appendLog(LoggedMessage{At: time.Now(), Channel: in.Channel, Dir: "out",
			From: in.ConversationID, Text: reply, Intent: "rename_flow"})
		return out, nil
	}
	if reply, handled := m.tryOpStart(ctx, in.ConversationID, in.Text); handled {
		m.appendLog(LoggedMessage{At: now, Channel: in.Channel, Dir: "in",
			From: in.SenderID, Text: in.Text, Intent: "rename_flow"})
		out.Text = reply
		m.appendLog(LoggedMessage{At: time.Now(), Channel: in.Channel, Dir: "out",
			From: in.ConversationID, Text: reply, Intent: "rename_flow"})
		return out, nil
	}
	if reply, handled := m.tryStartRenameByNumber(ctx, in.ConversationID, in.Text); handled {
		out.Text = reply
		m.appendLog(LoggedMessage{At: now, Channel: in.Channel, Dir: "in",
			From: in.SenderID, Text: in.Text, Intent: "rename_flow"})
		m.appendLog(LoggedMessage{At: time.Now(), Channel: in.Channel, Dir: "out",
			From: in.ConversationID, Text: reply, Intent: "rename_flow"})
		return out, nil
	}

	m.appendLog(LoggedMessage{
		At: now, Channel: in.Channel, Dir: "in",
		From: in.SenderID, Text: in.Text, Intent: string(intent.Kind),
	})

	switch intent.Kind {
	case IntentHelp:
		out.Text = HelpText()
		return out, nil

	case IntentImportLogs:
		logs, err := m.repo.ListImportLogs(ctx, 5)
		if err != nil {
			out.Text = "读取导入记录失败：" + err.Error()
			return out, nil
		}
		if len(logs) == 0 {
			out.Text = "还没有导入记录。发 CSV 即可导入（音浪 + 时长两个文件）。"
			return out, nil
		}
		kindLabel := map[string]string{"wave": "音浪", "duration": "时长"}
		sourceLabel := map[string]string{"bot": "机器人", "web": "网页"}
		lines := make([]string, 0, len(logs))
		for i, log := range logs {
			lines = append(lines, fmt.Sprintf("%d. %s %s %s：%d 条（匹配 %d/未匹配 %d） %s %s",
				i+1, friendlyDate(log.ImportDate.Format("2006-01-02")), kindLabel[log.Kind], log.FileName,
				log.RowCount, log.MatchedCount, log.UnmatchedCount,
				sourceLabel[log.Source], log.CreatedAt.Format("01-02 15:04")))
		}
		out.Text = fmt.Sprintf("最近 %d 次导入：\n%s", len(logs), strings.Join(lines, "\n"))
		return out, nil

	case IntentImportDate:
		m.rememberImportDate(in.ConversationID, intent.Date)
		out.Text = fmt.Sprintf(
			"已记住导入日期 %s（10 分钟内有效，可连传 %d 个文件）。请依次发送音浪与时长 CSV；不想导了发 q。",
			friendlyDate(intent.Date), pendingImportMaxFiles)
		// 指定了今天之后的日子：说明一句，但不替用户改——写哪天由他定。
		if t := parseOrNow(intent.Date, now); t.After(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
			out.Text += fmt.Sprintf("\n提醒：%s 还没到，音浪一般次日才出。", friendlyDate(intent.Date))
		}

	case IntentAddAnchor:
		o, aerr := m.handleAddAnchor(ctx, intent.Query, intent.DouyinNo)
		if aerr != nil {
			return o, aerr
		}
		out = o

	case IntentDailyReport:
		date := parseOrYesterday(intent.Date, now)
		images, err := m.buildDailyReportImages(ctx, date, intent.Gender)
		if err != nil {
			return out, err
		}
		if len(images) == 0 {
			out.Text = fmt.Sprintf("%s 暂无榜单数据，可能还没导入；先发 CSV 把数据补上。",
				date.Format("1月2日"))
			break
		}
		out.Images = images
		out.Text = fmt.Sprintf("%s 日榜（%d 张）", date.Format("1月2日"), len(images))

	case IntentMonthlyReport:
		period := intent.Period
		if period == "" {
			period = now.Format("2006-01")
		}
		rows, err := m.repo.ListMonthlyByPeriod(ctx, period, domain.Gender(intent.Gender))
		if err != nil {
			return out, err
		}
		out.Text = fmt.Sprintf("%s 月榜：%d 人，月音浪合计 %s", period, len(rows), sumWave(rows))
		out.Text += "\n（月榜图片模板待迁移）"

	case IntentYearlyReport:
		year := intent.Year
		if year == 0 {
			year = now.Year()
		}
		out.Text = fmt.Sprintf("%d 年度汇总：图片模板待迁移", year)

	case IntentPersonQuery:
		out.Text = fmt.Sprintf("查询「%s」（单人卡片模板待迁移）", intent.Query)

	case IntentDailyStar:
		out.Text = "每日之星：图片模板待迁移"

	case IntentPKGroup:
		out.Text = "PK 分组：图片模板待迁移"

	case IntentPushToggle:
		m.SetPush(intent.Enable)
		if intent.Enable {
			out.Text = "已开启：每天 1 点自动向大家索要 CSV 文件"
		} else {
			out.Text = "已关闭：不再定时索要 CSV 文件"
		}

	case IntentPushStatus:
		if m.PushEnabled() {
			out.Text = "每日 1 点索要 CSV：已开启"
		} else {
			out.Text = "每日 1 点索要 CSV：已关闭"
		}

	default:
		out.Text = "没听懂。发「帮助」看指令。"
	}

	m.appendLog(LoggedMessage{
		At: time.Now(), Channel: in.Channel, Dir: "out",
		From: in.ConversationID, Text: out.Text, Intent: string(intent.Kind),
		HasImage: len(out.Image) > 0 || len(out.Images) > 0,
	})
	return out, nil
}

// botDailyPageSize 每张日报图的行数上限，与网页导出接口的默认值一致。
// 50 行/页：男团 90+ 人两张，女团一张——30 行会切成 4 张，太碎。
const botDailyPageSize = 50

// buildDailyReportImages 生成日报图：女团一张（经典样式），男团按人数分页
// （苹果样式，超过 botDailyPageSize 行自动切成多张，页脚带 N/M）。
// 没指定性别就两个团都出；指定了只出该团。
func (m *Manager) buildDailyReportImages(ctx context.Context, date time.Time, gender string) ([]OutboundImage, error) {
	genders := []string{"female", "male"}
	if gender == "female" || gender == "male" {
		genders = []string{gender}
	}

	images := make([]OutboundImage, 0, 2)
	for _, g := range genders {
		rows, err := m.repo.ListDailyByDate(ctx, date, domain.Gender(g))
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue // 这个团当天没数据就不占一张图
		}

		all := make([]render.Row, 0, len(rows))
		for _, d := range rows {
			tier := ""
			if d.Tier != nil {
				tier = *d.Tier
			}
			master := ""
			if d.MasterName != nil {
				master = *d.MasterName
			}
			all = append(all, render.Row{
				Name: d.Name, DailyWave: d.Wave, TotalWave: d.CumulativeWave,
				DurationMinutes: d.Minutes, Tier: tier, IsLive: d.IsLive, MasterName: master,
			})
		}

		pageCount := (len(all) + botDailyPageSize - 1) / botDailyPageSize
		for p := 1; p <= pageCount; p++ {
			start := (p - 1) * botDailyPageSize
			end := p * botDailyPageSize
			if end > len(all) {
				end = len(all)
			}
			report := render.Report{
				Date: date.Format("2006-01-02"), Gender: g,
				Rows: all[start:end], Stats: all,
				Columns:   render.DefaultColumns(),
				PageIndex: p, PageCount: pageCount,
				ShowInactiveFooter: pageCount <= 1 || p == pageCount,
			}
			png, err := render.RenderPNG(report, render.ResolveStyle(g, ""))
			if err != nil {
				return nil, fmt.Errorf("生成日报图片失败: %w", err)
			}
			name := fmt.Sprintf("日报-%s-%s", date.Format("2006-01-02"), genderLabel(g))
			if pageCount > 1 {
				name += fmt.Sprintf("-%d", p)
			}
			images = append(images, OutboundImage{Data: png, Name: name + ".png"})
		}
	}
	return images, nil
}

func genderLabel(g string) string {
	if g == "female" {
		return "女团"
	}
	return "男团"
}

func sumWave(rows []domain.MonthlyMetric) string {
	var total int64
	for _, r := range rows {
		total += r.Wave
	}
	return render.FormatWave(total)
}

// parseOrYesterday 解析日期，空则昨天。
// 日报/每日之星默认 T-1：24 号发的是 23 号的数据，用今天查只会出空榜。
func parseOrYesterday(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback.AddDate(0, 0, -1)
	}
	return parseOrNow(s, fallback)
}

func parseOrNow(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return fallback
	}
	return t
}

// HelpText 指令菜单。
func HelpText() string {
	return strings.Join([]string{
		"可用指令：",
		"· 日报 / 每日报告 —— 昨天榜单图（数据 T+1，24 号发的是 23 号）",
		"· 今天 / 昨天 / 18号报告 / 9月11日报 —— 指定某天",
		"· 9.1 / 9月1日 / 1号 —— 预告导入日，随后传的 CSV 全部进该日（指定哪天就是哪天）",
		"· q —— 退出当前流程：作废导入日期口令、放弃改名/改号",
		"· 9月 / 2026年3月 —— 月榜",
		"· 2026年 —— 年度汇总",
		"· 艺名 —— 查某位主播",
		"· 艺名 9月 —— 查该主播某月",
		"· 姓名-抖音号 —— 添加主播（如 柚子-123456）",
		"· 直接发 CSV 文件 —— 默认导入昨天",
		"· 改名 —— 发抖音号，再回复新名字",
		"· 改号 —— 发姓名（多个号会让你挑），再回复新抖音号",
		"· 开启/关闭日报推送 —— 每天 1 点自动索要 CSV 的提醒开关",
		"· 帮助 —— 本菜单",
	}, "\n")
}
