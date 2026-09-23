// Package repo 封装所有 SQL 访问。只用 sqlx，不用 ORM：
// 本项目有大量聚合查询，ORM 挡在中间只会让 SQL 变不可控。
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"douyin-server/internal/domain"
)

// ErrNotFound 表示目标行不存在。调用方用 errors.Is 判断，
// HTTP 层据此返回 404 而不是 500。
var ErrNotFound = errors.New("记录不存在")

// Repo 持有连接池。所有方法都要求传入 context。
type Repo struct {
	db *sqlx.DB
}

// New 构造 Repo。
func New(db *sqlx.DB) *Repo {
	r := &Repo{db: db}
	// 导入日志扩展列幂等补齐（Next 侧建的老表可能没有这些列）。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r.ensureImportLogColumns(ctx)
	}()
	return r
}

// PersonFilter 是主播列表的筛选条件。
type PersonFilter struct {
	Gender   domain.Gender
	Status   domain.PersonStatus
	MasterID *uint64
	Keyword  string
	Limit    int
	Offset   int
}

// ListPersons 返回主播列表。gender/status 为空字符串时表示不筛选。
func (r *Repo) ListPersons(ctx context.Context, f PersonFilter) ([]domain.Person, error) {
	query := `SELECT id, name, gender, master_id, generation, group_name, avatar_url,
	                 hide_in_daily_report, status, joined_at, created_at, updated_at
	          FROM person
	          WHERE deleted_at IS NULL`
	args := []any{}

	if f.Gender != "" {
		query += " AND gender = ?"
		args = append(args, string(f.Gender))
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, string(f.Status))
	}
	if f.MasterID != nil {
		query += " AND master_id = ?"
		args = append(args, *f.MasterID)
	}
	if f.Keyword != "" {
		query += " AND name LIKE ?"
		args = append(args, "%"+f.Keyword+"%")
	}
	query += " ORDER BY name ASC"
	if f.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit, f.Offset)
	}

	var out []domain.Person
	if err := r.db.SelectContext(ctx, &out, query, args...); err != nil {
		return nil, fmt.Errorf("查询主播列表: %w", err)
	}
	return out, nil
}

// GetPerson 按 ID 取单个主播。
func (r *Repo) GetPerson(ctx context.Context, id uint64) (*domain.Person, error) {
	const query = `SELECT id, name, gender, master_id, generation, group_name, avatar_url,
	                      hide_in_daily_report, status, joined_at, created_at, updated_at
	               FROM person WHERE id = ? AND deleted_at IS NULL`

	var p domain.Person
	if err := r.db.GetContext(ctx, &p, query, id); err != nil {
		return nil, translateNotFound(err, "查询主播")
	}
	return &p, nil
}

// FindPersonsByName 按姓名精确查找（可能重名）。加主播指令用它判断
// 是「新建」还是「给已有主播绑号」。
func (r *Repo) FindPersonsByName(ctx context.Context, name string) ([]domain.Person, error) {
	const query = `SELECT id, name, gender, master_id, generation, group_name, avatar_url,
	                      hide_in_daily_report, status, joined_at, created_at, updated_at
	               FROM person WHERE name = ? AND deleted_at IS NULL ORDER BY id`

	var out []domain.Person
	if err := r.db.SelectContext(ctx, &out, query, name); err != nil {
		return nil, fmt.Errorf("按姓名查询主播: %w", err)
	}
	return out, nil
}

// CreatePerson 新建主播，返回自增 ID。
func (r *Repo) CreatePerson(ctx context.Context, p *domain.Person) error {
	const query = `INSERT INTO person
		(name, gender, master_id, generation, group_name, avatar_url, hide_in_daily_report, status, joined_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	res, err := r.db.ExecContext(ctx, query,
		p.Name, p.Gender, p.MasterID, p.Generation, p.GroupName,
		p.AvatarURL, p.HideInDailyReport, p.Status, p.JoinedAt,
	)
	if err != nil {
		return fmt.Errorf("新建主播: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("读取新主播 ID: %w", err)
	}
	p.ID = uint64(id)
	return nil
}

// UpdatePerson 更新主播资料。nil 指针字段表示不改动。
func (r *Repo) UpdatePerson(ctx context.Context, p *domain.Person) error {
	const query = `UPDATE person SET
		name = ?, gender = ?, master_id = ?, generation = ?, group_name = ?,
		avatar_url = ?, hide_in_daily_report = ?, status = ?, joined_at = ?
		WHERE id = ? AND deleted_at IS NULL`

	res, err := r.db.ExecContext(ctx, query,
		p.Name, p.Gender, p.MasterID, p.Generation, p.GroupName,
		p.AvatarURL, p.HideInDailyReport, p.Status, p.JoinedAt, p.ID,
	)
	if err != nil {
		return fmt.Errorf("更新主播: %w", err)
	}
	return ensureAffected(res, "更新主播")
}

// SoftDeletePerson 软删除。历史数据必须保留，硬删会毁掉月榜和年榜。
func (r *Repo) SoftDeletePerson(ctx context.Context, id uint64) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE person SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL", id)
	if err != nil {
		return fmt.Errorf("删除主播: %w", err)
	}
	return ensureAffected(res, "删除主播")
}

// FindAccountByDouyinNo 按抖音号找账号（anchor_id 或 douyin_no 任一命中）。
// 改名流程用它：用户只发一个抖音号，先定位到人与账号。
func (r *Repo) FindAccountByDouyinNo(ctx context.Context, no string) (*domain.Account, error) {
	var a domain.Account
	err := r.db.GetContext(ctx, &a,
		`SELECT id, person_id, anchor_id, douyin_no, anchor_name, is_primary, status, created_at
		   FROM account WHERE anchor_id = ? OR douyin_no = ? LIMIT 1`, no, no)
	if err != nil {
		return nil, translateNotFound(err, "按抖音号查账号")
	}
	return &a, nil
}

// UpdateAccountIDs 改账号的 anchor_id 与抖音号（改抖音号流程）。
// anchor_id 唯一键冲突由调用方通过返回的 MySQL 错误文案转达人话。
func (r *Repo) UpdateAccountIDs(ctx context.Context, accountID uint64, newAnchorID, newDouyinNo string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE account SET anchor_id = ?, douyin_no = ? WHERE id = ?`,
		newAnchorID, newDouyinNo, accountID)
	if err != nil {
		return fmt.Errorf("更新账号: %w", err)
	}
	return ensureAffected(res, "更新账号")
}

// RenamePerson 改主播姓名（改名流程）。历史数据引用 person.id，改名安全。
func (r *Repo) RenamePerson(ctx context.Context, personID uint64, newName string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE person SET name = ? WHERE id = ? AND deleted_at IS NULL", newName, personID)
	if err != nil {
		return fmt.Errorf("改名: %w", err)
	}
	return ensureAffected(res, "改名")
}

// ListAccounts 返回某主播绑定的全部抖音账号。
func (r *Repo) ListAccounts(ctx context.Context, personID uint64) ([]domain.Account, error) {
	var out []domain.Account
	if err := r.db.SelectContext(ctx, &out,
		`SELECT id, person_id, anchor_id, douyin_no, anchor_name, is_primary, status, created_at
		 FROM account WHERE person_id = ? ORDER BY is_primary DESC, id ASC`, personID); err != nil {
		return nil, fmt.Errorf("查询账号列表: %w", err)
	}
	return out, nil
}

// BindAccount 给主播绑定抖音账号。同一 anchor_id 重复绑定会返回冲突错误。
func (r *Repo) BindAccount(ctx context.Context, a *domain.Account) error {
	const query = `INSERT INTO account
		(person_id, anchor_id, douyin_no, anchor_name, is_primary, status)
		VALUES (?, ?, ?, ?, ?, ?)`

	res, err := r.db.ExecContext(ctx, query,
		a.PersonID, a.AnchorID, a.DouyinNo, a.AnchorName, a.IsPrimary, a.Status)
	if err != nil {
		return fmt.Errorf("绑定账号: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("读取新账号 ID: %w", err)
	}
	a.ID = uint64(id)
	return nil
}

// ResolveAnchorOwner 按 anchor_id 反查归属人，导入数据时用来补 person_id。
func (r *Repo) ResolveAnchorOwner(ctx context.Context, anchorID string) (uint64, error) {
	var personID uint64
	err := r.db.GetContext(ctx, &personID,
		"SELECT person_id FROM account WHERE anchor_id = ?", anchorID)
	if err != nil {
		return 0, translateNotFound(err, "反查账号归属")
	}
	return personID, nil
}

// ResolveAnchorOwnerFlexible 弹性归属匹配：anchor_id → 抖音号 → 唯一艺名。
// 与 615 的 matchImportRows 对齐——运营的 CSV 经常只有艺名列，
// 只认 ID 会让整张表全部「未匹配」。艺名撞名（多人同名）时不猜，返回未匹配。
func (r *Repo) ResolveAnchorOwnerFlexible(ctx context.Context, anchorID, name string) (uint64, error) {
	if anchorID != "" {
		var personID uint64
		err := r.db.GetContext(ctx, &personID,
			"SELECT person_id FROM account WHERE anchor_id = ?", anchorID)
		if err == nil {
			return personID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("反查账号归属: %w", err)
		}
		// ID 列里常被填抖音号，再试一次
		err = r.db.GetContext(ctx, &personID,
			"SELECT person_id FROM account WHERE douyin_no = ?", anchorID)
		if err == nil {
			return personID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("按抖音号反查: %w", err)
		}
	}
	if name != "" {
		var ids []uint64
		if err := r.db.SelectContext(ctx, &ids,
			"SELECT id FROM person WHERE name = ? AND deleted_at IS NULL", name); err != nil {
			return 0, fmt.Errorf("按艺名反查: %w", err)
		}
		if len(ids) == 1 {
			return ids[0], nil
		}
	}
	return 0, ErrNotFound
}

// ListTierRules 取某粒度的等级规则，按门槛从高到低排。
func (r *Repo) ListTierRules(ctx context.Context, scope domain.TierScope) ([]domain.TierRule, error) {
	var out []domain.TierRule
	if err := r.db.SelectContext(ctx, &out,
		`SELECT id, scope, label, min_wave, sort_order FROM tier_rule
		 WHERE scope = ? ORDER BY min_wave DESC`, string(scope)); err != nil {
		return nil, fmt.Errorf("查询等级规则: %w", err)
	}
	return out, nil
}

// UpsertAnchorAccount 建档/绑号的共用核心（bot「姓名-抖音号」与网页 CSV 批量导入共用）。
// anchorID 为主播 ID（长数字），douyinNo 为抖音号；缺一个用另一个顶替。
// 返回 action：
//   - "created"：新建主播 + 主账号
//   - "bound"：姓名已存在 → 给已有主播加副号
//   - "already"：号已绑（anchor_id 或 douyin_no 命中）→ 不动，personName 为号的主人
func (r *Repo) UpsertAnchorAccount(ctx context.Context, name, anchorID, douyinNo string, gender domain.Gender) (action, personName string, err error) {
	if strings.TrimSpace(name) == "" {
		return "", "", errors.New("姓名为空")
	}
	if anchorID == "" && douyinNo == "" {
		return "", "", errors.New("缺少主播 ID / 抖音号")
	}
	if douyinNo == "" {
		douyinNo = anchorID
	}
	if anchorID == "" {
		anchorID = douyinNo
	}

	// 号已绑给别人 → 不偷偷改绑
	keys := []string{anchorID}
	if douyinNo != anchorID {
		keys = append(keys, douyinNo)
	}
	for _, key := range keys {
		if ownerID, err := r.ResolveAnchorOwnerFlexible(ctx, key, ""); err == nil {
			if p, perr := r.GetPerson(ctx, ownerID); perr == nil {
				return "already", p.Name, nil
			}
		}
	}

	persons, err := r.FindPersonsByName(ctx, name)
	if err != nil {
		return "", "", err
	}
	isPrimary := true
	var person domain.Person
	if len(persons) > 0 {
		isPrimary = false
		person = persons[0]
	} else {
		p := &domain.Person{Name: name, Gender: gender, Status: domain.PersonStatusActive}
		if err := r.CreatePerson(ctx, p); err != nil {
			return "", "", err
		}
		persons, err = r.FindPersonsByName(ctx, name)
		if err != nil {
			return "", "", err
		}
		if len(persons) != 1 {
			return "", "", fmt.Errorf("新建主播「%s」后查询异常（命中 %d 条）", name, len(persons))
		}
		person = persons[0]
	}
	if err := r.BindAccount(ctx, &domain.Account{
		PersonID:   person.ID,
		AnchorID:   anchorID,
		DouyinNo:   douyinNo,
		AnchorName: name,
		IsPrimary:  isPrimary,
		Status:     "active",
	}); err != nil {
		return "", "", fmt.Errorf("绑定账号: %w", err)
	}
	if isPrimary {
		return "created", person.Name, nil
	}
	return "bound", person.Name, nil
}
