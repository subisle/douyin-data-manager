package repo

import (
	"context"
	"errors"
	"fmt"
)

// 应用级 KV 设置（app_setting 表）。只放需要跨重启持久化的开关，
// 别拿来当缓存用——读写都走数据库。

const keyPushEnabled = "push_enabled"
const keyRemindGroup = "remind_group"
const keyRemindPrivate = "remind_private"

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
