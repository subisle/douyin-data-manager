const fs = require("fs");
const path = require("path");
const mysql = require("mysql2/promise");
const { resolveDbConfig } = require("./db-config");
const {
  ensureDatabaseIndexes,
  ensureImportRecordsTable,
  ensureChannelAnchorBindsTable,
  ensureAnchorIncomeTables,
  ensurePersonDailyReportVisibilityColumn,
} = require("./db-maintenance");

// 复用根项目 .env（与 DouyinLang 同一套远程 MySQL）
const envPaths = [
  path.join(__dirname, "..", ".env"),
  process.resourcesPath ? path.join(process.resourcesPath, ".env") : null,
  path.join(process.cwd(), ".env"),
].filter(Boolean);
const envPath = envPaths.find((item) => fs.existsSync(item));
require("dotenv").config({
  path: envPath || envPaths[0],
  quiet: true,
});

/** @type {import('mysql2/promise').Pool | null} */
let pool = null;
let dailyReportVisibilityColumnReady = false;
// 防止重试递归：query/getConnection 重试用，避免包装方法自身无限嵌套
let dbRetryInProgress = false;

const LIVE_WAVE_THRESHOLD = 2;

/** 判断是否为连接被服务端断开的可恢复错误 */
function isConnectionLostError(err) {
  if (!err) return false;
  const code = String(err.code || "");
  const message = String(err.message || "");
  return (
    code === "PROTOCOL_CONNECTION_LOST" ||
    code === "ECONNRESET" ||
    code === "EPIPE" ||
    code === "ETIMEDOUT" ||
    /Connection lost/i.test(message) ||
    /The server closed the connection/i.test(message) ||
    /socket hang up/i.test(message) ||
    err.fatal === true
  );
}

/** 安全关闭并清空连接池，下次 getPool 时重建 */
function resetPool() {
  if (pool) {
    const old = pool;
    pool = null;
    try { old.end(); } catch (_) { /* ignore */ }
  }
}

function getPool() {
  if (!pool) {
    const { host, user, password, database, port } = resolveDbConfig();
    const positiveInt = (value, fallback) => {
      const n = Number(value);
      return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback;
    };
    pool = mysql.createPool({
      host,
      port,
      user,
      password,
      database,
      waitForConnections: true,
      // 低配设备（如 RK3318 盒子）可用 DB_POOL_LIMIT 收到 2，显著降低常驻内存
      connectionLimit: positiveInt(process.env.DB_POOL_LIMIT, 5),
      connectTimeout: 10000,
      // 远程 MySQL 防断连：开启 TCP keepalive + 缩短空闲超时，避免被服务端 wait_timeout 踢掉
      enableKeepAlive: true,
      keepAliveInitialDelay: 10000,
      idleTimeout: positiveInt(process.env.DB_POOL_IDLE_TIMEOUT_MS, 30000),
      maxIdle: positiveInt(process.env.DB_POOL_MAX_IDLE, 1),
      // DATE/DATETIME 直接返回字符串，避免 JS Date 经 UTC 转换导致日期回退一天
      dateStrings: true,
    });
    pool.on("error", (err) => {
      console.warn("[db] pool error, will reset on next query:", err?.code || err?.message);
      // 连接池致命错误（如服务器断开）时清空 pool，下次 getPool 重建
      if (isConnectionLostError(err)) {
        pool = null;
      }
    });

    // ---- 包装 query：连接丢失时自动重置连接池并重试一次 ----
    const originalQuery = pool.query.bind(pool);
    pool.query = async function wrappedQuery(sql, params) {
      try {
        return await originalQuery(sql, params);
      } catch (err) {
        if (isConnectionLostError(err) && !dbRetryInProgress) {
          console.warn("[db] query connection lost, reset pool and retry once:", err?.code || err?.message);
          dbRetryInProgress = true;
          try {
            resetPool();
            const fresh = getPool();
            return await fresh.query(sql, params);
          } finally {
            dbRetryInProgress = false;
          }
        }
        throw err;
      }
    };

    // ---- 包装 getConnection：连接丢失时自动重置连接池并重试一次 ----
    const originalGetConnection = pool.getConnection.bind(pool);
    pool.getConnection = async function wrappedGetConnection() {
      try {
        return await originalGetConnection();
      } catch (err) {
        if (isConnectionLostError(err) && !dbRetryInProgress) {
          console.warn("[db] getConnection connection lost, reset pool and retry once:", err?.code || err?.message);
          dbRetryInProgress = true;
          try {
            resetPool();
            const fresh = getPool();
            return await fresh.getConnection();
          } finally {
            dbRetryInProgress = false;
          }
        }
        throw err;
      }
    };
  }
  return pool;
}

async function ensureDailyReportVisibilityColumn(db = getPool()) {
  if (dailyReportVisibilityColumnReady) return false;
  const created = await ensurePersonDailyReportVisibilityColumn(db);
  dailyReportVisibilityColumnReady = true;
  return created;
}

function normalizeImportMeta(meta) {
  if (!meta || typeof meta !== "object") return null;
  const fileHash = String(meta.fileHash || "").trim().toLowerCase();
  const dataHash = String(meta.dataHash || "").trim().toLowerCase();
  const fileName = String(meta.fileName || "").trim().slice(0, 255);
  const rowCount = Number(meta.rowCount) || 0;
  if (!/^[a-f0-9]{32}$/.test(fileHash) || !/^[a-f0-9]{64}$/.test(dataHash)) {
    return null;
  }
  return { fileHash, dataHash, fileName, rowCount };
}

async function assertImportNotRecorded(db, kind, importDate, meta) {
  if (!meta) return;
  const [rows] = await db.query(
    `SELECT file_hash, data_hash, file_name, created_at
       FROM import_records
      WHERE kind = ? AND import_date = ? AND (file_hash = ? OR data_hash = ?)
      LIMIT 1`,
    [kind, importDate, meta.fileHash, meta.dataHash]
  );
  if (rows.length > 0) {
    const duplicatedBy = rows[0].file_hash === meta.fileHash ? "文件 MD5" : "导入数据";
    throw new Error(`${duplicatedBy} 已导入过，已阻止重复导入`);
  }
}

async function recordImport(db, kind, importDate, meta, info = null) {
  if (!meta) return;
  const source = String(info?.source || "bot").slice(0, 16);
  const matchedCount = Number(info?.matchedCount) || 0;
  const unmatchedCount = Number(info?.unmatchedCount) || 0;
  const duplicateRows = Number(info?.duplicateRows) || 0;
  await db.query(
    `INSERT INTO import_records
       (kind, import_date, file_hash, data_hash, file_name, row_count,
        source, matched_count, unmatched_count, duplicate_rows)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [kind, importDate, meta.fileHash, meta.dataHash, meta.fileName, meta.rowCount,
     source, matchedCount, unmatchedCount, duplicateRows]
  );
}

/**
 * 时长导入统一按「月」覆盖：'YYYY-MM'（或 'YYYY-MM-DD'）都归一到该月最后一天，
 * 同一月份的导入落在同一 import_date 快照点，UPSERT 即覆盖整月时长。
 */
function monthStart(monthEnd) {
  return `${String(monthEnd).slice(0, 7)}-01`;
}

function normalizeDurationImportDate(value) {
  const text = String(value || "").trim();
  const monthMatch = /^(\d{4})-(\d{2})$/.exec(text);
  if (monthMatch) {
    const year = Number(monthMatch[1]);
    const month = Number(monthMatch[2]);
    if (month < 1 || month > 12) throw new Error(`无效的时长导入月份: ${text}`);
    const lastDay = new Date(Date.UTC(year, month, 0)).getUTCDate();
    return `${monthMatch[1]}-${monthMatch[2]}-${String(lastDay).padStart(2, "0")}`;
  }
  const dayMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(text);
  if (dayMatch) {
    const month = Number(dayMatch[2]);
    const day = Number(dayMatch[3]);
    if (month < 1 || month > 12 || day < 1 || day > 31) {
      throw new Error(`无效的时长导入日期: ${text}`);
    }
    return normalizeDurationImportDate(`${dayMatch[1]}-${dayMatch[2]}`);
  }
  throw new Error(`无效的时长导入日期(应为 YYYY-MM 月份): ${text}`);
}

async function importSnapshotRows(db, kind, importDate, rows, meta, info = null) {
  if (!Array.isArray(rows) || rows.length === 0) return { inserted: 0 };

  let values;
  let upsertSql;
  // duration 月覆盖：事务内先删除该月范围内这批主播的旧时长快照，
  // 再写入本月的月末快照，保证「选 7 月导入 = 7 月时长整体覆盖」。
  let monthClear = null;
  if (kind === "wave") {
    values = rows
      .map((row) => {
        const anchorId = String(row.anchorId ?? "").trim();
        const waveValue = Number(row.waveValue);
        if (!anchorId || !Number.isFinite(waveValue)) return null;
        return [
          anchorId,
          importDate,
          Math.round(waveValue) || 0,
          Math.round(Number(row.rank)) || 0,
        ];
      })
      .filter(Boolean);
    upsertSql = `INSERT INTO wave_snapshots (anchor_id, import_date, wave_value, \`rank\`)
     VALUES ?
     ON DUPLICATE KEY UPDATE wave_value = VALUES(wave_value), \`rank\` = VALUES(\`rank\`)`;
  } else if (kind === "duration") {
    importDate = normalizeDurationImportDate(importDate);
    const monthStartDate = monthStart(importDate);
    values = rows
      .map((row) => {
        const anchorId = String(row.anchorId ?? "").trim();
        const totalMinutes = Number(row.totalMinutes);
        if (!anchorId || !Number.isFinite(totalMinutes)) return null;
        return [anchorId, importDate, Math.round(totalMinutes) || 0];
      })
      .filter(Boolean);
    const monthAnchorIds = Array.from(new Set(values.map((v) => v[0])));
    if (monthAnchorIds.length > 0) {
      monthClear = {
        sql: `DELETE FROM duration_snapshots
               WHERE import_date BETWEEN ? AND ? AND anchor_id IN (${monthAnchorIds.map(() => "?").join(",")})`,
        params: [monthStartDate, importDate, ...monthAnchorIds],
      };
    }
    upsertSql = `INSERT INTO duration_snapshots (anchor_id, import_date, total_minutes)
     VALUES ?
     ON DUPLICATE KEY UPDATE total_minutes = VALUES(total_minutes)`;
  } else {
    throw new Error(`不支持的快照导入类型: ${kind}`);
  }

  if (values.length === 0) return { inserted: 0 };

  const importMeta = normalizeImportMeta(meta);
  // DDL 会隐式提交 MySQL 事务，因此必须在获取事务连接前完成。
  if (importMeta) await ensureImportRecordsTable(db);

  const conn = await db.getConnection();
  let transactionStarted = false;
  try {
    await conn.beginTransaction();
    transactionStarted = true;
    await assertImportNotRecorded(conn, kind, importDate, importMeta);
    if (monthClear) await conn.query(monthClear.sql, monthClear.params);
    const [result] = await conn.query(upsertSql, [values]);
    await recordImport(conn, kind, importDate, importMeta, info);
    await conn.commit();
    transactionStarted = false;
    return { inserted: result.affectedRows };
  } catch (error) {
    if (transactionStarted) {
      try {
        await conn.rollback();
      } catch (rollbackError) {
        if (error && typeof error === "object") error.rollbackError = rollbackError;
      }
    }
    throw error;
  } finally {
    conn.release();
  }
}

/**
 * 聚合 persons + accounts 为主播列表（与 DouyinLang buildAnchorsFromNewSchema 一致）
 */
async function getAnchors() {
  const db = getPool();
  await ensureDailyReportVisibilityColumn(db);
  const [persons] = await db.query(
    `SELECT p.id, p.name, p.gender, p.master_id, m.name AS master_name,
            p.hide_in_daily_report,
            p.generation, p.created_at, p.updated_at
       FROM persons p
       LEFT JOIN persons m ON m.id = p.master_id
      ORDER BY p.id`
  );
  const [accounts] = await db.query(
    "SELECT id, person_id, anchor_id, douyin_no, anchor_name, is_primary, created_at FROM accounts"
  );

  /** @type {Map<number, any[]>} */
  const byPerson = new Map();
  for (const acc of accounts) {
    if (!byPerson.has(acc.person_id)) byPerson.set(acc.person_id, []);
    byPerson.get(acc.person_id).push(acc);
  }

  return persons.filter((p) => (byPerson.get(p.id) || []).length > 0).map((p) => {
    const list = byPerson.get(p.id) || [];
    const primary = list.find((a) => a.is_primary === 1) || list[0];
    const aliases = primary
      ? list.filter((a) => a.id !== primary.id).map((a) => a.anchor_id)
      : list.map((a) => a.anchor_id);
    return {
      id: p.id,
      name: p.name || (primary && primary.anchor_name) || "",
      gender: p.gender || "",
      generation: p.generation ?? null,
      masterId: p.master_id ?? null,
      masterName: p.master_name || null,
      hideInDailyReport: Number(p.hide_in_daily_report) === 1,
      anchorId: primary ? primary.anchor_id || "" : "",
      anchorName: primary ? primary.anchor_name || p.name || "" : p.name || "",
      douyinNo: primary ? primary.douyin_no || "" : "",
      accountCount: list.length,
      aliasIds: aliases,
      createdAt: p.created_at || null,
    };
  });
}

/**
 * 族谱：返回带师父名字的主播关系。
 * 展示主播列表中有账号绑定的人员，并补齐这些人员的必要师傅节点。
 */
async function getFamilyTree() {
  const db = getPool();
  const [persons] = await db.query(
    "SELECT id, name, gender, master_id, generation FROM persons ORDER BY generation IS NULL, generation, id"
  );

  const [accounts] = await db.query(
    `SELECT id, person_id, anchor_id, is_primary
       FROM accounts
      WHERE person_id IS NOT NULL
        AND anchor_id IS NOT NULL
        AND anchor_id != ''
      ORDER BY person_id, is_primary DESC, id ASC`
  );
  const accountByPerson = new Map();
  for (const account of accounts) {
    const personId = Number(account.person_id);
    if (!accountByPerson.has(personId)) accountByPerson.set(personId, []);
    accountByPerson.get(personId).push(account);
  }
  const anchorPersonIds = new Set(accounts.map((a) => Number(a.person_id)));
  const personById = new Map(persons.map((p) => [Number(p.id), p]));
  const hiddenIds = new Set();
  const childrenByMaster = new Map();

  for (const p of persons) {
    if (p.master_id == null) continue;
    const masterId = Number(p.master_id);
    if (!childrenByMaster.has(masterId)) childrenByMaster.set(masterId, []);
    childrenByMaster.get(masterId).push(Number(p.id));
  }

  const markHiddenBranch = (rootId) => {
    const stack = [Number(rootId)];
    while (stack.length > 0) {
      const id = stack.pop();
      if (hiddenIds.has(id)) continue;
      hiddenIds.add(id);
      const children = childrenByMaster.get(id) || [];
      stack.push(...children);
    }
  };

  for (const p of persons) {
    const name = String(p.name || "").trim();
    // 仅隐藏占位根「晓0」，其整支不进族谱图
    if (/^晓[0０]$/.test(name)) markHiddenBranch(p.id);
  }

  const visibleIds = new Set();
  for (const p of persons) {
    const id = Number(p.id);
    if (!anchorPersonIds.has(id) || hiddenIds.has(id)) continue;
    visibleIds.add(id);

    let cursor = p.master_id ? Number(p.master_id) : null;
    const visited = new Set([id]);
    while (cursor && personById.has(cursor) && !hiddenIds.has(cursor) && !visited.has(cursor)) {
      visibleIds.add(cursor);
      visited.add(cursor);
      const parent = personById.get(cursor);
      cursor = parent?.master_id ? Number(parent.master_id) : null;
    }
  }

  const visiblePersons = persons.filter((p) => visibleIds.has(Number(p.id)));
  const nameById = new Map(visiblePersons.map((p) => [p.id, p.name]));

  return visiblePersons.map((p) => {
    const personAccounts = accountByPerson.get(Number(p.id)) || [];
    const primary = personAccounts.find((a) => Number(a.is_primary) === 1) || personAccounts[0] || null;
    return {
      id: p.id,
      name: p.name || "",
      gender: p.gender || "",
      generation: p.generation ?? null,
      masterId: p.master_id ?? null,
      masterName: p.master_id ? nameById.get(p.master_id) || null : null,
      anchorId: primary ? primary.anchor_id || "" : "",
      accountCount: personAccounts.length,
      aliasIds: primary
        ? personAccounts.filter((a) => a.id !== primary.id).map((a) => a.anchor_id)
        : personAccounts.map((a) => a.anchor_id),
    };
  });
}

/**
 * 仪表盘统计概览。音浪/时长快照表当前可能为空，COUNT/SUM 会返回 0。
 */
async function getDashboardSummary() {
  const db = getPool();
  const [[personCount]] = await db.query(
    "SELECT COUNT(*) AS c FROM persons"
  );
  const [[accountCount]] = await db.query(
    "SELECT COUNT(*) AS c FROM accounts"
  );
  const [[waveAgg]] = await db.query(
    "SELECT COUNT(*) AS rows_count, COALESCE(SUM(wave_value),0) AS total FROM wave_snapshots"
  );
  const [[durationAgg]] = await db.query(
    "SELECT COUNT(*) AS rows_count FROM duration_snapshots"
  );
  const latestDurationMap = await getLatestDurationMap(db);
  const latestDurationTotal = Array.from(latestDurationMap.values()).reduce((s, v) => s + v, 0);
  const [[latestDataDateRow]] = await db.query(
    `SELECT MAX(import_date) AS latest_date FROM (
       SELECT import_date FROM wave_snapshots
       UNION ALL
       SELECT import_date FROM duration_snapshots
     ) dates`
  );

  const totalAnchors = Number(personCount.c) || 0;
  const totalWave = Number(waveAgg.total) || 0;
  const totalDuration = latestDurationTotal;
  const waveRows = Number(waveAgg.rows_count) || 0;
  const durationRows = Number(durationAgg.rows_count) || 0;
  // DATE 列优先按本地年月日格式化，避免 toISOString 把东八区午夜推前一天
  const normalizeDate = (value) => {
    if (!value) return null;
    if (value instanceof Date) {
      return `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`;
    }
    const text = String(value).trim();
    return text ? text.slice(0, 10) : null;
  };
  const latestDataDate = latestDataDateRow?.latest_date
    ? normalizeDate(latestDataDateRow.latest_date)
    : null;
  let notLiveCount = 0;

  if (latestDataDate) {
    const [liveRows] = await db.query(
      `SELECT p.id AS person_id,
              CASE WHEN COALESCE(SUM(w.wave_value), 0) >= ? THEN 1 ELSE 0 END AS is_live
         FROM persons p
         INNER JOIN accounts a ON a.person_id = p.id
          AND a.anchor_id IS NOT NULL
          AND a.anchor_id != ''
         LEFT JOIN wave_snapshots w ON w.anchor_id = a.anchor_id AND w.import_date = ?
        GROUP BY p.id`,
      [LIVE_WAVE_THRESHOLD, latestDataDate]
    );
    notLiveCount = liveRows.filter((row) => Number(row.is_live) !== 1).length;
  }

  const [[latestWaveRow]] = await db.query(
    "SELECT MAX(import_date) AS latest FROM wave_snapshots"
  );
  const [[latestDurationRow]] = await db.query(
    "SELECT MAX(import_date) AS latest FROM duration_snapshots"
  );
  const latestWaveDate = latestWaveRow?.latest
    ? normalizeDate(latestWaveRow.latest)
    : null;
  const latestDurationDate = latestDurationRow?.latest
    ? normalizeDate(latestDurationRow.latest)
    : null;

  return {
    totalAnchors,
    totalAccounts: Number(accountCount.c) || 0,
    notLiveCount,
    totalWave,
    totalDuration,
    avgWave: totalAnchors ? totalWave / totalAnchors : 0,
    avgDuration: totalAnchors ? totalDuration / totalAnchors : 0,
    dataCount: waveRows + durationRows,
    // 最近有数据的导入日（导出/报告默认日期用）
    latestDataDate,
    latestWaveDate,
    latestDurationDate,
  };
}

function pushHealthCheck(checks, key, label, status, detail) {
  checks.push({ key, label, status, detail });
}

function countStatus(checks) {
  if (checks.some((check) => check.status === "error")) return "error";
  if (checks.some((check) => check.status === "warning")) return "warning";
  return "ok";
}

async function getStartupHealth() {
  const checks = [];
  const counts = {
    persons: 0,
    accounts: 0,
    waveSnapshots: 0,
    durationSnapshots: 0,
    importRecords: 0,
  };

  let db;
  try {
    db = getPool();
    await db.query("SELECT 1");
    pushHealthCheck(checks, "database", "数据库连接", "ok", "连接正常");
  } catch (error) {
    pushHealthCheck(
      checks,
      "database",
      "数据库连接",
      "error",
      error?.message || String(error)
    );
    return { ok: false, status: "error", checks, counts };
  }

  try {
    await ensureImportRecordsTable(db);
    const createdVisibilityColumn = await ensureDailyReportVisibilityColumn(db);
    if (createdVisibilityColumn) {
      pushHealthCheck(checks, "daily-report-visibility", "日报显示字段", "ok", "已补齐日报隐藏字段");
    }
  } catch (error) {
    pushHealthCheck(
      checks,
      "schema-maintenance",
      "数据表维护",
      "warning",
      `数据表维护失败：${error?.message || String(error)}`
    );
  }

  const requiredTables = [
    "persons",
    "accounts",
    "wave_snapshots",
    "duration_snapshots",
    "tier_rules",
    "flag_scores",
    "flag_winners",
    "import_records",
  ];

  let existingTables = new Set();
  try {
    const placeholders = requiredTables.map(() => "?").join(",");
    const [rows] = await db.query(
      `SELECT table_name AS name
         FROM information_schema.tables
        WHERE table_schema = DATABASE()
          AND table_name IN (${placeholders})`,
      requiredTables
    );
    existingTables = new Set(rows.map((row) => row.name));
    const missing = requiredTables.filter((table) => !existingTables.has(table));
    pushHealthCheck(
      checks,
      "tables",
      "关键数据表",
      missing.length > 0 ? "error" : "ok",
      missing.length > 0 ? `缺少表：${missing.join("、")}` : "关键表完整"
    );

    const indexResult = await ensureDatabaseIndexes(db, existingTables);
    pushHealthCheck(
      checks,
      "indexes",
      "数据库索引优化",
      indexResult.failed.length > 0 ? "warning" : "ok",
      indexResult.failed.length > 0
        ? `部分索引优化失败：${indexResult.failed.slice(0, 2).join("；")}`
        : indexResult.created.length > 0
          ? `已补齐索引：${indexResult.created.join("、")}`
          : "关键查询索引完整"
    );
  } catch (error) {
    pushHealthCheck(
      checks,
      "tables",
      "关键数据表",
      "error",
      `表结构检查失败：${error?.message || String(error)}`
    );
  }

  const hasTables = (...tables) => tables.every((table) => existingTables.has(table));

  if (hasTables("persons", "accounts")) {
    const [[personCount]] = await db.query("SELECT COUNT(*) AS c FROM persons");
    const [[accountCount]] = await db.query("SELECT COUNT(*) AS c FROM accounts");
    counts.persons = Number(personCount.c) || 0;
    counts.accounts = Number(accountCount.c) || 0;
    pushHealthCheck(
      checks,
      "anchor-counts",
      "主播账号数据",
      counts.persons > 0 && counts.accounts > 0 ? "ok" : "warning",
      `主播 ${counts.persons} 人，账号 ${counts.accounts} 个`
    );

    const [[orphanAccounts]] = await db.query(
      `SELECT COUNT(*) AS c
         FROM accounts a
         LEFT JOIN persons p ON p.id = a.person_id
        WHERE a.person_id IS NOT NULL
          AND p.id IS NULL`
    );
    const [[orphanMasters]] = await db.query(
      `SELECT COUNT(*) AS c
         FROM persons p
         LEFT JOIN persons m ON m.id = p.master_id
        WHERE p.master_id IS NOT NULL
          AND m.id IS NULL`
    );
    const orphanAccountCount = Number(orphanAccounts.c) || 0;
    const orphanMasterCount = Number(orphanMasters.c) || 0;
    pushHealthCheck(
      checks,
      "relations",
      "人员关系完整性",
      orphanAccountCount > 0 || orphanMasterCount > 0 ? "error" : "ok",
      orphanAccountCount > 0 || orphanMasterCount > 0
        ? `孤儿账号 ${orphanAccountCount} 个，失效师傅关系 ${orphanMasterCount} 个`
        : "账号和师傅关系正常"
    );

    const [[duplicatePrimary]] = await db.query(
      `SELECT COUNT(*) AS c
         FROM (
           SELECT person_id
             FROM accounts
            WHERE person_id IS NOT NULL
              AND is_primary = 1
            GROUP BY person_id
           HAVING COUNT(*) > 1
         ) t`
    );
    const [[duplicateAnchorIds]] = await db.query(
      `SELECT COUNT(*) AS c
         FROM (
           SELECT anchor_id
             FROM accounts
            WHERE anchor_id IS NOT NULL
              AND anchor_id != ''
            GROUP BY anchor_id
           HAVING COUNT(*) > 1
         ) t`
    );
    const duplicatePrimaryCount = Number(duplicatePrimary.c) || 0;
    const duplicateAnchorIdCount = Number(duplicateAnchorIds.c) || 0;
    pushHealthCheck(
      checks,
      "duplicates",
      "重复账号检查",
      duplicatePrimaryCount > 0 || duplicateAnchorIdCount > 0 ? "warning" : "ok",
      duplicatePrimaryCount > 0 || duplicateAnchorIdCount > 0
        ? `重复主账号 ${duplicatePrimaryCount} 组，重复主播 ID ${duplicateAnchorIdCount} 组`
        : "未发现重复主账号"
    );
  }

  if (hasTables("wave_snapshots", "duration_snapshots")) {
    const [[waveCount]] = await db.query("SELECT COUNT(*) AS c FROM wave_snapshots");
    const [[durationCount]] = await db.query("SELECT COUNT(*) AS c FROM duration_snapshots");
    counts.waveSnapshots = Number(waveCount.c) || 0;
    counts.durationSnapshots = Number(durationCount.c) || 0;
  }

  if (hasTables("wave_snapshots", "duration_snapshots", "accounts")) {
    const [[unmatchedWave]] = await db.query(
      `SELECT COUNT(DISTINCT w.anchor_id) AS c
         FROM wave_snapshots w
         LEFT JOIN accounts a ON a.anchor_id = w.anchor_id
        WHERE a.id IS NULL`
    );
    const [[unmatchedDuration]] = await db.query(
      `SELECT COUNT(DISTINCT d.anchor_id) AS c
         FROM duration_snapshots d
         LEFT JOIN accounts a ON a.anchor_id = d.anchor_id
        WHERE a.id IS NULL`
    );
    const unmatchedWaveCount = Number(unmatchedWave.c) || 0;
    const unmatchedDurationCount = Number(unmatchedDuration.c) || 0;
    pushHealthCheck(
      checks,
      "unmatched-snapshots",
      "未匹配导入数据",
      unmatchedWaveCount > 0 || unmatchedDurationCount > 0 ? "warning" : "ok",
      unmatchedWaveCount > 0 || unmatchedDurationCount > 0
        ? `未匹配音浪账号 ${unmatchedWaveCount} 个，未匹配时长账号 ${unmatchedDurationCount} 个`
        : "导入数据均能匹配主播列表"
    );
  }

  if (hasTables("wave_snapshots", "duration_snapshots")) {
    const [[latestWave]] = await db.query("SELECT MAX(import_date) AS latest FROM wave_snapshots");
    const [[latestDuration]] = await db.query(
      "SELECT MAX(import_date) AS latest FROM duration_snapshots"
    );
    const latestWaveDate = latestWave.latest ? String(latestWave.latest).split("T")[0] : "无";
    const latestDurationDate = latestDuration.latest
      ? String(latestDuration.latest).split("T")[0]
      : "无";
    pushHealthCheck(
      checks,
      "latest-data",
      "最新数据日期",
      latestWave.latest || latestDuration.latest ? "ok" : "warning",
      `音浪 ${latestWaveDate}，时长 ${latestDurationDate}`
    );
  }

  if (hasTables("import_records")) {
    const [[recordCount]] = await db.query("SELECT COUNT(*) AS c FROM import_records");
    counts.importRecords = Number(recordCount.c) || 0;
  }

  const status = countStatus(checks);
  return { ok: status !== "error", status, checks, counts };
}

/**
 * 音浪榜：按累计音浪排名，附带所属家族（师父名）、累计时长、趋势。
 * 趋势 = 最近两期音浪导入日的对比。快照为空时返回 []。
 */
async function getWaveRanking(limit = 10) {
  const db = getPool();

  // 累计音浪排名 + 家族（师父名）—— 按人汇总其名下所有账号（含合并副号）
  const [rows] = await db.query(
    `SELECT p.id AS person_id,
            COALESCE(p.name, MIN(a.anchor_name)) AS name,
            m.name AS family,
            GROUP_CONCAT(DISTINCT a.anchor_id) AS anchor_ids,
            COALESCE(SUM(w.wave_value),0) AS wave
       FROM wave_snapshots w
       JOIN accounts a ON a.anchor_id = w.anchor_id
       LEFT JOIN persons p ON p.id = a.person_id
       LEFT JOIN persons m ON m.id = p.master_id
      GROUP BY p.id, p.name, m.name
      ORDER BY wave DESC
      LIMIT ?`,
    [limit]
  );
  if (rows.length === 0) return [];

  // 累计时长：取每个账号最新一次时长快照（导入值本身已是累计）
  const durMap = await getLatestDurationMap(db);

  // 趋势：最近两个音浪导入日的当日音浪对比
  const [dates] = await db.query(
    "SELECT DISTINCT import_date FROM wave_snapshots ORDER BY import_date DESC LIMIT 2"
  );
  let latestMap = new Map();
  let prevMap = new Map();
  if (dates[0]) {
    const [r] = await db.query(
      "SELECT anchor_id, SUM(wave_value) AS w FROM wave_snapshots WHERE import_date = ? GROUP BY anchor_id",
      [dates[0].import_date]
    );
    latestMap = new Map(r.map((x) => [x.anchor_id, Number(x.w) || 0]));
  }
  if (dates[1]) {
    const [r] = await db.query(
      "SELECT anchor_id, SUM(wave_value) AS w FROM wave_snapshots WHERE import_date = ? GROUP BY anchor_id",
      [dates[1].import_date]
    );
    prevMap = new Map(r.map((x) => [x.anchor_id, Number(x.w) || 0]));
  }

  return rows.map((r, i) => {
    // 该人名下所有账号（含合并副号）的 anchor_id 列表
    const anchorIds = r.anchor_ids ? String(r.anchor_ids).split(",") : [];
    // 汇总最近两期当日音浪与累计时长
    let latest = null;
    let prev = null;
    let duration = 0;
    for (const aid of anchorIds) {
      if (latestMap.has(aid)) latest = (latest || 0) + latestMap.get(aid);
      if (prevMap.has(aid)) prev = (prev || 0) + prevMap.get(aid);
      duration = Math.max(duration, durMap.get(aid) || 0);
    }
    let trend = "flat";
    if (latest != null && prev != null) {
      trend = latest > prev ? "up" : latest < prev ? "down" : "flat";
    }
    return {
      rank: i + 1,
      name: r.name || "",
      anchorId: anchorIds[0] || "",
      family: r.family || "",
      wave: Number(r.wave) || 0,
      duration,
      trend,
    };
  });
}

/**
 * 按性别的音浪趋势：每个导入日的当日音浪总和，分男/女两条序列。
 * 返回 { male: [{date, total}], female: [{date, total}] }
 * 快照为空时两条都为 []。
 */
async function getWaveTrendByGender() {
  const db = getPool();
  const [rows] = await db.query(
    `SELECT w.import_date AS date,
            p.gender AS gender,
            COALESCE(SUM(w.wave_value),0) AS total
       FROM wave_snapshots w
       JOIN accounts a ON a.anchor_id = w.anchor_id
       LEFT JOIN persons p ON p.id = a.person_id
      GROUP BY w.import_date, p.gender
      ORDER BY w.import_date ASC`
  );

  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];

  const male = [];
  const female = [];
  for (const r of rows) {
    const point = { date: fmt(r.date), total: Number(r.total) || 0 };
    if (r.gender === "male") male.push(point);
    else if (r.gender === "female") female.push(point);
  }
  return { male, female };
}

/**
 * 批量导入音浪快照（按 anchor_id + import_date UPSERT，可重复导入覆盖）。
 * rows: [{ anchorId, waveValue, rank }]
 */
async function importWaveSnapshots(importDate, rows, meta, info = null) {
  return importSnapshotRows(getPool(), "wave", importDate, rows, meta, info);
}

/**
 * 批量导入时长快照（同上 UPSERT）。
 * rows: [{ anchorId, totalMinutes }]
 */
async function importDurationSnapshots(importDate, rows, meta, info = null) {
  return importSnapshotRows(getPool(), "duration", importDate, rows, meta, info);
}

/**
 * 导入日志：最近 N 条导入记录（含来源与匹配统计），供网页「导入日志」页与机器人「导入记录」指令使用。
 */
async function listImportLogs(limit = 50) {
  const safeLimit = Math.min(Math.max(Number(limit) || 50, 1), 200);
  const db = getPool();
  await ensureImportRecordsTable(db);
  const [rows] = await db.query(
    `SELECT id, kind, import_date, file_name, row_count,
            source, matched_count, unmatched_count, duplicate_rows, created_at
       FROM import_records
      ORDER BY id DESC
      LIMIT ${safeLimit}`
  );
  return (rows || []).map((r) => ({
    id: Number(r.id) || 0,
    kind: String(r.kind || ""),
    importDate: r.import_date || null,
    fileName: String(r.file_name || ""),
    rowCount: Number(r.row_count) || 0,
    source: String(r.source || "bot"),
    matchedCount: Number(r.matched_count) || 0,
    unmatchedCount: Number(r.unmatched_count) || 0,
    duplicateRows: Number(r.duplicate_rows) || 0,
    createdAt: r.created_at || null,
  }));
}

async function getImportPreview(kind, importDate, anchorIds, meta) {
  const db = getPool();
  if (kind === "duration") importDate = normalizeDurationImportDate(importDate);
  const ids = Array.from(
    new Set((Array.isArray(anchorIds) ? anchorIds : []).map((id) => String(id || "").trim()).filter(Boolean))
  );
  const importMeta = normalizeImportMeta(meta);
  let duplicateFile = null;
  let duplicateData = null;

  if (importMeta) {
    await ensureImportRecordsTable(db);
    const [records] = await db.query(
      `SELECT file_hash, data_hash, file_name, row_count, created_at
         FROM import_records
        WHERE kind = ? AND import_date = ? AND (file_hash = ? OR data_hash = ?)
        ORDER BY created_at DESC
        LIMIT 2`,
      [kind, importDate, importMeta.fileHash, importMeta.dataHash]
    );
    for (const record of records) {
      const item = {
        fileName: record.file_name || "",
        rowCount: Number(record.row_count) || 0,
        createdAt: record.created_at || null,
      };
      if (record.file_hash === importMeta.fileHash) duplicateFile = item;
      if (record.data_hash === importMeta.dataHash) duplicateData = item;
    }
  }

  if (ids.length === 0) return { existing: [], duplicateFile, duplicateData };

  const ph = ids.map(() => "?").join(",");
  if (kind === "wave") {
    const [rows] = await db.query(
      `SELECT anchor_id, wave_value AS value, \`rank\`
         FROM wave_snapshots
        WHERE import_date = ? AND anchor_id IN (${ph})`,
      [importDate, ...ids]
    );
    return {
      existing: rows.map((r) => ({
        anchorId: r.anchor_id,
        value: Number(r.value) || 0,
        rank: Number(r.rank) || 0,
      })),
      duplicateFile,
      duplicateData,
    };
  }

  const [rows] = await db.query(
    `SELECT d.anchor_id, d.total_minutes AS value
       FROM duration_snapshots d
       INNER JOIN (
         SELECT anchor_id, MAX(import_date) AS latest_date
           FROM duration_snapshots
          WHERE import_date BETWEEN ? AND ? AND anchor_id IN (${ph})
          GROUP BY anchor_id
       ) latest
         ON latest.anchor_id = d.anchor_id
        AND latest.latest_date = d.import_date`,
    [monthStart(importDate), importDate, ...ids]
  );
  return {
    existing: rows.map((r) => ({
      anchorId: r.anchor_id,
      value: Number(r.value) || 0,
      rank: 0,
    })),
    duplicateFile,
    duplicateData,
  };
}

/**
 * 导出音浪快照（关联主播名），用于导出 CSV。可选按日期过滤。
 */
async function exportWaveSnapshots(importDate) {
  const db = getPool();
  const where = importDate ? "WHERE w.import_date = ?" : "";
  const params = importDate ? [importDate] : [];
  const [rows] = await db.query(
    `SELECT w.anchor_id, COALESCE(a.anchor_name, p.name) AS name,
            w.import_date, w.wave_value, w.\`rank\`
       FROM wave_snapshots w
       LEFT JOIN accounts a ON a.anchor_id = w.anchor_id
       LEFT JOIN persons p ON p.id = a.person_id
       ${where}
      ORDER BY w.import_date DESC, w.wave_value DESC`,
    params
  );
  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  return rows.map((r) => ({
    抖音号: r.anchor_id,
    昵称: r.name || "",
    日期: fmt(r.import_date),
    音浪: Number(r.wave_value) || 0,
    排名: Number(r.rank) || 0,
  }));
}

/**
 * 按账号集合汇总区间内音浪（含边界）。
 * 用于 bot 的「年音浪」等聚合查询：anchorIds 传主播主账号 + 合并副号。
 */
async function sumWaveSnapshotsForAccounts(anchorIds, fromDate, toDate) {
  const db = getPool();
  const ids = (Array.isArray(anchorIds) ? anchorIds : [])
    .map((value) => String(value || "").trim())
    .filter(Boolean);
  if (!ids.length) return 0;
  const from = normalizeSnapshotDate(fromDate);
  const to = normalizeSnapshotDate(toDate);
  const placeholders = ids.map(() => "?").join(",");
  const conditions = [`anchor_id IN (${placeholders})`];
  const params = [...ids];
  if (from) {
    conditions.push("import_date >= ?");
    params.push(from);
  }
  if (to) {
    conditions.push("import_date <= ?");
    params.push(to);
  }
  const [rows] = await db.query(
    `SELECT COALESCE(SUM(wave_value), 0) AS total FROM wave_snapshots WHERE ${conditions.join(" AND ")}`,
    params
  );
  return Number(rows?.[0]?.total) || 0;
}

/**
 * 导出时长快照（关联主播名）。
 * 时长导入值是累计分钟；传 date 时导出“截至该日”的累计时长（取 <= date 最近一次快照）。
 * 不传 date 时导出每个账号最新累计时长。
 */
async function exportDurationSnapshots(importDate) {
  const db = getPool();
  const asOfDate = normalizeSnapshotDate(importDate);
  const where = asOfDate ? "WHERE import_date <= ?" : "";
  const params = asOfDate ? [asOfDate] : [];
  const [rows] = await db.query(
    `SELECT d.anchor_id,
            COALESCE(a.anchor_name, p.name) AS name,
            d.import_date,
            d.total_minutes
       FROM duration_snapshots d
       INNER JOIN (
         SELECT anchor_id, MAX(import_date) AS latest_date
           FROM duration_snapshots
           ${where}
          GROUP BY anchor_id
       ) latest
         ON latest.anchor_id = d.anchor_id
        AND latest.latest_date = d.import_date
       LEFT JOIN accounts a ON a.anchor_id = d.anchor_id
       LEFT JOIN persons p ON p.id = a.person_id
      ORDER BY d.total_minutes DESC, d.anchor_id ASC`,
    params
  );
  const exportDate = asOfDate || null;
  return rows.map((r) => ({
    抖音号: r.anchor_id,
    昵称: r.name || "",
    日期: exportDate || normalizeSnapshotDate(r.import_date) || "",
    快照日期: normalizeSnapshotDate(r.import_date) || "",
    时长分钟: Number(r.total_minutes) || 0,
  }));
}

/**
 * 导出主播档案（含族谱信息）。
 */
async function exportAnchors() {
  const list = await getAnchors();
  return list.map((a) => ({
    主播: a.name,
    性别: a.gender === "male" ? "男" : a.gender === "female" ? "女" : "",
    日报显示: a.hideInDailyReport ? "不显示" : "显示",
    代数: a.generation ?? "",
    抖音ID: a.anchorId,
    抖音号: a.douyinNo,
    账号数: a.accountCount,
  }));
}

/**
 * 添加主播：同时创建 person 和主账号。
 */
async function addAnchor({ name, gender, anchorId, anchorName, douyinNo }) {
  if (!name || !anchorId) throw new Error("主播姓名和抖音ID不能为空");
  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    const [pRes] = await conn.query(
      "INSERT INTO persons (name, gender, master_id, generation, created_at, updated_at) VALUES (?, ?, NULL, NULL, NOW(), NOW())",
      [name, gender || ""]
    );
    const personId = pRes.insertId;
    await conn.query(
      "INSERT INTO accounts (person_id, anchor_id, anchor_name, douyin_no, is_primary, created_at) VALUES (?, ?, ?, ?, 1, NOW())",
      [personId, anchorId, anchorName || name, douyinNo || ""]
    );
    await conn.commit();
    return { id: personId };
  } catch (err) {
    await conn.rollback();
    if (err.code === "ER_DUP_ENTRY") throw new Error("抖音ID已存在");
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 批量导入主播：从 CSV 解析出的主播列表批量创建 person + account。
 * 已存在的 anchor_id 自动跳过，返回创建/跳过计数。
 */
async function batchImportAnchors(rows) {
  if (!Array.isArray(rows) || rows.length === 0) return { created: 0, skipped: 0 };
  const db = getPool();
  const conn = await db.getConnection();
  let created = 0;
  let skipped = 0;
  try {
    await conn.beginTransaction();
    for (const row of rows) {
      const anchorId = String(row.anchorId ?? "").trim();
      const name = String(row.name ?? "").trim();
      const douyinNo = String(row.douyinNo ?? "").trim();
      if (!anchorId || !name) {
        skipped++;
        continue;
      }
      await conn.query("SAVEPOINT batch_import_anchor_row");
      try {
        const [pRes] = await conn.query(
          "INSERT INTO persons (name, gender, master_id, generation, created_at, updated_at) VALUES (?, ?, NULL, NULL, NOW(), NOW())",
          [name, row.gender || ""]
        );
        await conn.query(
          "INSERT INTO accounts (person_id, anchor_id, anchor_name, douyin_no, is_primary, created_at) VALUES (?, ?, ?, ?, 1, NOW())",
          [pRes.insertId, anchorId, name, douyinNo]
        );
        await conn.query("RELEASE SAVEPOINT batch_import_anchor_row");
        created++;
      } catch (err) {
        await conn.query("ROLLBACK TO SAVEPOINT batch_import_anchor_row").catch(() => {});
        await conn.query("RELEASE SAVEPOINT batch_import_anchor_row").catch(() => {});
        if (err.code === "ER_DUP_ENTRY") {
          skipped++;
        } else {
          throw err;
        }
      }
    }
    await conn.commit();
    return { created, skipped };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 合并账号：把 secondary 主播的所有账号迁移到 primary 主播名下，并删除 secondary 的 person 记录。
 * 音浪始终随账号归属合并（按人汇总全部账号）。
 * mergeDuration=true 时保留副号时长；false 时删除被合并方账号的时长快照。
 * 展示时长默认取名下“时长最多”的那个账号，而不是多账号相加。
 */
async function mergeAccounts({ primaryPersonId, secondaryPersonId, mergeDuration = false }) {
  if (!primaryPersonId || !secondaryPersonId) throw new Error("请选择要合并的两个主播");
  if (Number(primaryPersonId) === Number(secondaryPersonId)) throw new Error("不能合并同一个主播");
  const shouldMergeDuration = Boolean(mergeDuration);
  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    const [[primaryPerson]] = await conn.query(
      "SELECT id, master_id FROM persons WHERE id = ? LIMIT 1",
      [primaryPersonId]
    );
    const [[secondaryPerson]] = await conn.query(
      "SELECT id, master_id FROM persons WHERE id = ? LIMIT 1",
      [secondaryPersonId]
    );
    if (!primaryPerson) throw new Error("保留的主播不存在");
    if (!secondaryPerson) throw new Error("被合并的主播不存在");

    const [primaryAccounts] = await conn.query(
      "SELECT id FROM accounts WHERE person_id = ? ORDER BY is_primary DESC, id ASC",
      [primaryPersonId]
    );
    if (primaryAccounts.length === 0) throw new Error("保留的主播没有账号");
    const primaryAccountId = primaryAccounts[0].id;

    const [secAccounts] = await conn.query(
      "SELECT id, anchor_id FROM accounts WHERE person_id = ?",
      [secondaryPersonId]
    );
    if (secAccounts.length === 0) throw new Error("被合并的主播没有账号");

    // 账号合并默认只合并音浪归属；时长可选。
    if (!shouldMergeDuration) {
      const secAnchorIds = secAccounts
        .map((acc) => String(acc.anchor_id || "").trim())
        .filter(Boolean);
      if (secAnchorIds.length > 0) {
        const ph = secAnchorIds.map(() => "?").join(",");
        await conn.query(
          `DELETE FROM duration_snapshots WHERE anchor_id IN (${ph})`,
          secAnchorIds
        );
      }
    }

    for (const acc of secAccounts) {
      await conn.query(
        "UPDATE accounts SET person_id = ?, is_primary = 0 WHERE id = ?",
        [primaryPersonId, acc.id]
      );
    }

    // 如果 A 原本认 B 为师，删除 B 后让 A 继承 B 的师傅，避免 A.master_id 指向自己。
    const primaryMasterId =
      Number(primaryPerson.master_id) === Number(secondaryPersonId)
        ? Number(secondaryPerson.master_id) === Number(primaryPersonId)
          ? null
          : secondaryPerson.master_id ?? null
        : primaryPerson.master_id ?? null;
    await conn.query(
      "UPDATE persons SET master_id = ? WHERE id = ?",
      [primaryMasterId, primaryPersonId]
    );

    // 把「认 B 为师傅」的其他徒弟重定向到 A（防止悬空 master_id）。
    await conn.query(
      "UPDATE persons SET master_id = ? WHERE master_id = ? AND id <> ?",
      [primaryPersonId, secondaryPersonId, primaryPersonId]
    );

    // 强制合并后只有一个主号，并优先保留 A 原来的主号。
    await conn.query(
      "UPDATE accounts SET is_primary = CASE WHEN id = ? THEN 1 ELSE 0 END WHERE person_id = ?",
      [primaryAccountId, primaryPersonId]
    );

    await conn.query("DELETE FROM persons WHERE id = ?", [secondaryPersonId]);
    await conn.commit();
    return {
      moved: secAccounts.length,
      mergeDuration: shouldMergeDuration,
    };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/** 一人多号时，时长默认取“时长最多”的账号，而不是相加。 */
function maxDurationAmongAnchors(anchorIds, durationMap) {
  let max = 0;
  for (const aid of anchorIds) {
    const value = Number(durationMap.get(aid) || 0) || 0;
    if (value > max) max = value;
  }
  return max;
}

function normalizeSnapshotDate(value) {
  if (!value) return null;
  if (value instanceof Date) return value.toISOString().slice(0, 10);
  const text = String(value).trim();
  return text ? text.slice(0, 10) : null;
}

/**
 * 时长快照语义：导入值是“截至 import_date 的累计分钟”，不是日增量。
 * 因此任意区间/截止日的累计时长，都取该账号在范围内/截止日前最近一次快照。
 */
async function getLatestDurationMap(db, options = {}) {
  const { anchorIds = null, asOfDate = null, fromDate = null } = options;
  const where = [];
  const params = [];

  if (Array.isArray(anchorIds)) {
    if (anchorIds.length === 0) return new Map();
    where.push(`anchor_id IN (${anchorIds.map(() => "?").join(",")})`);
    params.push(...anchorIds);
  }
  if (fromDate) {
    where.push("import_date >= ?");
    params.push(fromDate);
  }
  if (asOfDate) {
    where.push("import_date <= ?");
    params.push(asOfDate);
  }

  const whereSql = where.length ? `WHERE ${where.join(" AND ")}` : "";
  const [rows] = await db.query(
    `SELECT d.anchor_id, d.import_date, d.total_minutes
       FROM duration_snapshots d
       INNER JOIN (
         SELECT anchor_id, MAX(import_date) AS latest_date
           FROM duration_snapshots
           ${whereSql}
          GROUP BY anchor_id
       ) latest
         ON latest.anchor_id = d.anchor_id
        AND latest.latest_date = d.import_date`,
    params
  );

  const map = new Map();
  for (const row of rows) {
    map.set(row.anchor_id, Number(row.total_minutes) || 0);
  }
  return map;
}

function shiftIsoDate(dateStr, dayDelta) {
  const raw = normalizeSnapshotDate(dateStr);
  if (!raw) return null;
  const [y, m, d] = raw.split("-").map(Number);
  if (!y || !m || !d) return null;
  const utc = Date.UTC(y, m - 1, d) + dayDelta * 24 * 60 * 60 * 1000;
  return new Date(utc).toISOString().slice(0, 10);
}

/**
 * 由累计快照推导“当日新增时长”：
 * asOf(date) - asOf(date-1)，结果下限为 0。
 * 无新导入时差值为 0；跨多日才导入时，增量会记在导入日。
 */
async function getDurationDeltaMap(db, options = {}) {
  const { anchorIds = null, asOfDate } = options;
  const date = normalizeSnapshotDate(asOfDate);
  if (!date) return new Map();
  const prevDate = shiftIsoDate(date, -1);
  const currentMap = await getLatestDurationMap(db, { anchorIds, asOfDate: date });
  const previousMap = prevDate
    ? await getLatestDurationMap(db, { anchorIds, asOfDate: prevDate })
    : new Map();
  const deltaMap = new Map();
  for (const [anchorId, current] of currentMap) {
    const previous = Number(previousMap.get(anchorId) || 0) || 0;
    deltaMap.set(anchorId, Math.max(0, (Number(current) || 0) - previous));
  }
  return deltaMap;
}

/**
 * 批量删除主播：删除 persons + 关联 accounts + wave/duration 快照。
 * personIds: number[]
 */
async function deleteAnchors(personIds) {
  if (!Array.isArray(personIds) || personIds.length === 0)
    return { deleted: 0 };
  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    const ph = personIds.map(() => "?").join(",");

    // 查出这些 person 关联的所有 anchor_id
    const [accRows] = await conn.query(
      `SELECT DISTINCT anchor_id FROM accounts WHERE person_id IN (${ph}) AND anchor_id IS NOT NULL AND anchor_id != ''`,
      personIds
    );
    const anchorIds = accRows.map((r) => r.anchor_id);

    // 删除快照
    if (anchorIds.length > 0) {
      const aPh = anchorIds.map(() => "?").join(",");
      await conn.query(
        `DELETE FROM wave_snapshots WHERE anchor_id IN (${aPh})`,
        anchorIds
      );
      await conn.query(
        `DELETE FROM duration_snapshots WHERE anchor_id IN (${aPh})`,
        anchorIds
      );
    }

    // 把被删主播的徒弟重定向到其师傅（防止悬空 master_id）
    // 查出每个被删人的 master_id（用于重定向其徒弟）
    const delIdSet = new Set(personIds.map((x) => Number(x)));
    const [delPersons] = await conn.query(
      `SELECT id, master_id FROM persons WHERE id IN (${ph})`,
      personIds
    );
    for (const p of delPersons) {
      // 若师傅也在被删列表中，则该徒弟不宜再认一个即将消失的人为师，直接置空。
      // 无师傅的人同理清空其徒弟的 master_id。
      const newMaster =
        p.master_id && !delIdSet.has(Number(p.master_id)) ? p.master_id : null;
      await conn.query(
        `UPDATE persons SET master_id = ? WHERE master_id = ?`,
        [newMaster, p.id]
      );
    }

    // 兜底清洗：重定向后若仍有人指向已删除的人或指向自己（互为师徒等环形数据），一律置空，
    // 避免 getFamilyTree 解析出悬空或自指的师父。
    await conn.query(
      `UPDATE persons SET master_id = NULL WHERE master_id IN (${ph})`,
      personIds
    );
    await conn.query(
      `UPDATE persons SET master_id = NULL WHERE master_id = id`
    );

    // 删除 accounts
    await conn.query(
      `DELETE FROM accounts WHERE person_id IN (${ph})`,
      personIds
    );
    // 删除 persons
    const [res] = await conn.query(
      `DELETE FROM persons WHERE id IN (${ph})`,
      personIds
    );
    await conn.commit();
    return { deleted: res.affectedRows };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 查找重复主播：按姓名（不区分大小写）分组，返回有重复的组。
 * 每组包含同名的人员列表（含账号信息），供用户选择合并或删除。
 */
async function findDuplicateAnchors() {
  const db = getPool();
  // 按小写姓名分组，找出 >1 的
  const [persons] = await db.query(
    `SELECT id, name, gender, master_id, generation, created_at
       FROM persons
      ORDER BY name ASC, id ASC`
  );
  const [accounts] = await db.query(
    `SELECT id, person_id, anchor_id, douyin_no, anchor_name, is_primary
       FROM accounts ORDER BY person_id, id`
  );
  const accByPerson = new Map();
  for (const a of accounts) {
    if (!accByPerson.has(a.person_id)) accByPerson.set(a.person_id, []);
    accByPerson.get(a.person_id).push(a);
  }

  // 按小写名分组
  const groups = new Map();
  for (const p of persons) {
    const key = (p.name || "").trim().toLowerCase();
    if (!key) continue;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(p);
  }

  const duplicates = [];
  for (const [, list] of groups) {
    if (list.length < 2) continue;
    duplicates.push({
      name: list[0].name,
      count: list.length,
      persons: list.map((p) => {
        const accs = accByPerson.get(p.id) || [];
        const primary = accs.find((a) => a.is_primary === 1) || accs[0];
        return {
          id: p.id,
          name: p.name,
          gender: p.gender || "",
          generation: p.generation ?? null,
          masterId: p.master_id ?? null,
          createdAt: p.created_at || null,
          anchorId: primary ? primary.anchor_id || "" : "",
          douyinNo: primary ? primary.douyin_no || "" : "",
          accountCount: accs.length,
        };
      }),
    });
  }
  // 按人数降序
  duplicates.sort((a, b) => b.count - a.count);
  return duplicates;
}

/**
 * 总音浪趋势：每个导入日的全部音浪总和（不分性别）。
 */
async function getWaveTrendTotal() {
  const db = getPool();
  const [rows] = await db.query(
    `SELECT w.import_date AS date, COALESCE(SUM(w.wave_value),0) AS total
       FROM wave_snapshots w
      GROUP BY w.import_date
      ORDER BY w.import_date ASC`
  );
  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  return rows.map((r) => ({ date: fmt(r.date), total: Number(r.total) || 0 }));
}

/**
 * 主播人数趋势：每个导入日有音浪数据的主播数（去重）。
 */
async function getAnchorCountTrend() {
  const db = getPool();
  const [rows] = await db.query(
    `SELECT w.import_date AS date, COUNT(DISTINCT w.anchor_id) AS cnt
       FROM wave_snapshots w
      GROUP BY w.import_date
      ORDER BY w.import_date ASC`
  );
  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  return rows.map((r) => ({ date: fmt(r.date), total: Number(r.cnt) || 0 }));
}

/**
 * 更新主播姓名（同时更新 persons.name 和 accounts.anchor_name）。
 */
async function updateAnchorName({ personId, name }) {
  if (!personId || !name) throw new Error("参数不完整");
  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    await conn.query(
      "UPDATE persons SET name = ?, updated_at = NOW() WHERE id = ?",
      [name.trim(), personId]
    );
    await conn.query(
      "UPDATE accounts SET anchor_name = ? WHERE person_id = ? AND is_primary = 1",
      [name.trim(), personId]
    );
    await conn.commit();
    return { ok: true };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 更新主播基础信息：姓名、性别、主账号抖音ID、抖音号。
 */
async function updateAnchorInfo({ personId, name, gender, anchorId, douyinNo, hideInDailyReport }) {
  const id = Number(personId);
  const cleanName = String(name || "").trim();
  const cleanGender = String(gender || "").trim();
  const cleanAnchorId = String(anchorId || "").trim();
  const cleanDouyinNo = String(douyinNo || "").trim();
  const hideInReport = hideInDailyReport === true || hideInDailyReport === 1 || hideInDailyReport === "1";
  if (!id || !cleanName || !cleanAnchorId) throw new Error("主播姓名和抖音ID不能为空");
  if (cleanGender && !["male", "female"].includes(cleanGender)) throw new Error("性别参数不正确");

  const db = getPool();
  await ensureDailyReportVisibilityColumn(db);
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    const [persons] = await conn.query("SELECT id FROM persons WHERE id = ? FOR UPDATE", [id]);
    if (persons.length === 0) throw new Error("主播不存在");

    const [dup] = await conn.query(
      "SELECT person_id FROM accounts WHERE anchor_id = ? AND person_id <> ? LIMIT 1",
      [cleanAnchorId, id]
    );
    if (dup.length > 0) throw new Error("抖音ID已被其他主播使用");

    await conn.query(
      "UPDATE persons SET name = ?, gender = ?, hide_in_daily_report = ?, updated_at = NOW() WHERE id = ?",
      [cleanName, cleanGender, hideInReport ? 1 : 0, id]
    );

    const [primaryRows] = await conn.query(
      "SELECT id, anchor_id FROM accounts WHERE person_id = ? ORDER BY is_primary DESC, id ASC LIMIT 1",
      [id]
    );
    if (primaryRows.length === 0) {
      await conn.query(
        "INSERT INTO accounts (person_id, anchor_id, anchor_name, douyin_no, is_primary, created_at) VALUES (?, ?, ?, ?, 1, NOW())",
        [id, cleanAnchorId, cleanName, cleanDouyinNo]
      );
    } else {
      const oldAnchorId = String(primaryRows[0].anchor_id || "").trim();
      if (oldAnchorId && oldAnchorId !== cleanAnchorId) {
        await conn.query(
          `INSERT INTO wave_snapshots (anchor_id, import_date, wave_value, \`rank\`)
           SELECT ?, import_date, wave_value, \`rank\` FROM wave_snapshots WHERE anchor_id = ?
           ON DUPLICATE KEY UPDATE wave_value = VALUES(wave_value), \`rank\` = VALUES(\`rank\`)`,
          [cleanAnchorId, oldAnchorId]
        );
        await conn.query("DELETE FROM wave_snapshots WHERE anchor_id = ?", [oldAnchorId]);
        await conn.query(
          `INSERT INTO duration_snapshots (anchor_id, import_date, total_minutes)
           SELECT ?, import_date, total_minutes FROM duration_snapshots WHERE anchor_id = ?
           ON DUPLICATE KEY UPDATE total_minutes = VALUES(total_minutes)`,
          [cleanAnchorId, oldAnchorId]
        );
        await conn.query("DELETE FROM duration_snapshots WHERE anchor_id = ?", [oldAnchorId]);
      }
      await conn.query(
        "UPDATE accounts SET anchor_id = ?, anchor_name = ?, douyin_no = ?, is_primary = 1 WHERE id = ?",
        [cleanAnchorId, cleanName, cleanDouyinNo, primaryRows[0].id]
      );
    }
    await conn.query(
      "UPDATE accounts SET anchor_name = ? WHERE person_id = ? AND is_primary <> 1",
      [cleanName, id]
    );

    await conn.commit();
    return { ok: true };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 设置师傅。传 null 可清空师傅；拒绝自指和循环关系。
 */
async function updateAnchorMaster({ personId, masterId }) {
  const id = Number(personId);
  const nextMasterId = masterId == null || masterId === "" ? null : Number(masterId);
  if (!id) throw new Error("主播参数不完整");
  if (nextMasterId && nextMasterId === id) throw new Error("不能把自己设置为师傅");

  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    const [persons] = await conn.query("SELECT id, master_id, generation FROM persons");
    const byId = new Map(persons.map((p) => [Number(p.id), p]));
    if (!byId.has(id)) throw new Error("主播不存在");
    if (nextMasterId && !byId.has(nextMasterId)) throw new Error("师傅不存在");

    if (nextMasterId) {
      let cursor = nextMasterId;
      const visited = new Set();
      while (cursor) {
        if (cursor === id) throw new Error("不能设置循环师徒关系");
        if (visited.has(cursor)) break;
        visited.add(cursor);
        cursor = byId.get(cursor)?.master_id ? Number(byId.get(cursor).master_id) : null;
      }
    }

    const masterGeneration = nextMasterId ? byId.get(nextMasterId)?.generation : null;
    const generation = Number.isFinite(Number(masterGeneration)) ? Number(masterGeneration) + 1 : null;
    await conn.query(
      "UPDATE persons SET master_id = ?, generation = ?, updated_at = NOW() WHERE id = ?",
      [nextMasterId, generation, id]
    );
    await conn.commit();
    return { ok: true };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

async function getAnchorDailySnapshot(anchorId, importDate) {
  const cleanAnchorId = String(anchorId || "").trim();
  const cleanDate = String(importDate || "").trim();
  if (!cleanAnchorId || !cleanDate) throw new Error("主播ID和日期不能为空");
  const db = getPool();
  const [[wave]] = await db.query(
    "SELECT wave_value, `rank` FROM wave_snapshots WHERE anchor_id = ? AND import_date = ? LIMIT 1",
    [cleanAnchorId, cleanDate]
  );
  const [[duration]] = await db.query(
    "SELECT total_minutes FROM duration_snapshots WHERE anchor_id = ? AND import_date = ? LIMIT 1",
    [cleanAnchorId, cleanDate]
  );
  return {
    anchorId: cleanAnchorId,
    date: cleanDate,
    waveValue: wave ? Number(wave.wave_value) || 0 : null,
    rank: wave ? Number(wave.rank) || 0 : null,
    totalMinutes: duration ? Number(duration.total_minutes) || 0 : null,
  };
}

async function saveAnchorDailySnapshot({ anchorId, date, waveValue, rank, totalMinutes }) {
  const cleanAnchorId = String(anchorId || "").trim();
  const cleanDate = String(date || "").trim();
  if (!cleanAnchorId || !cleanDate) throw new Error("主播ID和日期不能为空");

  const hasWave = waveValue !== null && waveValue !== undefined && String(waveValue).trim() !== "";
  const hasDuration = totalMinutes !== null && totalMinutes !== undefined && String(totalMinutes).trim() !== "";
  if (!hasWave && !hasDuration) throw new Error("请至少填写音浪或时长");

  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    if (hasWave) {
      const wave = Math.max(0, Math.round(Number(waveValue) || 0));
      const cleanRank = Math.max(0, Math.round(Number(rank) || 0));
      await conn.query(
        `INSERT INTO wave_snapshots (anchor_id, import_date, wave_value, \`rank\`)
         VALUES (?, ?, ?, ?)
         ON DUPLICATE KEY UPDATE wave_value = VALUES(wave_value), \`rank\` = VALUES(\`rank\`)`,
        [cleanAnchorId, cleanDate, wave, cleanRank]
      );
    }
    if (hasDuration) {
      const minutes = Math.max(0, Math.round(Number(totalMinutes) || 0));
      await conn.query(
        `INSERT INTO duration_snapshots (anchor_id, import_date, total_minutes)
         VALUES (?, ?, ?)
         ON DUPLICATE KEY UPDATE total_minutes = VALUES(total_minutes)`,
        [cleanAnchorId, cleanDate, minutes]
      );
    }
    await conn.commit();
    return { ok: true };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 单个主播音浪趋势：按 anchor_id 查询每个导入日的音浪值。
 * anchorId 可选，不传则返回空。
 */
async function getAnchorWaveTrend(anchorId) {
  if (!anchorId) return [];
  const db = getPool();
  const [rows] = await db.query(
    `SELECT import_date AS date, wave_value AS total, \`rank\`
       FROM wave_snapshots
      WHERE anchor_id = ?
      ORDER BY import_date ASC`,
    [anchorId]
  );
  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  return rows.map((r) => ({
    date: fmt(r.date),
    total: Number(r.total) || 0,
    rank: Number(r.rank) || 0,
  }));
}

/**
 * 单个主播时长趋势：按账号查询每个导入日的累计时长（分钟）。
 */
async function getAnchorDurationTrend(anchorId) {
  if (!anchorId) return [];
  const db = getPool();
  const [rows] = await db.query(
    `SELECT import_date AS date, total_minutes AS total
       FROM duration_snapshots
      WHERE anchor_id = ?
      ORDER BY import_date ASC`,
    [String(anchorId)]
  );
  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  return rows.map((r) => ({
    date: fmt(r.date),
    total: Number(r.total) || 0,
  }));
}

/**
 * 多个主播音浪趋势：按 anchor_id 列表查询每个导入日每个主播的音浪值。
 * 返回 [{ anchorId, name, data: [{date, total, rank}] }]
 */
async function getAnchorsWaveTrend(anchorIds) {
  if (!anchorIds || anchorIds.length === 0) return [];
  const db = getPool();
  const placeholders = anchorIds.map(() => "?").join(",");
  const [rows] = await db.query(
    `SELECT w.anchor_id, COALESCE(a.anchor_name, p.name) AS name,
            w.import_date AS date, w.wave_value AS total, w.\`rank\`
       FROM wave_snapshots w
       LEFT JOIN accounts a ON a.anchor_id = w.anchor_id
       LEFT JOIN persons p ON p.id = a.person_id
      WHERE w.anchor_id IN (${placeholders})
      ORDER BY w.import_date ASC`,
    anchorIds
  );
  const fmt = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  const map = new Map();
  for (const r of rows) {
    const key = r.anchor_id;
    if (!map.has(key)) {
      map.set(key, { anchorId: key, name: r.name || key, data: [] });
    }
    map.get(key).data.push({
      date: fmt(r.date),
      total: Number(r.total) || 0,
      rank: Number(r.rank) || 0,
    });
  }
  return Array.from(map.values());
}

/**
 * 流动红旗：给定一个 personId（师傅），返回他 + 他所有徒弟的最新音浪和时长快照平均值。
 * 返回 { master: {id,name}, members: [{id,name,anchorId,wave,duration}], avgWave, avgDuration, count }
 */
async function getFlowingFlag(personId) {
  const db = getPool();

  // 1. 查师傅本人 + 他所有徒弟（仅男团，gender = 'male'）
  const [persons] = await db.query(
    `SELECT id, name, gender FROM persons
      WHERE (id = ? OR master_id = ?) AND gender = 'male'
      ORDER BY (id = ?) DESC, id ASC`,
    [personId, personId, personId]
  );

  if (!persons || persons.length === 0) {
    return {
      master: null,
      members: [],
      avgWave: 0,
      avgDuration: 0,
      count: 0,
    };
  }

  // 2. 查这些人对应的 accounts（每人名下全部账号，含合并副号）
  const personIds = persons.map((p) => p.id);
  const placeholders = personIds.map(() => "?").join(",");
  const [accounts] = await db.query(
    `SELECT id, person_id, anchor_id, anchor_name FROM accounts
      WHERE person_id IN (${placeholders})
        AND anchor_id IS NOT NULL AND anchor_id != ''`,
    personIds
  );

  const anchorsByPerson = new Map(); // personId -> [anchorId, ...]
  const primaryAnchorByPerson = new Map(); // personId -> 展示用主 anchorId
  for (const a of accounts) {
    if (!anchorsByPerson.has(a.person_id)) anchorsByPerson.set(a.person_id, []);
    anchorsByPerson.get(a.person_id).push(a.anchor_id);
    if (!primaryAnchorByPerson.has(a.person_id)) {
      primaryAnchorByPerson.set(a.person_id, a.anchor_id);
    }
  }

  // 3. 查最新一批音浪/时长快照（按人名下所有 anchor 汇总到该人）
  const anchorIds = [];
  for (const ids of anchorsByPerson.values()) anchorIds.push(...ids);
  let waveMap = new Map(); // anchorId -> wave_value
  let durationMap = new Map(); // anchorId -> duration_minutes

  if (anchorIds.length > 0) {
    const aPlaceholders = anchorIds.map(() => "?").join(",");

    // 最新导入日期
    const [latestDate] = await db.query(
      `SELECT MAX(import_date) AS latest FROM wave_snapshots WHERE anchor_id IN (${aPlaceholders})`,
      anchorIds
    );
    const latestWaveDate = latestDate[0]?.latest;

    if (latestWaveDate) {
      const [waveRows] = await db.query(
        `SELECT anchor_id, wave_value FROM wave_snapshots
          WHERE anchor_id IN (${aPlaceholders}) AND import_date = ?`,
        [...anchorIds, latestWaveDate]
      );
      for (const r of waveRows) {
        waveMap.set(r.anchor_id, Number(r.wave_value) || 0);
      }
    }

    // 最新时长快照
    const [latestDurDate] = await db.query(
      `SELECT MAX(import_date) AS latest FROM duration_snapshots WHERE anchor_id IN (${aPlaceholders})`,
      anchorIds
    );
    const latestDurDateRaw = latestDurDate[0]?.latest;

    if (latestDurDateRaw) {
      const [durRows] = await db.query(
        `SELECT anchor_id, total_minutes FROM duration_snapshots
          WHERE anchor_id IN (${aPlaceholders}) AND import_date = ?`,
        [...anchorIds, latestDurDateRaw]
      );
      for (const r of durRows) {
        durationMap.set(r.anchor_id, Number(r.total_minutes) || 0);
      }
    }
  }

  // 4. 组装成员数据（音浪按账号求和；时长取名下最多的账号）
  const members = persons.map((p) => {
    const ids = anchorsByPerson.get(p.id) || [];
    let wave = 0;
    let duration = 0;
    for (const aid of ids) {
      wave += waveMap.get(aid) ?? 0;
    }
    // 时长默认展示名下最多的那个账号
    duration = maxDurationAmongAnchors(ids, durationMap);
    return {
      id: p.id,
      name: p.name,
      gender: p.gender,
      anchorId: primaryAnchorByPerson.get(p.id) || "",
      wave,
      duration,
    };
  });

  const count = members.length;
  const totalWave = members.reduce((s, m) => s + m.wave, 0);
  const totalDuration = members.reduce((s, m) => s + m.duration, 0);

  return {
    master: { id: personId, name: persons[0]?.name || "" },
    members,
    avgWave: count > 0 ? totalWave / count : 0,
    avgDuration: count > 0 ? totalDuration / count : 0,
    count,
  };
}

/**
 * 结算指定月份各组流动红旗分数。
 * period 格式 'YYYY-MM'。对每个男性师傅分组，取该月所有批次快照的音浪/时长累计，
 * 计算各组人均月度音浪(avg_wave)与人均月度时长(avg_duration)。
 * 评分：分数 = 人均音浪/100 + 人均时长(分钟)/60，不归一化、不四舍五入。
 * 时长按小时计入（正音官方月度标准28小时≀28分）。
 * 整个过程在一个事务内保证原子性；DDL 已收敛到 init-db.js，运行时只做 DML。
 */
async function settleFlagScores(period) {
  if (!/^\d{4}-\d{2}$/.test(period)) {
    throw new Error("period 格式应为 YYYY-MM");
  }
  const db = getPool();
  const conn = await db.getConnection();

  try {
    await conn.beginTransaction();

    // 1. 日期范围
    const [y, m] = period.split("-").map(Number);
    const monthStart = period + "-01";
    const monthEnd = new Date(y, m, 0).toISOString().slice(0, 10);

    // 2. 找出所有有弟弟的男性师傅
    const [allPersons] = await conn.query(
      "SELECT id, name, master_id FROM persons WHERE gender = 'male' ORDER BY id"
    );
    const masterIdSet = new Set();
    for (const p of allPersons) {
      if (p.master_id) masterIdSet.add(p.master_id);
    }
    const masters = allPersons.filter((p) => masterIdSet.has(p.id));

    // 3. 一次性查出所有师傅组的成员（师傅本人 + 男弟弟）
    const masterIdList = masters.map((m) => m.id);
    const groupMemberMap = new Map();
    if (masterIdList.length > 0) {
      const mPh = masterIdList.map(() => "?").join(",");
      const [memberRows] = await conn.query(
        "SELECT id, master_id FROM persons" +
        " WHERE (master_id IN (" + mPh + ") OR id IN (" + mPh + ")) AND gender = 'male'" +
        " ORDER BY id",
        [...masterIdList, ...masterIdList]
      );
      for (const row of memberRows) {
        const key = row.master_id ?? row.id;
        if (!groupMemberMap.has(key)) groupMemberMap.set(key, []);
        groupMemberMap.get(key).push(row.id);
      }
    }

    // 4. 一次性查出所有组所有主播当月音浪/时长累计
    const waveSumMap = new Map();
    const durSumMap  = new Map();
    const personAnchorMap = new Map();

    if (masterIdList.length > 0) {
      const allMemberIds = [];
      for (const ids of groupMemberMap.values()) allMemberIds.push(...ids);
      if (allMemberIds.length > 0) {
        const aPh = allMemberIds.map(() => "?").join(",");
        const [accRows] = await conn.query(
          "SELECT person_id, anchor_id FROM accounts" +
          " WHERE person_id IN (" + aPh + ") AND anchor_id IS NOT NULL AND anchor_id != ''",
          allMemberIds
        );
        const allAnchorIds = [];
        for (const r of accRows) {
          allAnchorIds.push(r.anchor_id);
          if (!personAnchorMap.has(r.person_id)) personAnchorMap.set(r.person_id, []);
          personAnchorMap.get(r.person_id).push(r.anchor_id);
        }
        if (allAnchorIds.length > 0) {
          const awPh = allAnchorIds.map(() => "?").join(",");
          const [waveRows] = await conn.query(
            "SELECT anchor_id, COALESCE(SUM(wave_value),0) AS w FROM wave_snapshots" +
            " WHERE anchor_id IN (" + awPh + ") AND import_date BETWEEN ? AND ? GROUP BY anchor_id",
            [...allAnchorIds, monthStart, monthEnd]
          );
          for (const r of waveRows) waveSumMap.set(r.anchor_id, Number(r.w) || 0);
          const latestDurMap = await getLatestDurationMap(conn, {
            anchorIds: allAnchorIds,
            fromDate: monthStart,
            asOfDate: monthEnd,
          });
          for (const [anchorId, minutes] of latestDurMap) durSumMap.set(anchorId, minutes);
        }
      }
    }

    // 5. 在内存中组装各组评分结果
    const results = [];
    for (const master of masters) {
      const memberIds = groupMemberMap.get(master.id) || [];
      const denom = memberIds.length;
      if (denom === 0) continue;
      let waveSum = 0, durSum = 0;
      for (const pid of memberIds) {
        const aids = personAnchorMap.get(pid) || [];
        let personWave = 0;
        for (const aid of aids) {
          personWave += waveSumMap.get(aid) ?? 0;
        }
        // 时长按人取“时长最多”的账号，避免副号把组均时长抬高
        waveSum += personWave;
        durSum += maxDurationAmongAnchors(aids, durSumMap);
      }
      const avgWave     = waveSum / denom;
      const avgDuration = durSum  / denom;
      const score       = avgWave / 100 + avgDuration / 60;
      results.push({ masterId: master.id, masterName: master.name, memberCount: denom, avgWave, avgDuration, score });
    }

    // 6. 写入 flag_scores 和 flag_winners（事务内，批量插入）
    await conn.query("DELETE FROM flag_scores WHERE period = ?", [period]);
    await conn.query("DELETE FROM flag_winners WHERE period = ?", [period]);

    let firstWinner = null;
    if (results.length > 0) {
      const now = new Date();
      // 确定冠军：score 最高者。score 为浮点，用容差比较避免严格相等失配；
      // 同分时的 tie-break 明确规则（人均音浪高者 → masterId 小者），
      // 不再依赖 results 的查询顺序，保证结果可复现。
      const EPS = 1e-9;
      const better = (a, b) => {
        if (Math.abs(a.score - b.score) > EPS) return a.score > b.score;
        if (Math.abs(a.avgWave - b.avgWave) > EPS) return a.avgWave > b.avgWave;
        return a.masterId < b.masterId;
      };
      for (const r of results) {
        if (r.score > EPS && (!firstWinner || better(r, firstWinner))) {
          firstWinner = r;
        }
      }
      // 批量构建 flag_scores 插入
      const insertValues = [];
      const insertParams = [];
      for (const r of results) {
        const isWinner = firstWinner != null && r.masterId === firstWinner.masterId;
        insertValues.push("(?, ?, ?, ?, ?, ?, ?, ?)");
        insertParams.push(r.masterId, period, r.score, r.avgWave, r.avgDuration, r.memberCount, isWinner ? 1 : 0, now);
      }
      await conn.query(
        "INSERT INTO flag_scores (master_id, period, score, avg_wave, avg_duration, member_count, is_winner, settled_at) VALUES " + insertValues.join(", "),
        insertParams
      );
      // 写入冠军到 flag_winners
      if (firstWinner) {
        await conn.query(
          "INSERT INTO flag_winners (period, master_id, master_name, score, avg_wave, avg_duration, member_count, settled_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
          [period, firstWinner.masterId, firstWinner.masterName, firstWinner.score,
           firstWinner.avgWave, firstWinner.avgDuration, firstWinner.memberCount, now]
        );
      }
    }

    await conn.commit();
    return { period, groups: results.length, winner: firstWinner ? { masterId: firstWinner.masterId, masterName: firstWinner.masterName, score: firstWinner.score } : null };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 查询指定月份的流动红旗分组评分。
 */
async function getFlagGroups(period) {
  const db = getPool();
  const [rows] = await db.query(
    "SELECT fs.master_id, fs.score, fs.avg_wave, fs.avg_duration, fs.member_count, fs.is_winner, p.name AS master_name " +
    "FROM flag_scores fs LEFT JOIN persons p ON p.id = fs.master_id " +
    "WHERE fs.period = ? ORDER BY fs.score DESC",
    [period]
  );
  return rows.map(r => ({
    masterId: r.master_id,
    masterName: r.master_name || "",
    memberCount: r.member_count,
    score: Number(r.score) || 0,
    avgWave: Number(r.avg_wave) || 0,
    avgDuration: Number(r.avg_duration) || 0,
    isWinner: !!r.is_winner,
  }));
}

/**
 * 查询等级规则，按 sort_order 排序。
 */
async function getTierRules() {
  const db = getPool();
  const [rows] = await db.query("SELECT id, label, min_wave, sort_order FROM tier_rules ORDER BY sort_order");
  return rows.map(r => ({
    id: r.id,
    label: r.label,
    minWave: Number(r.min_wave) || 0,
    sortOrder: r.sort_order,
  }));
}

/**
 * 保存等级规则（全量替换）。在事务内先 DELETE 再批量 INSERT。
 */
async function saveTierRules(rules) {
  if (!Array.isArray(rules) || rules.length === 0) {
    throw new Error("等级规则不能为空");
  }
  const sanitizedRules = rules.map((rule, index) => {
    const label = String(rule?.label ?? "").trim();
    const minWave = Number(rule?.minWave);
    if (!label) throw new Error(`第 ${index + 1} 条等级规则缺少名称`);
    if (!Number.isFinite(minWave) || minWave < 0) {
      throw new Error(`第 ${index + 1} 条等级规则音浪门槛无效`);
    }
    return { label, minWave };
  });

  const db = getPool();
  const conn = await db.getConnection();
  try {
    await conn.beginTransaction();
    await conn.query("DELETE FROM tier_rules");
    const values = sanitizedRules.map(() => "(?, ?, ?)");
    const params = sanitizedRules.flatMap((r, i) => [r.label, r.minWave, i + 1]);
    await conn.query(
      "INSERT INTO tier_rules (label, min_wave, sort_order) VALUES " + values.join(", "),
      params
    );
    await conn.commit();
    return { saved: sanitizedRules.length };
  } catch (err) {
    await conn.rollback();
    throw err;
  } finally {
    conn.release();
  }
}

/**
 * 查询指定月份的流动红旗得主。
 */
async function getFlagWinner(period) {
  const db = getPool();
  const [rows] = await db.query(
    "SELECT period, master_id, master_name, score, avg_wave, avg_duration, member_count, settled_at " +
    "FROM flag_winners WHERE period = ?",
    [period]
  );
  if (rows.length === 0) return null;
  const r = rows[0];
  return {
    period: r.period,
    masterId: r.master_id,
    masterName: r.master_name || "",
    score: Number(r.score) || 0,
    avgWave: Number(r.avg_wave) || 0,
    avgDuration: Number(r.avg_duration) || 0,
    memberCount: r.member_count,
    settledAt: r.settled_at || null,
  };
}

/**
 * 生成每日音浪报告。
 * date 格式 'YYYY-MM-DD'，gender 为 'male' 或 'female'。
 * 返回当日音浪/时长、月累计音浪/时长、等级、是否开播。
 */
async function getDailyWaveReport(date, gender) {
  const db = getPool();
  await ensureDailyReportVisibilityColumn(db);
  // 1. 获取指定性别的主播 + 其名下所有账号 anchor（含合并副号），同时获取 master_id 用于查师傅名字
  const [personRows] = await db.query(
    "SELECT p.id AS person_id, p.name, p.master_id, a.anchor_id " +
    "FROM persons p " +
    "INNER JOIN accounts a ON a.person_id = p.id " +
    "WHERE p.gender = ? AND a.anchor_id IS NOT NULL AND a.anchor_id != '' " +
    "AND COALESCE(p.hide_in_daily_report, 0) = 0 " +
    "ORDER BY p.id",
    [gender]
  );

  // 按人归并其名下所有 anchor
  const personMap = new Map(); // personId -> { person_id, name, master_id, anchorIds: [] }
  for (const r of personRows) {
    if (!personMap.has(r.person_id)) {
      personMap.set(r.person_id, {
        person_id: r.person_id,
        name: r.name,
        master_id: r.master_id,
        anchorIds: [],
      });
    }
    personMap.get(r.person_id).anchorIds.push(r.anchor_id);
  }
  const persons = Array.from(personMap.values());

  // 1b. 获取所有人员名字映射，用于解析师傅名字
  const [allPersons] = await db.query("SELECT id, name FROM persons");
  const nameById = new Map(allPersons.map((p) => [p.id, p.name]));
  if (persons.length === 0) {
    return { date, gender, rows: [], summary: { total: 0, notLiveCount: 0, notLiveDays: 0, notLiveNames: [] } };
  }

  const anchorIds = [];
  for (const p of persons) anchorIds.push(...p.anchorIds);
  const ph = anchorIds.map(() => "?").join(",");

  // 2. 获取等级规则（按 min_wave 降序，便于匹配）
  const [tierRows] = await db.query(
    "SELECT label, min_wave FROM tier_rules ORDER BY min_wave DESC"
  );

  // 3. 当日音浪
  const [dailyWaves] = await db.query(
    "SELECT anchor_id, wave_value FROM wave_snapshots WHERE anchor_id IN (" + ph + ") AND import_date = ?",
    [...anchorIds, date]
  );
  const dailyWaveMap = new Map();
  for (const r of dailyWaves) dailyWaveMap.set(r.anchor_id, Number(r.wave_value) || 0);

  // 4. 月累计音浪（当月1日至指定日期）
  const [y, m] = date.split("-").map(Number);
  const monthStart = y + "-" + String(m).padStart(2, "0") + "-01";
  const [totalWaves] = await db.query(
    "SELECT anchor_id, COALESCE(SUM(wave_value), 0) AS total FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ? GROUP BY anchor_id",
    [...anchorIds, monthStart, date]
  );
  const totalWaveMap = new Map();
  for (const r of totalWaves) totalWaveMap.set(r.anchor_id, Number(r.total) || 0);

  // 5. 当日时长：累计快照差值 asOf(date) - asOf(date-1)
  const dailyDurMap = await getDurationDeltaMap(db, {
    anchorIds,
    asOfDate: date,
  });

  // 6. 月累计时长：取当月范围内最近一次累计快照（导入值本身已是累计）
  const totalDurMap = await getLatestDurationMap(db, {
    anchorIds,
    fromDate: monthStart,
    asOfDate: date,
  });

  const [monthlyWaveDates] = await db.query(
    "SELECT anchor_id, import_date, wave_value FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ?",
    [...anchorIds, monthStart, date]
  );
  const liveDateSet = new Set();
  const liveDatesByPerson = new Map();
  const personIdByAnchor = new Map();
  for (const p of persons) {
    for (const aid of p.anchorIds) personIdByAnchor.set(aid, p.person_id);
  }
  const normalizeDate = (value) =>
    value instanceof Date ? value.toISOString().slice(0, 10) : String(value).slice(0, 10);
  const waveByPersonDate = new Map();
  for (const row of monthlyWaveDates) {
    const personId = personIdByAnchor.get(row.anchor_id);
    if (!personId) continue;
    const key = `${personId}:${normalizeDate(row.import_date)}`;
    waveByPersonDate.set(key, (waveByPersonDate.get(key) || 0) + (Number(row.wave_value) || 0));
  }
  for (const [key, wave] of waveByPersonDate) {
    if (wave < LIVE_WAVE_THRESHOLD) continue;
    const [personIdRaw, dateKey] = key.split(":");
    const personId = Number(personIdRaw);
    liveDateSet.add(dateKey);
    const dates = liveDatesByPerson.get(personId) ?? new Set();
    dates.add(dateKey);
    liveDatesByPerson.set(personId, dates);
  }
  const dateToUtc = (value) => {
    const [yy, mm, dd] = String(value).split("-").map(Number);
    return Date.UTC(yy, mm - 1, dd);
  };
  let notLiveDays = 0;
  const reportDates = [];
  for (let cursor = dateToUtc(monthStart), end = dateToUtc(date); cursor <= end; cursor += 24 * 60 * 60 * 1000) {
    const key = new Date(cursor).toISOString().slice(0, 10);
    reportDates.push(key);
    if (!liveDateSet.has(key)) notLiveDays++;
  }

  // 7. 查找同队当前日期之前最近一个有音浪快照的日期，用于计算排名变化。
  const [prevDateRows] = await db.query(
    "SELECT MAX(import_date) AS prev_date FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date < ?",
    [...anchorIds, date]
  );
  const prevDateRaw = prevDateRows[0]?.prev_date;
  const prevDate = prevDateRaw
    ? (prevDateRaw instanceof Date ? prevDateRaw.toISOString().slice(0, 10) : String(prevDateRaw).slice(0, 10))
    : null;
  const previousRankByPerson = new Map();

  if (prevDate) {
    const [py, pm] = prevDate.split("-").map(Number);
    const prevMonthStart = py + "-" + String(pm).padStart(2, "0") + "-01";
    const [prevTotalWaves] = await db.query(
      "SELECT anchor_id, COALESCE(SUM(wave_value), 0) AS total FROM wave_snapshots " +
      "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ? GROUP BY anchor_id",
      [...anchorIds, prevMonthStart, prevDate]
    );
    const prevTotalWaveMap = new Map();
    for (const r of prevTotalWaves) prevTotalWaveMap.set(r.anchor_id, Number(r.total) || 0);

    const prevRows = persons.map((p) => {
      let totalWave = 0;
      for (const aid of p.anchorIds) totalWave += prevTotalWaveMap.get(aid) || 0;
      return { personId: p.person_id, totalWave };
    });
    prevRows.sort((a, b) => b.totalWave - a.totalWave || a.personId - b.personId);
    for (let i = 0; i < prevRows.length; i++) {
      previousRankByPerson.set(prevRows[i].personId, i + 1);
    }
  }

  // 8. 组装行数据（每人指标 = 其名下所有 anchor 之和）
  const rows = persons.map(p => {
    let dw = 0, tw = 0, dd = 0, td = 0;
    for (const aid of p.anchorIds) {
      dw += dailyWaveMap.get(aid) || 0;
      tw += totalWaveMap.get(aid) || 0;
    }
    // 时长默认展示名下最多的那个账号
    dd = maxDurationAmongAnchors(p.anchorIds, dailyDurMap);
    td = maxDurationAmongAnchors(p.anchorIds, totalDurMap);
    const isLive = dw >= LIVE_WAVE_THRESHOLD;
    const personLiveDates = liveDatesByPerson.get(p.person_id) ?? new Set();
    const personNotLiveDays = reportDates.filter((key) => !personLiveDates.has(key)).length;
    let tier = "";
    for (const tr of tierRows) {
      if (tw >= Number(tr.min_wave)) { tier = tr.label; break; }
    }
    return {
      _personId: p.person_id,
      rank: 0,
      previousRank: previousRankByPerson.get(p.person_id) || null,
      rankDelta: null,
      name: p.name,
      anchorId: p.anchorIds[0] || "",
      dailyWave: dw,
      totalWave: tw,
      dailyDuration: dd,
      totalDuration: td,
      notLiveDays: personNotLiveDays,
      tier,
      isLive,
      masterName: p.master_id ? nameById.get(p.master_id) || null : null,
    };
  });

  rows.sort((a, b) =>
    b.totalWave - a.totalWave ||
    b.dailyWave - a.dailyWave ||
    b.totalDuration - a.totalDuration ||
    b.dailyDuration - a.dailyDuration ||
    a._personId - b._personId
  );

  const tierCounts = new Map();
  for (const row of rows) {
    const baseTier = String(row.tier || "").trim();
    if (!baseTier || /\d+$/.test(baseTier)) continue;
    tierCounts.set(baseTier, (tierCounts.get(baseTier) || 0) + 1);
  }
  const tierSeq = new Map();
  for (const row of rows) {
    const baseTier = String(row.tier || "").trim();
    if (!baseTier || /\d+$/.test(baseTier) || (tierCounts.get(baseTier) || 0) <= 1) continue;
    const next = (tierSeq.get(baseTier) || 0) + 1;
    tierSeq.set(baseTier, next);
    row.tier = `${baseTier}${next}`;
  }

  for (let i = 0; i < rows.length; i++) {
    rows[i].rank = i + 1;
    rows[i].rankDelta = rows[i].previousRank ? rows[i].previousRank - rows[i].rank : null;
    delete rows[i]._personId;
  }

  const notLive = rows.filter(r => !r.isLive);
  return {
    date, gender, rows,
    summary: { total: rows.length, notLiveCount: notLive.length, notLiveDays, notLiveNames: notLive.map(r => r.name), previousDate: prevDate },
  };
}

/**
 * 生成月度报告：整月维度（当月音浪 + 当月时长 + 当月未播天数）。
 * month 格式 'YYYY-MM'，gender 为 'male' 或 'female'。
 * 时长取该月内最近一次累计快照（月导入落在月末，即整月时长）；
 * 未播天数 = 当月天数 - 该月有音浪(>= 阈值)的天数。
 */
async function getMonthlyReport(month, gender) {
  const db = getPool();
  await ensureDailyReportVisibilityColumn(db);
  const monthText = String(month || "").trim();
  if (!/^\d{4}-\d{2}$/.test(monthText)) throw new Error(`无效的报告月份: ${monthText}`);
  const [y, m] = monthText.split("-").map(Number);
  if (m < 1 || m > 12) throw new Error(`无效的报告月份: ${monthText}`);
  const monthStart = `${monthText}-01`;
  const lastDay = new Date(Date.UTC(y, m, 0)).getUTCDate();
  const monthEnd = `${monthText}-${String(lastDay).padStart(2, "0")}`;
  const daysInMonth = lastDay;
  const genderAll = gender === "all";
  const genderSql = genderAll ? "" : "p.gender = ? AND ";
  const genderParams = genderAll ? [] : [gender];

  // 1. 获取指定性别的主播 + 其名下所有账号 anchor（含合并副号）
  const [personRows] = await db.query(
    "SELECT p.id AS person_id, p.name, p.master_id, a.anchor_id " +
    "FROM persons p " +
    "INNER JOIN accounts a ON a.person_id = p.id " +
    "WHERE " + genderSql + "a.anchor_id IS NOT NULL AND a.anchor_id != '' " +
    "AND COALESCE(p.hide_in_daily_report, 0) = 0 " +
    "ORDER BY p.id",
    genderParams
  );
  const personMap = new Map();
  for (const r of personRows) {
    if (!personMap.has(r.person_id)) {
      personMap.set(r.person_id, {
        person_id: r.person_id,
        name: r.name,
        master_id: r.master_id,
        anchorIds: [],
      });
    }
    personMap.get(r.person_id).anchorIds.push(r.anchor_id);
  }
  const persons = Array.from(personMap.values());
  const [allPersons] = await db.query("SELECT id, name FROM persons");
  const nameById = new Map(allPersons.map((p) => [p.id, p.name]));
  if (persons.length === 0) {
    return {
      month: monthText,
      gender,
      rows: [],
      summary: { total: 0, notLiveCount: 0, notLiveDays: 0, notLiveNames: [], daysInMonth },
    };
  }

  const anchorIds = [];
  for (const p of persons) anchorIds.push(...p.anchorIds);
  const ph = anchorIds.map(() => "?").join(",");

  // 2. 等级规则
  const [tierRows] = await db.query(
    "SELECT label, min_wave FROM tier_rules ORDER BY min_wave DESC"
  );

  // 3. 当月音浪（1 号 ~ 月末）
  const [monthlyWaves] = await db.query(
    "SELECT anchor_id, COALESCE(SUM(wave_value), 0) AS total FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ? GROUP BY anchor_id",
    [...anchorIds, monthStart, monthEnd]
  );
  const totalWaveMap = new Map();
  for (const r of monthlyWaves) totalWaveMap.set(r.anchor_id, Number(r.total) || 0);

  // 4. 当月时长：该月范围内最近一次累计快照（月导入落在月末 = 整月时长）
  const totalDurMap = await getLatestDurationMap(db, {
    anchorIds,
    fromDate: monthStart,
    asOfDate: monthEnd,
  });

  // 5. 开播天数：按人按日聚合该月音浪，>= 阈值记一天
  const [monthlyWaveDates] = await db.query(
    "SELECT anchor_id, import_date, wave_value FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ?",
    [...anchorIds, monthStart, monthEnd]
  );
  const personIdByAnchor = new Map();
  for (const p of persons) {
    for (const aid of p.anchorIds) personIdByAnchor.set(aid, p.person_id);
  }
  const normalizeDate = (value) =>
    value instanceof Date ? value.toISOString().slice(0, 10) : String(value).slice(0, 10);
  const waveByPersonDate = new Map();
  for (const row of monthlyWaveDates) {
    const personId = personIdByAnchor.get(row.anchor_id);
    if (!personId) continue;
    const key = `${personId}:${normalizeDate(row.import_date)}`;
    waveByPersonDate.set(key, (waveByPersonDate.get(key) || 0) + (Number(row.wave_value) || 0));
  }
  const liveDateSet = new Set();
  const liveDatesByPerson = new Map();
  for (const [key, wave] of waveByPersonDate) {
    if (wave < LIVE_WAVE_THRESHOLD) continue;
    const [personIdRaw, dateKey] = key.split(":");
    const personId = Number(personIdRaw);
    liveDateSet.add(dateKey);
    const dates = liveDatesByPerson.get(personId) ?? new Set();
    dates.add(dateKey);
    liveDatesByPerson.set(personId, dates);
  }
  const dateToUtc = (value) => {
    const [yy, mm, dd] = String(value).split("-").map(Number);
    return Date.UTC(yy, mm - 1, dd);
  };
  let notLiveDays = 0;
  const reportDates = [];
  for (let cursor = dateToUtc(monthStart), end = dateToUtc(monthEnd); cursor <= end; cursor += 24 * 60 * 60 * 1000) {
    const key = new Date(cursor).toISOString().slice(0, 10);
    reportDates.push(key);
    if (!liveDateSet.has(key)) notLiveDays++;
  }

  // 6. 组装行（每人 = 其名下所有 anchor 之和；时长取名下最多的账号）
  const rows = persons.map((p) => {
    let tw = 0;
    for (const aid of p.anchorIds) tw += totalWaveMap.get(aid) || 0;
    const td = maxDurationAmongAnchors(p.anchorIds, totalDurMap);
    const personLiveDates = liveDatesByPerson.get(p.person_id) ?? new Set();
    const personNotLiveDays = reportDates.filter((key) => !personLiveDates.has(key)).length;
    let tier = "";
    for (const tr of tierRows) {
      if (tw >= Number(tr.min_wave)) { tier = tr.label; break; }
    }
    return {
      _personId: p.person_id,
      rank: 0,
      previousRank: null,
      rankDelta: null,
      name: p.name,
      anchorId: p.anchorIds[0] || "",
      dailyWave: 0,
      totalWave: tw,
      dailyDuration: 0,
      totalDuration: td,
      notLiveDays: personNotLiveDays,
      tier,
      isLive: personLiveDates.size > 0,
      masterName: p.master_id ? nameById.get(p.master_id) || null : null,
    };
  });

  rows.sort((a, b) =>
    b.totalWave - a.totalWave ||
    b.totalDuration - a.totalDuration ||
    a._personId - b._personId
  );

  const tierCounts = new Map();
  for (const row of rows) {
    const baseTier = String(row.tier || "").trim();
    if (!baseTier || /\d+$/.test(baseTier)) continue;
    tierCounts.set(baseTier, (tierCounts.get(baseTier) || 0) + 1);
  }
  const tierSeq = new Map();
  for (const row of rows) {
    const baseTier = String(row.tier || "").trim();
    if (!baseTier || /\d+$/.test(baseTier) || (tierCounts.get(baseTier) || 0) <= 1) continue;
    const next = (tierSeq.get(baseTier) || 0) + 1;
    tierSeq.set(baseTier, next);
    row.tier = `${baseTier}${next}`;
  }

  for (let i = 0; i < rows.length; i++) {
    rows[i].rank = i + 1;
    delete rows[i]._personId;
  }

  const notLive = rows.filter((r) => !r.isLive);
  return {
    month: monthText,
    gender,
    rows,
    summary: {
      total: rows.length,
      notLiveCount: notLive.length,
      notLiveDays,
      notLiveNames: notLive.map((r) => r.name),
      daysInMonth,
    },
  };
}

/**
 * 生成 PK 名单数据。
 * period 格式 'YYYY-MM'。计算每位主播当月去最高日均音浪（trimmed mean），
 * 按总音浪降序返回男女分组列表。
 */
async function getPkRoster(period, groupSize) {
  void groupSize;
  // period 为空时默认当前月份
  if (!period) {
    const now = new Date();
    period = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
  }
  if (!/^\d{4}-\d{2}$/.test(period)) {
    throw new Error("period 格式应为 YYYY-MM");
  }
  const db = getPool();
  const [y, m] = period.split("-").map(Number);
  const monthStart = period + "-01";
  const monthEnd = new Date(y, m, 0).toISOString().slice(0, 10);

  // 1. 获取所有主播 + 其名下所有账号 anchor（含合并副号）
  const [personRows] = await db.query(
    "SELECT p.id AS person_id, p.name, p.gender, a.anchor_id, a.douyin_no " +
    "FROM persons p " +
    "INNER JOIN accounts a ON a.person_id = p.id " +
    "WHERE a.anchor_id IS NOT NULL AND a.anchor_id != '' " +
    "ORDER BY p.id, a.is_primary DESC, a.id ASC"
  );
  if (personRows.length === 0) {
    return { period, males: [], females: [] };
  }

  // 按人归并其名下所有 anchor
  const personMap = new Map(); // personId -> { person_id, name, gender, anchorIds: [] }
  const anchorToPerson = new Map(); // anchorId -> personId
  for (const r of personRows) {
    if (!personMap.has(r.person_id)) {
      personMap.set(r.person_id, {
        person_id: r.person_id,
        name: r.name,
        gender: r.gender,
        anchorIds: [],
        douyinNos: [],
      });
    }
    personMap.get(r.person_id).anchorIds.push(r.anchor_id);
    if (r.douyin_no) personMap.get(r.person_id).douyinNos.push(r.douyin_no);
    anchorToPerson.set(r.anchor_id, r.person_id);
  }
  const persons = Array.from(personMap.values());

  const anchorIds = [];
  for (const p of persons) anchorIds.push(...p.anchorIds);
  const ph = anchorIds.map(() => "?").join(",");

  // 2. 获取当月每日音浪，按人归并——同一人多账号在同一天的音浪先按日相加，
  //    再对日序列算 trimmed mean（否则同一天会被拆成多条，去最高日失真）。
  const [waveRows] = await db.query(
    "SELECT anchor_id, import_date, wave_value FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ? ORDER BY import_date",
    [...anchorIds, monthStart, monthEnd]
  );
  const fmtDay = (d) =>
    d instanceof Date ? d.toISOString().split("T")[0] : String(d).split("T")[0];
  const dayByPerson = new Map(); // personId -> Map(dateStr -> summedWave)
  for (const r of waveRows) {
    const pid = anchorToPerson.get(r.anchor_id);
    if (pid == null) continue;
    if (!dayByPerson.has(pid)) dayByPerson.set(pid, new Map());
    const dm = dayByPerson.get(pid);
    const key = fmtDay(r.import_date);
    dm.set(key, (dm.get(key) || 0) + (Number(r.wave_value) || 0));
  }
  const { latestWaveFromDayMap } = require("../shared/pk-roster-stats");
  // personId -> 过滤阈值后的 [date, wave] 日序列（保留日期供 latestWave）
  const dayEntriesByPerson = new Map();
  const waveByPerson = new Map(); // personId -> [dailyWave...]
  for (const [pid, dm] of dayByPerson) {
    const entries = [...dm.entries()]
      .map(([date, w]) => [date, Number(w) || 0])
      .filter(([, w]) => w >= LIVE_WAVE_THRESHOLD);
    dayEntriesByPerson.set(pid, entries);
    waveByPerson.set(pid, entries.map(([, w]) => w));
  }

  // 3. 获取当月总时长：取当月范围内最近一次累计快照，再按人取最长账号
  const latestDurMap = await getLatestDurationMap(db, {
    anchorIds,
    fromDate: monthStart,
    asOfDate: monthEnd,
  });
  const durMap = new Map(); // personId -> max account minutes
  for (const [anchorId, minutes] of latestDurMap) {
    const pid = anchorToPerson.get(anchorId);
    if (pid == null) continue;
    durMap.set(pid, Math.max(durMap.get(pid) || 0, minutes));
  }

  // 4. 计算每位主播的统计数据
  const males = [];
  const females = [];
  for (const p of persons) {
    const days = waveByPerson.get(p.person_id) || [];
    const dayEntries = dayEntriesByPerson.get(p.person_id) || [];
    const totalWave = days.reduce((s, v) => s + v, 0);
    const waveDays = days.length;
    const maxWave = waveDays > 0 ? Math.max(...days) : 0;
    const minWave = waveDays > 0 ? Math.min(...days) : 0;
    const { latestWave, latestWaveDate } = latestWaveFromDayMap(new Map(dayEntries));
    // Trimmed mean: 去掉最高单日后取日均
    let trimmedAvg = 0;
    if (waveDays > 1) {
      const sorted = [...days].sort((a, b) => a - b);
      const trimmed = sorted.slice(0, -1);
      trimmedAvg = trimmed.reduce((s, v) => s + v, 0) / trimmed.length;
    } else if (waveDays === 1) {
      trimmedAvg = days[0];
    }
    const duration = durMap.get(p.person_id) || 0;
    const member = {
      personId: p.person_id, name: p.name, gender: p.gender, anchorId: p.anchorIds[0] || "",
      anchorIds: p.anchorIds,
      douyinNos: p.douyinNos,
      wave: totalWave, trimmedAvg, maxWave, minWave, waveDays, latestWave, latestWaveDate, duration, rank: 0,
    };
    if (p.gender === "male") males.push(member);
    else females.push(member);
  }

  males.sort((a, b) => b.wave - a.wave);
  females.sort((a, b) => b.wave - a.wave);
  for (let i = 0; i < males.length; i++) males[i].rank = i + 1;
  for (let i = 0; i < females.length; i++) females[i].rank = i + 1;

  return { period, males, females };
}

async function ensureStarBattleScoresTable(db) {
  await db.query(
    `CREATE TABLE IF NOT EXISTS star_battle_scores (
      period VARCHAR(7) NOT NULL,
      round_key VARCHAR(32) NOT NULL,
      group_key VARCHAR(32) NOT NULL,
      person_id INT NOT NULL,
      score DECIMAL(14, 2) NOT NULL DEFAULT 0,
      updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      PRIMARY KEY (period, round_key, group_key, person_id),
      INDEX idx_star_battle_scores_period (period),
      INDEX idx_star_battle_scores_person (person_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
  );
}

function normalizeBattlePeriod(period) {
  if (!period) {
    const now = new Date();
    period = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
  }
  period = String(period);
  if (!/^\d{4}-\d{2}$/.test(period)) {
    throw new Error("period 格式应为 YYYY-MM");
  }
  return period;
}

function normalizeScorePayload(payload) {
  if (!payload || typeof payload !== "object") {
    throw new Error("缺少分数数据");
  }
  const period = normalizeBattlePeriod(payload.period);
  const roundKey = String(payload.roundKey || "").trim().slice(0, 32);
  const groupKey = String(payload.groupKey || "").trim().slice(0, 32);
  const personId = Number(payload.personId);
  const rawScore = payload.score;
  if (!roundKey) throw new Error("缺少轮次");
  if (!groupKey) throw new Error("缺少分组");
  if (!Number.isInteger(personId) || personId <= 0) throw new Error("人员ID无效");
  if (rawScore === null || rawScore === undefined || rawScore === "") {
    return { period, roundKey, groupKey, personId, score: null };
  }
  const score = Number(rawScore);
  if (!Number.isFinite(score) || score < 0) throw new Error("分数必须是大于等于0的数字");
  return { period, roundKey, groupKey, personId, score: Math.round(score * 100) / 100 };
}

async function getStarBattleScores(period) {
  period = normalizeBattlePeriod(period);
  const db = getPool();
  await ensureStarBattleScoresTable(db);
  const [rows] = await db.query(
    `SELECT period, round_key, group_key, person_id, score, updated_at
       FROM star_battle_scores
      WHERE period = ?
      ORDER BY round_key, group_key, score DESC, person_id`,
    [period]
  );
  return rows.map((r) => ({
    period: r.period,
    roundKey: r.round_key,
    groupKey: r.group_key,
    personId: Number(r.person_id),
    score: Number(r.score) || 0,
    updatedAt: r.updated_at,
  }));
}

async function saveStarBattleScore(payload) {
  const item = normalizeScorePayload(payload);
  const db = getPool();
  await ensureStarBattleScoresTable(db);
  if (item.score === null) {
    await db.query(
      `DELETE FROM star_battle_scores
        WHERE period = ? AND round_key = ? AND group_key = ? AND person_id = ?`,
      [item.period, item.roundKey, item.groupKey, item.personId]
    );
    return { saved: false, deleted: true };
  }
  await db.query(
    `INSERT INTO star_battle_scores
       (period, round_key, group_key, person_id, score)
     VALUES (?, ?, ?, ?, ?)
     ON DUPLICATE KEY UPDATE score = VALUES(score), updated_at = CURRENT_TIMESTAMP`,
    [item.period, item.roundKey, item.groupKey, item.personId, item.score]
  );
  return { saved: true, deleted: false };
}

const DEFAULT_REWARD_CONFIG = {
  waveRules: [
    { label: "600万以上", minWave: 6000000, maxWave: null, amount: 5000 },
    { label: "400万-500万", minWave: 4000000, maxWave: 5000000, amount: 4000 },
    { label: "200万-300万", minWave: 2000000, maxWave: 3000000, amount: 3000 },
  ],
  durationRule: { thresholdMinutes: 136 * 60, firstPrize: 500 },
};

function sanitizeRewardConfig(config) {
  const source = config && typeof config === "object" ? config : DEFAULT_REWARD_CONFIG;
  const rawRules = Array.isArray(source.waveRules) && source.waveRules.length > 0
    ? source.waveRules
    : DEFAULT_REWARD_CONFIG.waveRules;
  const waveRules = rawRules.map((rule, index) => {
    const label = String(rule?.label ?? "").trim() || `规则${index + 1}`;
    const minWave = Number(rule?.minWave);
    const maxWaveRaw = rule?.maxWave;
    const maxWave = maxWaveRaw === null || maxWaveRaw === "" || maxWaveRaw === undefined
      ? null
      : Number(maxWaveRaw);
    const amount = Number(rule?.amount);
    if (!Number.isFinite(minWave) || minWave < 0) {
      throw new Error(`第 ${index + 1} 条音浪规则门槛无效`);
    }
    if (maxWave !== null && (!Number.isFinite(maxWave) || maxWave < minWave)) {
      throw new Error(`第 ${index + 1} 条音浪规则上限无效`);
    }
    if (!Number.isFinite(amount) || amount < 0) {
      throw new Error(`第 ${index + 1} 条音浪规则金额无效`);
    }
    return { label, minWave, maxWave, amount };
  });

  const durationRule = source.durationRule || DEFAULT_REWARD_CONFIG.durationRule;
  const thresholdMinutes = Number(durationRule.thresholdMinutes);
  const firstPrize = Number(durationRule.firstPrize);
  if (!Number.isFinite(thresholdMinutes) || thresholdMinutes < 0) {
    throw new Error("时长门槛无效");
  }
  if (!Number.isFinite(firstPrize) || firstPrize < 0) {
    throw new Error("时长奖励金额无效");
  }

  return { waveRules, durationRule: { thresholdMinutes, firstPrize } };
}

/**
 * 奖励机制报表。
 * period 格式 YYYY-MM。按人汇总当月累计音浪和时长。
 */
async function getRewardReport(period, config) {
  if (!period) {
    const now = new Date();
    period = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
  }
  if (!/^\d{4}-\d{2}$/.test(period)) {
    throw new Error("period 格式应为 YYYY-MM");
  }
  const rewardConfig = sanitizeRewardConfig(config);

  const db = getPool();
  const [y, m] = period.split("-").map(Number);
  const monthStart = `${period}-01`;
  const monthEnd = `${period}-${String(new Date(y, m, 0).getDate()).padStart(2, "0")}`;

  const [personRows] = await db.query(
    "SELECT p.id AS person_id, p.name, p.gender, a.anchor_id " +
    "FROM persons p " +
    "INNER JOIN accounts a ON a.person_id = p.id " +
    "WHERE a.anchor_id IS NOT NULL AND a.anchor_id != '' " +
    "ORDER BY p.id"
  );

  const personMap = new Map();
  const anchorToPerson = new Map();
  for (const r of personRows) {
    if (!personMap.has(r.person_id)) {
      personMap.set(r.person_id, {
        personId: r.person_id,
        name: r.name,
        gender: r.gender,
        anchorIds: [],
      });
    }
    personMap.get(r.person_id).anchorIds.push(r.anchor_id);
    anchorToPerson.set(r.anchor_id, r.person_id);
  }

  const persons = Array.from(personMap.values());
  if (persons.length === 0) {
    return {
      period,
      rows: [],
      waveRules: rewardConfig.waveRules,
      durationRule: { ...rewardConfig.durationRule, qualified: false, winnerPersonId: null },
      summary: { totalPeople: 0, waveWinners: 0, durationQualified: false, totalBonus: 0 },
    };
  }

  const anchorIds = [];
  for (const p of persons) anchorIds.push(...p.anchorIds);
  const ph = anchorIds.map(() => "?").join(",");

  const [waveRows] = await db.query(
    "SELECT anchor_id, COALESCE(SUM(wave_value), 0) AS total_wave FROM wave_snapshots " +
    "WHERE anchor_id IN (" + ph + ") AND import_date BETWEEN ? AND ? GROUP BY anchor_id",
    [...anchorIds, monthStart, monthEnd]
  );
  const waveByPerson = new Map();
  for (const r of waveRows) {
    const pid = anchorToPerson.get(r.anchor_id);
    if (pid == null) continue;
    waveByPerson.set(pid, (waveByPerson.get(pid) || 0) + (Number(r.total_wave) || 0));
  }

  const latestRewardDurMap = await getLatestDurationMap(db, {
    anchorIds,
    fromDate: monthStart,
    asOfDate: monthEnd,
  });
  const durationByPerson = new Map();
  for (const [anchorId, minutes] of latestRewardDurMap) {
    const pid = anchorToPerson.get(anchorId);
    if (pid == null) continue;
    durationByPerson.set(pid, Math.max(durationByPerson.get(pid) || 0, minutes));
  }

  const waveRules = rewardConfig.waveRules;

  const rows = persons.map((p) => {
    const wave = waveByPerson.get(p.personId) || 0;
    const duration = durationByPerson.get(p.personId) || 0;
    const matchedRule = waveRules.find((rule) => {
      const maxOk = rule.maxWave == null || wave <= rule.maxWave;
      return wave >= rule.minWave && maxOk;
    });
    return {
      personId: p.personId,
      name: p.name,
      gender: p.gender,
      anchorId: p.anchorIds[0] || "",
      wave,
      duration,
      waveReward: matchedRule ? matchedRule.amount : 0,
      waveRewardLabel: matchedRule ? matchedRule.label : "",
      durationReward: 0,
      totalReward: matchedRule ? matchedRule.amount : 0,
      waveRank: 0,
      durationRank: 0,
    };
  });

  const waveSorted = [...rows].sort((a, b) => b.wave - a.wave || a.personId - b.personId);
  for (let i = 0; i < waveSorted.length; i++) waveSorted[i].waveRank = i + 1;

  const durationSorted = [...rows].sort((a, b) => b.duration - a.duration || a.personId - b.personId);
  for (let i = 0; i < durationSorted.length; i++) durationSorted[i].durationRank = i + 1;

  const thresholdMinutes = rewardConfig.durationRule.thresholdMinutes;
  const durationQualified = rows.length > 0 && rows.every((r) => r.duration >= thresholdMinutes);
  const durationWinner = durationQualified ? durationSorted[0] : null;
  if (durationWinner) {
    durationWinner.durationReward = rewardConfig.durationRule.firstPrize;
    durationWinner.totalReward += rewardConfig.durationRule.firstPrize;
  }

  rows.sort((a, b) => b.totalReward - a.totalReward || b.wave - a.wave || a.personId - b.personId);

  const totalBonus = rows.reduce((sum, r) => sum + r.totalReward, 0);
  return {
    period,
    rows,
    waveRules,
    durationRule: {
      thresholdMinutes,
      firstPrize: rewardConfig.durationRule.firstPrize,
      qualified: durationQualified,
      winnerPersonId: durationWinner ? durationWinner.personId : null,
    },
    summary: {
      totalPeople: rows.length,
      waveWinners: rows.filter((r) => r.waveReward > 0).length,
      durationQualified,
      totalBonus,
    },
  };
}

/**
 * 验证应用密码，返回角色信息
 * - app_password → admin（全部权限）
 * - guest_password → guest（受限权限）
 */
async function verifyAppPassword(password) {
  const db = getPool();
  const input = String(password || "").trim();
  const [rows] = await db.query(
    "SELECT setting_key, setting_value FROM app_settings WHERE setting_key IN ('app_password', 'guest_password')"
  );
  for (const row of rows) {
    const stored = String(row.setting_value || "").trim();
    if (stored === input) {
      const role = row.setting_key === 'app_password' ? 'admin' : 'guest';
      return { ok: true, role };
    }
  }
  return { ok: false, reason: "密码错误" };
}

/**
 * 检查是否已设置应用密码（admin 或 guest 任一存在即可）
 */
async function hasAppPassword() {
  const db = getPool();
  const [rows] = await db.query(
    "SELECT setting_value FROM app_settings WHERE setting_key IN ('app_password', 'guest_password') AND setting_value IS NOT NULL AND setting_value != ''"
  );
  return { hasPassword: rows.length > 0 };
}

function buildPkGroups(payload) {
  const { buildPkGroups: engineBuild } = require("./pk-group-engine");
  return engineBuild(payload || {});
}

/**
 * 按辈分导出全部族谱 CSV 行。
 * 排序：代数升序 → 字辈(浩/狼/玖/啸/其他) → 姓名。
 * 返回中文表头对象数组，可直接 downloadCsv。
 */
async function exportFamilyRoster() {
  const db = getPool();
  const [persons] = await db.query(
    `SELECT id, name, gender, master_id, generation
       FROM persons
      ORDER BY generation IS NULL, generation, id`
  );
  if (persons.length === 0) return [];

  const personIds = persons.map((p) => Number(p.id));
  const nameById = new Map(persons.map((p) => [Number(p.id), p.name || ""]));

  const [accounts] = await db.query(
    `SELECT id, person_id, anchor_id, is_primary, douyin_no, anchor_name
       FROM accounts
      WHERE person_id IN (?)
      ORDER BY person_id, is_primary DESC, id ASC`,
    [personIds]
  );
  const accountByPerson = new Map();
  for (const a of accounts) {
    const pid = Number(a.person_id);
    if (!accountByPerson.has(pid)) accountByPerson.set(pid, []);
    accountByPerson.get(pid).push(a);
  }

  const SURNAME_ORDER = { 浩: 1, 狼: 2, 玖: 3, 啸: 4 };
  const genLabel = (g) => {
    if (g == null) return "未定代";
    if (Number(g) === 0) return "祖师";
    return `第${Number(g)}代`;
  };
  const surnameOf = (name) => {
    const ch = String(name || "").trim().charAt(0);
    return SURNAME_ORDER[ch] ? ch : "其他";
  };
  const genderLabel = (g) =>
    g === "male" ? "男" : g === "female" ? "女" : g || "";

  const rows = persons.map((p) => {
    const pid = Number(p.id);
    const personAccounts = accountByPerson.get(pid) || [];
    const primary =
      personAccounts.find((a) => Number(a.is_primary) === 1) ||
      personAccounts[0] ||
      null;
    const name = p.name || "";
    const surname = surnameOf(name);
    const generation = p.generation == null ? null : Number(p.generation);
    return {
      _gen: generation == null ? 999 : generation,
      _surnameOrd: SURNAME_ORDER[surname] || 99,
      _name: name,
      代数: generation == null ? "" : generation,
      辈分: genLabel(generation),
      字辈: surname,
      姓名: name,
      性别: genderLabel(p.gender),
      师父: p.master_id ? nameById.get(Number(p.master_id)) || "" : "",
      抖音号: primary ? primary.douyin_no || "" : "",
      主播ID: primary ? primary.anchor_id || "" : "",
      账号名: primary ? primary.anchor_name || "" : "",
      账号数: personAccounts.length,
    };
  });

  rows.sort((a, b) => {
    if (a._gen !== b._gen) return a._gen - b._gen;
    if (a._surnameOrd !== b._surnameOrd) return a._surnameOrd - b._surnameOrd;
    return String(a._name).localeCompare(String(b._name), "zh");
  });

  return rows.map(({ _gen, _surnameOrd, _name, ...rest }) => rest);
}

/**
 * 按字辈（名字首字）导出名单。
 * 支持的 surname 值: 浩/狼/玖/啸
 * 返回该字辈所有 person 的基本信息 + 账号 + 最新音浪。
 */
async function getRosterBySurname(surname) {
  const db = getPool();
  const likePattern = `${surname}%`;
  const [persons] = await db.query(
    `SELECT id, name, gender, master_id, generation
       FROM persons
      WHERE name LIKE ?
      ORDER BY generation IS NULL, generation, id`,
    [likePattern]
  );
  if (persons.length === 0) return [];
  const personIds = persons.map((p) => Number(p.id));
  const [accounts] = await db.query(
    `SELECT id, person_id, anchor_id, is_primary, douyin_no, anchor_name
       FROM accounts
      WHERE person_id IN (?)
        AND anchor_id IS NOT NULL
        AND anchor_id != ''
      ORDER BY person_id, is_primary DESC, id ASC`,
    [personIds]
  );
  // 获取最新一天音浪（wave_snapshots 用 anchor_id 关联，需先取所有 anchor_id）
  const allAnchorIds = accounts.map(a => a.anchor_id);
  const waveMap = new Map(); // personId -> dailyWave
  if (allAnchorIds.length > 0) {
    const ph2 = allAnchorIds.map(() => "?").join(",");
    const [latestDateRows] = await db.query(
      "SELECT MAX(import_date) AS latest FROM wave_snapshots WHERE anchor_id IN (" + ph2 + ")",
      allAnchorIds
    );
    const latestDate = latestDateRows[0]?.latest;
    if (latestDate) {
      const [waveRows] = await db.query(
        "SELECT anchor_id, wave_value FROM wave_snapshots WHERE anchor_id IN (" + ph2 + ") AND import_date = ?",
        [...allAnchorIds, latestDate]
      );
      const anchorWave = new Map();
      for (const r of waveRows) anchorWave.set(r.anchor_id, Number(r.wave_value) || 0);
      // 按人归并：取该人所有 anchor 的音浪之和
      for (const a of accounts) {
        const pid = Number(a.person_id);
        const w = anchorWave.get(a.anchor_id) || 0;
        waveMap.set(pid, (waveMap.get(pid) || 0) + w);
      }
    }
  }
  // 师傅名字
  const masterIds = [...new Set(persons.filter(p => p.master_id).map(p => Number(p.master_id)))];
  let masterNameMap = new Map();
  if (masterIds.length > 0) {
    const [masters] = await db.query('SELECT id, name FROM persons WHERE id IN (?)', [masterIds]);
    masterNameMap = new Map(masters.map(m => [Number(m.id), m.name]));
  }
  const accountByPerson = new Map();
  for (const a of accounts) {
    const pid = Number(a.person_id);
    if (!accountByPerson.has(pid)) accountByPerson.set(pid, []);
    accountByPerson.get(pid).push(a);
  }
  return persons.map(p => {
    const pid = Number(p.id);
    const personAccounts = accountByPerson.get(pid) || [];
    const primary = personAccounts.find(a => Number(a.is_primary) === 1) || personAccounts[0] || null;
    return {
      id: pid,
      name: p.name || "",
      gender: p.gender || "",
      generation: p.generation ?? null,
      masterName: p.master_id ? masterNameMap.get(Number(p.master_id)) || null : null,
      anchorId: primary ? primary.anchor_id || "" : "",
      douyinId: primary ? primary.douyin_no || "" : "",
      nickname: primary ? primary.anchor_name || "" : "",
      accountCount: personAccounts.length,
      aliasIds: primary ? personAccounts.filter(a => a.id !== primary.id).map(a => a.anchor_id) : personAccounts.map(a => a.anchor_id),
      dailyWave: waveMap.get(pid) || 0,
    };
  });
}

/* ============================================================
 * 主播收入（按月个人明细）
 * ========================================================== */

let anchorIncomeTablesReady = false;

async function ensureAnchorIncomeTablesReady(db = getPool()) {
  if (anchorIncomeTablesReady) return;
  await ensureAnchorIncomeTables(db);
  anchorIncomeTablesReady = true;
}

/** 'YYYY-MM' / 'YYYY-MM-DD' / 'YYYY/MM' → 'YYYY-MM' */
function normalizePeriod(value) {
  const text = String(value || "").trim();
  const match = text.match(/^(\d{4})[-/年](\d{1,2})/);
  if (!match) return "";
  const month = Number(match[2]);
  if (month < 1 || month > 12) return "";
  return `${match[1]}-${String(month).padStart(2, "0")}`;
}

function periodStart(period) {
  return `${period}-01`;
}

function periodEnd(period) {
  const [y, m] = period.split("-").map(Number);
  const lastDay = new Date(Date.UTC(y, m, 0)).getUTCDate();
  return `${period}-${String(lastDay).padStart(2, "0")}`;
}

function shiftPeriod(period, deltaMonths) {
  const [y, m] = period.split("-").map(Number);
  const total = y * 12 + (m - 1) + deltaMonths;
  const nextYear = Math.floor(total / 12);
  const nextMonth = (total % 12) + 1;
  return `${nextYear}-${String(nextMonth).padStart(2, "0")}`;
}

/** CSV 单元格：剥掉 Excel 的 ="123" 包裹，"-" 视作空 */
function cleanIncomeCell(value) {
  let text = String(value ?? "").trim();
  text = text.replace(/^="(.*)"$/, "$1").replace(/^=(.*)$/, "$1").trim();
  if (text === "-" || text === "—" || text === "" || text === "null") return "";
  return text;
}

function parseIncomeMoney(value) {
  const text = cleanIncomeCell(value).replace(/,/g, "");
  if (!text) return 0;
  const n = Number(text);
  return Number.isFinite(n) ? n : 0;
}

/** '2026/06/01 - 2026/06/30' → { startDate, endDate } */
function parseIncomeDateRange(value) {
  const text = cleanIncomeCell(value);
  if (!text) return { startDate: null, endDate: null };
  const parts = text.split(/\s*[-~至—]\s*/).map((item) => String(item).trim());
  const toIso = (raw) => {
    const match = String(raw || "").match(/^(\d{4})[-/年.](\d{1,2})[-/月.](\d{1,2})/);
    if (!match) return null;
    return `${match[1]}-${String(match[2]).padStart(2, "0")}-${String(match[3]).padStart(2, "0")}`;
  };
  if (parts.length >= 2) return { startDate: toIso(parts[0]), endDate: toIso(parts[1]) };
  const single = toIso(parts[0]);
  return { startDate: single, endDate: single };
}

/** 主播收入列表：按月返回人维度明细（含累计总收益、本期时长、上期收益） */
async function getAnchorIncome(periodInput) {
  const period = normalizePeriod(periodInput);
  if (!period) throw new Error("月份格式应为 YYYY-MM");
  const db = getPool();
  await ensureAnchorIncomeTablesReady(db);

  const monthStart = periodStart(period);
  const monthEnd = periodEnd(period);
  const prevPeriod = shiftPeriod(period, -1);

  const [accountRows] = await db.query(
    "SELECT a.anchor_id, a.douyin_no, a.anchor_name, a.person_id, a.is_primary " +
    "FROM accounts a WHERE a.anchor_id IS NOT NULL AND a.anchor_id != ''"
  );
  const [personRows] = await db.query(
    "SELECT id, name, gender, master_id FROM persons ORDER BY id"
  );

  const personMeta = new Map();
  for (const p of personRows) {
    personMeta.set(Number(p.id), {
      personId: Number(p.id),
      name: String(p.name || ""),
      gender: String(p.gender || ""),
    });
  }
  const accountsByPerson = new Map();
  const anchorToPerson = new Map();
  for (const a of accountRows) {
    const pid = Number(a.person_id);
    if (!pid || !personMeta.has(pid)) continue;
    anchorToPerson.set(a.anchor_id, pid);
    if (!accountsByPerson.has(pid)) accountsByPerson.set(pid, []);
    accountsByPerson.get(pid).push(a);
  }

  const allAnchorIds = accountRows.map((a) => a.anchor_id);

  // 本期收入明细（按账号）
  const [incomeRows] = allAnchorIds.length
    ? await db.query(
        "SELECT * FROM anchor_income WHERE period = ?",
        [period]
      )
    : [[]];

  // 累计（<= 本期）与上一期
  const [totalRows] = await db.query(
    "SELECT person_id, COALESCE(SUM(streamer_income), 0) AS total, " +
    "COALESCE(SUM(revenue), 0) AS revenue, COALESCE(SUM(guild_income), 0) AS guild " +
    "FROM anchor_income WHERE person_id IS NOT NULL AND period <= ? GROUP BY person_id",
    [period]
  );
  const [prevRows] = await db.query(
    "SELECT person_id, COALESCE(SUM(streamer_income), 0) AS total " +
    "FROM anchor_income WHERE person_id IS NOT NULL AND period = ? GROUP BY person_id",
    [prevPeriod]
  );
  const [profileRows] = await db.query(
    "SELECT person_id, join_date, opening_total, note FROM anchor_income_profiles"
  );

  const openingByPerson = new Map();
  const joinByPerson = new Map();
  for (const r of profileRows) {
    openingByPerson.set(Number(r.person_id), Number(r.opening_total) || 0);
    joinByPerson.set(Number(r.person_id), String(r.join_date || ""));
  }
  const totalByPerson = new Map();
  const revenueTotalByPerson = new Map();
  const guildTotalByPerson = new Map();
  for (const r of totalRows) {
    totalByPerson.set(Number(r.person_id), Number(r.total) || 0);
    revenueTotalByPerson.set(Number(r.person_id), Number(r.revenue) || 0);
    guildTotalByPerson.set(Number(r.person_id), Number(r.guild) || 0);
  }
  const prevByPerson = new Map();
  for (const r of prevRows) prevByPerson.set(Number(r.person_id), Number(r.total) || 0);

  // 本期时长：沿用日报口径，取该月范围内最近一次累计快照，人维度取名下最大账号
  const durationMap = allAnchorIds.length
    ? await getLatestDurationMap(db, {
        anchorIds: allAnchorIds,
        fromDate: monthStart,
        asOfDate: monthEnd,
      })
    : new Map();

  // 本期收入按人聚合
  const incomeByPerson = new Map();
  const orphanRows = [];
  for (const r of incomeRows) {
    const pid = r.person_id != null ? Number(r.person_id) : anchorToPerson.get(r.anchor_id) || null;
    if (!pid || !personMeta.has(pid)) {
      orphanRows.push({
        anchorId: String(r.anchor_id || ""),
        nickname: String(r.nickname || ""),
        douyinNo: String(r.douyin_no || ""),
        streamerIncome: Number(r.streamer_income) || 0,
        revenue: Number(r.revenue) || 0,
      });
      continue;
    }
    let bucket = incomeByPerson.get(pid);
    if (!bucket) {
      bucket = {
        revenue: 0,
        streamerIncome: 0,
        guildIncome: 0,
        anchorIds: [],
        nickname: "",
        douyinNo: "",
        startDate: null,
        endDate: null,
        streamerRatio: "",
        guildRatio: "",
        incomeName: "",
        feeType: "",
        remark: "",
      };
      incomeByPerson.set(pid, bucket);
    }
    bucket.revenue += Number(r.revenue) || 0;
    bucket.streamerIncome += Number(r.streamer_income) || 0;
    bucket.guildIncome += Number(r.guild_income) || 0;
    bucket.anchorIds.push(String(r.anchor_id || ""));
    if (!bucket.nickname && r.nickname) bucket.nickname = String(r.nickname);
    if (!bucket.douyinNo && r.douyin_no) bucket.douyinNo = String(r.douyin_no);
    if (!bucket.startDate && r.start_date) bucket.startDate = normalizeSnapshotDate(r.start_date);
    if (r.end_date) bucket.endDate = normalizeSnapshotDate(r.end_date);
    if (!bucket.streamerRatio && r.streamer_ratio) bucket.streamerRatio = String(r.streamer_ratio);
    if (!bucket.guildRatio && r.guild_ratio) bucket.guildRatio = String(r.guild_ratio);
    if (!bucket.incomeName && r.income_name) bucket.incomeName = String(r.income_name);
    if (!bucket.feeType && r.fee_type) bucket.feeType = String(r.fee_type);
    if (!bucket.remark && r.remark) bucket.remark = String(r.remark);
  }

  const rows = [];
  for (const meta of personMeta.values()) {
    const pid = meta.personId;
    const income = incomeByPerson.get(pid);
    const anchorIds = (accountsByPerson.get(pid) || []).map((a) => a.anchor_id);
    const opening = openingByPerson.get(pid) || 0;
    const cumulative = Number((opening + (totalByPerson.get(pid) || 0)).toFixed(2));
    rows.push({
      personId: pid,
      name: meta.name,
      gender: meta.gender,
      joinDate: joinByPerson.get(pid) || "",
      nickname: income?.nickname || "",
      douyinNo: income?.douyinNo || (accountsByPerson.get(pid) || []).map((a) => a.douyin_no).filter(Boolean)[0] || "",
      anchorIds: income?.anchorIds?.length ? income.anchorIds : anchorIds,
      startDate: income?.startDate || monthStart,
      endDate: income?.endDate || monthEnd,
      revenue: Number((income?.revenue || 0).toFixed(2)),
      streamerRatio: income?.streamerRatio || "",
      guildRatio: income?.guildRatio || "",
      streamerIncome: Number((income?.streamerIncome || 0).toFixed(2)),
      guildIncome: Number((income?.guildIncome || 0).toFixed(2)),
      incomeName: income?.incomeName || "",
      feeType: income?.feeType || "",
      remark: income?.remark || "",
      durationMinutes: maxDurationAmongAnchors(anchorIds, durationMap),
      prevIncome: Number((prevByPerson.get(pid) || 0).toFixed(2)),
      openingTotal: Number(opening.toFixed(2)),
      totalIncome: cumulative,
      hasIncome: Boolean(income),
    });
  }

  rows.sort(
    (a, b) =>
      Number(b.hasIncome) - Number(a.hasIncome) ||
      b.streamerIncome - a.streamerIncome ||
      a.personId - b.personId
  );

  const sum = (fn) => rows.reduce((acc, r) => acc + fn(r), 0);
  return {
    period,
    prevPeriod,
    startDate: rows.find((r) => r.hasIncome)?.startDate || monthStart,
    endDate: rows.find((r) => r.hasIncome)?.endDate || monthEnd,
    rows,
    orphans: orphanRows,
    summary: {
      total: rows.length,
      maleCount: rows.filter((r) => r.gender === "male").length,
      femaleCount: rows.filter((r) => r.gender === "female").length,
      incomeCount: rows.filter((r) => r.hasIncome).length,
      totalRevenue: Number(sum((r) => r.revenue).toFixed(2)),
      totalStreamerIncome: Number(sum((r) => r.streamerIncome).toFixed(2)),
      totalGuildIncome: Number(sum((r) => r.guildIncome).toFixed(2)),
      totalDuration: sum((r) => r.durationMinutes),
    },
  };
}

async function getIncomePeriods() {
  const db = getPool();
  await ensureAnchorIncomeTablesReady(db);
  const [rows] = await db.query(
    "SELECT period, COUNT(*) AS row_count, " +
    "COALESCE(SUM(streamer_income), 0) AS total_income, MAX(updated_at) AS updated_at " +
    "FROM anchor_income GROUP BY period ORDER BY period DESC"
  );
  return rows.map((r) => ({
    period: String(r.period),
    rowCount: Number(r.row_count) || 0,
    totalIncome: Number(r.total_income) || 0,
    updatedAt: r.updated_at ? String(r.updated_at).slice(0, 19).replace("T", " ") : null,
  }));
}

/** 解析一行平台导出的个人明细（放在 db 层，导入预览与落库共用同一套规则） */
function parseIncomeRow(raw) {
  const anchorId = cleanIncomeCell(raw.anchorId ?? raw["主播UID"] ?? raw["主播uid"]);
  const douyinNo = cleanIncomeCell(raw.douyinNo ?? raw["主播抖音号"]);
  const nickname = cleanIncomeCell(raw.nickname ?? raw["主播昵称"]);
  const { startDate, endDate } = parseIncomeDateRange(raw.dateRange ?? raw["起止日期"]);
  return {
    anchorId,
    douyinNo,
    nickname,
    startDate,
    endDate,
    incomeName: cleanIncomeCell(raw.incomeName ?? raw["收入名称"]),
    feeType: cleanIncomeCell(raw.feeType ?? raw["费用类型"]),
    revenue: parseIncomeMoney(raw.revenue ?? raw["本期流水（元）"]),
    streamerRatio: cleanIncomeCell(raw.streamerRatio ?? raw["主播分成比"]),
    guildRatio: cleanIncomeCell(raw.guildRatio ?? raw["公会分成比"]),
    streamerIncome: parseIncomeMoney(raw.streamerIncome ?? raw["主播收入（元）"]),
    guildIncome: parseIncomeMoney(raw.guildIncome ?? raw["公会收入（元）"]),
    remark: cleanIncomeCell(raw.remark ?? raw["备注"]),
    personId: raw.personId ? Number(raw.personId) || null : null,
  };
}

/**
 * 导入个人明细。
 * payload: { period, rows, replace }
 *  - rows 每项至少含 anchorId；personId 为空时按 anchor_id → 抖音号 → 昵称匹配主播
 *  - replace=true 时先清空该月全部记录
 */
async function importAnchorIncome(payload) {
  const period = normalizePeriod(payload?.period);
  if (!period) throw new Error("月份格式应为 YYYY-MM");
  const rows = Array.isArray(payload?.rows) ? payload.rows : [];
  if (rows.length === 0) throw new Error("没有可导入的数据行");

  const db = getPool();
  await ensureAnchorIncomeTablesReady(db);

  const [accountRows] = await db.query(
    "SELECT a.anchor_id, a.douyin_no, a.anchor_name, a.person_id FROM accounts a " +
    "INNER JOIN persons p ON p.id = a.person_id " +
    "WHERE a.anchor_id IS NOT NULL AND a.anchor_id != ''"
  );
  const byAnchorId = new Map();
  const byDouyinNo = new Map();
  const byNickname = new Map();
  const nicknameDup = new Set();
  for (const a of accountRows) {
    byAnchorId.set(a.anchor_id, Number(a.person_id));
    const no = String(a.douyin_no || "").trim();
    if (no) byDouyinNo.set(no, Number(a.person_id));
    const nm = String(a.anchor_name || "").trim();
    if (nm) {
      if (byNickname.has(nm)) nicknameDup.add(nm);
      else byNickname.set(nm, Number(a.person_id));
    }
  }

  const parsed = rows.map(parseIncomeRow);
  const usable = parsed.filter((r) => r.anchorId);
  const skipped = parsed.length - usable.length;

  if (payload?.replace) {
    await db.query("DELETE FROM anchor_income WHERE period = ?", [period]);
  }

  let saved = 0;
  let matched = 0;
  const unmatched = [];
  for (const row of usable) {
    let personId = row.personId;
    if (!personId) {
      personId =
        byAnchorId.get(row.anchorId) ||
        (row.douyinNo ? byDouyinNo.get(row.douyinNo) || null : null) ||
        (row.nickname && !nicknameDup.has(row.nickname) ? byNickname.get(row.nickname) || null : null);
    }
    if (personId) matched += 1;
    else unmatched.push({ anchorId: row.anchorId, douyinNo: row.douyinNo, nickname: row.nickname, streamerIncome: row.streamerIncome });

    await db.query(
      `INSERT INTO anchor_income
         (period, anchor_id, person_id, douyin_no, nickname, start_date, end_date,
          income_name, fee_type, revenue, streamer_ratio, guild_ratio,
          streamer_income, guild_income, remark)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON DUPLICATE KEY UPDATE
         person_id = VALUES(person_id),
         douyin_no = VALUES(douyin_no),
         nickname = VALUES(nickname),
         start_date = VALUES(start_date),
         end_date = VALUES(end_date),
         income_name = VALUES(income_name),
         fee_type = VALUES(fee_type),
         revenue = VALUES(revenue),
         streamer_ratio = VALUES(streamer_ratio),
         guild_ratio = VALUES(guild_ratio),
         streamer_income = VALUES(streamer_income),
         guild_income = VALUES(guild_income),
         remark = VALUES(remark)`,
      [
        period,
        row.anchorId,
        personId || null,
        row.douyinNo,
        row.nickname,
        row.startDate,
        row.endDate,
        row.incomeName,
        row.feeType,
        row.revenue,
        row.streamerRatio,
        row.guildRatio,
        row.streamerIncome,
        row.guildIncome,
        row.remark,
      ]
    );
    saved += 1;
  }

  return { period, saved, skipped, matched, unmatched, replaced: Boolean(payload?.replace) };
}

/** 保存入会时间 / 期初累计个人收益 */
async function saveAnchorIncomeProfile(payload) {
  const personId = Number(payload?.personId);
  if (!personId) throw new Error("缺少主播 ID");
  const db = getPool();
  await ensureAnchorIncomeTablesReady(db);
  const joinDate = String(payload?.joinDate ?? "").trim().slice(0, 32);
  const openingTotal = Number(payload?.openingTotal) || 0;
  const note = String(payload?.note ?? "").trim().slice(0, 255);

  await db.query(
    `INSERT INTO anchor_income_profiles (person_id, join_date, opening_total, note)
     VALUES (?, ?, ?, ?)
     ON DUPLICATE KEY UPDATE join_date = VALUES(join_date), opening_total = VALUES(opening_total), note = VALUES(note)`,
    [personId, joinDate, openingTotal, note]
  );
  return { personId, joinDate, openingTotal, note };
}

/** 清空某月收入（personIds 为空则整月清空） */
async function deleteAnchorIncome(periodInput, personIds) {
  const period = normalizePeriod(periodInput);
  if (!period) throw new Error("月份格式应为 YYYY-MM");
  const db = getPool();
  await ensureAnchorIncomeTablesReady(db);
  const ids = Array.isArray(personIds) ? personIds.map(Number).filter(Boolean) : [];
  if (ids.length === 0) {
    const [result] = await db.query("DELETE FROM anchor_income WHERE period = ?", [period]);
    return { period, deleted: Number(result.affectedRows) || 0 };
  }
  const ph = ids.map(() => "?").join(",");
  const [result] = await db.query(
    `DELETE FROM anchor_income WHERE period = ? AND person_id IN (${ph})`,
    [period, ...ids]
  );
  return { period, deleted: Number(result.affectedRows) || 0 };
}

// ---- 数据清理（按日期区间备份后删除）----

const CLEANUP_TARGETS = [
  { table: "wave_snapshots", label: "音浪快照", kind: "range", column: "import_date" },
  { table: "duration_snapshots", label: "时长快照", kind: "range", column: "import_date" },
  { table: "import_records", label: "导入记录", kind: "range", column: "import_date" },
  { table: "anchor_income", label: "收入结算", kind: "period", column: "period" },
];

function normalizeCleanupRange(fromInput, toInput) {
  const from = String(fromInput ?? "").trim();
  const to = String(toInput ?? "").trim();
  if (!/^\d{4}-\d{2}-\d{2}$/.test(from) || !/^\d{4}-\d{2}-\d{2}$/.test(to)) {
    throw new Error("日期格式应为 YYYY-MM-DD");
  }
  if (from > to) throw new Error("开始日期不能晚于结束日期");
  return { from, to, periodFrom: from.slice(0, 7), periodTo: to.slice(0, 7) };
}

function cleanupCondition(target, range) {
  if (target.kind === "period") {
    return {
      sql: "`period` BETWEEN ? AND ?",
      params: [range.periodFrom, range.periodTo],
    };
  }
  return {
    sql: `\`${target.column}\` BETWEEN ? AND ?`,
    params: [range.from, range.to],
  };
}

function cleanupBackupName(table) {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, "0");
  const stamp = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}_${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
  return `backup_purge_${table}_${stamp}`;
}

/** 按日期区间统计将被清理的数据量（只读，不做任何修改） */
async function getDataCleanupSummary(fromInput, toInput) {
  const range = normalizeCleanupRange(fromInput, toInput);
  const db = getPool();
  const [tables] = await db.query(
    `SELECT table_name AS name FROM information_schema.tables WHERE table_schema = DATABASE()`
  );
  const existing = new Set(tables.map((t) => t.name));

  const items = [];
  for (const target of CLEANUP_TARGETS) {
    if (!existing.has(target.table)) continue;
    const cond = cleanupCondition(target, range);
    const [[row]] = await db.query(
      `SELECT COUNT(*) AS c FROM \`${target.table}\` WHERE ${cond.sql}`,
      cond.params
    );
    items.push({
      table: target.table,
      label: target.label,
      kind: target.kind,
      count: Number(row?.c) || 0,
    });
  }

  return {
    from: range.from,
    to: range.to,
    items,
    total: items.reduce((sum, item) => sum + item.count, 0),
  };
}

/** 按日期区间清理数据：先把待删数据整行备份，再删除 */
async function deleteDataByDateRange(fromInput, toInput) {
  const range = normalizeCleanupRange(fromInput, toInput);
  const db = getPool();
  const [tables] = await db.query(
    `SELECT table_name AS name FROM information_schema.tables WHERE table_schema = DATABASE()`
  );
  const existing = new Set(tables.map((t) => t.name));

  const items = [];
  const backups = [];

  for (const target of CLEANUP_TARGETS) {
    if (!existing.has(target.table)) continue;
    const cond = cleanupCondition(target, range);
    const [[countRow]] = await db.query(
      `SELECT COUNT(*) AS c FROM \`${target.table}\` WHERE ${cond.sql}`,
      cond.params
    );
    const count = Number(countRow?.c) || 0;
    if (count === 0) {
      items.push({ table: target.table, label: target.label, deleted: 0 });
      continue;
    }

    const backupTable = cleanupBackupName(target.table);
    await db.query(
      `CREATE TABLE \`${backupTable}\` AS SELECT * FROM \`${target.table}\` WHERE ${cond.sql}`,
      cond.params
    );
    const [result] = await db.query(
      `DELETE FROM \`${target.table}\` WHERE ${cond.sql}`,
      cond.params
    );
    backups.push({ table: target.table, backupTable, rows: count });
    items.push({
      table: target.table,
      label: target.label,
      deleted: Number(result.affectedRows) || 0,
    });
  }

  return {
    from: range.from,
    to: range.to,
    items,
    backups,
    total: items.reduce((sum, item) => sum + item.deleted, 0),
  };
}

module.exports = {
  getPool,
  getAnchors,
  getFamilyTree,
  getDashboardSummary,
  getStartupHealth,
  getWaveRanking,
  getWaveTrendByGender,
  importWaveSnapshots,
  importDurationSnapshots,
  listImportLogs,
  getImportPreview,
  exportWaveSnapshots,
  exportDurationSnapshots,
  sumWaveSnapshotsForAccounts,
  exportAnchors,
  addAnchor,
  batchImportAnchors,
  mergeAccounts,
  deleteAnchors,
  findDuplicateAnchors,
  getWaveTrendTotal,
  getAnchorCountTrend,
  updateAnchorName,
  updateAnchorInfo,
  updateAnchorMaster,
  getAnchorDailySnapshot,
  saveAnchorDailySnapshot,
  getAnchorWaveTrend,
  getAnchorDurationTrend,
  getAnchorsWaveTrend,
  getFlowingFlag,
  settleFlagScores,
  getFlagGroups,
  getTierRules,
  saveTierRules,
  getDailyWaveReport,
  getMonthlyReport,
  getPkRoster,
  buildPkGroups,
  getStarBattleScores,
  saveStarBattleScore,
  getFlagWinner,
  getRewardReport,
  getRosterBySurname,
  exportFamilyRoster,
  getAnchorIncome,
  getIncomePeriods,
  importAnchorIncome,
  saveAnchorIncomeProfile,
  deleteAnchorIncome,
  getDataCleanupSummary,
  deleteDataByDateRange,
  verifyAppPassword,
  hasAppPassword,
  ensureChannelAnchorBindsTable: () => ensureChannelAnchorBindsTable(getPool()),
  __testing: { importSnapshotRows, normalizeDurationImportDate },
};
