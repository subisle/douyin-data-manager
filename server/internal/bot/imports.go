// CSV 导入技能：群里直接传文件入库，流程照抄 615 的 weixin-bot-commands.js。
//
// 日期解析规则（必须一致，运营已经按这个习惯用了很久）：
//  1. 消息文字明确指定（如「24号数据」）→ 该日
//  2. 会话在 10 分钟内预告过导入日（先发「9.11」再传文件）→ 预告日，最多 2 个文件
//  3. 都没有 → 默认昨天
//
// 刻意不读文件名里的日期：22 号发送的 CSV 不代表数据是 22 号的。
package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"douyin-server/internal/csvparse"
	"douyin-server/internal/render"
	"douyin-server/internal/repo"
)

const maxDownloadBytes = 20 << 20 // 20MB，QQ 附件直链也按这个限

var downloadClient = &http.Client{Timeout: 60 * time.Second}

// importSummary 索要会话的累计汇总：多工会多文件导完后合并汇报
// 「多少主播、多少音浪、前三名」。
type importSummary struct {
	persons   map[string]string // anchorID -> 展示名（CSV 姓名，缺了用 ID）
	waves     map[string]int64  // anchorID -> 音浪值
	waveTotal int64
}

func newImportSummary() *importSummary {
	return &importSummary{persons: map[string]string{}, waves: map[string]int64{}}
}

// add 把一次导入的匹配行并入汇总。kind=wave 时累计音浪，时长只计人头。
func (s *importSummary) add(preview repo.ImportPreview, kind csvparse.Kind) {
	for _, row := range preview.Rows {
		if row.PersonID == nil || row.AnchorID == "" {
			continue // 只统计真正入库的行
		}
		name := row.Name
		if name == "" {
			name = row.AnchorID
		}
		s.persons[row.AnchorID] = name
		if kind == csvparse.KindWave {
			if prev, ok := s.waves[row.AnchorID]; !ok || row.Next > prev {
				s.waveTotal += row.Next - prev
				s.waves[row.AnchorID] = row.Next
			}
		}
	}
}

// build 生成汇总文案；没有任何入库行时返回空串。
func (s *importSummary) build() string {
	if len(s.persons) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📦 本次共导入 %d 位主播的数据", len(s.persons))
	if s.waveTotal > 0 {
		fmt.Fprintf(&b, "，音浪合计 %s", render.FormatWave(s.waveTotal))
	}
	b.WriteString("。")

	type kv struct {
		name string
		wave int64
	}
	top := make([]kv, 0, 3)
	for id, w := range s.waves {
		top = append(top, kv{name: s.persons[id], wave: w})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].wave > top[j].wave })
	if len(top) > 3 {
		top = top[:3]
	}
	if len(top) > 0 {
		b.WriteString("\n🏆 前三名：")
		medals := []string{"🥇", "🥈", "🥉"}
		items := make([]string, 0, len(top))
		for i, t := range top {
			items = append(items, fmt.Sprintf("%s%s %s", medals[i], t.name, render.FormatWave(t.wave)))
		}
		b.WriteString(strings.Join(items, "、"))
	}
	return b.String()
}

// rememberImportDate 记住某会话的导入日期口令。
func (m *Manager) rememberImportDate(conversationID, date string) {
	d, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[conversationID] = &pendingImport{
		date:      d,
		kinds:     map[string]bool{},
		remaining: pendingImportMaxFiles,
		expiresAt: time.Now().Add(pendingImportTTL),
	}
}

// resolveInboundImportDate 决定这批文件进哪一天。返回 (日期, 来源, 口令)。
func (m *Manager) resolveInboundImportDate(conversationID string) (time.Time, string, *pendingImport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending[conversationID]
	if p != nil && time.Now().Before(p.expiresAt) && p.remaining > 0 {
		return p.date, "pending", p
	}
	if p != nil {
		delete(m.pending, conversationID) // 过期或用满，清掉
	}
	return time.Now().AddDate(0, 0, -1), "yesterday", nil
}

// consumePending 扣掉一个文件名额并记录本次文件类型。
func (m *Manager) consumePending(p *pendingImport, kind csvparse.Kind) {
	if p == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p.remaining--
	p.kinds[string(kind)] = true
	if p.remaining <= 0 {
		for id, v := range m.pending {
			if v == p {
				delete(m.pending, id)
			}
		}
	}
}

// handleInboundFile 处理带附件的消息：下载 → 校验 → 解析 → 匹配 → 入库 → 汇报。
func (m *Manager) handleInboundFile(ctx context.Context, in Inbound, now time.Time) (Outbound, error) {
	out := Outbound{ConversationID: in.ConversationID}

	att := in.Attachments[0] // 一次处理一个，多发的让用户重传
	data, err := fetchAttachment(ctx, att)
	if err != nil {
		out.Text = "附件下载失败：" + err.Error()
		return out, nil
	}
	if !strings.HasSuffix(strings.ToLower(att.FileName), ".csv") {
		out.Text = "请发送 CSV 格式的音浪或时长文件。\n默认导入到昨天；若要指定日期，请先发「24号数据」再传文件。\n（发了日期又不想导了：回一个「q」即可退出。）"
		return out, nil
	}

	text, err := decodeCSV(data)
	if err != nil {
		out.Text = "CSV 编码识别失败（UTF-8 与 GBK 都解不开）。"
		return out, nil
	}

	kind, rows, err := csvparse.Parse(strings.NewReader(text))
	if err != nil {
		out.Text = "CSV 解析失败：" + err.Error()
		return out, nil
	}
	if len(rows) == 0 {
		out.Text = "CSV 里没有有效数据行。"
		return out, nil
	}

	// 主播名单 CSV（表头有 姓名/昵称 + 抖音号，没有音浪/时长列）→ 批量建档绑号
	if kind == csvparse.KindAnchors {
		return m.handleAnchorCSV(ctx, rows)
	}

	if kind != csvparse.KindWave && kind != csvparse.KindDuration {
		out.Text = "看不出这是音浪表还是时长表（表头没有音浪/时长列）。"
		return out, nil
	}

	date, source, pending := m.resolveInboundImportDate(in.ConversationID)

	// 去重账本仅作记录（InsertImportRecord 是 upsert），不做硬拦截：
	// 快照按（主播, 日期）upsert，重复导入相同数据是幂等的，允许重发。
	fileHash, dataHash := repo.ComputeImportHashes(data, rows, kind)

	preview, err := m.repo.BuildImportPreview(ctx, rows, kind, date)
	if err != nil {
		return out, fmt.Errorf("构建导入预览: %w", err)
	}

	// 「有效行」= 解析出数据行的数量（非法行另列），与 615 文案口径一致。
	validRows := len(preview.Rows) - preview.Skipped
	matched := validRows - preview.Unmatched - preview.Duplicate

	// 非法行原因示例：帮运营自己看出问题（列名不认识 / 数值解析失败 / 缺 ID）
	errSamples := make([]string, 0, 3)
	seenErr := map[string]bool{}
	for _, row := range preview.Rows {
		if row.Err == "" || seenErr[row.Err] {
			continue
		}
		seenErr[row.Err] = true
		loc := ""
		if row.RawIndex > 0 {
			loc = fmt.Sprintf("第%d行：", row.RawIndex)
		}
		errSamples = append(errSamples, "· "+loc+row.Err)
		if len(errSamples) >= 3 {
			break
		}
	}

	batchID, err := m.repo.CreateBatch(ctx, date, "csv", "bot:"+in.Channel)
	if err != nil {
		return out, fmt.Errorf("创建导入批次: %w", err)
	}

	var affected int
	var skipped []string
	if kind == csvparse.KindDuration {
		affected, err = m.repo.ApplyDurationImport(ctx, preview.Rows, date, batchID)
	} else {
		affected, skipped, err = m.repo.ApplyImport(ctx, preview.Rows, kind, date, batchID)
	}
	if err != nil {
		_ = m.repo.FinishBatch(ctx, batchID, 0, err.Error())
		return out, fmt.Errorf("写入快照: %w", err)
	}

	// 只重算涉及的人（与 web 导入一致）
	personIDs := map[uint64]bool{}
	for _, row := range preview.Rows {
		if row.PersonID != nil {
			personIDs[*row.PersonID] = true
		}
	}
	for id := range personIDs {
		if err := m.repo.RecomputePerson(ctx, id, date, date); err != nil {
			_ = m.repo.FinishBatch(ctx, batchID, affected, err.Error())
			return out, fmt.Errorf("重算指标: %w", err)
		}
	}
	if err := m.repo.FinishBatch(ctx, batchID, affected, ""); err != nil {
		return out, fmt.Errorf("收尾导入批次: %w", err)
	}

	// 数据更新了：安排自动日报（防抖合并音浪+时长两份，只推一次）
	m.ScheduleAutoReport(date)

	// 记一笔导入账（与 615 共用 import_records 表，双端互相去重）
	if err := m.repo.InsertImportRecord(ctx, string(kind), date,
		fileHash, dataHash, att.FileName, affected, &repo.ImportStats{
			Source:    "bot",
			Matched:   matched,
			Unmatched: preview.Unmatched,
			Duplicate: preview.Duplicate,
		}); err != nil {
		// 账没记上不算失败：数据已入库，最多下次重复导入时再拦一次
		out.Text += "\n（注意：导入记录写入失败，同一文件可能被再次导入）"
	}

	// 汇总统计：并入会话累计（有口令时跨文件合并，最后一份文件给总数）
	var sum *importSummary
	if pending != nil {
		if pending.sum == nil {
			pending.sum = newImportSummary()
		}
		pending.sum.add(preview, kind)
		sum = pending.sum
		m.consumePending(pending, kind)
	} else {
		sum = newImportSummary()
		sum.add(preview, kind)
	}

	// 汇报文案与 615 逐字对齐，运营看惯了这格式
	label := "音浪"
	if kind == csvparse.KindDuration {
		label = "时长"
	}
	var sourceHint string
	switch source {
	case "yesterday":
		sourceHint = "（默认昨天；要指定日期请先发「X号数据」）"
	case "pending":
		sourceHint = "（按你预告的日期；不想导了发 q）"
	default:
		sourceHint = "（按消息指定日期）"
	}
	// 目标是未来的某天：平台数据通常次日才出，提醒一句但不改日期——
	// 运营明说了 9.1 就写 9.1，擅自纠正只会造成「我明明说了 9.1」的困惑。
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if date.After(todayMidnight) {
		sourceHint += fmt.Sprintf("\n提醒：%s 还没到，音浪一般是次日才能导出；真导错了发「q」，重传一次覆盖即可。",
			friendlyDate(date.Format("2006-01-02")))
	}
	lines := []string{
		fmt.Sprintf("已导入 %s 的%s数据%s：%d 条；未匹配 %d 条（已存档），重复行 %d 条，非法行 %d 条。",
			friendlyDate(date.Format("2006-01-02")), label, sourceHint,
			affected, preview.Unmatched, preview.Duplicate, preview.Skipped),
	}
	// 有未匹配的行：数据已经存了，之后一绑号就会自动算出来显示
	if preview.Unmatched > 0 {
		lines = append(lines, fmt.Sprintf("未匹配的 %d 条已存档，名单里还没有这些人；之后发「姓名-抖音号」加上，历史数据会自动显示。", preview.Unmatched))
		lines = append(lines, addAnchorHint())
	}
	if len(errSamples) > 0 {
		lines = append(lines, errSamples...)
		lines = append(lines, "（以上是前几种非法行示例）")
	}
	if pending != nil {
		got := map[string]bool{}
		for k := range pending.kinds {
			got[k] = true
		}
		got[string(kind)] = true
		var missing []string
		if !got["wave"] {
			missing = append(missing, "音浪")
		}
		if !got["duration"] {
			missing = append(missing, "时长")
		}
		if pending.remaining > 0 {
			if len(missing) > 0 {
				lines = append(lines, fmt.Sprintf("该日期口令还剩 %d 个文件可用，还缺：%s。",
					pending.remaining, strings.Join(missing, "、")))
			} else {
				lines = append(lines, fmt.Sprintf("该日期口令还剩 %d 个文件可用。", pending.remaining))
			}
		} else {
			if len(missing) > 0 {
				lines = append(lines, fmt.Sprintf("该日期口令已用满 %d 个文件（还缺：%s，可重发日期再传）。",
					pendingImportMaxFiles, strings.Join(missing, "、")))
			} else {
				lines = append(lines, fmt.Sprintf("该日期口令已用满 %d 个文件，音浪与时长都已入库。", pendingImportMaxFiles))
			}
		}
	}
	if len(skipped) > 0 && len(skipped) < 6 {
		lines = append(lines, "非法未入库账号："+strings.Join(skipped, "、"))
	}
	// 会话汇总：多工会多文件导完后给总数（主播数/音浪合计/前三名）；
	// 没有口令的单文件也算一轮，直接给。
	sessionDone := pending == nil || pending.remaining <= 0
	if sessionDone {
		if s := sum.build(); s != "" {
			lines = append(lines, s)
		}
	}
	out.Text = strings.Join(lines, "\n")
	return out, nil
}

// fetchAttachment 取附件内容。优先用通道给的 Fetch 闭包（微信 iLink 的
// 文件必须先走 CDN 下载 + AES 解密），没有闭包才按 URL 直连下载。
func fetchAttachment(ctx context.Context, att Attachment) ([]byte, error) {
	if att.Fetch != nil {
		data, err := att.Fetch(ctx)
		if err != nil {
			return nil, err
		}
		if len(data) > maxDownloadBytes {
			return nil, errors.New("附件超过大小上限（20MB）")
		}
		return data, nil
	}
	return downloadAttachment(ctx, att.URL)
}

// 协议相对地址（//…）补 https。强制大小上限，防止把内存吃爆。
func downloadAttachment(ctx context.Context, rawURL string) ([]byte, error) {
	url := strings.TrimSpace(rawURL)
	if url == "" {
		return nil, errors.New("缺少附件下载地址")
	}
	if strings.HasPrefix(url, "//") {
		url = "https:" + url
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, errors.New("附件超过大小上限（20MB）")
	}
	return data, nil
}

// decodeCSV 与 615 一致：优先 UTF-8，失败退 GB18030（运营常用 WPS 导出 GBK）。
func decodeCSV(data []byte) (string, error) {
	trimmed := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // 去 BOM
	if utf8.Valid(trimmed) {
		return string(trimmed), nil
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// addAnchorHint 未匹配时的引导文案。
func addAnchorHint() string {
	return "如果想添加主播，请按「姓名-抖音号」的格式发给我（例如：柚子-123456）；主播多的话直接发名单 CSV（表头含「姓名」和「抖音号」即可），批量添加。"
}

// friendlyDate 2026-09-11 → 「11号」（当年当月）/「9月11号」/「2026年9月11号」。
func friendlyDate(dateStr string) string {
	t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return dateStr
	}
	now := time.Now()
	switch {
	case t.Year() == now.Year() && t.Month() == now.Month():
		return fmt.Sprintf("%d号", t.Day())
	case t.Year() == now.Year():
		return fmt.Sprintf("%d月%d号", int(t.Month()), t.Day())
	default:
		return fmt.Sprintf("%d年%d月%d号", t.Year(), int(t.Month()), t.Day())
	}
}
