# AIStudio2API Cookie 推送脚本

`aistudio2api-cookie-push.user.js` 是一个篡改猴（Tampermonkey / Violentmonkey）脚本：
在已登录的 AI Studio 页面定时读取完整 Google 会话 Cookie 罐，推送到自建
AIStudio2API 的 `POST /api/accounts/push-cookies`，自动导入新账户或刷新已有账户，
替代"Cookie 过期 → 手动重新导出粘贴"。

## 1. 服务端

在 AIStudio2API 的 `.env` 里加一个随机长串（推送接口凭它鉴权）：

```dotenv
# 用户脚本推送 Cookie 的令牌，留空时推送接口关闭
ACCOUNT_PUSH_TOKEN=请换成至少 32 位的随机字符串
```

重启 AIStudio2API 后接口生效。留空时接口返回 `403 push_disabled`，不会暴露任何能力。

### 接口约定

```
POST /api/accounts/push-cookies
X-Push-Token: <ACCOUNT_PUSH_TOKEN>        # 也接受 Authorization: Bearer
Content-Type: application/json

{
  "cookies": [ {"name":"...","value":"...","domain":".google.com","path":"/",
                "expirationDate":1790000000,"secure":true,"httpOnly":true,
                "sameSite":"no_restriction"} ],
  "auth_user": 1,                          # 浏览器地址栏的 /u/N，数字或字符串
  "email": "user@example.com",             # 可选，当前页面识别出的账号
  "account_id": "user@example.com",        # 可选，上次推送返回的绑定
  "source": "userscript"
}
```

响应：

```json
{
  "account": {"id": "user@example.com", "label": "user@example.com", "state": "ready", "...": "..."},
  "created": false,
  "session_valid": true
}
```

- 账户已存在：按 `(name, domain, path)` 合并 Cookie，更新 `auth_user`（多登录顺序
  变化时跟随页面），重启该账户的 WAA Worker 并标记可调度。
- 账户不存在：按 `email` 或 `account_id` 落库；两者都没有时用 `auth_user` 打开
  AI Studio 对话页验证邮箱后导入。
- `session_valid` 只在本次推送真的向 AI Studio 验证过登录态时出现。

## 2. 浏览器脚本

1. 安装 `aistudio2api-cookie-push.user.js`。
2. 篡改猴设置 → 配置模式「高级」→ 安全栏「允许脚本访问 Cookie」设为 **all**
   （否则读不到 HttpOnly 的 `SID` / `SAPISID` / `__Secure-*PSID`）。
3. 菜单「⚙ 设置服务器地址」填服务地址（如 `https://aistudio.example.com`）。
4. 菜单「🔑 设置推送密钥」填与服务端一致的 `ACCOUNT_PUSH_TOKEN`。
5. 打开 `https://aistudio.google.com/` 任意页面，5 秒后自动推一次；右下角胶囊
   显示结果，点击可立即重推。

首次向该域名发请求时篡改猴会弹出授权框，选「总是允许」。

### 多账号（/u/0、/u/1 ...）

- 脚本按当前地址栏的 `/u/N` 取 `auth_user`，邮箱优先读页面 `WIZ_global_data.oPEP7c`。
- 首次推送成功后按「服务器 + u/N」记住服务端返回的 `account_id`；之后邮箱仍优先，
  识别不到时才用绑定，避免多登录顺序变化后把 A 号的 Cookie 推给 B 号。
- 建议用「👤 设置允许推送的账号」填小号邮箱白名单（可多个，逗号/空格分隔）：
  停在主号页面（`/u/0/`）时不会推主号，只按登录罐登记白名单里的子号；
  一次推送可同时刷新白名单内的多个 `/u/N`。
- 白名单为空时仍按共享登录罐识别全部 `/u/N` 子号并一起登记（`discover_all`）。

### 长期挂机

- 「⏱ 设置推送间隔」默认 60 分钟，后台标签页用 Worker 定时器绕开节流。
- 「🔊 静音音频保活」防止标签页被浏览器冻结/丢弃（丢弃后定时器一起停）。
- 「🔄 设置自动刷新间隔」在标签页不可见时整页刷新，让 Google 轮换 Cookie，
  刷新后自动再推一次。

### 在线更新

- 脚本带 `@updateURL` / `@downloadURL`，指向
  `https://raw.githubusercontent.com/544r/AIStudio2API/main/userscripts/...`；
  篡改猴按自己的周期检查，发现新版本会提示更新。
- 菜单「🔄 检查更新」立即比对版本；有更新时菜单会多出「⬆ 更新到 vX」，点击打开安装页。
- 页面加载 30 秒后自动检查一次，之后每 24 小时一次。

## 3. 本地构建（Windows / Linux）

管理端静态资源先构建一次（`internal/webui/dist` 是 `go:embed` 的目标）：

```bash
cd web && npm ci && npm run build && cd ..
```

SpiderMonkey 静态库与头文件按 CI 的 `.github/actions/spidermonkey` 下载，然后：

```bash
export CGO_CXXFLAGS="-I<解压目录>/include"
export CGO_LDFLAGS="-L<解压目录>/lib"
go build -trimpath -o aistudio2api ./cmd/aistudio2api
```

启动：

```bash
ACCOUNT_PUSH_TOKEN=... ./aistudio2api -listen 127.0.0.1:2048 -auth ./auth
```

## 4. 注意

- 推送的是完整 Google 会话 Cookie 罐，只发给自己信任的服务地址；服务端应放在
  HTTPS 后面，`ACCOUNT_PUSH_TOKEN` 不要复用管理密码。
- 脚本只读取 Cookie 并推送到你配置的地址，不修改页面、不上传其他数据。
- Cookie 仍在被浏览器正常轮换时，推送只是把最新值同步给服务端；若账户被服务端
  标记 `auth_required`，一次有效推送即可恢复。
