async function ensureImportRecordsTable(db) {
  await db.query(
    `CREATE TABLE IF NOT EXISTS import_records (
       id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
       kind VARCHAR(20) NOT NULL,
       import_date DATE NOT NULL,
       file_hash CHAR(32) NOT NULL,
       data_hash CHAR(64) NOT NULL,
       file_name VARCHAR(255) NOT NULL DEFAULT '',
       row_count INT NOT NULL DEFAULT 0,
       created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
       PRIMARY KEY (id),
       UNIQUE KEY uq_import_kind_date_file (kind, import_date, file_hash),
       UNIQUE KEY uq_import_kind_date_data (kind, import_date, data_hash)
     ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
  );
  // 导入日志扩展列（2026-09）：来源 bot/web + 匹配统计。老库幂等补列。
  const [cols] = await db.query(
    `SELECT COLUMN_NAME AS name FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'import_records'`
  );
  const have = new Set((cols || []).map((c) => String(c.name || "").toLowerCase()));
  const wanted = [
    ["source", "VARCHAR(16) NOT NULL DEFAULT 'bot'"],
    ["matched_count", "INT NOT NULL DEFAULT 0"],
    ["unmatched_count", "INT NOT NULL DEFAULT 0"],
    ["duplicate_rows", "INT NOT NULL DEFAULT 0"],
  ];
  for (const [name, def] of wanted) {
    if (!have.has(name)) {
      await db.query(`ALTER TABLE import_records ADD COLUMN ${name} ${def}`);
    }
  }
}

async function ensureChannelAnchorBindsTable(db) {
  await db.query(
    `CREATE TABLE IF NOT EXISTS channel_anchor_binds (
      id INT NOT NULL AUTO_INCREMENT,
      channel VARCHAR(16) NOT NULL,
      channel_user_id VARCHAR(128) NOT NULL,
      person_id INT NOT NULL,
      douyin_no VARCHAR(64) NOT NULL,
      anchor_id VARCHAR(64) NOT NULL DEFAULT '',
      created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
      updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      PRIMARY KEY (id),
      UNIQUE KEY uk_bind_user (channel, channel_user_id),
      UNIQUE KEY uk_bind_douyin (douyin_no),
      UNIQUE KEY uk_bind_person (person_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`
  );
}

async function ensureAnchorIncomeTables(db) {
  await db.query(
    `CREATE TABLE IF NOT EXISTS anchor_income (
      id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
      period VARCHAR(7) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'YYYY-MM',
      anchor_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
      person_id INT NULL,
      douyin_no VARCHAR(64) NOT NULL DEFAULT '',
      nickname VARCHAR(128) NOT NULL DEFAULT '',
      start_date DATE NULL,
      end_date DATE NULL,
      income_name VARCHAR(64) NOT NULL DEFAULT '',
      fee_type VARCHAR(64) NOT NULL DEFAULT '',
      revenue DECIMAL(14,2) NOT NULL DEFAULT 0 COMMENT '本期流水',
      streamer_ratio VARCHAR(16) NOT NULL DEFAULT '' COMMENT '主播分成比',
      guild_ratio VARCHAR(16) NOT NULL DEFAULT '' COMMENT '公会分成比',
      streamer_income DECIMAL(14,2) NOT NULL DEFAULT 0 COMMENT '主播收入',
      guild_income DECIMAL(14,2) NOT NULL DEFAULT 0 COMMENT '公会收入',
      remark VARCHAR(255) NOT NULL DEFAULT '',
      created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
      updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      PRIMARY KEY (id),
      UNIQUE KEY uk_anchor_income (period, anchor_id),
      KEY idx_anchor_income_period (period),
      KEY idx_anchor_income_person (person_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`
  );
  await db.query(
    `CREATE TABLE IF NOT EXISTS anchor_income_profiles (
      person_id INT NOT NULL,
      join_date VARCHAR(32) NOT NULL DEFAULT '' COMMENT '入会时间（原文，如 2023年/4/5）',
      opening_total DECIMAL(14,2) NOT NULL DEFAULT 0 COMMENT '期初累计个人收益',
      note VARCHAR(255) NOT NULL DEFAULT '',
      updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      PRIMARY KEY (person_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`
  );
}

async function ensurePersonDailyReportVisibilityColumn(db) {
  const [rows] = await db.query(
    `SELECT column_name AS name
       FROM information_schema.columns
      WHERE table_schema = DATABASE()
        AND table_name = 'persons'
        AND column_name = 'hide_in_daily_report'
      LIMIT 1`
  );
  if (rows.length > 0) return false;

  try {
    await db.query(
      "ALTER TABLE persons ADD COLUMN hide_in_daily_report TINYINT(1) NOT NULL DEFAULT 0 AFTER generation"
    );
    return true;
  } catch (error) {
    if (error?.code === "ER_DUP_FIELDNAME") return false;
    throw error;
  }
}

const REQUIRED_INDEXES = [
  { table: "persons", name: "idx_persons_master", columns: ["master_id"] },
  { table: "persons", name: "idx_persons_generation", columns: ["generation"] },
  { table: "accounts", name: "idx_accounts_anchor", columns: ["anchor_id"] },
  { table: "accounts", name: "idx_accounts_person", columns: ["person_id"] },
  { table: "accounts", name: "idx_accounts_person_primary", columns: ["person_id", "is_primary"] },
  { table: "wave_snapshots", name: "idx_wave_anchor_date", columns: ["anchor_id", "import_date"] },
  { table: "wave_snapshots", name: "idx_wave_date", columns: ["import_date"] },
  { table: "duration_snapshots", name: "idx_duration_anchor_date", columns: ["anchor_id", "import_date"] },
  { table: "duration_snapshots", name: "idx_duration_date", columns: ["import_date"] },
  { table: "flag_scores", name: "idx_flag_scores_master_period", columns: ["master_id", "period"] },
  { table: "flag_winners", name: "idx_flag_winners_period", columns: ["period"] },
];

async function ensureDatabaseIndexes(db, existingTables = null) {
  const availableTables = existingTables || (await getExistingTables(db));
  const created = [];
  const present = [];
  const failed = [];

  for (const spec of REQUIRED_INDEXES) {
    if (!availableTables.has(spec.table)) continue;
    try {
      const exists = await hasCoveringIndex(db, spec.table, spec.columns);
      if (exists) {
        present.push(spec.name);
        continue;
      }
      const columnSql = spec.columns.map((col) => `\`${col}\``).join(", ");
      await db.query(`CREATE INDEX \`${spec.name}\` ON \`${spec.table}\` (${columnSql})`);
      created.push(spec.name);
    } catch (error) {
      if (error?.code === "ER_DUP_KEYNAME") {
        present.push(spec.name);
      } else {
        failed.push(`${spec.table}.${spec.name}: ${error?.message || String(error)}`);
      }
    }
  }

  return { created, present, failed };
}

async function getExistingTables(db) {
  const [rows] = await db.query(
    `SELECT table_name AS name
       FROM information_schema.tables
      WHERE table_schema = DATABASE()`
  );
  return new Set(rows.map((row) => row.name));
}

async function hasCoveringIndex(db, table, columns) {
  const [rows] = await db.query(
    `SELECT index_name AS indexName, seq_in_index AS seq, column_name AS columnName
       FROM information_schema.statistics
      WHERE table_schema = DATABASE()
        AND table_name = ?
      ORDER BY index_name, seq_in_index`,
    [table]
  );

  const byIndex = new Map();
  for (const row of rows) {
    const list = byIndex.get(row.indexName) || [];
    list.push({ seq: Number(row.seq), columnName: row.columnName });
    byIndex.set(row.indexName, list);
  }

  for (const list of byIndex.values()) {
    const ordered = list.sort((a, b) => a.seq - b.seq).map((item) => item.columnName);
    const covers = columns.every((col, index) => ordered[index] === col);
    if (covers) return true;
  }
  return false;
}

module.exports = {
  ensureDatabaseIndexes,
  ensureImportRecordsTable,
  ensureChannelAnchorBindsTable,
  ensureAnchorIncomeTables,
  ensurePersonDailyReportVisibilityColumn,
};
