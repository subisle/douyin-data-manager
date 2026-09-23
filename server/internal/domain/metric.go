package domain

import (
	"fmt"
	"time"
)

// 音浪与时长都远小于 2^53，用 int64 直接编码成 JSON 数字是安全的；
// 前端不需要走字符串方案，少一层转换就少一处出错点。

// WaveSnapshot 平台累计音浪快照。只增不改，是差分的事实来源。
type WaveSnapshot struct {
	ID          uint64    `db:"id"            json:"id"`
	AnchorID    string    `db:"anchor_id"     json:"anchorId"`
	PersonID    uint64    `db:"person_id"     json:"personId"`
	BizDate     time.Time `db:"biz_date"      json:"bizDate"`
	WaveValue   int64     `db:"wave_value"    json:"waveValue"`
	RankInGuild *int      `db:"rank_in_guild" json:"rankInGuild,omitempty"`
	BatchID     *uint64   `db:"batch_id"      json:"batchId,omitempty"`
	CreatedAt   time.Time `db:"created_at"    json:"createdAt"`
}

// DurationSnapshot 平台累计直播时长快照（分钟）。
type DurationSnapshot struct {
	ID                uint64    `db:"id"                 json:"id"`
	AnchorID          string    `db:"anchor_id"          json:"anchorId"`
	PersonID          uint64    `db:"person_id"          json:"personId"`
	BizDate           time.Time `db:"biz_date"           json:"bizDate"`
	CumulativeMinutes int       `db:"cumulative_minutes" json:"cumulativeMinutes"`
	BatchID           *uint64   `db:"batch_id"           json:"batchId,omitempty"`
	CreatedAt         time.Time `db:"created_at"         json:"createdAt"`
}

// DailyMetric 日粒度物化指标。导出日报直接读这张表，不做现场 JOIN。
type DailyMetric struct {
	ID       uint64    `db:"id"                 json:"id"`
	PersonID uint64    `db:"person_id"          json:"personId"`
	AnchorID string    `db:"anchor_id"          json:"anchorId"`
	BizDate  time.Time `db:"biz_date"           json:"bizDate"`

	Wave int64 `db:"wave" json:"wave"`
	// CumulativeWave 存的是快照原值；但日报/导出场景（ListDailyByDate）
	// 会用当月 1 号至该日的日音浪总和覆盖它——运营口径的「累计总音浪」。
	CumulativeWave    int64      `db:"cumulative_wave"    json:"cumulativeWave"`
	PrevSnapshotDate  *time.Time `db:"prev_snapshot_date" json:"prevSnapshotDate,omitempty"`
	WaveSpan          int        `db:"wave_span"          json:"waveSpan"`
	WaveReliable      bool       `db:"wave_reliable"      json:"waveReliable"`
	Minutes           int        `db:"minutes"            json:"minutes"`
	CumulativeMinutes int        `db:"cumulative_minutes" json:"cumulativeMinutes"`
	MinutesSpan       int        `db:"minutes_span"       json:"minutesSpan"`
	MinutesReliable   bool       `db:"minutes_reliable"   json:"minutesReliable"`
	IsLive            bool       `db:"is_live"            json:"isLive"`
	Tier              *string    `db:"tier"               json:"tier,omitempty"`

	// 导出要用的派生字段，由 repo 层 JOIN person 补齐，不落库。
	Name       string  `db:"name"   json:"name,omitempty"`
	Gender     Gender  `db:"gender" json:"gender,omitempty"`
	MasterName *string `db:"master_name" json:"masterName,omitempty"`
}

// MonthlyMetric 月粒度物化：月音浪 + 月直播时长。
type MonthlyMetric struct {
	ID                uint64     `db:"id"                  json:"id"`
	PersonID          uint64     `db:"person_id"           json:"personId"`
	Period            string     `db:"period"              json:"period"`
	Wave              int64      `db:"wave"                json:"wave"`
	Minutes           int        `db:"minutes"             json:"minutes"`
	FormattedDuration string     `db:"formatted_duration"  json:"formattedDuration"`
	LiveDays          int        `db:"live_days"           json:"liveDays"`
	AbsentDays        int        `db:"absent_days"         json:"absentDays"`
	BestDayWave       int64      `db:"best_day_wave"       json:"bestDayWave"`
	BestDayDate       *time.Time `db:"best_day_date"       json:"bestDayDate,omitempty"`
	AvgWavePerLiveDay int64      `db:"avg_wave_per_live_day" json:"avgWavePerLiveDay"`
	Tier              *string    `db:"tier"                json:"tier,omitempty"`
	UnreliableDays    int        `db:"unreliable_days"     json:"unreliableDays"`

	Name   string `db:"name"   json:"name,omitempty"`
	Gender Gender `db:"gender" json:"gender,omitempty"`
}

// Year 从 "YYYY-MM" 形式的 period 取出年份，年度聚合用。
func (m MonthlyMetric) Year() int {
	if len(m.Period) < 4 {
		return 0
	}
	n := 0
	for i := 0; i < 4; i++ {
		c := m.Period[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// YearlyMetric 年度汇总。
type YearlyMetric struct {
	ID                uint64     `db:"id"                  json:"id"`
	PersonID          uint64     `db:"person_id"           json:"personId"`
	Year              int        `db:"year"                json:"year"`
	Wave              int64      `db:"wave"                json:"wave"`
	Minutes           int        `db:"minutes"             json:"minutes"`
	FormattedDuration string     `db:"formatted_duration"  json:"formattedDuration"`
	LiveDays          int        `db:"live_days"           json:"liveDays"`
	ActiveMonths      int        `db:"active_months"       json:"activeMonths"`
	BestMonth         *string    `db:"best_month"          json:"bestMonth,omitempty"`
	BestMonthWave     int64      `db:"best_month_wave"     json:"bestMonthWave"`
	BestDayWave       int64      `db:"best_day_wave"       json:"bestDayWave"`
	BestDayDate       *time.Time `db:"best_day_date"       json:"bestDayDate,omitempty"`
	AvgMonthWave      int64      `db:"avg_month_wave"      json:"avgMonthWave"`
	Tier              *string    `db:"tier"                json:"tier,omitempty"`
	YearRank          *int       `db:"year_rank"           json:"yearRank,omitempty"`

	Name   string `db:"name"   json:"name,omitempty"`
	Gender Gender `db:"gender" json:"gender,omitempty"`
}

// ImportBatch 一次导入的记账，用于幂等与追溯。
type ImportBatch struct {
	ID         uint64     `db:"id"          json:"id"`
	BizDate    time.Time  `db:"biz_date"    json:"bizDate"`
	Source     string     `db:"source"      json:"source"`
	Status     string     `db:"status"      json:"status"`
	RowCount   int        `db:"row_count"   json:"rowCount"`
	Operator   string     `db:"operator"    json:"operator"`
	ErrorText  *string    `db:"error_text"  json:"errorText,omitempty"`
	StartedAt  time.Time  `db:"started_at"  json:"startedAt"`
	FinishedAt *time.Time `db:"finished_at" json:"finishedAt,omitempty"`
}

// FormatDuration 把分钟数格式化成导出图里用的 "128h30m" 形式。
func FormatDuration(minutes int) string {
	if minutes <= 0 {
		return "0h"
	}
	h := minutes / 60
	m := minutes % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h) // 需要 fmt
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
