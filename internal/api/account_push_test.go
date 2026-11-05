package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pushAdmin 记录推送接口收到的账户输入
type pushAdmin struct {
	AdminService
	inputs []AccountPushInput
}

// PushAccountCookies 记录输入并返回推送后的账户
func (admin *pushAdmin) PushAccountCookies(_ context.Context, input AccountPushInput) (AccountPushResult, error) {
	admin.inputs = append(admin.inputs, input)
	label := input.Email
	if label == "" {
		label = input.AccountID
	}
	return AccountPushResult{Account: AdminAccount{ID: label, Label: label}, Created: true}, nil
}

const pushCookiesJSON = `[
  {"name":"__Secure-1PSID","value":"psid","domain":".google.com","path":"/","secure":true,"httpOnly":true,"sameSite":"no_restriction"},
  {"name":"SAPISID","value":"sapisid","domain":".google.com","path":"/","secure":true,"httpOnly":true},
  {"name":"__Secure-1PAPISID","value":"p1","domain":".google.com","path":"/","secure":true,"httpOnly":true},
  {"name":"__Secure-3PAPISID","value":"p3","domain":".google.com","path":"/","secure":true,"httpOnly":true}
]`

// TestAccountPushTokenGate 验证未配置令牌时接口关闭、令牌错误时拒绝
func TestAccountPushTokenGate(t *testing.T) {
	admin := &pushAdmin{}
	handler := NewHandler(nil, Config{Admin: admin})
	request := httptest.NewRequest("POST", "/api/accounts/push-cookies", strings.NewReader(`{"cookies":`+pushCookiesJSON+`}`))
	request.Header.Set("X-Push-Token", "anything")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "push_disabled") {
		t.Fatalf("未配置令牌 = %d %s", recorder.Code, recorder.Body)
	}

	handler = NewHandler(nil, Config{Admin: admin, AccountPushToken: "secret"})
	request = httptest.NewRequest("POST", "/api/accounts/push-cookies", strings.NewReader(`{"cookies":`+pushCookiesJSON+`}`))
	request.Header.Set("X-Push-Token", "wrong")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("令牌错误 = %d %s", recorder.Code, recorder.Body)
	}
}

// TestAccountPushCookies 验证跨站请求可用、Cookie JSON 被解析、auth_user 兼容字符串与数字
func TestAccountPushCookies(t *testing.T) {
	admin := &pushAdmin{}
	handler := NewHandler(nil, Config{Admin: admin, AccountPushToken: "secret"})
	body := `{"cookies":` + pushCookiesJSON + `,"auth_user":"1","email":"User@Example.com","source":"userscript"}`
	request := httptest.NewRequest("POST", "/api/accounts/push-cookies", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Origin", "https://aistudio.google.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("推送 = %d %s", recorder.Code, recorder.Body)
	}
	var result AccountPushResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("响应 JSON: %v", err)
	}
	if result.Account.ID != "user@example.com" || !result.Created {
		t.Fatalf("响应 = %+v", result)
	}
	if len(admin.inputs) != 1 {
		t.Fatalf("输入次数 = %d", len(admin.inputs))
	}
	input := admin.inputs[0]
	if input.AuthUser != 1 || input.Email != "user@example.com" || input.Source != "userscript" {
		t.Fatalf("输入 = %+v", input)
	}
	if !input.DiscoverAll {
		t.Fatal("未带 discover_all 时应默认识别全部子号")
	}
	if len(input.StorageState.Cookies) != 4 {
		t.Fatalf("Cookie 数 = %d", len(input.StorageState.Cookies))
	}
	if input.StorageState.Cookies[0].Name != "__Secure-1PSID" || !input.StorageState.Cookies[0].Secure {
		t.Fatalf("Cookie = %+v", input.StorageState.Cookies[0])
	}
}

// TestAccountPushDiscoverAllOptOut 验证显式关闭子号识别时不再传递 discover_all
func TestAccountPushDiscoverAllOptOut(t *testing.T) {
	admin := &pushAdmin{}
	handler := NewHandler(nil, Config{Admin: admin, AccountPushToken: "secret"})
	body := `{"cookies":` + pushCookiesJSON + `,"discover_all":false}`
	request := httptest.NewRequest("POST", "/api/accounts/push-cookies", strings.NewReader(body))
	request.Header.Set("X-Push-Token", "secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("推送 = %d %s", recorder.Code, recorder.Body)
	}
	if len(admin.inputs) != 1 || admin.inputs[0].DiscoverAll {
		t.Fatalf("输入 = %+v", admin.inputs)
	}
}

// TestAccountPushCookieText 验证 cookies 也接受 Cookie 请求头文本，auth_user 越界被拒绝
func TestAccountPushCookieText(t *testing.T) {
	admin := &pushAdmin{}
	handler := NewHandler(nil, Config{Admin: admin, AccountPushToken: "secret"})
	body := `{"cookies":"__Secure-1PSID=psid; SAPISID=sapisid; __Secure-1PAPISID=p1; __Secure-3PAPISID=p3","auth_user":0}`
	request := httptest.NewRequest("POST", "/api/accounts/push-cookies", strings.NewReader(body))
	request.Header.Set("X-Push-Token", "secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Cookie 文本推送 = %d %s", recorder.Code, recorder.Body)
	}

	request = httptest.NewRequest("POST", "/api/accounts/push-cookies", strings.NewReader(`{"cookies":`+pushCookiesJSON+`,"auth_user":10}`))
	request.Header.Set("X-Push-Token", "secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_auth_user") {
		t.Fatalf("越界 auth_user = %d %s", recorder.Code, recorder.Body)
	}
}
