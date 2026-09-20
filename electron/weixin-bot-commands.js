const crypto = require("crypto");
const Papa = require("papaparse");
const { createWeixinAnalytics } = require("./weixin-bot-analytics");
const { INSTRUCTION_HELP, SYSTEM_HELP_RE, sessionKeyFromContext } = require("./weixin-bot-mode");
const { matchDailyPushCommand, buildGenderTop3Text } = require("./weixin-bot-daily-push");
const { toDailyReportImagePages, toNotLiveReportImagePages, sortNotLiveReportRows, buildNotLiveCsvRows } = require("./weixin-bot-report");
const { renderDailyStarPng } = require("./weixin-bot-daily-star");
const { parseBindCommand, formatBindReply, createAnchorBindService } = require("./bot-anchor-bind");

const HELP_TEXT = INSTRUCTION_HELP;
const PENDING_IMPORT_DATE_TTL_MS = 10 * 60_000;
/** 同一个日期口令最多可覆盖的几个文件：音浪 + 时长，正好两个 */
const PENDING_IMPORT_MAX_FILES = 2;
/**
 * 已下线能力：AI 对话（人工客服 / 智能模式）与会话记忆口令。
 * 这些口令曾经用于切换 AI 模式，现在给明确回复，避免被当成艺名解析。
 */
const RETIRED_AI_RE =
  /^(?:人工客服|智能客服|客服|开启客服|打开客服|开启智能|打开智能|智能模式|AI模式|ai模式|纯指令|指令模式|仅指令|退出客服|关闭客服|结束客服|取消客服|关闭智能|退出智能|清空对话|清除记忆|清除对话|清除习惯|清除我的习惯|清空习惯)$/i;
const RETIRED_AI_REPLY = [
  "本系统已移除 AI 对话能力，现在只支持固定指令。",
  "发「帮助」查看指令菜单；发「9.11」再接连传音浪、时长两个 CSV 即可导入该日数据。发「导入记录」查看最近导入。",
].join("\n");

function normalizeText(value) {
  return String(value || "")
    .replace(/^\uFEFF/, "")
    .replace(/\r/g, "")
    .trim()
    .replace(/[。！!，,；;]+$/g, "")
    .trim();
}

function normalizeHeader(value) {
  return String(value || "")
    .replace(/^\uFEFF/, "")
    .trim()
    .toLowerCase()
    .replace(/[\s_＿\-—–·.。:：/\\|()[\]{}（）【】<>《》]/g, "");
}

function parseDateSpec(value) {
  const text = normalizeText(value);
  if (!text) return null;
  if (/^(?:今天|今日)$/.test(text)) return { type: "relative", offset: 0 };
  if (/^(?:昨天|昨日)$/.test(text)) return { type: "relative", offset: -1 };

  const full = text.match(/(20\d{2})\s*[年./-]\s*(\d{1,2})\s*[月./-]\s*(\d{1,2})\s*[日号]?/);
  if (full) return { type: "date", year: Number(full[1]), month: Number(full[2]), day: Number(full[3]) };
  const compact = text.match(/(20\d{2})(\d{2})(\d{2})/);
  if (compact) return { type: "date", year: Number(compact[1]), month: Number(compact[2]), day: Number(compact[3]) };
  // 年月（2026年9月 / 2026-09）：月粒度查询
  const yearMonth = text.match(/(20\d{2})\s*年\s*(\d{1,2})\s*月?$/);
  if (yearMonth) return { type: "month", year: Number(yearMonth[1]), month: Number(yearMonth[2]) };
  // 年（2026年 / 2026）：年粒度查询
  const yearOnly = text.match(/^(20\d{2})\s*年?$/);
  if (yearOnly) return { type: "year", year: Number(yearOnly[1]) };
  const monthDay = text.match(/(\d{1,2})\s*月\s*(\d{1,2})\s*[日号]?/);
  if (monthDay) return { type: "month-day", month: Number(monthDay[1]), day: Number(monthDay[2]) };
  // 点式月日（9.1 / 9.15 / 9.1日）：月粒度+日
  const dotMonthDay = text.match(/^(\d{1,2})[.．](\d{1,3})[日号]?$/);
  if (dotMonthDay) return { type: "month-day", month: Number(dotMonthDay[1]), day: Number(dotMonthDay[2]) };
  // 仅月（9月）：月粒度查询（按当年）
  const monthOnly = text.match(/^(\d{1,2})\s*月$/);
  if (monthOnly) return { type: "month-only", month: Number(monthOnly[1]) };
  const day = text.match(/(?:^|\s)(\d{1,2})\s*[日号](?=$|\s|音浪|文件|日报|报告|数据|_|\.)/);
  if (day) return { type: "day", day: Number(day[1]) };
  // normalizeText 去空格后：24号数据 / 24号音浪
  const dayCompact = text.match(/^(\d{1,2})[日号](?:数据|音浪|文件|日报|报告)?$/);
  if (dayCompact) return { type: "day", day: Number(dayCompact[1]) };
  return null;
}

function parseMonthSpec(value) {
  const text = normalizeText(value);
  if (!text) return null;
  const full = text.match(/(20\d{2})\s*[年./-]\s*(\d{1,2})\s*月?/);
  if (full) return { type: "month", year: Number(full[1]), month: Number(full[2]) };
  const compact = text.match(/(20\d{2})(\d{2})(?!\d)/);
  if (compact) return { type: "month", year: Number(compact[1]), month: Number(compact[2]) };
  const monthOnly = text.match(/(\d{1,2})\s*月/);
  if (monthOnly) return { type: "month-only", month: Number(monthOnly[1]) };
  return null;
}

function resolveMonthSpec(spec, fallbackDate = null) {
  const fallback = /^\d{4}-\d{2}-\d{2}$/.test(String(fallbackDate || ""))
    ? String(fallbackDate)
    : localYesterdayIso();
  const year = Number(fallback.slice(0, 4));
  const month = Number(fallback.slice(5, 7));
  if (!spec) return `${year}-${String(month).padStart(2, "0")}`;
  if (spec.type === "month") {
    const m = Number(spec.month);
    if (!Number.isFinite(m) || m < 1 || m > 12) throw new Error("月份无效");
    return `${Number(spec.year)}-${String(m).padStart(2, "0")}`;
  }
  if (spec.type === "month-only") {
    const m = Number(spec.month);
    if (!Number.isFinite(m) || m < 1 || m > 12) throw new Error("月份无效");
    return `${year}-${String(m).padStart(2, "0")}`;
  }
  return `${year}-${String(month).padStart(2, "0")}`;
}

const TRAILING_DATE_RE =
  /(今日|今天|昨日|昨天|20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?|20\d{6}|20\d{2}[年./-]\d{1,2}[月./-]?|20\d{2}年?|\d{1,2}\s*月|\d{1,2}\s*[日号]|\d{1,2}[.．]\d{1,3})$/;

/** 「艺名 9月」→ { query: "艺名", dateSpec: month-only }；无尾部日期则原样返回 */
function splitTrailingDateSpec(text) {
  const t = normalizeText(text);
  const m = t.match(TRAILING_DATE_RE);
  if (!m || !m.index) return { query: t, dateSpec: null };
  const query = t.slice(0, m.index).trim();
  if (!query) return { query: t, dateSpec: null };
  return { query, dateSpec: parseDateSpec(m[1]) };
}

/** 仅从用户文字解析导入日期；不读文件名，避免误把 22 号发的文件落到 22 */function parseExplicitImportDateFromText(text) {
  const original = normalizeText(text);
  if (!original) return null;
  // 优先识别「24号数据 / 24号音浪数据 / 数据24号」
  const labeled = original.match(/(\d{1,2})[日号](?:音浪|时长)?数据/)
    || original.match(/(?:音浪|时长)?数据(\d{1,2})[日号]/)
    || original.match(/(20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?)(?:音浪|时长)?数据/);
  if (labeled) {
    const raw = labeled[1] || labeled[0];
    const spec = parseDateSpec(String(raw).includes("数据") ? String(raw).replace(/数据/g, "") : raw);
    if (spec) return resolveDateSpec(spec, localYesterdayIso());
  }
  const spec = parseDateSpec(original);
  if (!spec) return null;
  // 仅当文案明显在指定导入日时才采纳（避免闲聊里的数字误触发）
  if (!/(?:数据|音浪|时长|导入|文件)/.test(original) && spec.type === "day") return null;
  return resolveDateSpec(spec, localYesterdayIso());
}

function localTodayIso() {
  const now = new Date();
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}

function localYesterdayIso() {
  return shiftDate(localTodayIso(), -1);
}

/** 2026-08-10 -> 10号（当年当月）；跨月补月；跨年补年 */
function dayToFriendly(dateStr) {
  const match = String(dateStr || "").match(/^(\d{4})-(\d{2})-(\d{2})$/);
  if (!match) return String(dateStr || "");
  const [, year, month, day] = match;
  const now = new Date();
  const thisYear = String(now.getFullYear());
  const thisMonth = String(now.getMonth() + 1).padStart(2, "0");
  const dayNum = Number(day);
  const monthNum = Number(month);
  if (year === thisYear && month === thisMonth) return `${dayNum}号`;
  if (year === thisYear) return `${monthNum}月${dayNum}号`;
  return `${year}年${monthNum}月${dayNum}号`;
}

function isValidDateParts(year, month, day) {
  const date = new Date(year, month - 1, day);
  return date.getFullYear() === year && date.getMonth() === month - 1 && date.getDate() === day;
}

function toIsoDate(year, month, day) {
  if (!isValidDateParts(year, month, day)) throw new Error("日期无效");
  return `${year}-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}

function shiftDate(date, offset) {
  const [year, month, day] = String(date).split("-").map(Number);
  const value = new Date(year, month - 1, day);
  value.setDate(value.getDate() + Number(offset || 0));
  return `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`;
}

function resolveDateSpec(spec, fallbackDate = null) {
  const fallback = /^\d{4}-\d{2}-\d{2}$/.test(String(fallbackDate || ""))
    ? String(fallbackDate)
    : (() => {
        const now = new Date();
        now.setDate(now.getDate() - 1);
        return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
      })();
  if (!spec) return fallback;
  if (spec.type === "relative") {
    const now = new Date();
    const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
    return shiftDate(today, spec.offset);
  }
  if (spec.type === "date") return toIsoDate(spec.year, spec.month, spec.day);
  if (spec.type === "month-day") return toIsoDate(Number(fallback.slice(0, 4)), spec.month, spec.day);
  if (spec.type === "day") return toIsoDate(Number(fallback.slice(0, 4)), Number(fallback.slice(5, 7)), spec.day);
  // 月粒度 → 当月 1 号；年粒度 → 当年 1 月 1 号（聚合查询在各自 handler 里另算）
  if (spec.type === "month") return toIsoDate(spec.year, spec.month, 1);
  if (spec.type === "month-only") return toIsoDate(Number(fallback.slice(0, 4)), spec.month, 1);
  if (spec.type === "year") return toIsoDate(spec.year, 1, 1);
  return fallback;
}

function parseReportGender(original) {
  const hasFemale = /(?:女团|女队|女性)/.test(original);
  const hasMale = /(?:男团|男队|男性)/.test(original);
  if (hasFemale && !hasMale) return "female";
  if (hasMale && !hasFemale) return "male";
  // 未写性别的「每日报告」默认双团；写了男女两边则也按双团
  if (hasFemale && hasMale) return "both";
  return "both";
}

function parseSingleGender(original) {
  return /(?:女团|女队|女性)/.test(original) ? "female" : "male";
}

function parseBotCommand(input) {
  const original = normalizeText(input);
  if (!original) return null;
  if (/^(?:\/?help|帮助|菜单|命令|指令)$/i.test(original)) return { type: "help" };

  const bindCommand = parseBindCommand(original);
  if (bindCommand) return bindCommand;

  const withoutGender = original.replace(/(?:男团|男队|男性|女团|女队|女性)/g, "").trim();

  // 报告/导出类命令只认「某一天」；月/年粒度 spec 对它们无意义 → 归一为 null（走默认日期）
  const toDailySpec = (spec) =>
    spec && (spec.type === "month" || spec.type === "month-only" || spec.type === "year") ? null : spec;

  const fileMatch = withoutGender.match(/^(?:\/?(?:音浪文件|导出音浪(?:文件)?))(?:\s*(.+))?$/i);
  if (fileMatch) return { type: "export-wave-file", dateSpec: toDailySpec(parseDateSpec(fileMatch[1] || "")) };

  const notLiveMatch = withoutGender.match(/^(?:\/?(?:未开播天数报告|未开播报告|未播天数报告|未播报告))(?:\s*(.+))?$/i)
    || withoutGender.match(/^(.+?)\s*(?:未开播天数报告|未开播报告|未播天数报告|未播报告)$/);
  if (notLiveMatch) {
    const rest = String(notLiveMatch[1] || "").trim();
    const dateSpec = toDailySpec(parseDateSpec(rest));
    const monthSpec = dateSpec ? null : parseMonthSpec(rest);
    return {
      type: "not-live-report",
      gender: parseReportGender(original),
      dateSpec,
      monthSpec,
    };
  }

  const reportMatch = withoutGender.match(/^(?:\/?(?:每日报告|日报|报告))(?:\s*(.+))?$/i);
  if (reportMatch) {
    return {
      type: "report",
      gender: parseReportGender(original),
      dateSpec: toDailySpec(parseDateSpec(reportMatch[1] || "")),
    };
  }

  // 「18号报告」→ 默认双团；可写 男团18号报告
  const dateOnlyReport = withoutGender.match(/^(今日|今天|昨日|昨天|20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?|20\d{6}|\d{1,2}\s*[日号])\s*报告$/);
  if (dateOnlyReport) {
    const hasGender = /(?:男团|男队|男性|女团|女队|女性)/.test(original);
    return {
      type: "report",
      gender: hasGender ? parseSingleGender(original) : "both",
      dateSpec: toDailySpec(parseDateSpec(dateOnlyReport[1])),
    };
  }

  const dateOnlyWave = withoutGender.match(/^(今日|今天|昨日|昨天|20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?|20\d{6}|\d{1,2}\s*[日号])\s*音浪$/);
  if (dateOnlyWave) {
    return {
      type: "report",
      gender: parseSingleGender(original),
      dateSpec: toDailySpec(parseDateSpec(dateOnlyWave[1])),
    };
  }

  const daysMatch = withoutGender.match(/^(.+?)\s*(?:多少日|多少天)音浪$/);
  if (daysMatch?.[1]?.trim()) return { type: "anchor-wave-days", query: daysMatch[1].trim() };

  // 「艺名 9月时长 / 艺名 2026年直播时长」→ 时长查询可带年月日粒度
  const durationMatch = withoutGender.match(/^(.+?)\s*(?:直播)?时长$/);
  if (durationMatch?.[1]?.trim()) {
    const durationSplit = splitTrailingDateSpec(durationMatch[1].trim());
    return { type: "anchor-duration", query: durationSplit.query, dateSpec: durationSplit.dateSpec };
  }

  const namedWaveMatch = withoutGender.match(
    /^(.+?)\s*(今日|今天|昨日|昨天|20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?|20\d{6}|20\d{2}[年./-]\d{1,2}[月./-]?|20\d{2}年?|\d{1,2}\s*月|\d{1,2}\s*[日号]|\d{1,2}[.．]\d{1,3})\s*音浪$/
  );
  if (namedWaveMatch?.[1]?.trim()) {
    return {
      type: "anchor-wave",
      query: namedWaveMatch[1].trim(),
      dateSpec: parseDateSpec(namedWaveMatch[2]),
    };
  }

  // 「艺名音浪」→ 最新日音浪
  const simpleWaveMatch = withoutGender.match(/^(.+?)\s*音浪$/);
  if (simpleWaveMatch?.[1]?.trim()
    && !/^(今日|今天|昨日|昨天|最新|音浪文件|导出)/.test(simpleWaveMatch[1].trim())) {
    return {
      type: "anchor-wave",
      query: simpleWaveMatch[1].trim(),
      dateSpec: null,
    };
  }

  // 「9.1 / 9月1日 / 24号 / 2026-09-01」→ 预告导入日期（10 分钟内发 CSV 生效）
  const importDateToken = original.match(
    /^\/?(\d{1,2}[.．]\d{1,3}[日号]?|\d{1,2}月\d{1,3}[日号]?|\d{1,2}[日号]|20\d{2}[年./-]\d{1,2}[月./-]\d{1,2}[日号]?|20\d{2}年\d{1,2}月)$/
  );
  if (importDateToken) return { type: "import-date", raw: importDateToken[1] };

  // 直接输入主播名/抖音号/主播 ID：返回库内全部相关数据
  if (
    withoutGender.length >= 1
    && withoutGender.length <= 40
    && !/[，。！？、；：,.!?;:]/.test(withoutGender)
    && !/^(今日|今天|昨日|昨天|音浪|文件|报告|日报|导出|帮助|菜单|命令|指令|人工|客服|智能|未开播|未播|第?[1-9一二三四五六七八九十]组|组[1-9一二三四五六七八九十]|各组)/.test(withoutGender)
  ) {
    return { type: "anchor-profile", query: withoutGender };
  }
  return null;
}

function formatWave(value) {
  const number = Number(value) || 0;
  if (number <= 0) return "0";
  if (number >= 100_000_000) {
    const yi = number / 100_000_000;
    const r = Math.round(yi * 10) / 10;
    return Number.isInteger(r) ? `${r} 亿` : `${r.toFixed(1)} 亿`;
  }
  // 低于一万：直接显示数字
  if (number < 10_000) {
    return Math.round(number).toLocaleString("zh-CN");
  }
  // ≥1 万用「万」，精确到 0.1 万（千）
  const wan = number / 10_000;
  const r = Math.round(wan * 10) / 10;
  if (r <= 0) return "0";
  return Number.isInteger(r) ? `${r} 万` : `${r.toFixed(1)} 万`;
}

function formatDuration(value) {
  const minutes = Math.max(0, Math.round(Number(value) || 0));
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return hours ? `${hours}小时${rest}分` : `${rest}分钟`;
}

function compactNumber(value) {
  return (Number(value) || 0).toLocaleString("zh-CN");
}

function findAnchor(query, anchors) {
  const needle = normalizeText(query).toLowerCase();
  if (!needle) return null;
  const fields = (anchor) => [
    anchor.name,
    anchor.anchorName,
    anchor.anchorId,
    anchor.douyinNo,
    ...(Array.isArray(anchor.aliasIds) ? anchor.aliasIds : []),
  ].map((value) => String(value || "").trim().toLowerCase()).filter(Boolean);
  const exact = anchors.filter((anchor) => fields(anchor).includes(needle));
  if (exact.length === 1) return exact[0];
  const fuzzy = anchors.filter((anchor) => fields(anchor).some((value) => value.includes(needle) || needle.includes(value)));
  return fuzzy.length === 1 ? fuzzy[0] : null;
}

function rowMatchesAnchor(row, anchor) {
  const ids = new Set([anchor.anchorId, ...(anchor.aliasIds || [])].map((value) => String(value || "")));
  return ids.has(String(row.anchorId || "")) || String(row.name || "").trim() === String(anchor.name || "").trim();
}

function findColumn(headers, names) {
  const wanted = names.map(normalizeHeader);
  return headers.find((header) => wanted.includes(normalizeHeader(header)));
}

function parseWaveValue(value) {
  if (typeof value === "number") return value;
  const text = String(value ?? "").trim();
  if (!text) return NaN;
  if (text.includes("万")) {
    const number = Number.parseFloat(text.replace(/,/g, "").replace("万", ""));
    return Number.isFinite(number) ? Math.round(number * 10_000) : NaN;
  }
  const number = Number.parseInt(text.replace(/,/g, "").replace(/[^\d-]/g, ""), 10);
  return Number.isFinite(number) ? number : NaN;
}

function parseDurationValue(value) {
  if (typeof value === "number") return value;
  const text = String(value ?? "").trim();
  if (!text) return NaN;
  const cn = text.match(/^(?:(\d+)\s*小时)?(?:(\d+)\s*分(?:钟)?)?(?:(\d+)\s*秒)?$/);
  if (cn && (cn[1] || cn[2] || cn[3])) {
    return Number(cn[1] || 0) * 60 + Number(cn[2] || 0) + Math.round(Number(cn[3] || 0) / 60);
  }
  if (text.includes(":")) {
    const parts = text.split(":").map((part) => Number.parseInt(part, 10) || 0);
    if (parts.length === 3) return parts[0] * 60 + parts[1] + Math.round(parts[2] / 60);
    if (parts.length === 2) return parts[0] * 60 + parts[1];
  }
  const number = Number.parseInt(text.replace(/[^\d-]/g, ""), 10);
  return Number.isFinite(number) ? number : NaN;
}

function inferImportKind(text, fileName, commandText) {
  const sources = [text.split(/\r?\n/, 1)[0], commandText, fileName]
    .map((value) => String(value || "").toLowerCase());
  for (const source of sources) {
    if (/时长|duration|开播|有效时长/.test(source)) return "duration";
    if (/音浪|wave|总音浪/.test(source)) return "wave";
  }
  return "wave";
}

function parseCsvText(text, kind) {
  const result = Papa.parse(text, { header: true, skipEmptyLines: true });
  if (result.errors?.length) {
    const first = result.errors[0];
    throw new Error(`CSV 解析失败：${first.message || "格式错误"}`);
  }
  const data = Array.isArray(result.data) ? result.data : [];
  const headers = Object.keys(data[0] || {});
  const idCol = findColumn(headers, ["主播id", "主播账号", "抖音号", "抖音ID", "anchor_id", "anchorId", "uid"]);
  const nameCol = findColumn(headers, ["主播名", "主播名称", "主播昵称", "昵称", "用户昵称", "姓名", "名字", "主播", "anchor_name", "anchorName", "name", "nickname"]);
  const valueCol = kind === "wave"
    ? findColumn(headers, ["音浪", "wave_value", "wave", "总音浪"])
    : findColumn(headers, ["时长", "duration", "duration_minutes", "直播时长", "开播有效时长", "有效时长", "开播时长"]);
  const rankCol = findColumn(headers, ["排名", "rank"]);
  if (!valueCol) throw new Error(`CSV 中未找到${kind === "wave" ? "音浪" : "时长"}列`);

  const rows = [];
  let skipped = 0;
  for (const row of data) {
    const anchorIdRaw = String(row[idCol || ""] || "").trim();
    const anchorName = String(row[nameCol || ""] || "").trim();
    const value = kind === "wave" ? parseWaveValue(row[valueCol]) : parseDurationValue(row[valueCol]);
    const rank = Number.parseInt(String(row[rankCol || ""] || "0"), 10) || 0;
    if ((!anchorIdRaw && !anchorName) || !Number.isFinite(value) || value < 0) {
      skipped += 1;
      continue;
    }
    rows.push({ anchorIdRaw, anchorName, value, rank });
  }
  return { rows, skipped, totalRows: data.length };
}

function matchImportRows(rows, anchors) {
  const byField = new Map();
  const nameOwners = new Map();
  for (const anchor of anchors) {
    const primaryId = String(anchor.anchorId || "").trim();
    if (primaryId) byField.set(primaryId, { anchor, accountId: primaryId });
    const douyinNo = String(anchor.douyinNo || "").trim();
    if (douyinNo && primaryId) byField.set(douyinNo, { anchor, accountId: primaryId });
    for (const aliasId of anchor.aliasIds || []) {
      const accountId = String(aliasId || "").trim();
      if (accountId) byField.set(accountId, { anchor, accountId });
    }
    const name = String(anchor.name || "").trim();
    if (name) {
      if (nameOwners.has(name)) nameOwners.set(name, null);
      else nameOwners.set(name, primaryId ? { anchor, accountId: primaryId } : null);
    }
  }
  const matched = [];
  const unmatched = [];
  for (const row of rows) {
    const rawId = row.anchorIdRaw.replace(/["'\s]/g, "");
    const match = byField.get(rawId) || nameOwners.get(row.anchorName.trim());
    if (!match) {
      unmatched.push(row);
      continue;
    }
    matched.push({
      anchorId: match.accountId,
      anchorName: match.anchor.anchorName || match.anchor.name || row.anchorName,
      value: row.value,
      rank: row.rank,
    });
  }
  const deduped = new Map();
  let duplicateRows = 0;
  for (const row of matched) {
    if (deduped.has(row.anchorId)) duplicateRows += 1;
    deduped.set(row.anchorId, row);
  }
  return { rows: Array.from(deduped.values()), unmatched, duplicateRows };
}

function buildImportMeta(buffer, fileName, kind, rows) {
  const canonical = rows
    .map((row) => ({
      anchorId: row.anchorId,
      value: Math.round(row.value) || 0,
      rank: kind === "wave" ? Math.round(row.rank) || 0 : 0,
    }))
    .sort((a, b) => a.anchorId.localeCompare(b.anchorId));
  return {
    fileHash: crypto.createHash("md5").update(buffer).digest("hex"),
    dataHash: crypto.createHash("sha256").update(JSON.stringify(canonical)).digest("hex"),
    fileName: String(fileName || "weixin.csv"),
    rowCount: canonical.length,
  };
}

function decodeCsv(buffer) {
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(buffer).replace(/^\uFEFF/, "");
  } catch {
    return new TextDecoder("gb18030").decode(buffer).replace(/^\uFEFF/, "");
  }
}

function csvBuffer(rows) {
  const text = Papa.unparse(rows);
  return Buffer.from(`\uFEFF${text}`, "utf8");
}

function normalizeIsoDate(value) {
  if (value == null || value === "") return null;
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) return null;
    // 本地年月日，避免 toISOString 在东八区把午夜推前一天
    return `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`;
  }
  const text = String(value).trim();
  if (!text) return null;
  const match = text.match(/(\d{4}-\d{2}-\d{2})/);
  return match ? match[1] : null;
}

async function getLatestDate(db, kind) {
  let summary = {};
  try {
    if (typeof db.getDashboardSummary === "function") {
      summary = (await db.getDashboardSummary()) || {};
    }
  } catch {
    summary = {};
  }
  const fromSummary = kind === "duration"
    ? (summary.latestDurationDate || summary.latestDataDate)
    : (summary.latestWaveDate || summary.latestDataDate);
  const fromSummaryDate = normalizeIsoDate(fromSummary);
  if (fromSummaryDate) return fromSummaryDate;

  try {
    if (kind === "duration") {
      const rows = typeof db.exportDurationSnapshots === "function"
        ? await db.exportDurationSnapshots()
        : [];
      return (Array.isArray(rows) ? rows : [])
        .map((row) => normalizeIsoDate(row?.快照日期 || row?.日期))
        .filter(Boolean)
        .sort()
        .at(-1) || null;
    }
    const rows = typeof db.exportWaveSnapshots === "function"
      ? await db.exportWaveSnapshots()
      : [];
    return (Array.isArray(rows) ? rows : [])
      .map((row) => normalizeIsoDate(row?.日期))
      .filter(Boolean)
      .sort()
      .at(-1) || null;
  } catch {
    return null;
  }
}

async function resolveReportDate(db, spec, kind = "wave") {
  return resolveDateSpec(spec, await getLatestDate(db, kind));
}

function genderLabel(gender) {
  return gender === "female" ? "女队" : "男团";
}

/**
 * 发送单团日报：每日之星文案 → 报告图（可多页）。
 * @param {{ withDate?: boolean }} [options] withDate 时在文案前加日期标题
 */
async function sendOneGenderReport(args, date, gender, db, renderReportPng, options = {}) {
  const label = genderLabel(gender);
  const report = await db.getDailyWaveReport(date, gender);
  if (!report?.rows?.length) {
    await args.replyText(`${date} 没有${label}主播数据。`);
    return false;
  }
  // 文案：日期（可选）+ 每日之星前三人名
  const top3Text = buildGenderTop3Text(date, gender, report, {
    withDate: Boolean(options.withDate),
    namesOnly: true,
  });
  await args.replyText(top3Text);

  try {
    // 标题交给渲染层按性别默认（男团星嗨艺创 / 女队薇笑传媒），与软件日报一致
    // 超过默认阈值才拆最多两张；人数不够保持一张
    const pages = await toDailyReportImagePages(renderReportPng, report, {});
    for (const page of pages) {
      const suffix = page.fileNameSuffix || "";
      await args.replyImage({
        buffer: page.buffer,
        fileName: `${date}_${label}_每日报告${suffix}.png`,
      });
    }
    return true;
  } catch (error) {
    await args.replyText(`${label}报告图片生成失败：${error instanceof Error ? error.message : String(error)}`);
    return false;
  }
}

async function sendReport(args, command, db, renderReportPng, extra = {}) {
  const date = await resolveReportDate(db, command.dateSpec, "wave");
  const available = await db.exportWaveSnapshots(date);
  if (!available.length) {
    await args.replyText(`${date} 没有音浪快照，暂时没有可发送的报告。`);
    return;
  }

  // 顺序：男团每日之星文案/报告 → 女队每日之星文案/报告；日期只出现在首条文案
  const genders = command.gender === "both" ? ["male", "female"] : [command.gender === "female" ? "female" : "male"];
  let first = true;
  for (const gender of genders) {
    await sendOneGenderReport(args, date, gender, db, renderReportPng, {
      withDate: first,
      renderDailyStarPng: extra.renderDailyStarPng,
    });
    first = false;
  }
}

function notLiveSummaryText(periodLabel, gender, report) {
  const label = genderLabel(gender);
  const total = report?.rows?.length || 0;
  const notLiveCount = Number(report?.summary?.notLiveCount) || 0;
  const notLiveDays = Number(report?.summary?.notLiveDays) || 0;
  return `${periodLabel} ${label}未开播天数报告：共 ${total} 人，未开播人数 ${notLiveCount} 人，未开播天数 ${notLiveDays} 天。`;
}

async function sendOneGenderNotLiveReport(args, periodLabel, gender, report, renderReportPng, options = {}) {
  const label = genderLabel(gender);
  const sorted = {
    ...report,
    gender,
    date: report.date || periodLabel,
    month: report.month || (periodLabel.length === 7 ? periodLabel : undefined),
    rows: sortNotLiveReportRows(report.rows || []),
  };
  if (!sorted.rows.length) {
    await args.replyText(`${periodLabel} 没有${label}主播数据。`);
    return false;
  }
  await args.replyText(notLiveSummaryText(periodLabel, gender, sorted));
  try {
    const pages = await toNotLiveReportImagePages(renderReportPng, sorted, {
      period: options.period || "daily",
    });
    for (const page of pages) {
      await args.replyImage({
        buffer: page.buffer,
        fileName: `${periodLabel}_${label}_未开播天数报告${page.fileNameSuffix || ""}.png`,
      });
    }
  } catch (error) {
    await args.replyText(`${label}未开播报告图片生成失败：${error instanceof Error ? error.message : String(error)}`);
    return false;
  }
  if (typeof args.replyFile === "function") {
    const csvRows = buildNotLiveCsvRows(sorted, {
      dateLabel: periodLabel,
      period: options.period || "daily",
    });
    await args.replyFile({
      buffer: csvBuffer(csvRows),
      fileName: `${periodLabel}_${label}_未开播天数.csv`,
    });
  }
  return true;
}

async function sendNotLiveReport(args, command, db, renderReportPng) {
  const latest = await getLatestDate(db, "wave");
  const genders = command.gender === "both" ? ["male", "female"] : [command.gender === "female" ? "female" : "male"];
  if (command.monthSpec) {
    const month = resolveMonthSpec(command.monthSpec, latest);
    if (typeof db.getMonthlyReport !== "function") {
      await args.replyText("月度未开播报告暂不可用。");
      return;
    }
    for (const gender of genders) {
      const report = await db.getMonthlyReport(month, gender);
      await sendOneGenderNotLiveReport(args, month, gender, report, renderReportPng, { period: "monthly" });
    }
    return;
  }
  const date = await resolveReportDate(db, command.dateSpec, "wave");
  const available = typeof db.exportWaveSnapshots === "function"
    ? await db.exportWaveSnapshots(date)
    : [{ 音浪: 1 }];
  if (!available.length) {
    await args.replyText(`${date} 没有音浪快照，暂时没有可发送的未开播报告。`);
    return;
  }
  for (const gender of genders) {
    const report = await db.getDailyWaveReport(date, gender);
    await sendOneGenderNotLiveReport(args, date, gender, report, renderReportPng, { period: "daily" });
  }
}

async function resolveAnchorOrReply(args, query, db) {
  const anchors = await db.getAnchors();
  const anchor = findAnchor(query, anchors);
  if (!anchor) {
    await args.replyText(`没有找到唯一主播“${query}”，请使用主播姓名、抖音号或主播 ID。`);
    return null;
  }
  return anchor;
}

/** 解析月粒度 dateSpec → "YYYY-MM" */
function resolveMonthText(spec, db) {
  if (spec?.type === "month") return `${spec.year}-${String(spec.month).padStart(2, "0")}`;
  if (spec?.type === "month-only") {
    const now = new Date();
    return `${now.getFullYear()}-${String(spec.month).padStart(2, "0")}`;
  }
  return null;
}

/** 「艺名 9月音浪 / 艺名 2026年音浪」→ 月/年聚合音浪 */
async function handleAnchorWaveRange(args, command, db, spec) {
  const anchor = await resolveAnchorOrReply(args, command.query, db);
  if (!anchor) return;
  const gender = anchor.gender === "female" ? "female" : "male";
  const ids = [anchor.anchorId, ...(anchor.aliasIds || [])].map((v) => String(v || "").trim()).filter(Boolean);

  if (spec.type === "year") {
    const now = new Date();
    const year = Number(spec.year);
    const toDate = year === now.getFullYear() ? localTodayIso() : `${year}-12-31`;
    if (typeof db.sumWaveSnapshotsForAccounts !== "function") {
      await args.replyText("当前数据库不支持年度汇总。");
      return;
    }
    const total = await db.sumWaveSnapshotsForAccounts(ids, `${year}-01-01`, toDate);
    await args.replyText(
      total > 0
        ? `${anchor.name} ${year}年音浪合计：${formatWave(total)}（截至 ${dayToFriendly(toDate)}）。`
        : `${anchor.name} ${year}年没有音浪数据。`
    );
    return;
  }

  const monthText = resolveMonthText(spec, db);
  if (!monthText) {
    await args.replyText("月份没看懂。请发「9月」或「2026年9月」。");
    return;
  }
  const report = typeof db.getMonthlyReport === "function"
    ? await db.getMonthlyReport(monthText, gender)
    : null;
  const row = (report?.rows || []).find((item) => rowMatchesAnchor(item, anchor));
  if (!row) {
    await args.replyText(`${anchor.name} ${monthText} 没有音浪数据。`);
    return;
  }
  const liveDays = Math.max(0, (Number(report.summary?.daysInMonth) || 0) - (Number(row.notLiveDays) || 0));
  await args.replyText(
    `${anchor.name} ${monthText} 音浪合计：${formatWave(row.totalWave)}，有音浪 ${liveDays} 天。`
  );
}

/** 「艺名 9月时长 / 艺名 2026年时长」→ 月/年粒度时长 */
async function handleAnchorDurationRange(args, command, db, spec) {
  const anchor = await resolveAnchorOrReply(args, command.query, db);
  if (!anchor) return;
  const gender = anchor.gender === "female" ? "female" : "male";

  if (spec.type === "year") {
    const now = new Date();
    const year = Number(spec.year);
    const asOf = year === now.getFullYear() ? localTodayIso() : `${year}-12-31`;
    const rows = typeof db.exportDurationSnapshots === "function"
      ? await db.exportDurationSnapshots(asOf)
      : [];
    const ids = new Set([anchor.anchorId, ...(anchor.aliasIds || [])].map((v) => String(v || "")));
    const matches = rows.filter((row) => ids.has(String(row.抖音号 || "")));
    if (!matches.length) {
      await args.replyText(`${anchor.name} ${year}年没有时长快照。`);
      return;
    }
    const best = matches.reduce((cur, row) => Number(row.时长分钟) > Number(cur.时长分钟) ? row : cur);
    await args.replyText(`${anchor.name} 截至 ${dayToFriendly(asOf)} 的累计直播时长：${formatDuration(best.时长分钟)}。`);
    return;
  }

  const monthText = resolveMonthText(spec, db);
  if (!monthText) {
    await args.replyText("月份没看懂。请发「9月」或「2026年9月」。");
    return;
  }
  const report = typeof db.getMonthlyReport === "function"
    ? await db.getMonthlyReport(monthText, gender)
    : null;
  const row = (report?.rows || []).find((item) => rowMatchesAnchor(item, anchor));
  if (!row) {
    await args.replyText(`${anchor.name} ${monthText} 没有时长数据。`);
    return;
  }
  await args.replyText(`${anchor.name} ${monthText} 直播时长：${formatDuration(row.totalDuration)}。`);
}

async function handleAnchorDuration(args, command, db, analytics) {
  const durationSpec = command.dateSpec;
  if (durationSpec && (durationSpec.type === "month" || durationSpec.type === "month-only" || durationSpec.type === "year")) {
    await handleAnchorDurationRange(args, command, db, durationSpec);
    return;
  }
  if (analytics) {
    const result = await analytics.getAnchorDuration({ query: command.query });
    if (!result.ok) {
      if (result.candidates?.length) {
        await args.replyText(`没有找到唯一主播“${command.query}”，请使用主播姓名、抖音号或主播 ID。`);
      } else {
        await args.replyText(result.error || `没有找到唯一主播“${command.query}”，请使用主播姓名、抖音号或主播 ID。`);
      }
      return;
    }
    if (!result.found) {
      await args.replyText(result.message || `${command.query} 没有时长快照。`);
      return;
    }
    await args.replyText(result.text);
    return;
  }
  const anchor = await resolveAnchorOrReply(args, command.query, db);
  if (!anchor) return;
  const date = await resolveReportDate(db, null, "duration");
  const rows = await db.exportDurationSnapshots(date);
  const ids = new Set([anchor.anchorId, ...(anchor.aliasIds || [])].map((value) => String(value || "")));
  const matches = rows.filter((row) => ids.has(String(row.抖音号 || "")));
  if (!matches.length) {
    await args.replyText(`${anchor.name} 在 ${date} 之前没有时长快照。`);
    return;
  }
  const best = matches.reduce((current, row) => Number(row.时长分钟) > Number(current.时长分钟) ? row : current);
  await args.replyText(`${anchor.name} 截至 ${date} 的累计直播时长：${formatDuration(best.时长分钟)}（${compactNumber(best.时长分钟)} 分钟）。`);
}

/**
 * 直接输入名字：汇总该主播库内全部已有数据（基础信息 + 音浪/时长全量）。
 */
async function handleAnchorProfile(args, command, db, analytics) {
  if (analytics) {
    const result = await analytics.getAnchorFullProfile({ query: command.query });
    if (!result.ok) {
      await args.replyText(`没有找到唯一主播“${command.query}”，请使用主播姓名、抖音号或主播 ID。`);
      return;
    }
    for (const part of result.textParts || [result.summaryText].filter(Boolean)) {
      if (part) await args.replyText(part);
    }
    return;
  }
  // 无 analytics 时的最小回退
  const anchor = await resolveAnchorOrReply(args, command.query, db);
  if (!anchor) return;
  await args.replyText(`【${anchor.name}】库内全部数据\n主播ID：${anchor.anchorId || "-"}`);
}

async function handleAnchorWaveDays(args, command, db, analytics) {
  if (analytics) {
    const result = await analytics.getAnchorWaveDays({ query: command.query });
    if (!result.ok) {
      await args.replyText(`没有找到唯一主播“${command.query}”，请使用主播姓名、抖音号或主播 ID。`);
      return;
    }
    if (!result.found) {
      await args.replyText(result.message || `${command.query} 没有可用音浪数据。`);
      return;
    }
    await args.replyText(result.text);
    return;
  }
  const anchor = await resolveAnchorOrReply(args, command.query, db);
  if (!anchor) return;
  const date = await resolveReportDate(db, null, "wave");
  const report = await db.getDailyWaveReport(date, anchor.gender === "female" ? "female" : "male");
  const row = report?.rows?.find((item) => rowMatchesAnchor(item, anchor));
  if (!row) {
    await args.replyText(`${anchor.name} 在 ${date} 没有可用音浪数据。`);
    return;
  }
  const dayOfMonth = Number(date.slice(8, 10)) || 0;
  const liveDays = Math.max(0, dayOfMonth - (Number(row.notLiveDays) || 0));
  await args.replyText(`${anchor.name} 截至 ${date} 本月有音浪 ${liveDays} 天，累计音浪 ${formatWave(row.totalWave)}，${Number(date.slice(5, 7))}月未播 ${row.notLiveDays || 0} 天。`);
}

async function handleAnchorWave(args, command, db, analytics) {
  const waveSpec = command.dateSpec;
  if (waveSpec && (waveSpec.type === "month" || waveSpec.type === "month-only" || waveSpec.type === "year")) {
    await handleAnchorWaveRange(args, command, db, waveSpec);
    return;
  }
  if (analytics) {
    const date = command.dateSpec ? resolveDateSpec(command.dateSpec, await getLatestDate(db, "wave")) : null;
    const result = await analytics.getAnchorWaveProfile({ query: command.query, date });
    if (!result.ok) {
      await args.replyText(`没有找到唯一主播“${command.query}”，请使用主播姓名、抖音号或主播 ID。`);
      return;
    }
    if (!result.found) {
      await args.replyText(result.message || `${command.query} 没有可用音浪数据。`);
      return;
    }
    await args.replyText(
      `${result.anchor.name} ${result.asOfDate} 日音浪：${result.dailyWaveText}；截至当日累计音浪：${result.totalWaveText}。`
    );
    return;
  }
  const anchor = await resolveAnchorOrReply(args, command.query, db);
  if (!anchor) return;
  const date = await resolveReportDate(db, command.dateSpec, "wave");
  const report = await db.getDailyWaveReport(date, anchor.gender === "female" ? "female" : "male");
  const row = report?.rows?.find((item) => rowMatchesAnchor(item, anchor));
  if (!row) {
    await args.replyText(`${anchor.name} 在 ${date} 没有可用音浪数据。`);
    return;
  }
  await args.replyText(`${anchor.name} ${date} 日音浪：${formatWave(row.dailyWave)}；截至当日累计音浪：${formatWave(row.totalWave)}。`);
}

async function handleExportWaveFile(args, command, db) {
  const date = await resolveReportDate(db, command.dateSpec, "wave");
  const rows = await db.exportWaveSnapshots(date);
  if (!rows.length) {
    await args.replyText(`${date} 没有音浪快照，暂时没有可发送的文件。`);
    return;
  }
  const buffer = csvBuffer(rows);
  await args.replyText(`正在发送 ${date} 音浪文件，共 ${rows.length} 条。`);
  await args.replyFile({ buffer, fileName: `${date}_音浪数据.csv` });
}

/** 会话键：与串行队列、导入日期预告共用同一套「账号 / 群 / 用户」隔离 */
const pendingImportKey = sessionKeyFromContext;

/**
 * 日期口令窗口：先发「9.11」，再连发音浪 + 时长两个 CSV，两个都落到 9.11。
 * 窗口内最多覆盖 PENDING_IMPORT_MAX_FILES 个文件，用满即失效（避免很久后误导入到旧日期）。
 */
function createPendingImportDateStore({ maxFiles = PENDING_IMPORT_MAX_FILES } = {}) {
  const limit = Number.isFinite(maxFiles) && maxFiles > 0 ? Math.floor(maxFiles) : PENDING_IMPORT_MAX_FILES;
  /** @type {Map<string, { date: string, expiresAt: number, remaining: number, kinds: Set<string> }>} */
  const store = new Map();

  function read(args) {
    const key = sessionKeyFromContext(args);
    const item = store.get(key);
    if (!item) return null;
    if (Date.now() > item.expiresAt) {
      store.delete(key);
      return null;
    }
    return item;
  }

  return {
    maxFiles: limit,
    set(args, date) {
      const key = sessionKeyFromContext(args);
      store.set(key, {
        date,
        expiresAt: Date.now() + PENDING_IMPORT_DATE_TTL_MS,
        remaining: limit,
        kinds: new Set(),
      });
      return key;
    },
    /** 取用一次：返回当时快照，未用满则保留窗口供下一个文件继续用 */
    consume(args) {
      const item = read(args);
      if (!item) return null;
      item.remaining -= 1;
      const snapshot = {
        date: item.date,
        remaining: item.remaining,
        kinds: new Set(item.kinds),
      };
      if (item.remaining <= 0) store.delete(sessionKeyFromContext(args));
      return snapshot;
    },
    /** 记录本次导入的文件类型（音浪 / 时长），用于提示还缺哪个 */
    noteKind(args, kind) {
      const item = read(args);
      if (item && kind) item.kinds.add(kind);
    },
    peek(args) {
      return read(args)?.date || null;
    },
    clear(args) {
      store.delete(sessionKeyFromContext(args));
    },
  };
}

/**
 * CSV 导入日期规则：
 * 1) 消息文字明确指定日期（如「24号数据」）→ 该日
 * 2) 否则若会话有 10 分钟内预告的导入日 → 预告日（同一口令可覆盖 2 个文件）
 * 3) 否则默认「昨天」
 * 不再使用文件名里的日期，避免 22 号发送的 csv 误导入 22 号。
 */
function resolveInboundImportDate(args, pendingDates) {
  const fromText = parseExplicitImportDateFromText(args.text || "");
  if (fromText) {
    pendingDates.clear(args);
    return { date: fromText, source: "message", remaining: 0, receivedKinds: new Set() };
  }
  const pending = pendingDates.consume(args);
  if (pending) {
    return { date: pending.date, source: "pending", remaining: pending.remaining, receivedKinds: pending.kinds };
  }
  return { date: localYesterdayIso(), source: "yesterday", remaining: 0, receivedKinds: new Set() };
}

async function handleInboundFile(args, db, pendingDates, dailyPush = null) {
  args.assertLease?.();
  const file = await args.downloadMedia(args.fileItem);
  args.assertLease?.();
  const fileName = String(file.fileName || "weixin-file.bin");
  if (!/\.csv$/i.test(fileName)) {
    await args.replyText("请发送 CSV 格式的音浪或时长文件。\n默认导入到昨天；若要指定日期，请先发「24号数据」再传文件。");
    return;
  }
  const text = decodeCsv(file.buffer);
  const kind = inferImportKind(text, fileName, args.text);
  const parsed = parseCsvText(text, kind);
  const anchors = await db.getAnchors();
  const matched = matchImportRows(parsed.rows, anchors);
  if (!matched.rows.length) {
    await args.replyText(`文件已解析，但没有匹配到主播。有效行 ${parsed.rows.length}，未匹配 ${matched.unmatched.length}。`);
    return;
  }
  const resolved = resolveInboundImportDate(args, pendingDates);
  const date = resolved.date;
  const meta = buildImportMeta(file.buffer, fileName, kind, matched.rows);
  const importRows = kind === "wave"
    ? matched.rows.map((row) => ({ anchorId: row.anchorId, waveValue: row.value, rank: row.rank }))
    : matched.rows.map((row) => ({ anchorId: row.anchorId, totalMinutes: row.value }));
  const importInfo = {
    source: "bot",
    matchedCount: matched.rows.length,
    unmatchedCount: matched.unmatched.length,
    duplicateRows: matched.duplicateRows,
  };
  args.assertLease?.();
  if (kind === "wave") {
    await db.importWaveSnapshots(date, importRows, meta, importInfo);
  } else {
    await db.importDurationSnapshots(date, importRows, meta, importInfo);
  }
  args.assertLease?.();
  const label = kind === "wave" ? "音浪" : "时长";
  const sourceHint = resolved.source === "yesterday"
    ? "（默认昨天；指定日期请先发「X号数据」）"
    : resolved.source === "pending"
      ? "（按你预告的日期）"
      : "（按消息指定日期）";
  const alreadyKinds = resolved.receivedKinds || new Set();
  const replyLines = [
    `已导入 ${dayToFriendly(date)} 的${label}数据${sourceHint}：${matched.rows.length} 条；未匹配 ${matched.unmatched.length} 条，重复行 ${matched.duplicateRows} 条，非法行 ${parsed.skipped} 条。`,
  ];
  if (alreadyKinds.has(kind)) {
    replyLines.push(`注意：该日期的${label}本次是第二次导入，已覆盖之前的数据。`);
  }
  if (resolved.source === "pending") {
    const got = new Set(alreadyKinds);
    got.add(kind);
    const missing = [
      got.has("wave") ? null : "音浪",
      got.has("duration") ? null : "时长",
    ].filter(Boolean);
    if (resolved.remaining > 0) {
      replyLines.push(
        `该日期口令还剩 ${resolved.remaining} 个文件可用${missing.length ? `，还缺：${missing.join("、")}` : ""}。`
      );
    } else {
      replyLines.push(
        `该日期口令已用满 ${resolved.remaining + 1} 个文件${missing.length ? `（还缺：${missing.join("、")}，可重发日期再传）` : "，音浪与时长都已入库"}。`
      );
    }
  }
  await args.replyText(replyLines.join("\n"));
  if (resolved.source === "pending") pendingDates.noteKind(args, kind);
  if (kind === "wave" && dailyPush && typeof dailyPush.notifyAfterImport === "function") {
    try {
      void dailyPush.notifyAfterImport(date, { delayMs: 1500 });
    } catch {
      // ignore schedule errors
    }
  }
}


const CUSTOM_ACTIONS = new Set(["reply", "daily_report", "male_report", "female_report", "wave_file", "help"]);

function matchCustomCommand(text, customCommands) {
  const original = normalizeText(text);
  if (!original) return null;
  const list = Array.isArray(customCommands) ? customCommands : [];
  const lower = original.toLowerCase();
  for (const item of list) {
    if (!item || item.enabled === false) continue;
    const trigger = normalizeText(item.trigger);
    if (!trigger) continue;
    if (trigger.toLowerCase() !== lower) continue;
    const action = String(item.action || "reply").trim();
    if (!CUSTOM_ACTIONS.has(action)) continue;
    return {
      type: "custom",
      action,
      replyText: String(item.replyText || "").trim(),
      trigger,
    };
  }
  return null;
}

async function handleCustomCommand(args, command, db, renderReportPng, extra = {}) {
  if (command.action === "reply") {
    const text = command.replyText || "已收到。";
    await args.replyText(text);
    return;
  }
  if (command.action === "help") {
    await args.replyText(HELP_TEXT);
    return;
  }
  if (command.action === "daily_report") {
    await sendReport(args, { type: "report", gender: "both", dateSpec: null }, db, renderReportPng, extra);
    return;
  }
  if (command.action === "male_report") {
    await sendReport(args, { type: "report", gender: "male", dateSpec: null }, db, renderReportPng, extra);
    return;
  }
  if (command.action === "female_report") {
    await sendReport(args, { type: "report", gender: "female", dateSpec: null }, db, renderReportPng, extra);
    return;
  }
  if (command.action === "wave_file") {
    await handleExportWaveFile(args, { type: "export-wave-file", dateSpec: null }, db);
  }
}

async function handleBindCommand(args, command, bindService) {
  if (!bindService) {
    await args.replyText("绑定功能暂不可用。");
    return true;
  }
  const context = {
    channel: args.channel,
    fromUserId: args.fromUserId || args.userId,
    userId: args.userId,
  };
  let result;
  if (command.type === "bind") {
    result = await bindService.bind({ ...context, douyinNo: command.douyinNo });
  } else if (command.type === "bind-status") {
    result = await bindService.status(context);
  } else if (command.type === "unbind") {
    result = await bindService.unbind(context);
  } else {
    return false;
  }
  await args.replyText(formatBindReply(result));
  return true;
}

/** 「9.11 / 9月11日 / 11号」→ 预告导入日期：10 分钟内可连传 2 个 CSV（音浪 + 时长），都导入该日 */
async function handleImportDateAnnounce(args, command, pendingImportDates) {
  const spec = parseDateSpec(String(command?.raw || ""));
  if (!spec || !pendingImportDates) {
    await args.replyText("日期格式没看懂。请发「9.11」「9月11日」「11号」或「2026-09-11」，再发 CSV 文件。");
    return;
  }
  const date = resolveDateSpec(spec, localYesterdayIso());
  pendingImportDates.set(args, date);
  await args.replyText(
    `已记住导入日期 ${dayToFriendly(date)}（10 分钟内有效，可连传 ${pendingImportDates.maxFiles || PENDING_IMPORT_MAX_FILES} 个文件）。请依次发送音浪与时长 CSV。`
  );
}

async function dispatchBusinessCommand(args, command, { db, renderReportPng, renderDailyStarPng = null, analytics, bindService = null, pendingImportDates = null }) {
  if (!command) return false;
  if (command.type === "report") await sendReport(args, command, db, renderReportPng, { renderDailyStarPng });
  else if (command.type === "not-live-report") await sendNotLiveReport(args, command, db, renderReportPng);
  else if (command.type === "anchor-profile") await handleAnchorProfile(args, command, db, analytics);
  else if (command.type === "anchor-duration") await handleAnchorDuration(args, command, db, analytics);
  else if (command.type === "anchor-wave-days") await handleAnchorWaveDays(args, command, db, analytics);
  else if (command.type === "anchor-wave") await handleAnchorWave(args, command, db, analytics);
  else if (command.type === "export-wave-file") await handleExportWaveFile(args, command, db);
  else if (command.type === "import-date") await handleImportDateAnnounce(args, command, pendingImportDates);
  else if (command.type === "bind" || command.type === "bind-status" || command.type === "unbind") {
    await handleBindCommand(args, command, bindService);
  }
  else return false;
  return true;
}

function createWeixinCommandHandler({ db, renderReportPng, renderDailyStarPng: renderDailyStarPngOpt = null, analytics: sharedAnalytics = null, dailyPush = null } = {}) {
  if (!db) throw new Error("微信机器人命令处理缺少数据库");
  if (typeof renderReportPng !== "function") throw new Error("微信机器人命令处理缺少图片渲染器");
  const pendingImportDates = createPendingImportDateStore();
  const analytics = sharedAnalytics || createWeixinAnalytics({ db, renderReportPng });
  const bindService = createAnchorBindService({ db });
  const deps = { db, renderReportPng, renderDailyStarPng: renderDailyStarPngOpt, analytics, bindService, pendingImportDates };

  async function handleCommand(args) {
    try {
      const items = Array.isArray(args.items) ? args.items : [];
      const fileItem = items.find((item) => item?.type === 4 && item.file_item);
      if (fileItem) {
        await handleInboundFile({ ...args, fileItem }, db, pendingImportDates, dailyPush);
        return { handled: true };
      }

      // 预告导入日：先说「9.11」，再连传音浪 + 时长两个 CSV
      const importDateHint = parseExplicitImportDateFromText(args.text || "");
      if (importDateHint && /(?:数据|导入)/.test(normalizeText(args.text || ""))) {
        pendingImportDates.set(args, importDateHint);
        await args.replyText(
          `已记住导入日期 ${importDateHint}（10 分钟内有效，可连传 ${pendingImportDates.maxFiles} 个文件）。请依次发送音浪与时长 CSV。`
        );
        return { handled: true };
      }

      // 「导入记录」：最近 5 次导入（含来源与匹配统计）
      if (/^导入(记录|日志)$/.test(normalizeText(args.text || ""))) {
        try {
          const logs = typeof db.listImportLogs === "function" ? await db.listImportLogs(5) : [];
          if (!logs.length) {
            await args.replyText("还没有导入记录。发 CSV 即可导入（音浪 + 时长两个文件）。");
            return { handled: true };
          }
          const kindLabel = { wave: "音浪", duration: "时长" };
          const sourceLabel = { bot: "机器人", web: "网页" };
          const lines = logs.map((log, i) => {
            const day = String(log.importDate || "").slice(5).replace("-", "月") + "日";
            const time = log.createdAt
              ? String(log.createdAt).replace("T", " ").slice(5, 16)
              : "";
            return `${i + 1}. ${day} ${kindLabel[log.kind] || log.kind} ${log.fileName || "未命名"}：${log.rowCount} 条（匹配 ${log.matchedCount}/未匹配 ${log.unmatchedCount}） ${sourceLabel[log.source] || log.source} ${time}`;
          });
          await args.replyText(`最近 ${logs.length} 次导入：\n${lines.join("\n")}`);
        } catch (error) {
          await args.replyText(`读取导入记录失败：${error?.message || error}`);
        }
        return { handled: true };
      }

      const pushCommand = matchDailyPushCommand(args.text);
      if (pushCommand) {
        if (!dailyPush) {
          await args.replyText("日报推送能力未就绪。");
          return { handled: true };
        }
        const actorUserId = String(args.fromUserId || args.userId || "").trim();
        const accountId = String(args.accountId || "").trim();
        try {
          if (pushCommand.type === "status") {
            const text = typeof dailyPush.getStatusText === "function"
              ? dailyPush.getStatusText()
              : "无法读取推送状态";
            await args.replyText(text);
            return { handled: true };
          }
          if (pushCommand.type === "enable") {
            dailyPush.setEnabled(true, { actorUserId, accountId });
            await args.replyText("已开启日报自动推送。音浪数据更新后将发送每日之星（前三名）文案与报告图。发「关闭日报推送」可关闭。");
            return { handled: true };
          }
          if (pushCommand.type === "disable") {
            dailyPush.setEnabled(false, { actorUserId, accountId });
            await args.replyText("已关闭日报自动推送。");
            return { handled: true };
          }
        } catch (error) {
          await args.replyText(error instanceof Error ? error.message : String(error));
          return { handled: true };
        }
      }

      if (SYSTEM_HELP_RE.test(args.text)) {
        await args.replyText(INSTRUCTION_HELP);
        return { handled: true };
      }

      // 已下线的 AI 相关口令：明确说明，避免被当成艺名去查库
      if (RETIRED_AI_RE.test(normalizeText(args.text || ""))) {
        await args.replyText(RETIRED_AI_REPLY);
        return { handled: true };
      }

      // 自定义指令（用户自己配的触发词）
      const custom = matchCustomCommand(args.text, args.settings?.customCommands);
      if (custom) {
        await handleCustomCommand(args, custom, db, renderReportPng, { renderDailyStarPng: renderDailyStarPngOpt });
        return { handled: true };
      }

      const command = parseBotCommand(args.text);
      if (!command) return { handled: false };

      if (command.type === "help") {
        await args.replyText(INSTRUCTION_HELP);
        return { handled: true };
      }

      const ok = await dispatchBusinessCommand(args, command, deps);
      return { handled: ok };
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (typeof args.replyText === "function") {
        await args.replyText(`处理失败：${message}`);
      }
      return { handled: true, error: message };
    }
  }

  handleCommand.analytics = analytics;
  return handleCommand;
}

module.exports = {
  HELP_TEXT,
  normalizeText,
  parseDateSpec,
  resolveDateSpec,
  normalizeIsoDate,
  getLatestDate,
  parseBotCommand,
  parseMonthSpec,
  resolveMonthSpec,
  parseBindCommand,
  matchCustomCommand,
  parseWaveValue,
  parseDurationValue,
  inferImportKind,
  parseCsvText,
  matchImportRows,
  buildImportMeta,
  pendingImportKey,
  createPendingImportDateStore,
  resolveInboundImportDate,
  createWeixinCommandHandler,
  matchDailyPushCommand,
  RETIRED_AI_RE,
};
