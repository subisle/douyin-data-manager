// 前端唯一的数据出口：只走 HTTP，不再有 Electron IPC。
// 后端是 Go，字段命名与 internal/domain 里的 json tag 一一对应。

const BASE = "/api/v1";

export type Gender = "male" | "female" | "unknown";

export interface Person {
  id: number;
  name: string;
  gender: Gender;
  masterId?: number | null;
  generation?: number | null;
  groupName?: string | null;
  avatarUrl?: string | null;
  hideInDailyReport: boolean;
  status: "active" | "left" | "paused";
  createdAt: string;
}

export interface Account {
  id: number;
  personId: number;
  anchorId: string;
  douyinNo: string;
  anchorName: string;
  isPrimary: boolean;
  status: string;
}

export interface DailyRow {
  personId: number;
  anchorId: string;
  bizDate: string;
  wave: number;
  cumulativeWave: number;
  waveSpan: number;
  waveReliable: boolean;
  minutes: number;
  isLive: boolean;
  tier?: string | null;
  name: string;
  gender: Gender;
  masterName?: string | null;
}

export interface MonthlyRow {
  personId: number;
  period: string;
  wave: number;
  minutes: number;
  formattedDuration: string;
  liveDays: number;
  absentDays: number;
  bestDayWave: number;
  avgWavePerLiveDay: number;
  tier?: string | null;
  unreliableDays: number;
  name: string;
  gender: Gender;
}

export interface YearlyRow {
  personId: number;
  year: number;
  wave: number;
  minutes: number;
  formattedDuration: string;
  liveDays: number;
  activeMonths: number;
  bestMonth?: string | null;
  bestMonthWave: number;
  bestDayWave: number;
  avgMonthWave: number;
  tier?: string | null;
  name: string;
  gender: Gender;
}

export interface ImportResult {
  batchId: number;
  imported: number;
  persons: number;
  skipped: string[];
}

export interface DayPoint {
  date: string;
  female: number;
  male: number;
  total: number;
}

export interface AbsentRow {
  personId: number;
  name: string;
  gender: string;
  days: number;
}

export interface TopRow {
  personId: number;
  name: string;
  gender: string;
  wave: number;
  minutes: number;
  tier?: string;
}

export interface Summary {
  date: string;
  totalWave: number;
  prevWave: number;
  liveCount: number;
  totalCount: number;
  monthWave: number;
  monthMinutes: number;
  monthProgress: number;
}

export interface Dashboard {
  summary: Summary;
  trend: DayPoint[];
  absent: AbsentRow[];
  top: TopRow[];
}

export interface PreviewRow {
  status: "new" | "changed" | "unchanged" | "unmatched" | "duplicate" | "skipped";
  anchorId: string;
  name: string;
  personId?: number;
  current?: number;
  next: number;
  rank?: number;
  err?: string;
}

export interface ImportPreview {
  kind: string;
  rowCount: number;
  rows: PreviewRow[];
  newCount: number;
  changedCount: number;
  unchangedCount: number;
  unmatchedCount: number;
  duplicateCount: number;
  skippedCount: number;
}

export interface PreviewResponse {
  filename: string;
  date: string;
  preview: ImportPreview;
  /** 非 null：该文件/同内容在此日期已导入过，导入会被拦截 */
  duplicate: { import_date: string; file_name: string; row_count: number } | null;
}

// 上传走 FormData，不能套 request()（它强制 application/json，
// 会让浏览器设不上 multipart boundary）。
async function upload<T>(path: string, fd: FormData): Promise<T> {
  let res: Response;
  try {
    res = await fetch(BASE + path, { method: "POST", body: fd });
  } catch {
    throw new Error("连不上后端，确认 Go 服务已启动（默认 :8080）");
  }
  const json = (await res.json().catch(() => ({}))) as {
    data?: T;
    error?: { code: string; message: string };
  };
  if (!res.ok) {
    throw new Error(json.error?.message ?? `请求失败（${res.status}）`);
  }
  return json.data as T;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(BASE + path, {
      headers: { "Content-Type": "application/json" },
      ...init,
    });
  } catch {
    throw new Error("连不上后端，确认 Go 服务已启动（默认 :8080）");
  }

  const json = (await res.json().catch(() => ({}))) as {
    data?: T;
    error?: { code: string; message: string };
  };
  if (!res.ok) {
    throw new Error(json.error?.message ?? `请求失败（${res.status}）`);
  }
  return json.data as T;
}

const qs = (params: Record<string, string | undefined>) => {
  const usp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v) usp.set(k, v);
  }
  const s = usp.toString();
  return s ? `?${s}` : "";
};

// ---------------- 机器人 ----------------

export interface BotChannelStatus {
  name: string;
  running: boolean;
  connected: boolean;
  note?: string;
}

export interface BotLog {
  at: string;
  channel: string;
  dir: "in" | "out";
  from: string;
  text: string;
  intent?: string;
  hasImage?: boolean;
}

export interface WeixinQR {
  qrcode: string;
  url: string;
  expiresAt?: string;
}

export interface LoginStatus {
  phase: string;
  scanned: boolean;
  loggedIn: boolean;
  nickname?: string;
  note?: string;
}

export interface InjectMedia {
  name: string;
  size: number;
  dataUrl?: string;
}

export interface InjectResult {
  text: string;
  conversation: string;
  images: InjectMedia[];
  files: InjectMedia[];
}

export interface ImportLogRow {
  id: number;
  kind: string;
  import_date: string;
  file_name: string;
  row_count: number;
  source: string;
  matched_count: number;
  unmatched_count: number;
  duplicate_rows: number;
  created_at: string;
}

// 主播 CSV 批量导入
export interface AnchorPreviewRow {
  rawIndex: number;
  anchorId: string;
  douyinNo: string;
  name: string;
  bound?: boolean;
  boundTo?: string;
}

export interface AnchorImportResult {
  created: number;
  bound: number;
  already: number;
  failed: number;
  alreadyDetail: { name: string; id: string; owner?: string }[];
  failedDetail: { name: string; id: string; error: string }[];
}

export const api = {
  listImportLogs: (limit = 100) =>
    request<ImportLogRow[]>(`/imports/logs?limit=${limit}`),

  listPersons: (params: { gender?: string; status?: string; keyword?: string } = {}) =>
    request<Person[]>("/persons" + qs(params)),

  createPerson: (body: Partial<Person>) =>
    request<Person>("/persons", { method: "POST", body: JSON.stringify(body) }),

  previewAnchors: (csv: string) =>
    request<{ count: number; items: AnchorPreviewRow[] }>("/persons/import-anchors/preview", {
      method: "POST",
      body: JSON.stringify({ csv }),
    }),

  importAnchors: (gender: string, items: { name: string; anchorId: string; douyinNo: string }[]) =>
    request<AnchorImportResult>("/persons/import-anchors", {
      method: "POST",
      body: JSON.stringify({ gender, items }),
    }),

  updatePerson: (id: number, body: Partial<Person>) =>
    request<Person>(`/persons/${id}`, { method: "PATCH", body: JSON.stringify(body) }),

  deletePerson: (id: number) =>
    request<unknown>(`/persons/${id}`, { method: "DELETE" }),

  listAccounts: (personId: number) => request<Account[]>(`/persons/${personId}/accounts`),

  bindAccount: (personId: number, body: Partial<Account>) =>
    request<Account>(`/persons/${personId}/accounts`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  dashboard: (params: { date?: string; days?: string; top?: string } = {}) =>
    request<Dashboard>("/metrics/dashboard" + qs(params)),

  daily: (date: string, gender?: string) =>
    request<DailyRow[]>(`/metrics/daily${qs({ date, gender })}`),

  monthly: (period: string, gender?: string) =>
    request<MonthlyRow[]>(`/metrics/monthly${qs({ period, gender })}`),

  yearly: (year: string, gender?: string) =>
    request<YearlyRow[]>(`/metrics/yearly${qs({ year, gender })}`),

  importSnapshots: (body: unknown) =>
    request<ImportResult>("/imports/snapshots", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  recompute: (body: { from?: string; to?: string; personId?: number }) =>
    request<{ persons: number }>("/imports/recompute", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  previewCSV: (file: File, date: string) => {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("date", date);
    return upload<PreviewResponse>("/imports/preview", fd);
  },

  importCSV: (file: File, date: string) => {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("date", date);
    return upload<{
      batchId: number;
      kind: string;
      imported: number;
      persons: number;
      skipped: string[];
      counts: { new: number; changed: number; unchanged: number; unmatched: number };
    }>("/imports/csv", fd);
  },

  batchDeletePersons: (ids: number[]) =>
    request<{ deleted: number }>("/persons/batch-delete", {
      method: "POST",
      body: JSON.stringify({ ids }),
    }),

  duplicatePersons: () =>
    request<{ name: string; persons: Person[] }[]>("/persons/duplicates"),

  mergePersons: (primaryPersonId: number, secondaryPersonId: number, mergeDuration: boolean) =>
    request<unknown>("/persons/merge", {
      method: "POST",
      body: JSON.stringify({ primaryPersonId, secondaryPersonId, mergeDuration }),
    }),

  // ---------------- 机器人 ----------------

  botStatus: () =>
    request<{ channels: BotChannelStatus[]; push: boolean }>("/bots/status"),

  botStart: (name: string) => request<null>(`/bots/${name}/start`, { method: "POST" }),

  botStop: (name: string) => request<null>(`/bots/${name}/stop`, { method: "POST" }),

  botSetPush: (enabled: boolean) =>
    request<{ push: boolean }>("/bots/push", {
      method: "POST",
      body: JSON.stringify({ enabled }),
    }),

  /** 立即向所有活跃会话索要 CSV 文件（每日 1 点定时版走服务端调度） */
  botRemindNow: () =>
    request<{ text: string; channels: string[] }>("/bots/remind", { method: "POST" }),

  botMessages: (limit = 50) => request<BotLog[]>(`/bots/messages?limit=${limit}`),

  botParse: (text: string) =>
    request<{ input: string; intent: { Kind: string; Date?: string; Period?: string; Query?: string } }>(
      "/bots/parse",
      { method: "POST", body: JSON.stringify({ text }) },
    ),

  botWeixinQRCode: () => request<WeixinQR>("/bots/weixin/qrcode", { method: "POST" }),

  botWeixinLoginStatus: () => request<LoginStatus>("/bots/weixin/qrcode/status"),

  botQqCredentials: (appId: string, clientSecret: string) =>
    request<{ mounted: boolean }>("/bots/qq/credentials", {
      method: "POST",
      body: JSON.stringify({ appId, clientSecret }),
    }),

  /** 网页端假装自己在群里说话：走与微信/QQ 完全相同的 Handle 流程 */
  injectToBot: (payload: { text: string; conversation?: string; file?: File | null }) => {
    const fd = new FormData();
    fd.append("text", payload.text ?? "");
    if (payload.conversation) fd.append("conversation", payload.conversation);
    if (payload.file) fd.append("file", payload.file);
    return upload<InjectResult>("/bots/inject", fd);
  },

  setMaster: (personId: number, masterId: number | null) =>
    request<unknown>(`/persons/${personId}/master`, {
      method: "PATCH",
      body: JSON.stringify({ masterId }),
    }),
};

// 音浪用千分位展示，避免大数字读错位数。
export const fmtWave = (n: number) => (n ?? 0).toLocaleString("zh-CN");

export const fmtMinutes = (m: number) => {
  if (!m) return "—";
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${h}h${String(mm).padStart(2, "0")}m` : `${h}h`;
};
