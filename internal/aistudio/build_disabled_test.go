package aistudio

import "testing"

// TestBuildDisabled 验证 fork 禁用 Build 后 -build 模型与 assistant 通道都不可用。
func TestBuildDisabled(t *testing.T) {
	pool := NewAccountPool(nil, 1)
	if pool.AssistantModel("gemini-3.8-flash-build") {
		t.Fatal("-build 模型不应被识别为 assistant 模型")
	}
	pool.mu.Lock()
	enabled := pool.channelEnabledLocked(ChannelAssistant)
	pool.mu.Unlock()
	if enabled {
		t.Fatal("assistant 通道不应启用")
	}
	for _, channel := range pool.selectionChannelsLocked(AccountSelection{ModelID: "gemini-3.8-flash-build", Method: "generateContent"}) {
		if channel == ChannelAssistant {
			t.Fatalf("-build 模型不应选中 assistant 通道: %v", channel)
		}
	}
}
