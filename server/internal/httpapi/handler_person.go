package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"douyin-server/internal/csvparse"
	"douyin-server/internal/domain"
	"douyin-server/internal/repo"
)

func (s *Server) listPersons(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	gender := domain.Gender(q.Get("gender"))
	if gender != "" && !gender.Valid() {
		badRequest(w, "gender 只能是 male / female / unknown")
		return
	}
	status := domain.PersonStatus(q.Get("status"))
	if status != "" && !status.Valid() {
		badRequest(w, "status 只能是 active / left / paused")
		return
	}

	f := repo.PersonFilter{Gender: gender, Status: status, Keyword: q.Get("keyword")}
	f.Limit = queryInt(q.Get("limit"), 200)
	f.Offset = queryInt(q.Get("offset"), 0)

	persons, err := s.repo.ListPersons(r.Context(), f)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, persons)
}

func (s *Server) createPerson(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string              `json:"name"`
		Gender     domain.Gender       `json:"gender"`
		Status     domain.PersonStatus `json:"status"`
		MasterID   *uint64             `json:"masterId"`
		Generation *int                `json:"generation"`
		GroupName  *string             `json:"groupName"`
		AvatarURL  *string             `json:"avatarUrl"`
		Hide       bool                `json:"hideInDailyReport"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Name == "" {
		badRequest(w, "name 不能为空")
		return
	}
	if req.Gender == "" {
		req.Gender = domain.GenderUnknown
	}
	if !req.Gender.Valid() {
		badRequest(w, "gender 非法")
		return
	}
	if req.Status == "" {
		req.Status = domain.PersonStatusActive
	}
	if !req.Status.Valid() {
		badRequest(w, "status 非法")
		return
	}

	p := &domain.Person{
		Name:              req.Name,
		Gender:            req.Gender,
		Status:            req.Status,
		MasterID:          req.MasterID,
		Generation:        req.Generation,
		GroupName:         req.GroupName,
		AvatarURL:         req.AvatarURL,
		HideInDailyReport: req.Hide,
	}
	if err := s.repo.CreatePerson(r.Context(), p); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getPerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.repo.GetPerson(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			notFound(w, "主播不存在")
			return
		}
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) updatePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	existing, err := s.repo.GetPerson(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			notFound(w, "主播不存在")
			return
		}
		internalError(w, err)
		return
	}

	// 部分更新：只覆盖请求里出现过的字段，未出现的保持原值。
	var req struct {
		Name       *string              `json:"name"`
		Gender     *domain.Gender       `json:"gender"`
		Status     *domain.PersonStatus `json:"status"`
		MasterID   *uint64              `json:"masterId"`
		Generation *int                 `json:"generation"`
		GroupName  *string              `json:"groupName"`
		AvatarURL  *string              `json:"avatarUrl"`
		Hide       *bool                `json:"hideInDailyReport"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Gender != nil {
		if !req.Gender.Valid() {
			badRequest(w, "gender 非法")
			return
		}
		existing.Gender = *req.Gender
	}
	if req.Status != nil {
		if !req.Status.Valid() {
			badRequest(w, "status 非法")
			return
		}
		existing.Status = *req.Status
	}
	if req.MasterID != nil {
		existing.MasterID = req.MasterID
	}
	if req.Generation != nil {
		existing.Generation = req.Generation
	}
	if req.GroupName != nil {
		existing.GroupName = req.GroupName
	}
	if req.AvatarURL != nil {
		existing.AvatarURL = req.AvatarURL
	}
	if req.Hide != nil {
		existing.HideInDailyReport = *req.Hide
	}
	if existing.Name == "" {
		badRequest(w, "name 不能为空")
		return
	}

	if err := s.repo.UpdatePerson(r.Context(), existing); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) deletePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.repo.SoftDeletePerson(r.Context(), id); err != nil {
		if isNotFound(err) {
			notFound(w, "主播不存在")
			return
		}
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nil)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	accounts, err := s.repo.ListAccounts(r.Context(), id)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) bindAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		AnchorID   string `json:"anchorId"`
		DouyinNo   string `json:"douyinNo"`
		AnchorName string `json:"anchorName"`
		IsPrimary  bool   `json:"isPrimary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.AnchorID == "" {
		badRequest(w, "anchorId 不能为空")
		return
	}

	a := &domain.Account{
		PersonID:   id,
		AnchorID:   req.AnchorID,
		DouyinNo:   req.DouyinNo,
		AnchorName: req.AnchorName,
		IsPrimary:  req.IsPrimary,
		Status:     "active",
	}
	if err := s.repo.BindAccount(r.Context(), a); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func pathID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		badRequest(w, "id 不是合法的正整数")
		return 0, false
	}
	return id, true
}

/* ------------------------- 615 主播管理的其余操作 ------------------------- */

// batchDeletePersons POST /api/v1/persons/batch-delete
func (s *Server) batchDeletePersons(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []uint64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if len(req.IDs) == 0 {
		badRequest(w, "ids 不能为空")
		return
	}
	n, err := s.repo.BatchDeletePersons(r.Context(), req.IDs)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// duplicatePersons GET /api/v1/persons/duplicates —— 重复数据检测（按姓名分组）
func (s *Server) duplicatePersons(w http.ResponseWriter, r *http.Request) {
	groups, err := s.repo.FindDuplicatePersons(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// mergePersons POST /api/v1/persons/merge
func (s *Server) mergePersons(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrimaryPersonID   uint64 `json:"primaryPersonId"`
		SecondaryPersonID uint64 `json:"secondaryPersonId"`
		MergeDuration     bool   `json:"mergeDuration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.PrimaryPersonID == 0 || req.SecondaryPersonID == 0 {
		badRequest(w, "primaryPersonId 与 secondaryPersonId 都不能为空")
		return
	}
	if err := s.repo.MergePersons(r.Context(), req.PrimaryPersonID, req.SecondaryPersonID, req.MergeDuration); err != nil {
		writeError(w, http.StatusBadRequest, "MERGE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nil)
}

// setMaster PATCH /api/v1/persons/{id}/master —— 传 masterId 为 null 表示清空
func (s *Server) setMaster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		MasterID *uint64 `json:"masterId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if err := s.repo.SetMaster(r.Context(), id, req.MasterID); err != nil {
		if isNotFound(err) {
			notFound(w, "主播不存在")
			return
		}
		writeError(w, http.StatusBadRequest, "SET_MASTER_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nil)
}

// saveSnapshot POST /api/v1/persons/{id}/snapshot —— 手工改某天的音浪/时长
func (s *Server) saveSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Date     string `json:"date"`
		AnchorID string `json:"anchorId"`
		Wave     int64  `json:"wave"`
		Minutes  int    `json:"minutes"`
		Rank     *int   `json:"rank"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	date, err := time.ParseInLocation(isoDate, req.Date, time.Local)
	if err != nil {
		badRequest(w, "date 格式应为 YYYY-MM-DD")
		return
	}
	if req.AnchorID == "" {
		badRequest(w, "anchorId 不能为空")
		return
	}
	if err := s.repo.SaveDailySnapshot(r.Context(), req.AnchorID, id, date, req.Wave, req.Minutes, req.Rank); err != nil {
		internalError(w, err)
		return
	}
	if err := s.repo.RecomputePerson(r.Context(), id, date, date); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nil)
}

func queryInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

// syncFrom615 POST /api/v1/persons/sync-615 —— 从 615 全量同步：
// 主播/账号 + 音浪快照 + 时长快照同步是同步返回的；指标重算（100+ 人 ×
// 逐人多条 SQL，打远端库要好几分钟）丢进后台 goroutine，完成/失败进日志。
// 幂等，可反复执行；以 615 为准，Go 侧独有字段（分组/头像/状态）保留原值。
func (s *Server) syncFrom615(w http.ResponseWriter, r *http.Request) {
	result, err := s.repo.SyncPersonsFrom615(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	result.WavesSynced, result.WavesSkipped, err = s.repo.SyncWavesFrom615(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	result.DurationsSynced, result.DurationsSkipped, err = s.repo.SyncDurationsFrom615(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	ranges, err := s.repo.PersonsToRecompute(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	result.PeopleRecomputed = len(ranges)

	go func() {
		ctx := context.Background()
		for i, pr := range ranges {
			if err := s.repo.RecomputePerson(ctx, pr.ID, pr.From, pr.To); err != nil {
				slog.Error("615 同步重算失败", "personID", pr.ID, "err", err)
				return
			}
			if (i+1)%20 == 0 {
				slog.Info("615 重算进度", "done", i+1, "total", len(ranges))
			}
		}
		slog.Info("615 同步重算完成", "people", len(ranges))
	}()

	writeJSON(w, http.StatusOK, result)
}

// importAnchorsPreview POST /api/v1/persons/import-anchors/preview
// 解析任意 CSV，提取主播身份列（主播 ID / 抖音号 / 姓名）供前端勾选。
// 不限制 CSV 类型：榜单 CSV（如 创想1号到21号.csv）里的主播也能直接拿来建档。
func (s *Server) importAnchorsPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CSV string `json:"csv"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	text := strings.TrimPrefix(req.CSV, "\uFEFF")
	if strings.TrimSpace(text) == "" {
		badRequest(w, "CSV 内容为空")
		return
	}

	_, rows, err := csvparse.Parse(strings.NewReader(text))
	if err != nil {
		badRequest(w, "CSV 解析失败："+err.Error())
		return
	}

	type item struct {
		RawIndex int    `json:"rawIndex"`
		AnchorID string `json:"anchorId"`
		DouyinNo string `json:"douyinNo"`
		Name     string `json:"name"`
		Bound    bool   `json:"bound"`           // 该号已在主播库中
		BoundTo  string `json:"boundTo,omitempty"` // 绑给了谁
	}
	items := make([]item, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		id := row.AnchorID
		if id == "" {
			id = row.DouyinNo
		}
		if row.Name == "" || id == "" {
			continue
		}
		key := id + "|" + row.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		it := item{RawIndex: row.RawIndex, AnchorID: row.AnchorID, DouyinNo: row.DouyinNo, Name: row.Name}
		// 已在库里的标记出来，前端默认隐藏，避免重复导入
		if ownerID, err := s.repo.ResolveAnchorOwnerFlexible(r.Context(), id, ""); err == nil {
			if p, perr := s.repo.GetPerson(r.Context(), ownerID); perr == nil {
				it.Bound = true
				it.BoundTo = p.Name
			}
		}
		items = append(items, it)
		if len(items) >= 500 {
			break
		}
	}
	if len(items) == 0 {
		badRequest(w, "CSV 里没有能识别出「姓名 + 主播ID/抖音号」的行")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(items), "items": items})
}

// importAnchors POST /api/v1/persons/import-anchors
// 批量建档/绑号。规则与 bot「姓名-抖音号」一致：号已绑跳过、重名加副号、新名建档。
func (s *Server) importAnchors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gender domain.Gender `json:"gender"`
		Items  []struct {
			Name     string `json:"name"`
			AnchorID string `json:"anchorId"`
			DouyinNo string `json:"douyinNo"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "请求体不是合法 JSON")
		return
	}
	if req.Gender == "" {
		req.Gender = domain.GenderUnknown
	}
	if !req.Gender.Valid() {
		badRequest(w, "gender 只能是 male / female / unknown")
		return
	}
	if len(req.Items) == 0 {
		badRequest(w, "items 不能为空")
		return
	}
	if len(req.Items) > 500 {
		badRequest(w, "单次最多导入 500 行，请分批")
		return
	}

	type detail struct {
		Name  string `json:"name"`
		ID    string `json:"id"`
		Owner string `json:"owner,omitempty"`
		Error string `json:"error,omitempty"`
	}
	var created, bound, already, failed int
	var alreadyList, failedList []detail
	for _, it := range req.Items {
		action, personName, err := s.repo.UpsertAnchorAccount(r.Context(), it.Name, it.AnchorID, it.DouyinNo, req.Gender)
		if err != nil {
			failed++
			failedList = append(failedList, detail{Name: it.Name, ID: it.AnchorID, Error: err.Error()})
			continue
		}
		switch action {
		case "created":
			created++
		case "bound":
			bound++
		default:
			already++
			if len(alreadyList) < 10 {
				alreadyList = append(alreadyList, detail{Name: it.Name, ID: it.AnchorID, Owner: personName})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "bound": bound, "already": already, "failed": failed,
		"alreadyDetail": alreadyList, "failedDetail": failedList,
	})
}
