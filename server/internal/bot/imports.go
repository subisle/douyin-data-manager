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
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"douyin-server/internal/csvparse"
	"douyin-server/internal/repo"
)

const maxDownloadBytes = 20 << 20 // 20MB，QQ 附件直链也按这个限

var downloadClient = &http.Client{Timeout: 60 * time.Second}

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
	data, err := downloadAttachment(ctx, att.URL)
	if err != nil {
		out.Text = "附件下载失败：" + err.Error()
		return out, nil
	}
	if !strings.HasSuffix(strings.ToLower(att.FileName), ".csv") {
		out.Text = "请发送 CSV 格式的音浪或时长文件。\n默认导入到昨天；若要指定日期，请先发「24号数据」再传文件。"
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
	if kind != csvparse.KindWave && kind != csvparse.KindDuration {
		out.Text = "看不出这是音浪表还是时长表（表头没有音浪/时长列）。"
		return out, nil
	}
	if len(rows) == 0 {
		out.Text = "CSV 里没有有效数据行。"
		return out, nil
	}

	date, source, pending := m.resolveInboundImportDate(in.ConversationID)

	// 去重：文件 MD5 + 规范化数据 SHA256，与 615 共用 import_records 账本。
	// 口径：同一文件/同一内容对**同一日期**只导一次；换个日期再导允许。
	fileHash, dataHash := repo.ComputeImportHashes(data, rows, kind)
	if rec, err := m.repo.FindImportRecord(ctx, string(kind), date, fileHash, dataHash); err == nil && rec != nil {
		out.Text = fmt.Sprintf("这个文件在 %s 已导入过（%s，%d 行），已阻止重复导入。",
			friendlyDate(date.Format("2006-01-02")),
			friendlyDate(rec.ImportAt.Format("2006-01-02")), rec.RowCount)
		return out, nil
	}

	preview, err := m.repo.BuildImportPreview(ctx, rows, kind, date)
	if err != nil {
		return out, fmt.Errorf("构建导入预览: %w", err)
	}

	// 一行都没匹配到：直接说清楚，不写库（615 同款）。
	// 「有效行」= 解析出数据行的数量（非法行另列），与 615 文案口径一致。
	validRows := len(preview.Rows) - preview.Skipped
	matched := validRows - preview.Unmatched - preview.Duplicate
	if matched <= 0 {
		lines := []string{
			fmt.Sprintf("文件已解析，但没有匹配到主播。有效行 %d，未匹配 %d，非法行 %d。",
				validRows, preview.Unmatched, preview.Skipped),
		}
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
		if len(errSamples) > 0 {
			lines = append(lines, errSamples...)
			lines = append(lines, "（以上是前几种错误示例）")
		}
		lines = append(lines, addAnchorHint())
		out.Text = strings.Join(lines, "\n")
		return out, nil
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

	if pending != nil {
		m.consumePending(pending, kind)
	}

	// 汇报文案与 615 逐字对齐，运营看惯了这格式
	label := "音浪"
	if kind == csvparse.KindDuration {
		label = "时长"
	}
	var sourceHint string
	switch source {
	case "yesterday":
		sourceHint = "（默认昨天；指定日期请先发「X号数据」）"
	case "pending":
		sourceHint = "（按你预告的日期）"
	default:
		sourceHint = "（按消息指定日期）"
	}
	lines := []string{
		fmt.Sprintf("已导入 %s 的%s数据%s：%d 条；未匹配 %d 条，重复行 %d 条，非法行 %d 条。",
			friendlyDate(date.Format("2006-01-02")), label, sourceHint,
			affected, preview.Unmatched, preview.Duplicate, preview.Skipped),
	}
	// 有未匹配的行：引导用「姓名-抖音号」补录，补完重发文件
	if preview.Unmatched > 0 {
		lines = append(lines, addAnchorHint())
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
		lines = append(lines, "未入库账号："+strings.Join(skipped, "、"))
	}
	out.Text = strings.Join(lines, "\n")
	return out, nil
}

// downloadAttachment 下载附件直链。QQ 事件里的 URL 自带 rkey 鉴权参数，
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
	return "如果想添加主播，请按「姓名-抖音号」的格式发给我（例如：柚子-123456），添加后重发文件即可匹配。"
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
