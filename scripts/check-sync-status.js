// 合并主播的旧 anchor 快照归狼彬（person 122），写入 Go 新表
const mysql = require('mysql2/promise');

const OWNERS = {
  '7433044807845119038': 122, // 狼森（合并入狼彬）
  '995522614658570': 122,     // 狼彬旧号
  '64212739559257': 122,      // 狼晴（合并入狼彬）
  '7621097353860498491': 122, // 啸言（合并入狼彬）
  '3062563069497744': 122,    // 狼月（合并入狼彬）
};

async function main() {
  const conn = await mysql.createConnection({
    host: 'mysql7.sqlpub.com', port: 3312, user: 'douyinxs',
    password: 'WABZfpfGGlPSxlrs', database: 'douyinxs',
    dateStrings: true, connectTimeout: 15000,
  });
  try {
    const [p] = await conn.query('SELECT id, name FROM person WHERE id = 122');
    if (!p.length) { console.error('person 122 不存在，中止'); return; }
    console.log('归属人:', JSON.stringify(p[0]));

    const anchorIds = Object.keys(OWNERS);
    const [waves] = await conn.query(
      'SELECT anchor_id, import_date, wave_value, `rank` FROM wave_snapshots WHERE anchor_id IN (?)', [anchorIds]);
    const [durs] = await conn.query(
      'SELECT anchor_id, import_date, total_minutes FROM duration_snapshots WHERE anchor_id IN (?)', [anchorIds]);

    for (const r of waves) {
      await conn.query(
        `INSERT INTO wave_snapshot (anchor_id, person_id, biz_date, wave_value, rank_in_guild)
         VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE
         person_id = VALUES(person_id), wave_value = VALUES(wave_value), rank_in_guild = VALUES(rank_in_guild)`,
        [r.anchor_id, 122, r.import_date, r.wave_value, r.rank]);
    }
    for (const r of durs) {
      await conn.query(
        `INSERT INTO duration_snapshot (anchor_id, person_id, biz_date, cumulative_minutes)
         VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE
         person_id = VALUES(person_id), cumulative_minutes = VALUES(cumulative_minutes)`,
        [r.anchor_id, 122, r.import_date, r.total_minutes]);
    }
    console.log(`音浪快照写入: ${waves.length} 行`);
    console.log(`时长快照写入: ${durs.length} 行`);

    for (const t of ['wave_snapshot', 'duration_snapshot']) {
      const [r] = await conn.query(`SELECT COUNT(*) c FROM ${t}`);
      console.log(`count ${t}: ${r[0].c}`);
    }
  } finally { await conn.end(); }
}
main().catch(e => { console.error('ERR', e.message); process.exit(1); });
