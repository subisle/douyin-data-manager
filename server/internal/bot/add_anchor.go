// 「姓名-抖音号」加主播：运营在群里发一条消息就把新人建档+绑号，
// 省得为一个人专门开电脑上网页。设计约束：
//   - 抖音号已被占用 → 明确告知绑给了谁，不偷偷改绑
//   - 姓名已存在（唯一）→ 不新建，直接给已有主播加账号
//   - 重名（多个）→ 不猜，让运营换个更独特的名字
//
// 同一套规则也用于主播名单 CSV 的批量导入（handleAnchorCSV）。
// 建档/绑号核心在 repo.UpsertAnchorAccount，这里只负责文案。
package bot

import (
	"context"
	"fmt"
	"strings"

	"douyin-server/internal/csvparse"
	"douyin-server/internal/domain"
)

// handleAddAnchor 处理「姓名-抖音号」，返回给用户的回复。
func (m *Manager) handleAddAnchor(ctx context.Context, name, douyinNo string) (Outbound, error) {
	out := Outbound{}
	action, personName, err := m.repo.UpsertAnchorAccount(ctx, name, douyinNo, douyinNo, domain.GenderUnknown)
	if err != nil {
		return out, err
	}
	switch action {
	case "already":
		out.Text = fmt.Sprintf("抖音号 %s 已经绑定给主播「%s」了，不用重复添加。", douyinNo, personName)
	case "bound":
		out.Text = fmt.Sprintf("已把抖音号 %s 绑到已有主播「%s」。现在发 CSV 就能匹配了。", douyinNo, personName)
	default:
		out.Text = fmt.Sprintf("已添加主播「%s」（抖音号 %s）。现在发 CSV 就能匹配了；性别等资料可在网页上补。", name, douyinNo)
	}
	return out, nil
}

// handleAnchorCSV 处理主播名单 CSV（表头含 姓名/昵称 + 抖音号，无音浪/时长列）。
// 批量走与「姓名-抖音号」完全相同的建档规则，回复汇总结果。
func (m *Manager) handleAnchorCSV(ctx context.Context, rows []csvparse.Row) (Outbound, error) {
	out := Outbound{}

	var created, bound, already, skipped int
	var notes []string
	addNote := func(format string, args ...any) {
		if len(notes) >= 5 {
			return
		}
		notes = append(notes, "· "+fmt.Sprintf(format, args...))
	}

	for _, row := range rows {
		name := row.Name
		id := row.AnchorID
		if id == "" {
			id = row.DouyinNo
		}
		if name == "" || id == "" {
			skipped++
			addNote("第%d行：缺姓名或抖音号，跳过", row.RawIndex)
			continue
		}
		action, personName, err := m.repo.UpsertAnchorAccount(ctx, name, id, row.DouyinNo, domain.GenderUnknown)
		if err != nil {
			skipped++
			addNote("第%d行（%s）导入失败：%v", row.RawIndex, name, err)
			continue
		}
		switch action {
		case "created":
			created++
		case "bound":
			bound++
		default:
			already++
			if already <= 3 {
				addNote("抖音号 %s 已绑给「%s」，未重复添加", id, personName)
			}
		}
	}

	lines := []string{
		fmt.Sprintf("主播名单已导入：新建 %d 人，已有主播加号 %d 个，重复跳过 %d 条，无法导入 %d 条。",
			created, bound, already, skipped),
	}
	lines = append(lines, notes...)
	if skipped > 0 && len(notes) < 5 {
		lines = append(lines, "（无法导入的行请补全姓名和抖音号后重发）")
	}
	out.Text = strings.Join(lines, "\n")
	return out, nil
}
