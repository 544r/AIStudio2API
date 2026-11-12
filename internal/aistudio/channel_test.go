package aistudio

import (
	"context"
	"reflect"
	"testing"
)

// TestChannelSelection 验证 Build 通道移除后只剩 Playground：即使配置里写了 build，
// 生成、计数与限定通道的选择都只落到 Playground，显式指定 Build 的选择没有通道可用。
func TestChannelSelection(t *testing.T) {
	pool := NewAccountPool(nil, 1)
	pool.SetUpstreamChannels([]Channel{ChannelPlayground, ChannelBuild})
	for _, item := range []struct {
		selection AccountSelection
		want      []Channel
	}{
		{AccountSelection{ModelID: "gemini", Method: "generateContent"}, []Channel{ChannelPlayground}},
		{AccountSelection{ModelID: "gemini", Method: "generateContent", Channel: ChannelBuild}, nil},
		{AccountSelection{ModelID: "gemini", Method: "generateContent", ResourceID: "file", Channel: ChannelBuild}, []Channel{ChannelPlayground}},
		{AccountSelection{ModelID: "gemini", Method: "countTokens"}, []Channel{ChannelPlayground}},
		{AccountSelection{ModelID: "gemini", Method: "countTokens", Channel: ChannelBuild}, []Channel{ChannelPlayground}},
		{AccountSelection{ModelID: "gemini", Method: "generateContent", PlaygroundOnly: true, Channel: ChannelBuild}, []Channel{ChannelPlayground}},
	} {
		if got := pool.selectionChannelsLocked(item.selection); !reflect.DeepEqual(got, item.want) {
			t.Errorf("selection=%+v channels=%v want=%v", item.selection, got, item.want)
		}
	}
	if got := pool.selectionChannelsLocked(AccountSelection{ModelID: "gemini", Method: "generateContent", Channel: ChannelBuild}); len(got) != 0 {
		t.Fatalf("disabled Build channels=%v", got)
	}
}

// TestRetiredBuildNeverSelected 验证优先通道设为 Build 时也不会选到 Build，
// 账户只按 Playground 出租约。
func TestRetiredBuildNeverSelected(t *testing.T) {
	model := Model{ID: "gemini", Methods: []string{"generateContent"}, Capabilities: map[string]bool{"chat_model": true}}
	accounts := []*Account{
		{ID: "a@example.com", Config: AccountConfig{Enabled: true}, State: AccountReady, Models: []Model{model}, modelAccessGeneration: 1},
		{ID: "b@example.com", Config: AccountConfig{Enabled: true}, State: AccountReady, Models: []Model{model}, modelAccessGeneration: 1},
	}
	pool := NewAccountPool(accounts, 1)
	pool.SetUpstreamChannels([]Channel{ChannelPlayground, ChannelBuild})
	for _, account := range accounts {
		if err := pool.SetBuildCatalog(account.ID, []Model{model}); err != nil {
			t.Fatal(err)
		}
	}
	selection := AccountSelection{ModelID: "gemini", Method: "generateContent", PreferredChannel: ChannelBuild}
	used := make(map[string]struct{})
	for range 4 {
		lease, _, err := pool.TryAcquireFor(context.Background(), selection)
		if err != nil || lease == nil {
			t.Fatalf("lease=%v err=%v", lease, err)
		}
		if lease.Channel() != ChannelPlayground {
			t.Fatalf("account=%s channel=%s, want playground", lease.Account().ID, lease.Channel())
		}
		used[lease.Account().ID] = struct{}{}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if len(used) != 2 {
		t.Fatalf("used accounts=%v, want both accounts", used)
	}
}

// TestIsRetiredBuildModel 验证 -build 后缀识别覆盖带 models/ 前缀的写法
func TestIsRetiredBuildModel(t *testing.T) {
	for _, modelID := range []string{"gemini-4-build", "models/gemini-4-build"} {
		if !IsRetiredBuildModel(modelID) {
			t.Fatalf("%q should be retired", modelID)
		}
	}
	for _, modelID := range []string{"gemini-4", "models/gemini-4", "build", "gemini-build-preview"} {
		if IsRetiredBuildModel(modelID) {
			t.Fatalf("%q should not be retired", modelID)
		}
	}
}
