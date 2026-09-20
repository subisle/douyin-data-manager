const assert = require("node:assert/strict");
const test = require("node:test");
const { __testing } = require("./db");

const META = {
  fileHash: "a".repeat(32),
  dataHash: "b".repeat(64),
  fileName: "snapshot.csv",
  rowCount: 1,
};

function createDatabaseMock(options = {}) {
  const events = [];
  const errorAt = options.errorAt || null;
  const errors = {
    duplicate: new Error("文件 MD5 已导入过，已阻止重复导入"),
    upsert: new Error("upsert failed"),
    record: new Error("record failed"),
    commit: new Error("commit failed"),
    rollback: new Error("rollback failed"),
  };

  const conn = {
    async beginTransaction() {
      events.push("begin");
    },
    async query(sql, params) {
      if (/^SELECT file_hash/.test(sql.trim())) {
        events.push("assert");
        if (errorAt === "duplicate") {
          return [[{ file_hash: META.fileHash, data_hash: META.dataHash }]];
        }
        return [[]];
      }
      if (/^INSERT INTO (wave|duration)_snapshots/.test(sql.trim())) {
        events.push("upsert");
        if (errorAt === "upsert") throw errors.upsert;
        assert.ok(Array.isArray(params[0]));
        return [{ affectedRows: options.affectedRows ?? 1 }];
      }
      if (/^DELETE FROM duration_snapshots/.test(sql.trim())) {
        events.push("clear-month");
        options.onClearMonth?.(sql, params);
        return [{ affectedRows: 0 }];
      }
      if (/^INSERT INTO import_records/.test(sql.trim())) {
        events.push("record");
        if (errorAt === "record") throw errors.record;
        return [{ affectedRows: 1 }];
      }
      throw new Error(`Unexpected connection query: ${sql}`);
    },
    async commit() {
      events.push("commit");
      if (errorAt === "commit") throw errors.commit;
    },
    async rollback() {
      events.push("rollback");
      if (errorAt === "rollback") throw errors.rollback;
    },
    release() {
      events.push("release");
    },
  };

  const db = {
    async query(sql) {
      if (/^SELECT COLUMN_NAME/.test(sql.trim())) {
        // ensureImportRecordsTable 幂等加列检查：返回全列已存在，跳过 ALTER
        return [[
          { name: "id" }, { name: "kind" }, { name: "import_date" },
          { name: "file_hash" }, { name: "data_hash" }, { name: "file_name" },
          { name: "row_count" }, { name: "created_at" }, { name: "source" },
          { name: "matched_count" }, { name: "unmatched_count" }, { name: "duplicate_rows" },
        ]];
      }
      assert.match(sql, /^CREATE TABLE IF NOT EXISTS import_records/);
      events.push("ensure-schema");
      return [{ affectedRows: 0 }];
    },
    async getConnection() {
      events.push("get-connection");
      return conn;
    },
  };

  return { db, events, errors };
}

test("wave import uses one connection and commits after recording metadata", async () => {
  const { db, events } = createDatabaseMock({ affectedRows: 2 });

  const result = await __testing.importSnapshotRows(
    db,
    "wave",
    "2026-07-22",
    [{ anchorId: "anchor-a", waveValue: 1200, rank: 1 }],
    META
  );

  assert.deepEqual(result, { inserted: 2 });
  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "upsert",
    "record",
    "commit",
    "release",
  ]);
});

test("duplicate detection rolls back before the snapshot UPSERT", async () => {
  const { db, events } = createDatabaseMock({ errorAt: "duplicate" });

  await assert.rejects(
    __testing.importSnapshotRows(
      db,
      "wave",
      "2026-07-22",
      [{ anchorId: "anchor-a", waveValue: 1200, rank: 1 }],
      META
    ),
    /已导入过/
  );

  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "rollback",
    "release",
  ]);
});

test("duration UPSERT failure rolls back and releases the connection", async () => {
  const { db, events, errors } = createDatabaseMock({ errorAt: "upsert" });

  await assert.rejects(
    __testing.importSnapshotRows(
      db,
      "duration",
      "2026-07-22",
      [{ anchorId: "anchor-a", totalMinutes: 360 }],
      META
    ),
    (error) => error === errors.upsert
  );

  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "clear-month",
    "upsert",
    "rollback",
    "release",
  ]);
});

test("import record failure rolls back the completed snapshot UPSERT", async () => {
  const { db, events, errors } = createDatabaseMock({ errorAt: "record" });

  await assert.rejects(
    __testing.importSnapshotRows(
      db,
      "wave",
      "2026-07-22",
      [{ anchorId: "anchor-a", waveValue: 1200, rank: 1 }],
      META
    ),
    (error) => error === errors.record
  );

  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "upsert",
    "record",
    "rollback",
    "release",
  ]);
});

test("commit failure attempts rollback and still releases the connection", async () => {
  const { db, events, errors } = createDatabaseMock({ errorAt: "commit" });

  await assert.rejects(
    __testing.importSnapshotRows(
      db,
      "wave",
      "2026-07-22",
      [{ anchorId: "anchor-a", waveValue: 1200, rank: 1 }],
      META
    ),
    (error) => error === errors.commit
  );

  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "upsert",
    "record",
    "commit",
    "rollback",
    "release",
  ]);
});

test("rollback failure preserves the import error and releases the connection", async () => {
  const { db, events, errors } = createDatabaseMock({ errorAt: "rollback" });
  const originalQuery = db.getConnection;
  db.getConnection = async () => {
    const conn = await originalQuery();
    const originalRecordQuery = conn.query;
    conn.query = async (sql, params) => {
      if (/^INSERT INTO import_records/.test(sql.trim())) throw errors.record;
      return originalRecordQuery(sql, params);
    };
    return conn;
  };

  await assert.rejects(
    __testing.importSnapshotRows(
      db,
      "wave",
      "2026-07-22",
      [{ anchorId: "anchor-a", waveValue: 1200, rank: 1 }],
      META
    ),
    (error) => error === errors.record && error.rollbackError === errors.rollback
  );

  assert.deepEqual(events.slice(-2), ["rollback", "release"]);
});

test("normalizeDurationImportDate maps YYYY-MM to the month's last day", () => {
  assert.equal(__testing.normalizeDurationImportDate("2026-07"), "2026-07-31");
  assert.equal(__testing.normalizeDurationImportDate("2026-02"), "2026-02-28");
  assert.equal(__testing.normalizeDurationImportDate("2028-02"), "2028-02-29");
  assert.equal(__testing.normalizeDurationImportDate("2026-12"), "2026-12-31");
});

test("normalizeDurationImportDate maps YYYY-MM-DD into its month's last day", () => {
  assert.equal(__testing.normalizeDurationImportDate("2026-07-22"), "2026-07-31");
  assert.equal(__testing.normalizeDurationImportDate("2026-07-01"), "2026-07-31");
});

test("normalizeDurationImportDate rejects malformed input", () => {
  assert.throws(() => __testing.normalizeDurationImportDate("2026-13"), /无效的时长导入月份/);
  assert.throws(() => __testing.normalizeDurationImportDate("2026-7"), /无效的时长导入日期/);
  assert.throws(() => __testing.normalizeDurationImportDate("2026-07-32"), /无效的时长导入日期/);
  assert.throws(() => __testing.normalizeDurationImportDate(""), /无效的时长导入日期/);
});

test("duration import normalizes the month to the last day before UPSERT", async () => {
  const { db, events } = createDatabaseMock({ affectedRows: 2 });

  const result = await __testing.importSnapshotRows(
    db,
    "duration",
    "2026-07",
    [{ anchorId: "anchor-a", totalMinutes: 360 }],
    META
  );

  assert.deepEqual(result, { inserted: 2 });
  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "clear-month",
    "upsert",
    "record",
    "commit",
    "release",
  ]);
});

test("duration import rejects a non-date month value", async () => {
  const { db } = createDatabaseMock();

  await assert.rejects(
    __testing.importSnapshotRows(
      db,
      "duration",
      "2026-7",
      [{ anchorId: "anchor-a", totalMinutes: 360 }],
      META
    ),
    /无效的时长导入日期/
  );
});

test("duration month import clears the whole month for imported anchors before UPSERT", async () => {
  let cleared = null;
  const { db, events } = createDatabaseMock({
    affectedRows: 2,
    onClearMonth: (sql, params) => {
      cleared = { sql, params };
    },
  });

  const result = await __testing.importSnapshotRows(
    db,
    "duration",
    "2026-07",
    [
      { anchorId: "anchor-a", totalMinutes: 360 },
      { anchorId: "anchor-b", totalMinutes: 720 },
      { anchorId: "anchor-a", totalMinutes: 420 },
    ],
    META
  );

  assert.deepEqual(result, { inserted: 2 });
  assert.deepEqual(events, [
    "ensure-schema",
    "get-connection",
    "begin",
    "assert",
    "clear-month",
    "upsert",
    "record",
    "commit",
    "release",
  ]);
  assert.match(cleared.sql, /^DELETE FROM duration_snapshots/);
  assert.match(cleared.sql, /import_date BETWEEN \? AND \?/);
  // 月份范围 2026-07-01 ~ 2026-07-31 + 去重后的主播
  assert.deepEqual(cleared.params, ["2026-07-01", "2026-07-31", "anchor-a", "anchor-b"]);
});

test("wave import never clears monthly rows", async () => {
  let cleared = false;
  const { db, events } = createDatabaseMock({
    affectedRows: 1,
    onClearMonth: () => {
      cleared = true;
    },
  });

  await __testing.importSnapshotRows(
    db,
    "wave",
    "2026-07-22",
    [{ anchorId: "anchor-a", waveValue: 1200, rank: 1 }],
    META
  );

  assert.equal(cleared, false);
  assert.ok(!events.includes("clear-month"));
});
