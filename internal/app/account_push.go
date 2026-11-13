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
// auth_user 验证登录的邮箱并导入新账户。开启 discover_all 时还会按共享登录罐识别
// 其它 u/N 子号，一次推送把所有已登录账号都登记上；白名单非空时只登记名单内的邮箱，
// skip_current 表示当前页面账号不在白名单，只登记名单内的其它子号。
func (admin *runtimeAdmin) PushAccountCookies(ctx context.Context, input api.AccountPushInput) (api.AccountPushResult, error) {
	state := input.StorageState
	if err := state.Validate(); err != nil {
		return api.AccountPushResult{}, invalidAccount(err)
	}
	if _, err := aistudio.NewSigner().Authorization(state); err != nil {
		return api.AccountPushResult{}, invalidAccount(fmt.Errorf("认证状态无法用于 AI Studio: %w", err))
	}

	allowed := normalizePushEmails(input.AllowedEmails)
	if input.SkipCurrent && len(allowed) == 0 {
		return api.AccountPushResult{}, invalidAccount(errors.New("skip_current 需要提供 allowed_emails"))
	}

	// 邮箱来自当前 u/N 页面的 WIZ 数据，比历史绑定更可信：多登录顺序变化后
	// 绑定可能指向别的子号，因此有邮箱时以邮箱为准，绑定只在识别不到邮箱时兜底。
	accountID := strings.ToLower(strings.TrimSpace(input.Email))
	if accountID == "" {
		accountID = strings.ToLower(strings.TrimSpace(input.AccountID))
	}
	skipCurrent := input.SkipCurrent
	verified := false
	if accountID == "" && !skipCurrent {
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
	if accountID != "" && len(allowed) > 0 {
		if _, ok := allowed[accountID]; !ok {
			skipCurrent = true
		}
	}
	if skipCurrent && !input.DiscoverAll {
		return api.AccountPushResult{}, invalidAccount(errors.New("skip_current 需要同时开启 discover_all"))
	}

	var account api.AdminAccount
	var created bool
	if skipCurrent {
		// 当前页面是白名单外的号（例如主号 /u/0）：不刷新它，只按同一份登录罐
		// 识别并登记白名单里的其它子号。
		admin.requests.log(accountID, "INFO", "用户脚本推送跳过当前账号（不在白名单）")
	} else {
		var err error
		account, created, err = admin.upsertPushedAccount(ctx, accountID, input, state)
		if err != nil {
			return api.AccountPushResult{}, err
		}
	}
	result := api.AccountPushResult{Account: account, Created: created}
	if input.DiscoverAll {
		discovered, discoveryErr := admin.discoverPushedAccounts(ctx, state, input, accountID, allowed)
		result.Discovered = discovered
		if discoveryErr != nil {
			result.DiscoveryError = discoveryErr.Error()
			admin.requests.log(accountID, "WARN", "用户脚本子号识别失败 | 错误="+discoveryErr.Error())
		} else {
			verified = true
		}
	}
	result.SessionValid = verifiedPointer(verified)
	return result, nil
}

// pushEmailLimit 是一次推送最多接受的白名单邮箱数
const pushEmailLimit = 32

// normalizePushEmails 把白名单邮箱小写、去空白、去重，空列表返回 nil
func normalizePushEmails(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			email := strings.ToLower(strings.TrimSpace(item))
			if email == "" || len(allowed) >= pushEmailLimit {
				continue
			}
			allowed[email] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

// upsertPushedAccount 刷新已有账户或导入新账户，返回账户与是否为新建
func (admin *runtimeAdmin) upsertPushedAccount(
	ctx context.Context,
	accountID string,
	input api.AccountPushInput,
	state aistudio.StorageState,
) (api.AdminAccount, bool, error) {
	if account, err := admin.pool.Account(accountID); err == nil {
		updated, refreshErr := admin.refreshPushedAccount(ctx, account.ID, input, state)
		return updated, false, refreshErr
	} else if !errors.Is(err, aistudio.ErrAccountNotFound) {
		return api.AdminAccount{}, false, accountOperationError(err)
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
				return updated, false, refreshErr
			}
		}
		return api.AdminAccount{}, false, err
	}
	admin.requests.log(account.ID, "INFO", "账户由用户脚本推送导入 | 会话="+strconv.Itoa(input.AuthUser))
	return account, true, nil
}

// discoverPushedAccounts 按同一份登录罐识别 u/N 子号，把未登记的账号一并导入；
// allowed 非空时只登记名单内的邮箱。
func (admin *runtimeAdmin) discoverPushedAccounts(
	ctx context.Context,
	state aistudio.StorageState,
	input api.AccountPushInput,
	primary string,
	allowed map[string]struct{},
) (int, error) {
	accounts, err := chromeauth.DiscoverCookieAccounts(ctx, &state, admin.effectiveProxy(""))
	if err != nil {
		return 0, err
	}
	discovered := 0
	var failures []error
	for _, item := range accounts {
		email := strings.ToLower(strings.TrimSpace(item.Email))
		if email == "" || email == primary {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[email]; !ok {
				continue
			}
		}
		pushed := api.AccountPushInput{
			AuthUser: item.AuthUser, Email: email, Source: input.Source,
		}
		if _, _, err := admin.upsertPushedAccount(ctx, email, pushed, state); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", email, err))
			continue
		}
		admin.requests.log(email, "INFO", "账户由用户脚本推送自动登记 | 会话="+strconv.Itoa(item.AuthUser))
		discovered++
	}
	return discovered, errors.Join(failures...)
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
	current := account.StorageState
	state := current
	state.Cookies = mergePushedCookies(current.Cookies, pushed.Cookies)
	state.Origins = mergePushedOrigins(current.Origins, pushed.Origins)
	if _, err := aistudio.NewSigner().Authorization(state); err != nil {
		return api.AdminAccount{}, errors.Join(invalidAccount(fmt.Errorf("认证状态无法用于 AI Studio: %w", err)), lease.Release())
	}
	config := account.Config
	authUserChanged := config.AuthUser != input.AuthUser
	// 推送内容与账户现有状态一致时跳过刷新：Worker 重启会把刚建立信任的会话
	// 打回冷启动（旗舰模型首个请求可能吃到上游瞬时 PERMISSION_DENIED），而这次
	// 推送没有任何新信息。鉴权失效的账户仍照常刷新，那是自救路径。
	if !authUserChanged && account.State == aistudio.AccountReady && storageStatesEqual(current, state) {
		if err := lease.Release(); err != nil {
			return api.AdminAccount{}, err
		}
		admin.requests.log(accountID, "INFO", "账户由用户脚本推送（Cookie 无变化，跳过刷新）| Cookie="+strconv.Itoa(len(pushed.Cookies)))
		return admin.account(accountID)
	}
	if authUserChanged {
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

// storageStatesEqual 比较两份 storage state 的会话内容是否一致。
//
// Cookie 按 (名称, 域去掉前导点, 路径) 比较值；过期时间与 secure/httpOnly 等属性
// 不参与比较——它们不影响上游请求。SIDCC 家族是 Google 每次响应都会轮换的反滥用
// 标记，浏览器罐与服务端罐必然不同，也不参与比较，否则推送永远被判定为有变化。
// localStorage 按 origin 比较键值。
func storageStatesEqual(left, right aistudio.StorageState) bool {
	cookieKey := func(cookie aistudio.StateCookie) [3]string {
		domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cookie.Domain), "."))
		path := strings.TrimSpace(cookie.Path)
		if path == "" {
			path = "/"
		}
		return [3]string{cookie.Name, domain, path}
	}
	comparable := func(cookies []aistudio.StateCookie) map[[3]string]string {
		values := make(map[[3]string]string, len(cookies))
		for _, cookie := range cookies {
			if rotatingSessionCookieNames[cookie.Name] {
				continue
			}
			values[cookieKey(cookie)] = cookie.Value
		}
		return values
	}
	leftCookies, rightCookies := comparable(left.Cookies), comparable(right.Cookies)
	if len(leftCookies) != len(rightCookies) {
		return false
	}
	for key, value := range leftCookies {
		if other, found := rightCookies[key]; !found || other != value {
			return false
		}
	}
	if len(left.Origins) != len(right.Origins) {
		return false
	}
	originValues := func(origins []aistudio.StorageOrigin) map[string]map[string]string {
		values := make(map[string]map[string]string, len(origins))
		for _, origin := range origins {
			items := make(map[string]string, len(origin.LocalStorage))
			for _, item := range origin.LocalStorage {
				items[item.Name] = item.Value
			}
			values[origin.Origin] = items
		}
		return values
	}
	leftOrigins, rightOrigins := originValues(left.Origins), originValues(right.Origins)
	for origin, items := range leftOrigins {
		other, found := rightOrigins[origin]
		if !found || len(other) != len(items) {
			return false
		}
		for name, value := range items {
			if otherValue, found := other[name]; !found || otherValue != value {
				return false
			}
		}
	}
	return true
}

// rotatingSessionCookieNames 是每次上游响应都会轮换、不承载登录态的反滥用 Cookie
var rotatingSessionCookieNames = map[string]bool{
	"SIDCC":            true,
	"__Secure-1PSIDCC": true,
	"__Secure-3PSIDCC": true,
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
