package repo

import (
	"context"
	"fmt"
	"sort"
	"time"

	"douyin-server/internal/domain"
	"douyin-server/internal/metric"
)

const dateLayout = "2006-01-02"

// RecomputePerson 重算某主播的日/月/年三层指标。
//
// 为什么敢物化：单人一年的快照不到 1000 行，全量重算只要几毫秒。
// 物化表脏了随时能重建，不用担心"物化视图对不上"这种慢性病。
//
// from/to 控制**写回**范围，但差分仍从该账号的第一条快照开始算，
// 否则区间起点那天的日音浪会因为没有基线而被误记成 0。
func (r *Repo) RecomputePerson(ctx context.Context, personID uint64, from, to time.Time) error {
	accounts, err := r.ListAccounts(ctx, personID)
	if err != nil {
		return err
	}
	anchorIDs := make([]string, 0, len(accounts))
	primaryAnchor := ""
	for _, a := range accounts {
		anchorIDs = append(anchorIDs, a.AnchorID)
		if a.IsPrimary || primaryAnchor == "" {
			primaryAnchor = a.AnchorID
		}
	}
	if len(anchorIDs) == 0 {
		return nil // 没绑账号，没有可算的数据
	}

	waves, err := r.listWaveSnapshotsOfAnchors(ctx, anchorIDs)
	if err != nil {
		return err
	}
	durations, err := r.listDurationSnapshotsOfAnchors(ctx, anchorIDs)
	if err != nil {
		return err
	}

	// 按账号拆成序列。音浪序列是主时间轴，时长按同一天补齐。
	waveSeq := map[string][]domain.WaveSnapshot{}
	for _, w := range waves {
		waveSeq[w.AnchorID] = append(waveSeq[w.AnchorID], w)
	}
	durMap := map[string]map[string]int{}
	for _, d := range durations {
		if durMap[d.AnchorID] == nil {
			durMap[d.AnchorID] = map[string]int{}
		}
		durMap[d.AnchorID][d.BizDate.Format(dateLayout)] = d.CumulativeMinutes
	}

	// dailyByDate 按人聚合：一个人可能绑多个账号，当天增量是所有账号之和。
	dailyByDate := map[string]*domain.DailyMetric{}
	var anomalies []pendingAnomaly

	for anchor, seq := range waveSeq {
		sort.Slice(seq, func(i, j int) bool { return seq[i].BizDate.Before(seq[j].BizDate) })

		var prev *metric.Point
		for _, w := range seq {
			key := w.BizDate.Format(dateLayout)
			minutes := 0
			if m, ok := durMap[anchor][key]; ok {
				minutes = m
			}
			cur := metric.Point{Date: w.BizDate, Wave: w.WaveValue, Minutes: minutes}

			dm, found := metric.ComputeDaily(personID, anchor, cur, prev)
			for _, a := range found {
				anomalies = append(anomalies, pendingAnomaly{
					AnchorID: anchor, BizDate: w.BizDate, Kind: a.Kind, Detail: a.Detail,
				})
			}

			acc, ok := dailyByDate[key]
			if !ok {
				cp := dm
				cp.AnchorID = primaryAnchor
				dailyByDate[key] = &cp
				prev = &cur
				continue
			}
			acc.Wave += dm.Wave
			acc.Minutes += dm.Minutes
			acc.CumulativeWave += dm.CumulativeWave
			acc.CumulativeMinutes += dm.CumulativeMinutes
			acc.IsLive = acc.IsLive || dm.IsLive
			acc.WaveReliable = acc.WaveReliable && dm.WaveReliable
			acc.MinutesReliable = acc.MinutesReliable && dm.MinutesReliable
			if dm.WaveSpan > acc.WaveSpan {
				acc.WaveSpan = dm.WaveSpan
			}
			if dm.MinutesSpan > acc.MinutesSpan {
				acc.MinutesSpan = dm.MinutesSpan
			}
			prev = &cur
		}
	}

	dailyRules, err := r.ListTierRules(ctx, domain.TierScopeDaily)
	if err != nil {
		return err
	}
	monthRules, err := r.ListTierRules(ctx, domain.TierScopeMonthly)
	if err != nil {
		return err
	}
	yearRules, err := r.ListTierRules(ctx, domain.TierScopeYearly)
	if err != nil {
		return err
	}

	// 只写回 [from, to]，其余日期留着不动。
	fromKey := from.Format(dateLayout)
	toKey := to.Format(dateLayout)
	byMonth := map[string][]domain.DailyMetric{}

	for key, dm := range dailyByDate {
		if key < fromKey || key > toKey {
			continue
		}
		dm.Tier = metric.ResolveTier(dm.Wave, dailyRules)
		byMonth[key[:7]] = append(byMonth[key[:7]], *dm)
	}

	if err := r.upsertDailyMetrics(ctx, dailyByDate); err != nil {
		return err
	}

	// 月指标：先重算涉及到的每个完整月，再upsert。
	periods := make([]string, 0, len(byMonth))
	for p := range byMonth {
		periods = append(periods, p)
	}
	sort.Strings(periods)

	monthly := make([]domain.MonthlyMetric, 0, len(periods))
	for _, p := range periods {
		days := byMonth[p]
		sort.Slice(days, func(i, j int) bool { return days[i].BizDate.Before(days[j].BizDate) })
		mm := metric.AggregateMonthly(personID, p, days)
		mm.Tier = metric.ResolveTier(mm.Wave, monthRules)
		monthly = append(monthly, mm)
	}
	if err := r.upsertMonthlyMetrics(ctx, monthly); err != nil {
		return err
	}

	// 年指标：取该主播全年 12 个月重算，而不是只算本次涉及的月，
	// 否则年会因为少算几个月而偏低。
	years := map[int]struct{}{}
	for _, p := range periods {
		if len(p) >= 4 {
			years[atoiYear(p[:4])] = struct{}{}
		}
	}
	for year := range years {
		rows, err := r.ListMonthlyOfYear(ctx, personID, year)
		if err != nil {
			return err
		}
		// 本次重算的月要覆盖掉库里的旧值
		for _, mm := range monthly {
			if mm.Year() == year {
				replaced := false
				for i := range rows {
					if rows[i].Period == mm.Period {
						rows[i] = mm
						replaced = true
						break
					}
				}
				if !replaced {
					rows = append(rows, mm)
				}
			}
		}
		ym := metric.AggregateYearly(personID, year, rows)
		ym.Tier = metric.ResolveTier(ym.Wave, yearRules)
		if err := r.upsertYearlyMetrics(ctx, []domain.YearlyMetric{ym}); err != nil {
			return err
		}
	}

	for _, a := range anomalies {
		if err := r.RecordAnomaly(ctx, a.AnchorID, personID, a.BizDate, a.Kind, a.Detail); err != nil {
			return err
		}
	}
	return nil
}

type pendingAnomaly struct {
	AnchorID string
	BizDate  time.Time
	Kind     string
	Detail   string
}

// RecomputeAll 重算全量。数据量小，直接逐个主播跑；真到几千人再谈并发。
func (r *Repo) RecomputeAll(ctx context.Context, from, to time.Time) (int, error) {
	var ids []uint64
	if err := r.db.SelectContext(ctx, &ids,
		"SELECT id FROM person WHERE deleted_at IS NULL ORDER BY id"); err != nil {
		return 0, fmt.Errorf("查询主播 ID: %w", err)
	}
	for _, id := range ids {
		if err := r.RecomputePerson(ctx, id, from, to); err != nil {
			return 0, fmt.Errorf("重算主播 %d: %w", id, err)
		}
	}
	return len(ids), nil
}

func (r *Repo) upsertDailyMetrics(ctx context.Context, byDate map[string]*domain.DailyMetric) error {
	if len(byDate) == 0 {
		return nil
	}
	const query = `INSERT INTO daily_metric
		(person_id, anchor_id, biz_date, wave, cumulative_wave, prev_snapshot_date,
		 wave_span, wave_reliable, minutes, cumulative_minutes, minutes_span,
		 minutes_reliable, is_live, tier, recomputed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,NOW(3))
		ON DUPLICATE KEY UPDATE
		  anchor_id = VALUES(anchor_id),
		  wave = VALUES(wave), cumulative_wave = VALUES(cumulative_wave),
		  prev_snapshot_date = VALUES(prev_snapshot_date), wave_span = VALUES(wave_span),
		  wave_reliable = VALUES(wave_reliable), minutes = VALUES(minutes),
		  cumulative_minutes = VALUES(cumulative_minutes), minutes_span = VALUES(minutes_span),
		  minutes_reliable = VALUES(minutes_reliable), is_live = VALUES(is_live),
		  tier = VALUES(tier), recomputed_at = NOW(3)`

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, dm := range byDate {
		if _, err := tx.ExecContext(ctx, query,
			dm.PersonID, dm.AnchorID, dm.BizDate, dm.Wave, dm.CumulativeWave,
			dm.PrevSnapshotDate, dm.WaveSpan, dm.WaveReliable, dm.Minutes,
			dm.CumulativeMinutes, dm.MinutesSpan, dm.MinutesReliable, dm.IsLive, dm.Tier,
		); err != nil {
			return fmt.Errorf("写入日指标(%s): %w", dm.BizDate.Format(dateLayout), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交日指标: %w", err)
	}
	return nil
}

func (r *Repo) upsertMonthlyMetrics(ctx context.Context, rows []domain.MonthlyMetric) error {
	if len(rows) == 0 {
		return nil
	}
	const query = `INSERT INTO monthly_metric
		(person_id, period, wave, minutes, formatted_duration, live_days, absent_days,
		 best_day_wave, best_day_date, avg_wave_per_live_day, tier, unreliable_days)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE
		  wave = VALUES(wave), minutes = VALUES(minutes),
		  formatted_duration = VALUES(formatted_duration), live_days = VALUES(live_days),
		  absent_days = VALUES(absent_days), best_day_wave = VALUES(best_day_wave),
		  best_day_date = VALUES(best_day_date),
		  avg_wave_per_live_day = VALUES(avg_wave_per_live_day),
		  tier = VALUES(tier), unreliable_days = VALUES(unreliable_days)`

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, m := range rows {
		if _, err := tx.ExecContext(ctx, query,
			m.PersonID, m.Period, m.Wave, m.Minutes, m.FormattedDuration,
			m.LiveDays, m.AbsentDays, m.BestDayWave, m.BestDayDate,
			m.AvgWavePerLiveDay, m.Tier, m.UnreliableDays,
		); err != nil {
			return fmt.Errorf("写入月指标(%s): %w", m.Period, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交月指标: %w", err)
	}
	return nil
}

func (r *Repo) upsertYearlyMetrics(ctx context.Context, rows []domain.YearlyMetric) error {
	if len(rows) == 0 {
		return nil
	}
	const query = `INSERT INTO yearly_metric
		(person_id, year, wave, minutes, formatted_duration, live_days, active_months,
		 best_month, best_month_wave, best_day_wave, best_day_date, avg_month_wave, tier)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE
		  wave = VALUES(wave), minutes = VALUES(minutes),
		  formatted_duration = VALUES(formatted_duration), live_days = VALUES(live_days),
		  active_months = VALUES(active_months), best_month = VALUES(best_month),
		  best_month_wave = VALUES(best_month_wave), best_day_wave = VALUES(best_day_wave),
		  best_day_date = VALUES(best_day_date), avg_month_wave = VALUES(avg_month_wave),
		  tier = VALUES(tier)`

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, y := range rows {
		if _, err := tx.ExecContext(ctx, query,
			y.PersonID, y.Year, y.Wave, y.Minutes, y.FormattedDuration, y.LiveDays,
			y.ActiveMonths, y.BestMonth, y.BestMonthWave, y.BestDayWave, y.BestDayDate,
			y.AvgMonthWave, y.Tier,
		); err != nil {
			return fmt.Errorf("写入年指标(%d): %w", y.Year, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交年指标: %w", err)
	}
	return nil
}

// ListDailyByDate 取某天全团日榜，JOIN 出姓名与性别，供导出直接渲染。
// cumulative_wave 不取快照原值，而是**当月 1 号至该日的日音浪总和**
// （按人汇总全部账号）——日报「累计总音浪」列对齐 615 的月累计口径：
// 运营说的「总音浪」= 1 号到数据日的累计，不是当日快照值。
func (r *Repo) ListDailyByDate(ctx context.Context, date time.Time, gender domain.Gender) ([]domain.DailyMetric, error) {
	query := `SELECT d.id, d.person_id, d.anchor_id, d.biz_date, d.wave,
	                 (SELECT COALESCE(SUM(d2.wave), 0) FROM daily_metric d2
	                   WHERE d2.person_id = d.person_id
	                     AND d2.biz_date >= DATE_FORMAT(?, '%Y-%m-01') AND d2.biz_date <= ?) AS cumulative_wave,
	                 d.prev_snapshot_date, d.wave_span, d.wave_reliable, d.minutes,
	                 d.cumulative_minutes, d.minutes_span, d.minutes_reliable, d.is_live, d.tier,
	                 p.name AS name, p.gender AS gender, m.name AS master_name
	          FROM daily_metric d
	          JOIN person p ON p.id = d.person_id
	          LEFT JOIN person m ON m.id = p.master_id
	          WHERE d.biz_date = ? AND p.deleted_at IS NULL AND p.status = 'active'
	            AND p.hide_in_daily_report = 0`
	args := []any{date, date, date}

	if gender != "" {
		query += " AND p.gender = ?"
		args = append(args, string(gender))
	}
	// 排名按累计总音浪（当月 1 号至数据日的累加），其次当日音浪。
	query += " ORDER BY cumulative_wave DESC, d.wave DESC, p.name ASC"

	var out []domain.DailyMetric
	if err := r.db.SelectContext(ctx, &out, query, args...); err != nil {
		return nil, fmt.Errorf("查询日榜: %w", err)
	}
	if out == nil {
		out = []domain.DailyMetric{} // 空结果序列化成 [] 而不是 null，前端少一层判空
	}
	return out, nil
}

// ListMonthlyByPeriod 取某月全团月榜（月音浪 + 月直播时长）。
func (r *Repo) ListMonthlyByPeriod(ctx context.Context, period string, gender domain.Gender) ([]domain.MonthlyMetric, error) {
	query := `SELECT mm.person_id, mm.period, mm.wave, mm.minutes, mm.formatted_duration,
	                 mm.live_days, mm.absent_days, mm.best_day_wave, mm.best_day_date,
	                 mm.avg_wave_per_live_day, mm.tier, mm.unreliable_days,
	                 p.name AS name, p.gender AS gender
	          FROM monthly_metric mm
	          JOIN person p ON p.id = mm.person_id
	          WHERE mm.period = ? AND p.deleted_at IS NULL AND p.status = 'active'`
	args := []any{period}

	if gender != "" {
		query += " AND p.gender = ?"
		args = append(args, string(gender))
	}
	query += " ORDER BY mm.wave DESC, mm.minutes DESC"

	var out []domain.MonthlyMetric
	if err := r.db.SelectContext(ctx, &out, query, args...); err != nil {
		return nil, fmt.Errorf("查询月榜: %w", err)
	}
	if out == nil {
		out = []domain.MonthlyMetric{} // 空结果序列化成 [] 而不是 null，前端少一层判空
	}
	return out, nil
}

// ListYearlyByYear 取某年年度汇总。
func (r *Repo) ListYearlyByYear(ctx context.Context, year int, gender domain.Gender) ([]domain.YearlyMetric, error) {
	query := `SELECT y.person_id, y.year, y.wave, y.minutes, y.formatted_duration, y.live_days,
	                 y.active_months, y.best_month, y.best_month_wave, y.best_day_wave,
	                 y.best_day_date, y.avg_month_wave, y.tier, y.year_rank,
	                 p.name AS name, p.gender AS gender
	          FROM yearly_metric y
	          JOIN person p ON p.id = y.person_id
	          WHERE y.year = ? AND p.deleted_at IS NULL`
	args := []any{year}

	if gender != "" {
		query += " AND p.gender = ?"
		args = append(args, string(gender))
	}
	query += " ORDER BY y.wave DESC"

	var out []domain.YearlyMetric
	if err := r.db.SelectContext(ctx, &out, query, args...); err != nil {
		return nil, fmt.Errorf("查询年度汇总: %w", err)
	}
	return out, nil
}

// ListDailyRange 取 [from, to] 区间内全员的日指标，JOIN 出姓名与性别。
// 首页的四份数据（KPI/趋势/预警/榜单）全部由这一份结果算出。
func (r *Repo) ListDailyRange(ctx context.Context, from, to time.Time) ([]domain.DailyMetric, error) {
	const query = `SELECT d.person_id, d.anchor_id, d.biz_date, d.wave, d.cumulative_wave,
	                      d.wave_span, d.wave_reliable, d.minutes, d.cumulative_minutes,
	                      d.is_live, d.tier,
	                      p.name AS name, p.gender AS gender, m.name AS master_name
	               FROM daily_metric d
	               JOIN person p ON p.id = d.person_id
	               LEFT JOIN person m ON m.id = p.master_id
	               WHERE d.biz_date BETWEEN ? AND ?
	                 AND p.deleted_at IS NULL AND p.status = 'active'
	               ORDER BY d.biz_date ASC`

	var out []domain.DailyMetric
	if err := r.db.SelectContext(ctx, &out, query, from, to); err != nil {
		return nil, fmt.Errorf("查询区间日指标: %w", err)
	}
	return out, nil
}

// ListMonthlyOfYear 取某主播某年全部月指标，用于汇总年度。
func (r *Repo) ListMonthlyOfYear(ctx context.Context, personID uint64, year int) ([]domain.MonthlyMetric, error) {
	var out []domain.MonthlyMetric
	if err := r.db.SelectContext(ctx, &out,
		`SELECT person_id, period, wave, minutes, formatted_duration, live_days, absent_days,
		        best_day_wave, best_day_date, avg_wave_per_live_day, tier, unreliable_days
		 FROM monthly_metric WHERE person_id = ? AND period LIKE ?
		 ORDER BY period ASC`, personID, fmt.Sprintf("%d-%%", year)); err != nil {
		return nil, fmt.Errorf("查询年度月指标: %w", err)
	}
	return out, nil
}

func atoiYear(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
