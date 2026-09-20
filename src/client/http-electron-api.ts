import type {
  DataCleanupResult,
  DataCleanupSummary,
  IpcResult,
  QqBotInstanceStatus,
  QqBotMessage,
  QqBotSettings,
  QqBotStatus,
  WeixinBotMessage,
  WeixinBotStatus,
} from "@/types/electron";

type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

type ApiEnvelope<T> =
  | { success: true; data: T }
  | { success: false; error: string | { code?: string; message?: string; detail?: unknown } };

function qs(params: Record<string, string | number | boolean | null | undefined>) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === "") continue;
    search.set(key, String(value));
  }
  const text = search.toString();
  return text ? `?${text}` : "";
}

function enc(value: string | number) {
  return encodeURIComponent(String(value));
}

function normalizeError(error: unknown) {
  if (typeof error === "string") return error;
  if (error && typeof error === "object" && "message" in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === "string") return message;
  }
  return error instanceof Error ? error.message : String(error || "请求失败");
}

function apiBaseUrl() {
  const base = process.env.NEXT_PUBLIC_API_BASE_URL || "";
  return base.replace(/\/$/, "");
}

async function request<T>(
  path: string,
  options: { method?: HttpMethod; body?: unknown } = {}
): Promise<IpcResult<T>> {
  try {
    const headers: Record<string, string> = {};
    if (options.body !== undefined) headers["Content-Type"] = "application/json";

    const response = await fetch(`${apiBaseUrl()}/api/v1${path}`, {
      method: options.method ?? "GET",
      credentials: "same-origin",
      headers: Object.keys(headers).length > 0 ? headers : undefined,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });

    const payload = (await response.json().catch(() => null)) as ApiEnvelope<T> | null;
    if (!response.ok) {
      return {
        success: false,
        error: normalizeError(payload?.success === false ? payload.error : `HTTP ${response.status}`),
      };
    }
    if (!payload) return { success: false, error: "响应为空" };
    if (!payload.success) return { success: false, error: normalizeError(payload.error) };
    return { success: true, data: payload.data };
  } catch (error) {
    return {
      success: false,
      error: error instanceof Error ? error.message : String(error),
    };
  }
}

function pollResource<T>(
  loader: () => Promise<IpcResult<T>>,
  onData: (data: T) => void,
  intervalMs = 1500
) {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const tick = async () => {
    if (stopped) return;
    try {
      const result = await loader();
      if (!stopped && result.success) onData(result.data);
    } catch {
      // ignore transient poll errors
    }
    if (!stopped) timer = setTimeout(() => void tick(), intervalMs);
  };
  void tick();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
  };
}


const noopAsync = async () => undefined;

export function createHttpElectronApi(): ElectronAPI {
  return {
    windowMinimize: noopAsync,
    windowMaximize: async () => false,
    windowClose: noopAsync,
    windowIsMaximized: async () => false,
    onMaximizeChange: () => () => undefined,
    getAppInfo: () =>
      Promise.resolve({
        name: "douyin-web",
        version: "web",
        productName: "抖音数据管理 Web",
        isPackaged: false,
        platform: "web",
        arch: "web",
        electron: "none",
        updateProxy: "",
      }),
    startLivePkMonitor: () =>
      Promise.resolve({
        success: false,
        error: "浏览器预览模式不支持直播监控",
      }),
    openLivePkEmbeddedMonitor: () =>
      Promise.resolve({
        success: false,
        error: "浏览器预览模式不支持内嵌直播监控",
      }),
    setLivePkEmbeddedBounds: () =>
      Promise.resolve({
        success: true,
        data: { embedded: false, liveRoomUrl: "" },
      }),
    closeLivePkEmbeddedMonitor: () =>
      Promise.resolve({
        success: true,
        data: { status: "idle", startedAt: null, lastError: null, lastRankAt: null },
      }),
    reloadLivePkEmbeddedView: () =>
      Promise.resolve({
        success: false,
        error: "浏览器预览模式不支持内嵌直播监控",
      }),
    setLivePkEmbeddedMuted: (muted) =>
      Promise.resolve({
        success: true,
        data: { muted },
      }),
    onLivePkEmbeddedState: () => () => undefined,
    startLivePkMonitorFromUrl: () =>
      Promise.resolve({
        success: false,
        error: "浏览器预览模式不支持内置直播监控",
      }),
    stopLivePkMonitor: () =>
      Promise.resolve({
        success: true,
        data: { status: "idle", startedAt: null, lastError: null, lastRankAt: null },
      }),
    getLivePkMonitorStatus: () =>
      Promise.resolve({
        success: true,
        data: { status: "idle", startedAt: null, lastError: null, lastRankAt: null },
      }),
    startLivePkMultiMonitor: () =>
      Promise.resolve({
        success: false,
        error: "浏览器预览模式不支持多主播监控",
      }),
    stopLivePkMultiMonitor: () =>
      Promise.resolve({
        success: true,
        data: {
          status: "idle",
          roomCount: 0,
          runningCount: 0,
          pendingCount: 0,
          errorCount: 0,
          maxRooms: 0,
          captureConcurrency: 0,
          preferProtocol: true,
          scoreOnly: true,
          updatedAt: new Date().toISOString(),
          rooms: [],
        },
      }),
    getLivePkMultiMonitorStatus: () =>
      Promise.resolve({
        success: true,
        data: {
          status: "idle",
          roomCount: 0,
          runningCount: 0,
          pendingCount: 0,
          errorCount: 0,
          maxRooms: 0,
          captureConcurrency: 0,
          preferProtocol: true,
          scoreOnly: true,
          updatedAt: new Date().toISOString(),
          rooms: [],
        },
      }),
    onLivePkMultiStatus: () => () => undefined,
    saveLivePkCookie: () =>
      Promise.resolve({
        success: false,
        error: "浏览器预览模式不支持保存直播 Cookie",
      }),
    readLivePkCookie: () =>
      Promise.resolve({
        success: true,
        data: { saved: false, cookie: "", updatedAt: null },
      }),
    clearLivePkCookie: () =>
      Promise.resolve({
        success: true,
        data: { saved: false },
      }),
    onLivePkStatus: () => () => undefined,
    onLivePkRank: () => () => undefined,
    onLivePkGift: () => () => undefined,
    onLivePkMember: () => () => undefined,
    onLivePkChat: () => () => undefined,
    onLivePkEvent: () => () => undefined,
    onLivePkError: () => () => undefined,
    onLivePkCaptureStatus: () => () => undefined,

    getWeixinBotStatus: () => request<WeixinBotStatus>("/bots/weixin"),
    getWeixinBotMessages: () => request<WeixinBotMessage[]>("/bots/weixin/messages"),
    getWeixinBotSettings: (accountId) => request(`/bots/weixin/settings${qs({ accountId })}`),
    startWeixinBotLogin: () => request("/bots/weixin/login", { method: "POST" }),
    cancelWeixinBotLogin: () => request("/bots/weixin/login/cancel", { method: "POST" }),
    startWeixinBot: (accountId) => request("/bots/weixin/start", { method: "POST", body: { accountId } }),
    stopWeixinBot: (accountId) => request("/bots/weixin/stop", { method: "POST", body: { accountId } }),
    disconnectWeixinBot: (accountId) => request("/bots/weixin/disconnect", { method: "POST", body: { accountId } }),
    setActiveWeixinBotAccount: (accountId) =>
      request("/bots/weixin/active-account", { method: "POST", body: { accountId } }),
    sendWeixinBotMessage: (payload) => request("/bots/weixin/send", { method: "POST", body: payload }),
    saveWeixinBotSettings: (payload) => request("/bots/weixin/settings", { method: "POST", body: payload }),
    clearWeixinBotMessages: () => request("/bots/weixin/messages/clear", { method: "POST" }),
    onWeixinBotStatus: (listener) => pollResource(() => request<WeixinBotStatus>("/bots/weixin"), listener),
    onWeixinBotMessage: (listener) => {
      let lastCount = -1;
      return pollResource(async () => {
        const result = await request<WeixinBotMessage[]>("/bots/weixin/messages");
        if (result.success && result.data.length !== lastCount) {
          lastCount = result.data.length;
          const last = result.data[result.data.length - 1];
          if (last) listener(last);
        }
        return result;
      }, () => undefined, 2000);
    },
    onWeixinBotMessagesCleared: (listener) => {
      let lastCount: number | null = null;
      return pollResource(async () => {
        const result = await request<WeixinBotMessage[]>("/bots/weixin/messages");
        if (result.success) {
          if (lastCount !== null && result.data.length === 0 && lastCount > 0) listener();
          lastCount = result.data.length;
        }
        return result;
      }, () => undefined, 2500);
    },
    getQqBotStatus: () => request<QqBotStatus>("/bots/qq"),
    // ── 多机器人：一份配置一个实例 ──
    getQqBotInstances: () => request<QqBotInstanceStatus[]>("/bots/qq/bots"),
    getQqBotInstanceStatus: (key: string) => request<QqBotStatus>(`/bots/qq/bots/${enc(key)}`),
    getQqBotInstanceSettings: (key: string) =>
      request<QqBotSettings>(`/bots/qq/bots/${enc(key)}/settings`),
    saveQqBotInstanceSettings: (key: string, payload) =>
      request<QqBotSettings>(`/bots/qq/bots/${enc(key)}/settings`, { method: "POST", body: payload }),
    connectQqBotInstance: (key: string) =>
      request<QqBotStatus>(`/bots/qq/bots/${enc(key)}/connect`, { method: "POST" }),
    disconnectQqBotInstance: (key: string) =>
      request<QqBotStatus>(`/bots/qq/bots/${enc(key)}/disconnect`, { method: "POST" }),
    getQqBotMessages: () => request<QqBotMessage[]>("/bots/qq/messages"),
    getQqBotSettings: () => request("/bots/qq/settings"),
    saveQqBotSettings: (payload) => request("/bots/qq/settings", { method: "POST", body: payload }),
    connectQqBot: () => request("/bots/qq/connect", { method: "POST" }),
    disconnectQqBot: () => request("/bots/qq/disconnect", { method: "POST" }),
    clearQqBotMessages: () => request("/bots/qq/messages/clear", { method: "POST" }),
    onQqBotStatus: (listener) => pollResource(() => request<QqBotStatus>("/bots/qq"), listener),
    onQqBotMessage: (listener) => {
      let lastCount = -1;
      return pollResource(async () => {
        const result = await request<QqBotMessage[]>("/bots/qq/messages");
        if (result.success && result.data.length !== lastCount) {
          lastCount = result.data.length;
          const last = result.data[result.data.length - 1];
          if (last) listener(last);
        }
        return result;
      }, () => undefined, 2000);
    },
    onQqBotMessagesCleared: (listener) => {
      let lastCount: number | null = null;
      return pollResource(async () => {
        const result = await request<QqBotMessage[]>("/bots/qq/messages");
        if (result.success) {
          if (lastCount !== null && result.data.length === 0 && lastCount > 0) listener();
          lastCount = result.data.length;
        }
        return result;
      }, () => undefined, 2500);
    },

    getAnchors: () => request("/anchors"),
    getFamilyTree: () => request("/family-tree"),
    getRosterBySurname: (surname: string) => request(`/roster/${encodeURIComponent(surname)}`),
    exportFamilyRoster: () => request("/exports/family-roster"),
    getDashboardSummary: () => request("/dashboard/summary"),
    getStartupHealth: () => request("/startup-health"),
    getWaveRanking: (limit) => request(`/dashboard/wave-ranking${qs({ limit })}`),
    getWaveTrendByGender: () => request("/dashboard/wave-trend-by-gender"),
    importWave: (date, rows, meta) =>
      request("/imports/wave", { method: "POST", body: { date, rows, meta } }),
    listImportLogs: (limit = 50) =>
      request(`/imports/logs${qs({ limit })}`),
    importDuration: (date, rows, meta) =>
      request("/imports/duration", { method: "POST", body: { date, rows, meta } }),
    getImportPreview: (kind, date, anchorIds, meta) =>
      request("/imports/preview", { method: "POST", body: { kind, date, anchorIds, meta } }),
    exportWave: (date) => request(`/exports/wave${qs({ date })}`),
    exportDuration: (date) => request(`/exports/duration${qs({ date })}`),
    exportAnchors: () => request("/exports/anchors"),
    addAnchor: (payload) => request("/anchors", { method: "POST", body: payload }),
    batchImportAnchors: (rows) => request("/anchors/batch-import", { method: "POST", body: { rows } }),
    mergeAccounts: (payload) => request("/anchors/merge-accounts", { method: "POST", body: payload }),
    deleteAnchors: (personIds) => request("/anchors", { method: "DELETE", body: { ids: personIds } }),
    findDuplicateAnchors: () => request("/anchors/duplicates"),
    getWaveTrendTotal: () => request("/dashboard/wave-trend-total"),
    getAnchorCountTrend: () => request("/dashboard/anchor-count-trend"),
    updateAnchorName: (payload) =>
      request(`/anchors/${enc(payload.personId)}/name`, { method: "PATCH", body: payload }),
    updateAnchorInfo: (payload) =>
      request(`/anchors/${enc(payload.personId)}`, { method: "PATCH", body: payload }),
    updateAnchorMaster: (payload) =>
      request(`/anchors/${enc(payload.personId)}/master`, { method: "PATCH", body: payload }),
    getAnchorDailySnapshot: (anchorId, date) =>
      request(`/anchors/${enc(anchorId)}/daily-snapshot${qs({ date })}`),
    saveAnchorDailySnapshot: (payload) =>
      request(`/anchors/${enc(payload.anchorId)}/daily-snapshot`, { method: "PUT", body: payload }),
    getAnchorWaveTrend: (anchorId) => request(`/anchors/${enc(anchorId)}/wave-trend`),
    getAnchorsWaveTrend: (anchorIds) => request(`/anchors-wave-trend${qs({ ids: anchorIds.join(",") })}`),
    getFlowingFlag: (personId) => request(`/flags/person/${enc(personId)}`),
    getFlagGroups: (period) => request(`/flags/groups${qs({ period })}`),
    settleFlagScores: (period) => request("/flags/settle", { method: "POST", body: { period } }),
    getDataCleanupSummary: (from, to) =>
      request<DataCleanupSummary>(`/data-cleanup/preview${qs({ from, to })}`),
    deleteDataByDateRange: (from, to) =>
      request<DataCleanupResult>("/data-cleanup", { method: "DELETE", body: { from, to } }),
    getTierRules: () => request("/reports/tier-rules"),
    saveTierRules: (rules) => request("/reports/tier-rules", { method: "PUT", body: { rules } }),
    getDailyWaveReport: (date, gender) => request(`/reports/daily-wave${qs({ date, gender })}`),
    getMonthlyReport: (month, gender) => request(`/reports/monthly${qs({ month, gender })}`),
    getPkRoster: (period, groupSize) => request(`/pk/roster${qs({ period, groupSize })}`),
    buildPkGroups: (payload) => request("/pk/groups", { method: "POST", body: payload }),
    savePkLayoutSnapshot: async () => ({ success: true as const, data: null }),
    readPkLayoutSnapshot: async () => ({ success: true as const, data: null }),
    clearPkLayoutSnapshot: async () => ({ success: true as const, data: null }),
    savePkGroupsPresets: async () => ({ success: true as const, data: [] }),
    readPkGroupsPresets: async () => ({ success: true as const, data: [] }),
    getStarBattleScores: (period) => request(`/star-battle/scores${qs({ period })}`),
    saveStarBattleScore: (payload) =>
      request("/star-battle/scores", { method: "POST", body: payload }),
    getFlagWinner: (period) => request(`/flags/winner${qs({ period })}`),
    getRewardReport: (period, config) => {
      if (config) return request("/rewards/report", { method: "POST", body: { period, config } });
      return request(`/rewards/report${qs({ period })}`);
    },

    // ── 主播收入 ──
    getAnchorIncome: (period) => request(`/income${qs({ period })}`),
    getIncomePeriods: () => request("/income/periods"),
    importAnchorIncome: (payload) => request("/income/import", { method: "POST", body: payload }),
    saveAnchorIncomeProfile: (payload) =>
      request("/income/profile", { method: "POST", body: payload }),
    deleteAnchorIncome: (period, personIds) =>
      request("/income", { method: "DELETE", body: { period, personIds: personIds ?? [] } }),

    // ── 应用密码锁（Web 模式 stub）──
    verifyAppPassword: () => Promise.resolve({ success: true, data: { ok: true, role: "admin" as const } }),
    hasAppPassword: () => Promise.resolve({ success: true, data: { hasPassword: false } }),

    checkForUpdates: () => Promise.resolve({ success: true, data: { status: "web-unavailable" } }),
    downloadUpdate: () => Promise.resolve({ success: true, data: { status: "web-unavailable" } }),
    installUpdate: () => Promise.resolve({ success: true, data: { status: "web-unavailable" } }),
    getUpdateStatus: () =>
      Promise.resolve({
        success: true,
        data: {
          status: "idle",
          info: null,
          progress: null,
          error: null,
          feed: null,
          checkedAt: null,
        },
      }),
    onUpdateStatus: () => () => undefined,
  };
}

let httpApi: ElectronAPI | null = null;
let hybridApi: ElectronAPI | null = null;

function getHttpApi(): ElectronAPI {
  if (!httpApi) httpApi = createHttpElectronApi();
  return httpApi;
}

/**
 * Electron preload 只在进程启动时注入一次。
 * 开发中若热更了前端但没重启 Electron，window.electronAPI 会缺新方法。
 * 这里对缺失方法自动回退到 HTTP API（Next /api/v1），避免 "is not a function"。
 */
function createHybridApi(electronApi: ElectronAPI): ElectronAPI {
  const http = getHttpApi();
  return new Proxy(electronApi, {
    get(target, prop, receiver) {
      if (typeof prop === "symbol") return Reflect.get(target, prop, receiver);
      const value = Reflect.get(target, prop, receiver);
      if (value !== undefined && value !== null) return value;
      const fallback = Reflect.get(http, prop, http);
      return fallback;
    },
  }) as ElectronAPI;
}

export function getDataApi(): ElectronAPI | undefined {
  if (typeof window === "undefined") return undefined;
  if (window.electronAPI) {
    if (!hybridApi) hybridApi = createHybridApi(window.electronAPI);
    return hybridApi;
  }
  return getHttpApi();
}
