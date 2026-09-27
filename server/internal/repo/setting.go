package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 应用级 KV 设置（app_setting 表）。只放需要跨重启持久化的开关，
// 别拿来当缓存用——读写都走数据库。

const keyPushEnabled = "push_enabled"
const keyRemindGroup = "remind_group"
const keyRemindPrivate = "remind_private"
const keyDataUpdatedAt = "data_updated_at"

// 日报图标题（男女各一个）。空值 = 用 render 包的内置默认。
// 存自定义标题而不是生效值，改默认样式时不用迁移数据。
const keyReportTitleMale = "report_title_male"
const keyReportTitleFemale = "report_title_female"

// 日报图列勾选（逗号分隔，615 列键）。空值 = 默认五列。
// 导出页勾选后自动保存，机器人发图与手动导出共用同一份。
const keyReportColumns = "report_columns"

// GetReportColumns 读保存的列勾选串；未设置返回空串。
func (r *Repo) GetReportColumns(ctx context.Context) string {
	v, err := r.GetSetting(ctx, keyReportColumns)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// SetReportColumns 保存列勾选串（空串 = 恢复默认列）。
func (r *Repo) SetReportColumns(ctx context.Context, raw string) error {
	return r.SetSetting(ctx, keyReportColumns, strings.TrimSpace(raw))
}

// GetReportTitle 读某个性别的自定义日报图标题；未设置返回空串。
func (r *Repo) GetReportTitle(ctx context.Context, gender string) string {
	key := keyReportTitleMale
	if gender == "female" {
		key = keyReportTitleFemale
	}
	v, err := r.GetSetting(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// SetReportTitle 保存某个性别的日报图标题；空串 = 恢复默认。
func (r *Repo) SetReportTitle(ctx context.Context, gender, title string) error {
	key := keyReportTitleMale
	if gender == "female" {
		key = keyReportTitleFemale
	}
	return r.SetSetting(ctx, key, strings.TrimSpace(title))
}

// ReportTitles 男/女两个自定义标题（管理页回显用）。
type ReportTitles struct {
	Male   string `json:"male"`
	Female string `json:"female"`
}

func (r *Repo) GetReportTitles(ctx context.Context) ReportTitles {
	return ReportTitles{
		Male:   r.GetReportTitle(ctx, "male"),
		Female: r.GetReportTitle(ctx, "female"),
	}
}

// GetSetting 读取设置；不存在返回 ErrNotFound。
func (r *Repo) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.GetContext(ctx, &v,
		"SELECT setting_value FROM app_setting WHERE setting_key = ?", key)
	if err != nil {
		return "", translateNotFound(err, "读取设置 "+key)
	}
	return v, nil
}

// SetSetting 写入设置（upsert）。
func (r *Repo) SetSetting(ctx context.Context, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO app_setting (setting_key, setting_value) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE setting_value = VALUES(setting_value)`,
		key, value)
	if err != nil {
		return fmt.Errorf("写入设置 %s: %w", key, err)
	}
	return nil
}

// MarkDataUpdated 记一笔「业务数据已更新」的时间戳（尽力而为，失败不阻塞业务）。
// 盒子上的 /srv/douyin/sync-to-sqlpub.sh（每分钟 cron）比对它与上次已同步值，
// 有变化就把 Go 业务表推到公网备份库（mysql7.sqlpub.com）。
// 所有会改动业务数据的路径都要调：指标重算、导入、主播/账号资料修改。
func (r *Repo) MarkDataUpdated(ctx context.Context) {
	_ = r.SetSetting(ctx, keyDataUpdatedAt, time.Now().Format(time.RFC3339))
}

// GetPushEnabled 读推送开关，缺省关闭。启动时装载一次。
func (r *Repo) GetPushEnabled(ctx context.Context) (bool, error) {
	v, err := r.GetSetting(ctx, keyPushEnabled)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return v == "1", nil
}

// SetPushEnabled 持久化推送开关。
func (r *Repo) SetPushEnabled(ctx context.Context, enabled bool) error {
	v := "0"
	if enabled {
		v = "1"
	}
	return r.SetSetting(ctx, keyPushEnabled, v)
}

// GetReminderTargets 读索要范围（群聊/私聊），缺省都开——与历史行为一致。
func (r *Repo) GetReminderTargets(ctx context.Context) (groups, private bool, err error) {
	groups, private = true, true
	if v, err := r.GetSetting(ctx, keyRemindGroup); err == nil {
		groups = v == "1"
	}
	if v, err := r.GetSetting(ctx, keyRemindPrivate); err == nil {
		private = v == "1"
	}
	return groups, private, nil
}

// SetReminderTargets 持久化索要范围。
func (r *Repo) SetReminderTargets(ctx context.Context, groups, private bool) error {
	g, p := "0", "0"
	if groups {
		g = "1"
	}
	if private {
		p = "1"
	}
	if err := r.SetSetting(ctx, keyRemindGroup, g); err != nil {
		return err
	}
	return r.SetSetting(ctx, keyRemindPrivate, p)
}
