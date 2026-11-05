package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// AccountPushInput 是用户脚本推送的一次 Cookie 罐：脚本按页面上的 u/N 给出 auth_user，
// 首次推送可只给邮箱提示，之后带上服务端返回的 account_id 直接绑定账户。
type AccountPushInput struct {
	StorageState aistudio.StorageState `json:"-"`
	AuthUser     int                   `json:"auth_user"`
	Email        string                `json:"email,omitempty"`
	AccountID    string                `json:"account_id,omitempty"`
	Source       string                `json:"source,omitempty"`
}

// AccountPushResult 是一次推送的结果：账户为推送后的摘要，SessionValid 只在本次推送
// 向 AI Studio 验证过登录态时有值。
type AccountPushResult struct {
	Account      AdminAccount `json:"account"`
	Created      bool         `json:"created"`
	SessionValid *bool        `json:"session_valid,omitempty"`
}

// accountPushRequest 是推送接口的请求体，cookies 接受 Cookie-Editor 风格的 JSON 数组
// 或一段 Cookie 请求头文本，auth_user 接受数字或字符串形式的 0-9。
type accountPushRequest struct {
	Cookies   json.RawMessage `json:"cookies"`
	AuthUser  json.RawMessage `json:"auth_user"`
	Email     string          `json:"email"`
	AccountID string          `json:"account_id"`
	Source    string          `json:"source"`
}

// handleAccountPushCookies 接收用户脚本推送的 Cookie 罐并导入或刷新账户，不经过管理会话。
// 该接口只为脚本设计，凭 ACCOUNT_PUSH_TOKEN 鉴权；未配置令牌时保持关闭。
func (s *server) handleAccountPushCookies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.TrimSpace(s.config.AccountPushToken) == "" {
		writeAdminError(w, http.StatusForbidden, "push_disabled", "Set ACCOUNT_PUSH_TOKEN to enable cookie push")
		return
	}
	if !s.pushAuthorized(r) {
		writeAdminError(w, http.StatusUnauthorized, "push_token_invalid", "Push token is missing or invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var input accountPushRequest
	if err := decodeJSON(r, &input); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid push payload")
		return
	}
	authUser, err := parsePushAuthUser(input.AuthUser)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_auth_user", err.Error())
		return
	}
	cookieText, err := pushCookieText(input.Cookies)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_cookies", err.Error())
		return
	}
	state, err := aistudio.ParseCookieText(cookieText)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_cookies", err.Error())
		return
	}
	result, err := s.config.Admin.PushAccountCookies(r.Context(), AccountPushInput{
		StorageState: state,
		AuthUser:     authUser,
		Email:        strings.ToLower(strings.TrimSpace(input.Email)),
		AccountID:    strings.ToLower(strings.TrimSpace(input.AccountID)),
		Source:       strings.TrimSpace(input.Source),
	})
	if err != nil {
		writeAdminUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// pushAuthorized 校验推送令牌：优先读 X-Push-Token，也认 Authorization Bearer。
// 不接受查询参数，避免令牌出现在代理与访问日志里。
func (s *server) pushAuthorized(r *http.Request) bool {
	expected := strings.TrimSpace(s.config.AccountPushToken)
	if expected == "" {
		return false
	}
	candidates := []string{
		r.Header.Get("X-Push-Token"),
		bearerToken(r.Header.Get("Authorization")),
	}
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" &&
			subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1 {
			return true
		}
	}
	return false
}

// pushCookieText 把 cookies 字段转成 ParseCookieText 接受的文本；字符串形式原样使用
func pushCookieText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", errors.New("cookies 不能为空")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", errors.New("cookies 字符串无效")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return "", errors.New("cookies 不能为空")
		}
		return text, nil
	}
	return string(trimmed), nil
}

// parsePushAuthUser 解析 0-9 的会话序号，接受数字与数字字符串两种写法
func parsePushAuthUser(raw json.RawMessage) (int, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, nil
	}
	trimmed = strings.Trim(trimmed, `"`)
	value, err := strconv.Atoi(trimmed)
	if err != nil || value < 0 || value > 9 {
		return 0, errors.New("auth_user 必须是 0-9 的整数")
	}
	return value, nil
}
