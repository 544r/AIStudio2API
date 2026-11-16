package aistudio

import (
	"context"
	"reflect"
	"testing"
)

// switchAfterPool 创建三个就绪账户并按每 N 条换号策略配置账户池
func switchAfterPool(turns int) (*AccountPool, AccountSelection) {
	model := Model{ID: "gemini", Methods: []string{"generateContent"}, Capabilities: map[string]bool{"chat_model": true}}
	accounts := []*Account{
		{ID: "a@example.com", Config: AccountConfig{Enabled: true}, State: AccountReady, Models: []Model{model}, modelAccessGeneration: 1},
		{ID: "b@example.com", Config: AccountConfig{Enabled: true}, State: AccountReady, Models: []Model{model}, modelAccessGeneration: 1},
		{ID: "c@example.com", Config: AccountConfig{Enabled: true}, State: AccountReady, Models: []Model{model}, modelAccessGeneration: 1},
	}
	pool := NewAccountPool(accounts, 1)
	pool.SetUpstreamChannels([]Channel{ChannelPlayground})
	pool.SetRoutingStrategy("switch-after")
	pool.SetRoutingSwitchTurns(turns)
	return pool, AccountSelection{ModelID: "gemini", Method: "generateContent"}
}

// pickSwitchAfter 取得账户槽位、记下一次成功生成并归还槽位
func pickSwitchAfter(t *testing.T, pool *AccountPool, selection AccountSelection) string {
	t.Helper()
	lease, _, err := pool.TryAcquireFor(context.Background(), selection)
	if err != nil || lease == nil {
		t.Fatalf("lease=%v err=%v", lease, err)
	}
	accountID := lease.Account().ID
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	pool.RecordRoutingTurn(accountID, "gemini")
	return accountID
}

// TestSwitchAfterTurnsCyclesAccounts 验证同一模型成功 N 条后换到下一个账户并循环
func TestSwitchAfterTurnsCyclesAccounts(t *testing.T) {
	pool, selection := switchAfterPool(2)
	var picks []string
	for range 7 {
		picks = append(picks, pickSwitchAfter(t, pool, selection))
	}
	want := []string{"a@example.com", "a@example.com", "b@example.com", "b@example.com", "c@example.com", "c@example.com", "a@example.com"}
	if !reflect.DeepEqual(picks, want) {
		t.Fatalf("picks=%v want=%v", picks, want)
	}
}

// TestSwitchAfterUsesAccountOrder 验证换号顺序遵循 ACCOUNT_ORDER
func TestSwitchAfterUsesAccountOrder(t *testing.T) {
	pool, selection := switchAfterPool(2)
	pool.SetAccountOrder([]string{"c@example.com", "a@example.com", "b@example.com"})
	var picks []string
	for range 7 {
		picks = append(picks, pickSwitchAfter(t, pool, selection))
	}
	want := []string{"c@example.com", "c@example.com", "a@example.com", "a@example.com", "b@example.com", "b@example.com", "c@example.com"}
	if !reflect.DeepEqual(picks, want) {
		t.Fatalf("picks=%v want=%v", picks, want)
	}
}

// TestSwitchAfterIgnoresUnrecordedAttempts 验证失败重试不推进换号计数
func TestSwitchAfterIgnoresUnrecordedAttempts(t *testing.T) {
	pool, selection := switchAfterPool(2)
	if got := pickSwitchAfter(t, pool, selection); got != "a@example.com" {
		t.Fatalf("first pick=%s", got)
	}
	for range 3 {
		lease, _, err := pool.TryAcquireFor(context.Background(), selection)
		if err != nil || lease == nil {
			t.Fatalf("lease=%v err=%v", lease, err)
		}
		if got := lease.Account().ID; got != "a@example.com" {
			t.Fatalf("unrecorded attempt picked %s, want a@example.com", got)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if got := pickSwitchAfter(t, pool, selection); got != "a@example.com" {
		t.Fatalf("second recorded pick=%s, want a@example.com", got)
	}
	if got := pickSwitchAfter(t, pool, selection); got != "b@example.com" {
		t.Fatalf("after two recorded picks=%s, want b@example.com", got)
	}
}
