// 一键同步：远端 sqlpub(615 staging 主档) → 盒子本地库 → sync-615 → 重算
// 用法：node scripts/sync-remote-to-box.js
// 背景（2026-09-21）：主库已迁到盒子本地 MySQL，Electron 仍直写远端 staging 表。
const mysql = require('mysql2/promise');

const REMOTE = { host: 'mysql7.sqlpub.com', port: 3312, user: 'douyinxs', password: 'WABZfpfGGlPSxlrs', database: 'douyinxs' };
const BOX = { host: '192.168.0.13', port: 3306, user: 'douyinxs', password: 'WABZfpfGGlPSxlrs', database: 'douyinxs' };
const BOX_API = 'http://192.168.0.13:3000';
// staging 主档表（Electron 直写远端）；import_records 两边各自演化，不参与刷新
const STAGING = ['persons', 'accounts', 'wave_snapshots', 'duration_snapshots'];

async function main() {
  const remote = await mysql.createConnection({ ...REMOTE, dateStrings: true, connectTimeout: 15000 });
  const box = await mysql.createConnection({ ...BOX, dateStrings: true, connectTimeout: 15000 });
  try {
    // 1) staging 全量刷新：远端 → 盒子（REPLACE 语义，行数小直接干）
    //    旧 615 表带外键（accounts.person_id → persons.id），刷新期间临时关掉
    await box.query('SET FOREIGN_KEY_CHECKS=0');
    for (const t of STAGING) {
      const [rows] = await remote.query(`SELECT * FROM \`${t}\``);
      if (!rows.length) { console.log(`${t}: 远端 0 行，跳过`); continue; }
      const cols = Object.keys(rows[0]);
      const colList = cols.map(c => '`' + c + '`').join(',');
      const ph = cols.map(() => '?').join(',');
      await box.query(`DELETE FROM \`${t}\``);
      const [res] = await box.query(
        `INSERT INTO \`${t}\` (${colList}) VALUES ${rows.map(() => `(${ph})`).join(',')}`,
        rows.flatMap(r => cols.map(c => r[c])));
      console.log(`${t}: 刷新 ${res.affectedRows} 行`);
    }
    await box.query('SET FOREIGN_KEY_CHECKS=1');

    // 2) 触发 sync-615（主播/账号/音浪/时长 → Go 业务表）
    const syncRes = await fetch(`${BOX_API}/api/v1/persons/sync-615`, { method: 'POST' });
    const syncJson = await syncRes.json();
    console.log('sync-615:', JSON.stringify(syncJson.data ?? syncJson));

    // 3) 全量重算（约 3 分钟，同步执行；fetch 要放宽超时）
    console.log('重算中（约 3 分钟）...');
    const reRes = await fetch(`${BOX_API}/api/v1/imports/recompute`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from: '2026-07-01', to: new Date().toISOString().slice(0, 10) }),
      signal: AbortSignal.timeout(600000),
    });
    const reJson = await reRes.json().catch(() => ({}));
    console.log('recompute:', reRes.ok ? JSON.stringify(reJson.data ?? reJson) : `HTTP ${reRes.status}（服务端可能仍在跑，可稍后验证）`);
  } finally {
    await remote.end();
    await box.end();
  }
}
main().catch(e => { console.error('ERR', e.message); process.exit(1); });
