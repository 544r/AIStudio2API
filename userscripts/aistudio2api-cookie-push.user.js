// ==UserScript==
// @name         AIStudio2API Cookie Pusher
// @namespace    aistudio2api-push
// @version      1.1.0
// @description  定时把 AI Studio 页面的最新 Cookie 罐推送到自建 AIStudio2API 服务，按 u/N 序号自动导入或刷新账户，替代手动粘贴
// @author       aistudio2api
// @match        https://aistudio.google.com/*
// @match        https://accounts.google.com/*
// @run-at       document-idle
// @noframes
// @updateURL    https://raw.githubusercontent.com/544r/AIStudio2API/main/userscripts/aistudio2api-cookie-push.user.js
// @downloadURL  https://raw.githubusercontent.com/544r/AIStudio2API/main/userscripts/aistudio2api-cookie-push.user.js
// @grant        unsafeWindow
// @grant        GM_cookie
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @grant        GM_unregisterMenuCommand
// @grant        GM_openInTab
// @connect      *
// ==/UserScript==

/*
 * 用法：
 * 1. 服务端 .env 里设置 ACCOUNT_PUSH_TOKEN=<随机长串> 并重启 AIStudio2API，
 *    推送接口 POST /api/accounts/push-cookies 才启用。
 * 2. 篡改猴 菜单 → 「⚙ 设置服务器地址」填服务地址（如 https://aistudio.example.com），
 *    「🔑 设置推送密钥」填同一个 ACCOUNT_PUSH_TOKEN。
 * 3. 篡改猴 需把「允许脚本访问 Cookie」设为 all，否则读不到 HttpOnly 的会话 Cookie：
 *    管理面板 → 设置 → 配置模式选「高级」→ 安全栏 → 设为 all → 保存。
 * 4. 首次推送时 篡改猴 会弹出"连接到 xxx"的授权框，选「总是允许」。
 * 5. 多账号浏览器建议点「👤 设置允许推送的账号」，填入小号邮箱：停在主号
 *    页面（常见于默认的 /u/0/）时自动推送会直接跳过，不会把主号传上去。
 * 6. 页面加载后自动推一次，之后按设定间隔重推（默认 60 分钟）；右下角胶囊
 *    显示最近一次结果，点击胶囊立即手动推送。
 * 7. 长期挂机可选两个保护（菜单里开关，默认关闭）：
 *    · 「🔊 静音音频保活」：循环播放静音音轨，防止标签页被冻结/丢弃；
 *    · 「🔄 设置自动刷新间隔」：每 N 分钟在标签页不可见时整页刷新一次，
 *      让页面重新发请求、轮换 Cookie，刷新后自动再推送一次。
 * 8. 首次推送成功后脚本按「服务器 + u/N」记住服务端返回的账户 ID；页面邮箱
 *    优先用于识别账号（多登录顺序变化后绑定可能过期），识别不到邮箱时才用绑定。
 * 9. 脚本只在 aistudio.google.com 页面推送；accounts.google.com 的 Cookie
 *    （LSID / __Host-*PLSID / __Host-GAPS）靠 @match 才读得到，别删。
 * 10. 在线更新：@updateURL 指向 GitHub 上的脚本源码，篡改猴会按自己的周期
 *     检查更新；菜单「🔄 检查更新」可立即比对版本并打开安装页。
 *     发现新版本时菜单会多出「⬆ 更新到 vX」。
 *
 * 说明：
 * - 推送的是完整 Google 会话 Cookie 罐（含 SID / SAPISID / __Secure-*），
 *   仅发送到你配置的服务，请使用可信的自建地址。
 * - 后台标签页的主线程定时器会被节流，这里用 Worker 定时器绕开节流。
 */

(function () {
    "use strict";

    // 页面原始 window：Trusted Types 策略对象必须由页面上下文创建
    const W = typeof unsafeWindow !== "undefined" ? unsafeWindow : window;

    // ==================== 常量 ====================

    // GenerateContent 端点：GM_cookie 按这个 URL 查询 = 浏览器实际发送的 Cookie 头（含 HttpOnly）
    const GENERATE_URL =
        "https://alkalimakersuite-pa.clients6.google.com/$rpc/google.internal.alkali.applications.makersuite.v1.MakerSuiteService/GenerateContent";
    const PAGE_URL = "https://aistudio.google.com/prompts/";
    const ACCOUNTS_URL = "https://accounts.google.com/";

    // 服务端 ParseCookieText 必须能算签名并建立会话，缺任一项都会被直接拒绝
    const REQUIRED_COOKIES = [
        "__Secure-1PSID",
        "SAPISID",
        "__Secure-1PAPISID",
        "__Secure-3PAPISID",
    ];
    const CORE_PROBE_NAMES = [
        "SAPISID",
        "__Secure-1PAPISID",
        "__Secure-3PAPISID",
        "__Secure-1PSID",
        "__Secure-3PSID",
        "SID",
        "HSID",
        "SSID",
    ];
    // accounts.google.com 的 host-only 长寿凭据：按页面/端点 URL 查不到，必须单独查
    const ACCOUNTS_URL_NAMES = ["LSID", "__Host-1PLSID", "__Host-3PLSID", "__Host-GAPS"];

    const DEFAULT_INTERVAL_MIN = 60;
    const PUSH_TIMEOUT_MS = 45000;
    const INITIAL_PUSH_DELAY_MS = 5000;
    const DEFAULT_REFRESH_MIN = 0;
    const MIN_REFRESH_MIN = 30;
    const PUSH_PATH = "/api/accounts/push-cookies";

    const STORE_SERVER = "a2a_server";
    const STORE_TOKEN = "a2a_token";
    const STORE_INTERVAL = "a2a_interval_min";
    const STORE_ALLOWED_EMAILS = "a2a_allowed_emails";
    const STORE_AUDIO_KEEPALIVE = "a2a_audio_keepalive";
    const STORE_REFRESH_MIN = "a2a_refresh_min";
    const STORE_LAST_REFRESH = "a2a_last_refresh";

    const SCRIPT_VERSION = "1.1.0";
    const UPDATE_URL =
        "https://raw.githubusercontent.com/544r/AIStudio2API/main/userscripts/aistudio2api-cookie-push.user.js";
    const UPDATE_CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;

    // ==================== 配置 ====================

    function gmGet(key, fallback) {
        try {
            const value = GM_getValue(key, undefined);
            return value === undefined ? fallback : value;
        } catch (_) {
            return fallback;
        }
    }

    function gmSet(key, value) {
        try {
            GM_setValue(key, value);
        } catch (_) {}
    }

    function normalizeServer(raw) {
        let server = String(raw || "").trim();
        if (!server) return "";
        if (!/^https?:\/\//i.test(server)) server = "https://" + server;
        return server.replace(/\/+$/, "");
    }

    function loadConfig() {
        return {
            server: normalizeServer(gmGet(STORE_SERVER, "")),
            token: String(gmGet(STORE_TOKEN, "") || "").trim(),
            intervalMin: Math.max(5, Number(gmGet(STORE_INTERVAL, DEFAULT_INTERVAL_MIN)) || DEFAULT_INTERVAL_MIN),
            allowedEmails: String(gmGet(STORE_ALLOWED_EMAILS, "") || "")
                .split(/[\s,;]+/)
                .map((item) => item.trim().toLowerCase())
                .filter(Boolean),
            audioKeepAlive: !!gmGet(STORE_AUDIO_KEEPALIVE, false),
            refreshMin: Math.max(0, Number(gmGet(STORE_REFRESH_MIN, DEFAULT_REFRESH_MIN)) || 0),
        };
    }

    const cfg = loadConfig();

    function saveServer() {
        const input = prompt(
            "AIStudio2API 服务地址（例如 https://aistudio.example.com）：",
            cfg.server || ""
        );
        if (input === null) return;
        const next = normalizeServer(input);
        if (!next) {
            alert("地址不能为空");
            return;
        }
        cfg.server = next;
        gmSet(STORE_SERVER, next);
        log(`服务器地址已保存: ${next}`);
        pushNow("修改服务器后");
    }

    function saveToken() {
        const input = prompt(
            "推送密钥（服务端 .env 的 ACCOUNT_PUSH_TOKEN）：",
            cfg.token || ""
        );
        if (input === null) return;
        const next = String(input).trim();
        if (!next) {
            alert("密钥不能为空；未设置 ACCOUNT_PUSH_TOKEN 时推送接口是关闭的");
            return;
        }
        cfg.token = next;
        gmSet(STORE_TOKEN, next);
        log("推送密钥已保存");
        pushNow("修改密钥后");
    }

    function saveInterval() {
        const input = prompt(
            `自动推送间隔（分钟，最少 5，当前 ${cfg.intervalMin}）：`,
            String(cfg.intervalMin)
        );
        if (input === null) return;
        const minutes = Math.max(5, Math.round(Number(input) || 0));
        if (!minutes) {
            alert("请输入有效分钟数");
            return;
        }
        cfg.intervalMin = minutes;
        gmSet(STORE_INTERVAL, minutes);
        restartTimers();
        alert(`已设为每 ${minutes} 分钟推送`);
    }

    function saveAllowedEmails() {
        const input = prompt(
            "只推送这些邮箱（逗号/空格分隔，留空表示不限制）：",
            cfg.allowedEmails.join(", ")
        );
        if (input === null) return;
        const emails = String(input)
            .split(/[\s,;]+/)
            .map((item) => item.trim().toLowerCase())
            .filter(Boolean);
        cfg.allowedEmails = emails;
        gmSet(STORE_ALLOWED_EMAILS, emails.join(","));
        alert(emails.length ? `只推送：${emails.join(", ")}` : "已取消邮箱限制");
    }

    function saveRefreshInterval() {
        const input = prompt(
            `自动刷新间隔（分钟，0 关闭，最少 ${MIN_REFRESH_MIN}，当前 ${cfg.refreshMin}）：`,
            String(cfg.refreshMin)
        );
        if (input === null) return;
        const minutes = Math.round(Number(input) || 0);
        if (minutes !== 0 && minutes < MIN_REFRESH_MIN) {
            alert(`自动刷新最短 ${MIN_REFRESH_MIN} 分钟`);
            return;
        }
        cfg.refreshMin = minutes;
        gmSet(STORE_REFRESH_MIN, minutes);
        restartTimers();
        alert(minutes ? `已设为每 ${minutes} 分钟刷新一次` : "已关闭自动刷新");
    }

    function toggleAudioKeepAlive() {
        cfg.audioKeepAlive = !cfg.audioKeepAlive;
        gmSet(STORE_AUDIO_KEEPALIVE, cfg.audioKeepAlive);
        if (cfg.audioKeepAlive) startAudioKeepAlive();
        else stopAudioKeepAlive();
        refreshAudioMenuLabel();
        refreshBadge();
        if (cfg.audioKeepAlive) alert(`音频保活已开启：${audioKeepAliveLabel()}`);
    }

    let audioMenuId = null;

    function refreshAudioMenuLabel() {
        if (audioMenuId !== null) {
            try {
                GM_unregisterMenuCommand(audioMenuId);
            } catch (_) {}
        }
        audioMenuId = GM_registerMenuCommand(
            `🔊 静音音频保活: ${audioKeepAliveLabel()}（点击切换）`,
            toggleAudioKeepAlive
        );
    }

    function registerMenus() {
        GM_registerMenuCommand("⚙ 设置服务器地址", saveServer);
        GM_registerMenuCommand("🔑 设置推送密钥", saveToken);
        GM_registerMenuCommand("⏱ 设置推送间隔", saveInterval);
        GM_registerMenuCommand("👤 设置允许推送的账号", saveAllowedEmails);
        GM_registerMenuCommand("🔄 设置自动刷新间隔", saveRefreshInterval);
        GM_registerMenuCommand("🔄 检查更新", () => checkUpdate(true));
        GM_registerMenuCommand("ℹ 查看状态", () => {
            const lines = [
                `脚本版本: ${SCRIPT_VERSION}`,
                `服务器: ${cfg.server || "未配置"}`,
                `推送密钥: ${cfg.token ? "已配置" : "未配置"}`,
                `推送间隔: ${cfg.intervalMin} 分钟`,
                `自动刷新: ${cfg.refreshMin ? cfg.refreshMin + " 分钟" : "关闭"}`,
                `自动更新: 每 24 小时检查`,
                `账号限制: ${cfg.allowedEmails.join(", ") || "不限"}`,
                `当前页面: u/${accountPathPrefix()}`,
                `识别邮箱: ${detectEmail() || "未识别"}`,
                `绑定账户: ${getBoundAccountId() || "无"}`,
                `最近推送: ${state.lastPushText || "尚未推送"}`,
                `最近结果: ${state.lastResult || "-"}`,
            ];
            alert(lines.join("\n"));
        });
        refreshAudioMenuLabel();
    }

    // ==================== 在线更新 ====================

    let updateMenuId = null;

    function gmGetText(url) {
        return new Promise((resolve, reject) => {
            try {
                GM_xmlhttpRequest({
                    method: "GET",
                    url,
                    timeout: 30000,
                    onload: (response) =>
                        response.status === 200
                            ? resolve(response.responseText)
                            : reject(new Error(`HTTP ${response.status}`)),
                    onerror: () => reject(new Error("网络错误")),
                    ontimeout: () => reject(new Error("请求超时")),
                });
            } catch (error) {
                reject(error);
            }
        });
    }

    function parseUserscriptVersion(text) {
        const match = String(text).match(/\/\/\s*@version\s+([^\s]+)/);
        return match ? match[1] : "";
    }

    function compareVersions(left, right) {
        const a = String(left).split(".").map((part) => parseInt(part, 10) || 0);
        const b = String(right).split(".").map((part) => parseInt(part, 10) || 0);
        const length = Math.max(a.length, b.length);
        for (let index = 0; index < length; index++) {
            const x = a[index] || 0;
            const y = b[index] || 0;
            if (x !== y) return x > y ? 1 : -1;
        }
        return 0;
    }

    function openInstallPage() {
        try {
            if (typeof GM_openInTab === "function") {
                GM_openInTab(UPDATE_URL, { active: true, insert: true });
                return;
            }
        } catch (_) {}
        window.open(UPDATE_URL, "_blank", "noopener");
    }

    // refreshUpdateMenu 在发现新版本时加一个「更新到 vX」菜单，没有更新时移除
    function refreshUpdateMenu(version) {
        if (updateMenuId !== null) {
            try {
                GM_unregisterMenuCommand(updateMenuId);
            } catch (_) {}
            updateMenuId = null;
        }
        if (!version) return;
        updateMenuId = GM_registerMenuCommand(`⬆ 更新到 v${version}`, openInstallPage);
    }

    async function checkUpdate(manual) {
        if (state.checkingUpdate) return;
        state.checkingUpdate = true;
        try {
            const text = await gmGetText(UPDATE_URL);
            const remote = parseUserscriptVersion(text);
            if (!remote) throw new Error("脚本里没有 @version");
            const comparison = compareVersions(remote, SCRIPT_VERSION);
            if (comparison > 0) {
                log(`发现新版本 v${remote}（当前 v${SCRIPT_VERSION}）`);
                refreshUpdateMenu(remote);
                if (
                    manual &&
                    confirm(`发现新版本 v${remote}，当前 v${SCRIPT_VERSION}。现在更新？`)
                ) {
                    openInstallPage();
                }
            } else {
                refreshUpdateMenu("");
                if (manual) alert(`已是最新版本 v${SCRIPT_VERSION}`);
            }
        } catch (error) {
            const message = (error && error.message) || String(error);
            log(`⚠ 检查更新失败: ${message}`);
            if (manual) alert(`检查更新失败：${message}`);
        } finally {
            state.checkingUpdate = false;
        }
    }

    // ==================== 账户与绑定 ====================

    const state = {
        pushing: false,
        lastPushAt: 0,
        lastPushText: "",
        lastResult: "",
        badgeKind: "idle",
        badgeText: "",
        timer: null,
        refreshTimer: null,
        updateTimer: null,
        checkingUpdate: false,
    };

    let badgeEl = null;

    function log(msg) {
        console.log("[AIStudio2API-Push]", msg);
    }

    function accountPathPrefix() {
        const match = location.pathname.match(/^\/u\/(\d+)\//);
        return match ? match[1] : "0";
    }

    function bindingKey() {
        const server = String(cfg.server || "").replace(/[^a-z0-9]+/gi, "_");
        return `a2a_bind_${server}_u${accountPathPrefix()}`;
    }

    function getBoundAccountId() {
        return String(gmGet(bindingKey(), "") || "");
    }

    function setBoundAccountId(accountId) {
        if (!accountId) return;
        gmSet(bindingKey(), String(accountId));
    }

    // 只认和当前 u/N 同源的邮箱：优先读页面内嵌的 WIZ_global_data.oPEP7c
    function detectEmail() {
        const norm = (value) => (typeof value === "string" ? value.trim().toLowerCase() : "");
        const EMAIL_RE = /[\w.+-]+@[\w-]+\.[a-zA-Z0-9.-]+/;
        const pageWindow = typeof W !== "undefined" && W ? W : window;

        try {
            const email = pageWindow.WIZ_global_data && pageWindow.WIZ_global_data.oPEP7c;
            if (typeof email === "string" && EMAIL_RE.test(email)) return norm(email);
        } catch (_) {}

        try {
            const html = (document.documentElement && document.documentElement.innerHTML) || "";
            const match = html.match(/"oPEP7c"\s*:\s*"([^"@\s]+@[^"\s]+)"/);
            if (match && match[1]) return norm(match[1]);
        } catch (_) {}
        return "";
    }

    // ==================== Cookie 收集 ====================

    function gmCookieList(query) {
        return new Promise((resolve) => {
            try {
                if (typeof GM_cookie !== "undefined" && typeof GM_cookie.list === "function") {
                    GM_cookie.list(query, (cookies, error) => {
                        resolve({ cookies: cookies || [], error: error || null });
                    });
                    return;
                }
                if (typeof GM !== "undefined" && GM.cookie && typeof GM.cookie.list === "function") {
                    GM.cookie
                        .list(query)
                        .then((cookies) => resolve({ cookies: cookies || [], error: null }))
                        .catch((error) => resolve({ cookies: [], error }));
                    return;
                }
            } catch (error) {
                resolve({ cookies: [], error });
                return;
            }
            resolve({ cookies: [], error: "当前脚本管理器没有 Cookie API（GM_cookie）" });
        });
    }

    function isGoogleDomain(domain) {
        const value = String(domain || "").replace(/^\./, "").toLowerCase();
        return value === "google.com" || value.endsWith(".google.com");
    }

    // 按 (name|domain|path) 去重：同一名字在不同域/路径上是不同 Cookie，不能按名收敛
    async function collectCookies() {
        const byKey = new Map();
        const addCookie = (cookie, source) => {
            if (!cookie || !cookie.name || !isGoogleDomain(cookie.domain)) return;
            const key = `${cookie.name}|${cookie.domain}|${(cookie.path || "/").toLowerCase()}`;
            const previous = byKey.get(key);
            if (!previous) {
                byKey.set(key, { ...cookie, source });
                return;
            }
            if (previous.source !== "reqURL" && source === "reqURL") {
                byKey.set(key, { ...cookie, source });
            }
        };

        // ① 按 GenerateContent 端点 URL 查询：拿到浏览器实际发送的 Cookie 头
        const request = await gmCookieList({ url: GENERATE_URL });
        if (!request.error) request.cookies.forEach((cookie) => addCookie(cookie, "reqURL"));
        else log(`⚠ 端点 URL 查询失败: ${String(request.error).slice(0, 80)}`);

        // ② 按页面 URL 补漏
        const page = await gmCookieList({ url: PAGE_URL });
        if (!page.error) page.cookies.forEach((cookie) => addCookie(cookie, "pageURL"));

        // ③ 核心 Cookie 缺失时按 name 补查（个别管理器不支持组合查询）
        const names = new Set([...byKey.values()].map((cookie) => cookie.name));
        for (const name of CORE_PROBE_NAMES.filter((item) => !names.has(item))) {
            const result = await gmCookieList({ name });
            if (result.error) continue;
            result.cookies.filter((cookie) => isGoogleDomain(cookie.domain)).forEach((cookie) => addCookie(cookie, "byName"));
        }

        // ④ accounts.google.com 的 host-only 长寿凭据
        let accountsHit = 0;
        const accounts = await gmCookieList({ url: ACCOUNTS_URL });
        if (!accounts.error) {
            for (const cookie of accounts.cookies) {
                if (!isGoogleDomain(cookie.domain)) continue;
                addCookie(cookie, "accountsURL");
                accountsHit += 1;
            }
        }
        const collected = new Set([...byKey.values()].map((cookie) => cookie.name));
        for (const name of ACCOUNTS_URL_NAMES.filter((item) => !collected.has(item))) {
            const result = await gmCookieList({ name });
            if (result.error) continue;
            result.cookies
                .filter((cookie) => isGoogleDomain(cookie.domain))
                .forEach((cookie) => addCookie(cookie, "byName"));
        }

        const finalCookies = [...byKey.values()].map(({ source, ...rest }) => rest);
        log(
            `Cookie 收集: 端点=${request.error ? "失败" : request.cookies.length} | ` +
                `页面=${page.error ? "失败" : page.cookies.length} | ` +
                `accounts域=${accountsHit} | 合计 ${finalCookies.length} 条`
        );
        return finalCookies;
    }

    // 服务端要求 __Secure-1PSID 与三项 SAPISID 签名 Cookie，缺了先拦下给出明确提示
    function missingSessionCookies(cookies) {
        const names = new Set(cookies.map((cookie) => cookie.name));
        return REQUIRED_COOKIES.filter((name) => !names.has(name));
    }

    // ==================== 推送 ====================

    function gmPost(url, headers, body) {
        return new Promise((resolve, reject) => {
            try {
                GM_xmlhttpRequest({
                    method: "POST",
                    url,
                    headers,
                    data: body,
                    timeout: PUSH_TIMEOUT_MS,
                    onload: (response) => resolve(response),
                    onerror: () => reject(new Error("网络错误")),
                    ontimeout: () => reject(new Error("请求超时")),
                });
            } catch (error) {
                reject(error);
            }
        });
    }

    function errorMessage(data, status) {
        if (data && data.error && typeof data.error === "object") {
            return data.error.message || data.error.code || `HTTP ${status}`;
        }
        if (data && typeof data.detail === "string") return data.detail;
        return `HTTP ${status}`;
    }

    async function pushNow(reason) {
        if (state.pushing) return;
        if (!cfg.server) {
            log("⚠ 尚未配置服务器地址（篡改猴 菜单 → 「⚙ 设置服务器地址」）");
            setBadge("error", "未配置服务器地址");
            return;
        }
        if (!cfg.token) {
            log("⚠ 尚未配置推送密钥（服务端 .env 的 ACCOUNT_PUSH_TOKEN）");
            setBadge("error", "未配置推送密钥");
            return;
        }
        if (!location.hostname.endsWith("aistudio.google.com")) return;

        state.pushing = true;
        setBadge("busy", "推送中…");
        try {
            const cookies = await collectCookies();
            const missing = missingSessionCookies(cookies);
            if (missing.length) {
                log(
                    `❌ 缺少会话必需 Cookie: ${missing.join(", ")}（已收集: ` +
                        cookies.map((cookie) => cookie.name).join(", ") +
                        "）。请把 篡改猴「允许脚本访问 Cookie」设为 all 后刷新重试"
                );
                setBadge("error", `缺 Cookie: ${missing.join("/")}`);
                return;
            }

            const detectedEmail = detectEmail().toLowerCase();
            if (cfg.allowedEmails.length) {
                if (!detectedEmail) {
                    log(
                        "⚠ 无法识别当前页面账号邮箱，白名单模式下已跳过本次推送。" +
                            "请在该账号的 AI Studio 页面手动点「立即推送」，或清空白名单"
                    );
                    setBadge("error", "无法识别账号，已跳过");
                    return;
                }
                if (!cfg.allowedEmails.includes(detectedEmail)) {
                    log(`⚠ 当前账号 ${detectedEmail} 不在白名单内，已跳过推送`);
                    setBadge("error", "账号不在白名单");
                    return;
                }
            }

            const payload = {
                cookies,
                auth_user: Number(accountPathPrefix()),
                email: detectedEmail || undefined,
                account_id: getBoundAccountId() || undefined,
                source: "userscript",
                // 多账号浏览器共用一份登录罐：让服务端顺手把全部 u/N 子号都登记上。
                // 设置了白名单时关闭，避免把用户明确排除的主号也带上去。
                discover_all: cfg.allowedEmails.length === 0,
            };
            const headers = {
                "Content-Type": "application/json",
                "X-Push-Token": cfg.token,
                Authorization: `Bearer ${cfg.token}`,
            };
            log(
                `☁ 开始推送（${reason}）：${cookies.length} 条 Cookie，u/${payload.auth_user}` +
                    (detectedEmail ? `，账号 ${detectedEmail}` : "")
            );
            const response = await gmPost(cfg.server + PUSH_PATH, headers, JSON.stringify(payload));
            let data = {};
            try {
                data = JSON.parse(response.responseText || "{}");
            } catch (_) {}

            const account = (data && data.account) || {};
            if (response.status === 200 && account.id) {
                setBoundAccountId(account.id);
                const validText =
                    data.session_valid === true
                        ? "登录态有效"
                        : data.session_valid === false
                          ? "登录态已失效"
                          : "登录态未验证";
                const text =
                    `✅ 已推送（${validText}）` +
                    (data.created ? "，新建账户" : "，刷新账户") +
                    (data.discovered_accounts > 0
                        ? `，自动登记 ${data.discovered_accounts} 个子号`
                        : "") +
                    ` · ${account.label || account.id}`;
                if (data.discovery_error) log(`⚠ 子号识别失败: ${data.discovery_error}`);
                log(text);
                state.lastPushAt = Date.now();
                state.lastPushText = new Date().toLocaleString("zh-CN", { hour12: false });
                state.lastResult = text;
                setBadge("ok", `${validText} · ${shortTime()}`);
            } else {
                const detail = errorMessage(data, response.status);
                log(`❌ 推送失败 [${response.status}]: ${detail}`);
                state.lastResult = `失败 ${response.status}: ${detail}`;
                setBadge("error", `推送失败 ${response.status}`);
            }
        } catch (error) {
            log(`❌ 推送异常: ${(error && error.message) || error}`);
            state.lastResult = String((error && error.message) || error);
            setBadge("error", "推送异常");
        } finally {
            state.pushing = false;
        }
    }

    function shortTime() {
        const now = new Date();
        return `${String(now.getHours()).padStart(2, "0")}:${String(now.getMinutes()).padStart(2, "0")}`;
    }

    // ==================== 状态胶囊 ====================

    function ensureBadge() {
        if (badgeEl) return badgeEl;
        badgeEl = document.createElement("div");
        badgeEl.id = "aistudio2api-push-badge";
        badgeEl.style.cssText = [
            "position:fixed",
            "right:12px",
            "bottom:12px",
            "z-index:2147483647",
            "padding:6px 10px",
            "border-radius:999px",
            "font:12px/1.4 system-ui,-apple-system,'Segoe UI',sans-serif",
            "color:#fff",
            "background:#3f51b5",
            "box-shadow:0 2px 10px rgba(0,0,0,.25)",
            "cursor:pointer",
            "user-select:none",
            "opacity:.92",
        ].join(";");
        badgeEl.title = "点击立即推送 Cookie";
        badgeEl.addEventListener("click", () => pushNow("手动"));
        document.body.appendChild(badgeEl);
        return badgeEl;
    }

    function setBadge(kind, text) {
        state.badgeKind = kind;
        state.badgeText = text;
        const element = ensureBadge();
        const colors = { idle: "#5c6bc0", ok: "#2e7d32", error: "#c62828", busy: "#1565c0" };
        element.style.background = colors[kind] || colors.idle;
        const audioMark = audioKeepAliveActive() ? (keepAlive.blocked ? " · ⚠" : " · 🔊") : "";
        element.textContent = `AIStudio2API 推送 · ${text}${audioMark}`;
        element.title = audioKeepAliveActive()
            ? `点击立即推送 Cookie\n静音音频保活: ${audioKeepAliveLabel()}`
            : "点击立即推送 Cookie";
    }

    function refreshBadge() {
        if (!state.badgeText) return;
        setBadge(state.badgeKind || "idle", state.badgeText);
    }

    // ==================== 音频保活 ====================

    // 构造 1 秒全零采样的 WAV：循环播放时标签页始终处于"正在播放音频"状态，
    // Chrome 不会冻结/丢弃它（标签页被丢弃后定时推送也会停摆）。
    const keepAlive = { el: null, ctx: null, url: "", gestureRetry: null, blocked: false };

    function audioKeepAliveLabel() {
        if (!audioKeepAliveActive()) return "关";
        if (keepAlive.blocked) return "开（被拦截，点页面后恢复）";
        if (keepAlive.el && !keepAlive.el.paused) return "开（静音播放中）";
        if (keepAlive.ctx && keepAlive.ctx.state === "running") return "开（Web Audio 播放中）";
        return "开（启动中）";
    }

    function makeSilentWavUrl() {
        const sampleRate = 8000;
        const samples = sampleRate;
        const buffer = new ArrayBuffer(44 + samples * 2);
        const view = new DataView(buffer);
        const writeStr = (offset, text) => {
            for (let index = 0; index < text.length; index++) {
                view.setUint8(offset + index, text.charCodeAt(index));
            }
        };
        writeStr(0, "RIFF");
        view.setUint32(4, 36 + samples * 2, true);
        writeStr(8, "WAVE");
        writeStr(12, "fmt ");
        view.setUint32(16, 16, true);
        view.setUint16(20, 1, true);
        view.setUint16(22, 1, true);
        view.setUint32(24, sampleRate, true);
        view.setUint32(28, sampleRate * 2, true);
        view.setUint16(32, 2, true);
        view.setUint16(34, 16, true);
        writeStr(36, "data");
        view.setUint32(40, samples * 2, true);
        return URL.createObjectURL(new Blob([buffer], { type: "audio/wav" }));
    }

    function audioKeepAliveActive() {
        return !!(keepAlive.el || keepAlive.ctx);
    }

    function startAudioKeepAlive() {
        if (audioKeepAliveActive()) return true;
        try {
            const url = makeSilentWavUrl();
            const element = document.createElement("audio");
            element.src = url;
            element.loop = true;
            element.volume = 0.01;
            element.style.display = "none";
            element.addEventListener("playing", () => {
                keepAlive.blocked = false;
                refreshAudioMenuLabel();
                refreshBadge();
            });
            document.body.appendChild(element);
            keepAlive.el = element;
            keepAlive.url = url;
            keepAlive.blocked = false;
            log("✅ 音频保活已开启（静音循环）");
            resumeKeepAliveAudio();
            return true;
        } catch (error) {
            log(`❌ 音频保活开启失败: ${(error && error.message) || error}`);
            return false;
        }
    }

    function stopAudioKeepAlive() {
        disarmGestureRetry();
        keepAlive.blocked = false;
        if (keepAlive.el) {
            try {
                keepAlive.el.pause();
            } catch (_) {}
            try {
                keepAlive.el.remove();
            } catch (_) {}
            keepAlive.el = null;
        }
        if (keepAlive.ctx) {
            try {
                keepAlive.ctx.close();
            } catch (_) {}
            keepAlive.ctx = null;
        }
        if (keepAlive.url) {
            try {
                URL.revokeObjectURL(keepAlive.url);
            } catch (_) {}
            keepAlive.url = "";
        }
    }

    // 自动播放策略会拦截"没有用户手势"的播放：静音轨道通常放行；仍被拦截时
    // 退回 Web Audio 极低增益振荡器，并在首次点击/按键后自动补启。
    function resumeKeepAliveAudio() {
        const element = keepAlive.el;
        if (!element) return;
        const playing = element.play();
        if (playing && typeof playing.then === "function") {
            playing
                .then(() => {
                    keepAlive.blocked = false;
                    refreshAudioMenuLabel();
                    refreshBadge();
                })
                .catch(() => {
                    keepAlive.blocked = true;
                    startWebAudioKeepAlive();
                    armGestureRetry();
                    refreshAudioMenuLabel();
                    refreshBadge();
                });
        }
    }

    function startWebAudioKeepAlive() {
        if (keepAlive.ctx) return;
        try {
            const Context = W.AudioContext || W.webkitAudioContext;
            if (!Context) return;
            const context = new Context();
            const oscillator = context.createOscillator();
            const gain = context.createGain();
            gain.gain.value = 0.0001; // -80dB，听不见但存在真实音频流
            oscillator.frequency.value = 440;
            oscillator.connect(gain);
            gain.connect(context.destination);
            oscillator.start();
            keepAlive.ctx = context;
            keepAlive.blocked = context.state !== "running";
            log(
                keepAlive.blocked
                    ? "⚠ 音频保活已创建 Web Audio 上下文，等待用户手势后播放"
                    : "✅ 音频保活已切换为 Web Audio 静音振荡器"
            );
        } catch (error) {
            log(`⚠ Web Audio 保活不可用: ${(error && error.message) || error}`);
        }
    }

    function armGestureRetry() {
        if (keepAlive.gestureRetry) return;
        const retry = () => {
            disarmGestureRetry();
            if (keepAlive.el) {
                keepAlive.el
                    .play()
                    .then(() => {
                        keepAlive.blocked = false;
                        refreshAudioMenuLabel();
                        refreshBadge();
                    })
                    .catch(() => {});
            }
            if (keepAlive.ctx && keepAlive.ctx.state === "suspended") {
                keepAlive.ctx
                    .resume()
                    .then(() => {
                        keepAlive.blocked = keepAlive.ctx ? keepAlive.ctx.state !== "running" : false;
                        refreshAudioMenuLabel();
                        refreshBadge();
                    })
                    .catch(() => {});
            }
        };
        keepAlive.gestureRetry = retry;
        document.addEventListener("pointerdown", retry, { once: true });
        document.addEventListener("keydown", retry, { once: true });
        log("⚠ 音频保活被自动播放策略拦截：在页面点一下即可启用");
    }

    function disarmGestureRetry() {
        const retry = keepAlive.gestureRetry;
        if (!retry) return;
        document.removeEventListener("pointerdown", retry);
        document.removeEventListener("keydown", retry);
        keepAlive.gestureRetry = null;
    }

    // ==================== 调度 ====================

    // 后台标签页的主线程定时器会被节流，这里把定时器放进 Worker 线程绕开节流。
    // AI Studio 启用了 Trusted Types，Worker 的 scriptURL 是 TrustedScriptURL 槽位。
    let ttPolicy = null;

    function trustedWorkerUrl(rawUrl) {
        const trustedTypes = W.trustedTypes;
        if (!trustedTypes || !trustedTypes.createPolicy) return rawUrl;
        if (!ttPolicy) {
            try {
                ttPolicy = trustedTypes.createPolicy("aistudio2api-push", {
                    createScriptURL: (source) => source,
                });
            } catch (_) {
                ttPolicy = trustedTypes.defaultPolicy || null;
            }
        }
        return ttPolicy ? ttPolicy.createScriptURL(rawUrl) : null;
    }

    function createWorkerTimer(intervalMs, onTick) {
        const fallbackToTimer = () => {
            const timer = setInterval(onTick, intervalMs);
            log(`已回退主线程定时器：每 ${Math.round(intervalMs / 60000)} 分钟`);
            return timer;
        };
        try {
            const source =
                "const t=setInterval(()=>postMessage('tick')," +
                intervalMs +
                ");onmessage=(e)=>{if(e.data==='stop'){clearInterval(t);close()}};";
            const url = URL.createObjectURL(new Blob([source], { type: "text/javascript" }));
            const workerUrl = trustedWorkerUrl(url);
            if (!workerUrl) throw new Error("Trusted Types 锁定，无法构造 Worker 脚本 URL");
            const worker = new Worker(workerUrl);
            worker.onmessage = (event) => {
                if (event.data === "tick") onTick();
            };
            worker.onerror = () => {
                log("⚠ Worker 定时器异常，回退主线程定时器");
                fallbackToTimer();
            };
            log(`已启动 Worker 定时器：每 ${Math.round(intervalMs / 60000)} 分钟`);
            return worker;
        } catch (error) {
            log(`⚠ Worker 定时器不可用（${(error && error.message) || error}），回退主线程定时器`);
            return fallbackToTimer();
        }
    }

    function stopTimer(timer) {
        if (!timer) return;
        try {
            if (typeof timer.postMessage === "function") timer.postMessage("stop");
            else clearInterval(timer);
        } catch (_) {}
    }

    function restartTimers() {
        stopTimer(state.timer);
        stopTimer(state.refreshTimer);
        state.timer = createWorkerTimer(cfg.intervalMin * 60 * 1000, () => pushNow("定时"));
        state.refreshTimer = cfg.refreshMin
            ? createWorkerTimer(cfg.refreshMin * 60 * 1000, maybeRefreshPage)
            : null;
    }

    // 定时刷新：让页面重新走一遍加载流程（真实请求 → Google 轮换 Cookie），
    // 刷新后脚本会重新推送一次。只在标签页不可见时执行，避免打断正在使用的页面。
    function maybeRefreshPage() {
        if (!cfg.refreshMin) return;
        if (location.hostname !== "aistudio.google.com") return;
        if (document.visibilityState === "visible") return;
        if (state.pushing) return;
        const last = Number(gmGet(STORE_LAST_REFRESH, 0)) || 0;
        if (last && Date.now() - last < cfg.refreshMin * 60 * 1000 * 0.9) return;
        gmSet(STORE_LAST_REFRESH, Date.now());
        log(`🔄 自动刷新页面轮换 Cookie（间隔 ${cfg.refreshMin} 分钟），刷新后会自动再推送一次`);
        setTimeout(() => location.reload(), 1500);
    }

    function init() {
        if (!location.hostname.endsWith("aistudio.google.com")) return;

        registerMenus();
        if (cfg.audioKeepAlive) startAudioKeepAlive();
        setBadge("idle", cfg.server ? "准备推送" : "未配置服务器");
        log(`脚本已加载 v${SCRIPT_VERSION}，服务器: ${cfg.server || "未配置"}`);

        if (!cfg.server) {
            log("⚠ 请先通过 篡改猴 菜单设置服务器地址（「⚙ 设置服务器地址」）");
        } else if (!cfg.token) {
            log("⚠ 请通过 篡改猴 菜单设置推送密钥（服务端 .env 的 ACCOUNT_PUSH_TOKEN）");
        } else {
            setTimeout(() => pushNow("页面加载"), INITIAL_PUSH_DELAY_MS);
        }

        restartTimers();
        // 页面加载后延迟检查一次更新，之后每 24 小时检查；篡改猴自身也会按
        // @updateURL 的周期检查，二选一先发现新版本即可
        setTimeout(() => checkUpdate(false), 30000);
        state.updateTimer = createWorkerTimer(UPDATE_CHECK_INTERVAL_MS, () =>
            checkUpdate(false)
        );
        document.addEventListener("visibilitychange", () => {
            if (document.visibilityState !== "visible") return;
            const intervalMs = cfg.intervalMin * 60 * 1000;
            if (!state.lastPushAt || Date.now() - state.lastPushAt >= intervalMs) {
                pushNow("标签页恢复可见");
            }
        });
    }

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", init, { once: true });
    } else {
        init();
    }
})();
