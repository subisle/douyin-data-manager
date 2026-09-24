package bot

import (
	"testing"
	"time"
)

// q 退出：发了日期口令又反悔，是这个机器人最常见的误操作。
// 必须 Beck 两头都能验——IntentQuit 的常量只是为了让管理器先一步拦下。
func TestIsQuitCommand(t *testing.T) {
	quit := []string{"q", "Q", " q ", "quit", "退出", "取消", "算了"}
	for _, s := range quit {
		if !IsQuitCommand(s) {
			t.Errorf("IsQuitCommand(%q) = false, 期望 true", s)
		}
	}
	// 不能被当成退出的：艺名可能是单字/单字母组合，群里也常发这类东西
	notQuit := []string{"qq", "去", "q发言人", "9.1", "柚子", "改名", ""}
	for _, s := range notQuit {
		if IsQuitCommand(s) {
			t.Errorf("IsQuitCommand(%q) = true, 期望 false", s)
		}
	}
}

func TestCancelConversation(t *testing.T) {
	m := NewManager(nil)
	conv := "conv-1"

	// 什么都没有：也要回话，不能让用户以为机器人掉线
	if got := m.cancelConversation(conv); got == "" {
		t.Fatal("没有进行中的流程时也应返回提示文案")
	}

	// 有日期口令
	m.rememberImportDate(conv, time.Now().AddDate(0, 0, -1).Format("2006-01-02"))
	if _, ok := m.pending[conv]; !ok {
		t.Fatal("rememberImportDate 没写入 pending")
	}
	if got := m.cancelConversation(conv); got == "" {
		t.Fatal("取消日期口令后应有回复")
	}
	if _, ok := m.pending[conv]; ok {
		t.Fatal("q 之后 pending 应被清掉")
	}

	// 有改名流程
	m.setPendingOp(conv, &pendingOp{kind: opRename, step: 1})
	if got := m.cancelConversation(conv); got == "" {
		t.Fatal("取消改名流程后应有回复")
	}
	if _, ok := m.pendingOps[conv]; ok {
		t.Fatal("q 之后待办应被清掉")
	}
}

// ParseIntent 也要认得出 q，否则 /bots/parse 的答案和真实行为不一致。
func TestParseIntentQuit(t *testing.T) {
	got := ParseIntent("q", time.Now())
	if got.Kind != IntentQuit {
		t.Fatalf("ParseIntent(q) kind = %q, 期望 %q", got.Kind, IntentQuit)
	}
}
