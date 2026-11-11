package app

import (
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// TestStorageStatesEqual 验证推送去重的判定口径：会话 Cookie 与 localStorage 的值
// 变化才算变化，轮换型反滥用 Cookie（SIDCC 家族）与顺序差异不触发刷新。
func TestStorageStatesEqual(t *testing.T) {
	cookie := func(name, value, domain, path string) aistudio.StateCookie {
		return aistudio.StateCookie{Name: name, Value: value, Domain: domain, Path: path}
	}
	base := func() aistudio.StorageState {
		return aistudio.StorageState{
			Cookies: []aistudio.StateCookie{
				cookie("SAPISID", "sig-1", ".google.com", "/"),
				cookie("__Secure-1PSID", "psid-1", ".google.com", "/"),
				cookie("LSID", "lsid-1", "accounts.google.com", "/"),
			},
			Origins: []aistudio.StorageOrigin{{
				Origin:       "https://aistudio.google.com",
				LocalStorage: []aistudio.StorageItem{{Name: "k", Value: "v"}},
			}},
		}
	}

	cases := []struct {
		name   string
		mutate func(state *aistudio.StorageState)
		equal  bool
	}{
		{"完全一致", func(*aistudio.StorageState) {}, true},
		{
			"仅顺序不同",
			func(state *aistudio.StorageState) {
				cookies := state.Cookies
				state.Cookies = []aistudio.StateCookie{cookies[2], cookies[0], cookies[1]}
			},
			true,
		},
		{
			"域前导点写法不同",
			func(state *aistudio.StorageState) {
				state.Cookies[0].Domain = "google.com"
			},
			true,
		},
		{
			"路径缺省与 / 等价",
			func(state *aistudio.StorageState) {
				state.Cookies[0].Path = ""
			},
			true,
		},
		{
			"新增的 SIDCC 噪声不触发刷新",
			func(state *aistudio.StorageState) {
				state.Cookies = append(state.Cookies, cookie("SIDCC", "new-noise", ".google.com", "/"))
			},
			true,
		},
		{
			"过期时间不同不参与比较",
			func(state *aistudio.StorageState) {
				state.Cookies[1].Expires = 1893456000
			},
			true,
		},
		{
			"会话 Cookie 值变化",
			func(state *aistudio.StorageState) {
				state.Cookies[1].Value = "psid-2"
			},
			false,
		},
		{
			"新增会话 Cookie",
			func(state *aistudio.StorageState) {
				state.Cookies = append(state.Cookies, cookie("OSID", "osid-1", "aistudio.google.com", "/"))
			},
			false,
		},
		{
			"localStorage 变化",
			func(state *aistudio.StorageState) {
				state.Origins[0].LocalStorage[0].Value = "v2"
			},
			false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			left := base()
			right := base()
			testCase.mutate(&right)
			if got := storageStatesEqual(left, right); got != testCase.equal {
				t.Fatalf("storageStatesEqual = %v, want %v", got, testCase.equal)
			}
			// 比较必须对称：顺序与判定的差异不应让结果翻转
			if got := storageStatesEqual(right, left); got != testCase.equal {
				t.Fatalf("storageStatesEqual（反向） = %v, want %v", got, testCase.equal)
			}
		})
	}
}

// TestMergePushedCookiesOverridesByKeyOnly 验证推送合并语义：同键覆盖、推送缺失的保留，
// 域名按去掉前导点后匹配，路径缺省按 / 匹配。
func TestMergePushedCookiesOverridesByKeyOnly(t *testing.T) {
	current := []aistudio.StateCookie{
		{Name: "SAPISID", Value: "old", Domain: ".google.com", Path: "/"},
		{Name: "OSID", Value: "server-only", Domain: "aistudio.google.com", Path: "/"},
	}
	pushed := []aistudio.StateCookie{
		{Name: "SAPISID", Value: "new", Domain: "google.com", Path: "/"},
		{Name: "__Secure-1PSID", Value: "psid", Domain: ".google.com", Path: ""},
	}
	merged := mergePushedCookies(current, pushed)
	values := make(map[string]string, len(merged))
	for _, cookie := range merged {
		values[cookie.Name] = cookie.Value
	}
	if values["SAPISID"] != "new" {
		t.Fatalf("SAPISID = %q, want pushed value", values["SAPISID"])
	}
	if values["OSID"] != "server-only" {
		t.Fatalf("OSID = %q, want retained server value", values["OSID"])
	}
	if values["__Secure-1PSID"] != "psid" {
		t.Fatalf("__Secure-1PSID = %q, want pushed value", values["__Secure-1PSID"])
	}
	if len(merged) != 3 {
		t.Fatalf("merged length = %d, want 3", len(merged))
	}
}
