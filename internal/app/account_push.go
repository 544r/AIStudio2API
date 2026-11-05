package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/chromeauth"
)

// PushAccountCookies 用用户脚本推送的 Cookie 罐刷新已有账户，账户不存在时按推送的
// auth_user 验证登录的邮箱并导入新账户。推送接口由独立令牌鉴权，不走管理会话。
func (admin *runtimeAdmin) PushAccountCookies(ctx context.Context, input api.AccountPushInput) (api.AccountPushResult, error) {
	state := input.StorageState
	if err := state.Validate(); err != nil {
		return api.AccountPushResult{}, invalidAccount(err)
	}
	if _, err := aistudio.NewSigner().Authorization(state); err != nil {
		return api.AccountPushResult{}, invalidAccount(fmt.Errorf("认证状态无法用于 AI Studio: %w", err))
	}

	// 邮箱来自当前 u/N 页面的 WIZ 数据，比历史绑定更可信：多登录顺序变化后
	// 绑定可能指向别的子号，因此有邮箱时以邮箱为准，绑定只在识别不到邮箱时兜底。
	accountID := strings.ToLower(strings.TrimSpace(input.Email))
	if accountID == "" {
		accountID = strings.ToLower(strings.TrimSpace(input.AccountID))
	}
	verified := false
	if accountID == "" {
		// 脚本没给出账户时按 auth_user 打开对话页，识别这个会话登录的邮箱，
		// 多账号浏览器里推送的是哪个子号就以哪个子号的邮箱落库。
		verification, err := chromeauth.VerifyAuthUser(ctx, &state, admin.effectiveProxy(""), input.AuthUser)
		if errors.Is(err, chromeauth.ErrSignedOut) {
			return api.AccountPushResult{}, &adminOperationError{
				status: http.StatusBadRequest, code: "signed_out", message: err.Error(),
			}
		}
		if err != nil {
			return api.AccountPushResult{}, invalidAccount(err)
		}
		accountID = strings.ToLower(strings.TrimSpace(verification.Email))
		verified = true
	}

	if account, err := admin.pool.Account(accountID); err == nil {
		updated, refreshErr := admin.refreshPushedAccount(ctx, account.ID, input, state)
		if refreshErr != nil {
			return api.AccountPushResult{}, refreshErr
		}
		return api.AccountPushResult{Account: updated, SessionValid: verifiedPointer(verified)}, nil
	} else if !errors.Is(err, aistudio.ErrAccountNotFound) {
		return api.AccountPushResult{}, accountOperationError(err)
	}

	account, err := admin.ImportAccountState(ctx, api.AccountStateInput{
		Label: accountID, AuthUser: input.AuthUser, StorageState: state,
	})
	if err != nil {
		// 并发推送同一账户时另一个请求可能刚建好目录，此时按刷新处理
		var operationErr *adminOperationError
		if errors.As(err, &operationErr) && operationErr.code == "account_exists" {
			if existing, lookupErr := admin.pool.Account(accountID); lookupErr == nil {
				updated, refreshErr := admin.refreshPushedAccount(ctx, existing.ID, input, state)
				if refreshErr != nil {
					return api.AccountPushResult{}, refreshErr
				}
				return api.AccountPushResult{Account: updated, SessionValid: verifiedPointer(verified)}, nil
			}
		}
		return api.AccountPushResult{}, err
	}
	admin.requests.log(account.ID, "INFO", "账户由用户脚本推送导入 | 会话="+strconv.Itoa(input.AuthUser))
	return api.AccountPushResult{Account: account, Created: true, SessionValid: verifiedPointer(verified)}, nil
}

// refreshPushedAccount 在账户租约内合并推送的 Cookie、更新会话序号并重启 Worker
func (admin *runtimeAdmin) refreshPushedAccount(
	ctx context.Context,
	accountID string,
	input api.AccountPushInput,
	pushed aistudio.StorageState,
) (api.AdminAccount, error) {
	lease, err := admin.pool.AcquireAccount(ctx, accountID)
	if err != nil {
		return api.AdminAccount{}, accountOperationError(err)
	}
	account := lease.Account()
	state := account.StorageState
	state.Cookies = mergePushedCookies(state.Cookies, pushed.Cookies)
	state.Origins = mergePushedOrigins(state.Origins, pushed.Origins)
	if _, err := aistudio.NewSigner().Authorization(state); err != nil {
		return api.AdminAccount{}, errors.Join(invalidAccount(fmt.Errorf("认证状态无法用于 AI Studio: %w", err)), lease.Release())
	}
	config := account.Config
	if config.AuthUser != input.AuthUser {
		// 浏览器里 Google 多登录的序号可能变化，推送的 u/N 就是当前真实序号
		config.AuthUser = input.AuthUser
		if err := lease.SaveConfig(config); err != nil {
			return api.AdminAccount{}, errors.Join(err, lease.Release())
		}
	}
	if err := admin.workers.Reset(accountID); err != nil {
		return api.AdminAccount{}, errors.Join(err, lease.Release())
	}
	if err := lease.SaveStorageState(state); err != nil {
		return api.AdminAccount{}, errors.Join(err, lease.Release())
	}
	if err := admin.service.changeModels(func() error { return admin.pool.MarkReady(accountID) }); err != nil {
		return api.AdminAccount{}, errors.Join(err, lease.Release())
	}
	if err := lease.Release(); err != nil {
		return api.AdminAccount{}, err
	}
	admin.requests.log(accountID, "INFO", "账户由用户脚本推送刷新 | Cookie="+strconv.Itoa(len(pushed.Cookies)))
	return admin.account(accountID)
}

// mergePushedCookies 按 (名称, 域, 路径) 用推送值覆盖账户现有 Cookie，保留推送里没有的长寿凭据
func mergePushedCookies(current []aistudio.StateCookie, pushed []aistudio.StateCookie) []aistudio.StateCookie {
	merged := make([]aistudio.StateCookie, 0, len(current)+len(pushed))
	index := make(map[[3]string]int, len(current))
	key := func(cookie aistudio.StateCookie) [3]string {
		domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cookie.Domain), "."))
		path := strings.TrimSpace(cookie.Path)
		if path == "" {
			path = "/"
		}
		return [3]string{cookie.Name, domain, path}
	}
	for _, cookie := range current {
		index[key(cookie)] = len(merged)
		merged = append(merged, cookie)
	}
	for _, cookie := range pushed {
		identity := key(cookie)
		if position, exists := index[identity]; exists {
			merged[position] = cookie
			continue
		}
		index[identity] = len(merged)
		merged = append(merged, cookie)
	}
	return merged
}

// mergePushedOrigins 按 origin 用推送的 localStorage 覆盖账户现有项
func mergePushedOrigins(current []aistudio.StorageOrigin, pushed []aistudio.StorageOrigin) []aistudio.StorageOrigin {
	if len(pushed) == 0 {
		return current
	}
	merged := make([]aistudio.StorageOrigin, 0, len(current)+len(pushed))
	index := make(map[string]int, len(current))
	for _, origin := range current {
		index[origin.Origin] = len(merged)
		merged = append(merged, origin)
	}
	for _, origin := range pushed {
		if position, exists := index[origin.Origin]; exists {
			merged[position] = origin
			continue
		}
		index[origin.Origin] = len(merged)
		merged = append(merged, origin)
	}
	return merged
}

// verifiedPointer 返回登录态验证结果，未验证时返回 nil 让 JSON 省略该字段
func verifiedPointer(verified bool) *bool {
	if !verified {
		return nil
	}
	value := true
	return &value
}
