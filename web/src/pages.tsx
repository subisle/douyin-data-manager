import { useCallback, useEffect, useState } from "react";
import { api, fmtMinutes, fmtWave, type AnchorImportResult, type AnchorPreviewRow, type ImportLogRow, type MonthlyRow, type Person, type YearlyRow } from "./api";

// 统一的加载态封装：每个页面都要 loading / error / reload，别复制五遍。
export function useLoad<T>(loader: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setData(await loader());
    } catch (e) {
      setError((e as Error).message);
      setData(null);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  useEffect(() => {
    void load();
  }, [load]);

  return { data, error, loading, reload: load };
}

const todayLocal = () => {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

const medal = (i: number) =>
  i === 0 ? <span className="medal">🥇</span> : i === 1 ? <span className="medal">🥈</span> : i === 2 ? <span className="medal">🥉</span> : String(i + 1).padStart(2, "0");

const genderLabel: Record<string, string> = { male: "男团", female: "女队", unknown: "未标注" };

function Status({ loading, error }: { loading: boolean; error: string }) {
  if (loading) return <p className="muted">加载中…</p>;
  if (error) return <p className="err">出错了：{error}</p>;
  return null;
}

/* ------------------------------- 主播管理 ------------------------------- */

export function PersonsPage() {
  const { data, error, loading, reload } = useLoad(() => api.listPersons(), []);
  const [name, setName] = useState("");
  const [gender, setGender] = useState("female");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const create = async () => {
    if (!name.trim()) return;
    setBusy(true);
    setMsg("");
    try {
      await api.createPerson({ name: name.trim(), gender: gender as Person["gender"] });
      setName("");
      await reload();
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (p: Person) => {
    if (!confirm(`确定删除「${p.name}」？历史数据保留，只是不再出现在榜单里。`)) return;
    await api.deletePerson(p.id);
    await reload();
  };

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>主播管理</h3>
      <div className="toolbar">
        <input placeholder="主播姓名" value={name} onChange={(e) => setName(e.target.value)} />
        <select value={gender} onChange={(e) => setGender(e.target.value)}>
          <option value="female">女队</option>
          <option value="male">男团</option>
          <option value="unknown">未标注</option>
        </select>
        <button onClick={create} disabled={busy || !name.trim()}>
          新增主播
        </button>
        <button className="ghost" onClick={() => void reload()}>
          刷新
        </button>
        {msg && <span className="err">{msg}</span>}
      </div>

      <Status loading={loading} error={error} />

      <table>
        <thead>
          <tr>
            <th style={{ width: 60 }}>ID</th>
            <th>姓名</th>
            <th>性别</th>
            <th>状态</th>
            <th>分组</th>
            <th>隐藏</th>
            <th style={{ width: 90 }}>操作</th>
          </tr>
        </thead>
        <tbody>
          {(data ?? []).map((p) => (
            <tr key={p.id}>
              <td className="muted">{p.id}</td>
              <td>{p.name}</td>
              <td>{genderLabel[p.gender] ?? p.gender}</td>
              <td>{p.status}</td>
              <td className="muted">{p.groupName ?? "—"}</td>
              <td className="muted">{p.hideInDailyReport ? "是" : "否"}</td>
              <td>
                <button className="ghost danger" onClick={() => void remove(p)}>
                  删除
                </button>
              </td>
            </tr>
          ))}
          {data && data.length === 0 && (
            <tr>
              <td colSpan={7} className="muted">
                还没有主播，先在上面加一个，再去「数据导入」页绑定抖音号。
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <BindAccount onDone={() => void reload()} persons={data ?? []} />
      <AnchorCsvImport onDone={() => void reload()} />
    </div>
  );
}

// 从 CSV 批量导入主播：选文件 → 预览勾选（多选）→ 每行可改自定义名字 → 建档绑号。
// 榜单 CSV（带音浪列的）也能用：只提取「主播 ID / 抖音号 / 姓名」身份列。
function AnchorCsvImport({ onDone }: { onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<(AnchorPreviewRow & { selected: boolean; customName: string })[]>([]);
  const [fileName, setFileName] = useState("");
  const [gender, setGender] = useState("female");
  const [showBound, setShowBound] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<AnchorImportResult | null>(null);

  const boundCount = items.filter((it) => it.bound).length;
  const visible = showBound ? items : items.filter((it) => !it.bound);
  const selected = items.filter((it) => it.selected && !it.bound);
  const patch = (idx: number, patchObj: Partial<{ selected: boolean; customName: string }>) =>
    setItems((prev) => prev.map((x, i) => (i === idx ? { ...x, ...patchObj } : x)));

  const onFile = async (file: File | null) => {
    if (!file) return;
    setErr("");
    setResult(null);
    setFileName(file.name);
    try {
      const buf = await file.arrayBuffer();
      let text: string;
      try {
        text = new TextDecoder("utf-8", { fatal: true }).decode(buf);
      } catch {
        text = new TextDecoder("gb18030").decode(buf);
      }
      text = text.replace(/^\uFEFF/, "");
      const preview = await api.previewAnchors(text);
      setItems(preview.items.map((it) => ({ ...it, selected: !it.bound, customName: it.name })));
    } catch (e) {
      setItems([]);
      setErr((e as Error).message);
    }
  };

  const run = async () => {
    if (!selected.length) return;
    setBusy(true);
    setErr("");
    setResult(null);
    try {
      const res = await api.importAnchors(
        gender,
        selected.map((it) => ({
          name: it.customName.trim() || it.name,
          anchorId: it.anchorId,
          douyinNo: it.douyinNo || it.anchorId,
        })),
      );
      setResult(res);
      onDone();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="toolbar" style={{ marginTop: 12 }}>
        <button className="ghost" onClick={() => setOpen(!open)}>
          {open ? "收起 CSV 导入" : "从 CSV 导入主播"}
        </button>
        <span className="muted">从榜单/名单 CSV 里勾选主播批量建档，名字可改。</span>
      </div>

      {open && (
        <div className="panel" style={{ marginTop: 8 }}>
          <div className="toolbar">
            <input type="file" accept=".csv,text/csv" onChange={(e) => void onFile(e.target.files?.[0] ?? null)} />
            <select value={gender} onChange={(e) => setGender(e.target.value)}>
              <option value="female">女队</option>
              <option value="male">男团</option>
              <option value="unknown">未标注</option>
            </select>
            <button className="ghost" disabled={!items.length} onClick={() => setItems(items.map((it) => ({ ...it, selected: !it.bound })))}>
              全选
            </button>
            <button className="ghost" disabled={!items.length} onClick={() => setItems(items.map((it) => ({ ...it, selected: false })))}>
              全不选
            </button>
          </div>

          {fileName && (
            <p className="muted">
              已解析 {fileName}：共 {items.length} 个主播，其中 {boundCount} 个已在库里（默认隐藏不重复导入），
              待导入 {items.length - boundCount} 个，已勾选 {selected.length} 个。「导入名字」列可改成你想要的名字。
            </p>
          )}
          {boundCount > 0 && (
            <div className="toolbar">
              <label style={{ cursor: "pointer" }}>
                <input type="checkbox" checked={showBound} onChange={(e) => setShowBound(e.target.checked)} /> 显示已在库中的
              </label>
            </div>
          )}
          {err && <p className="err">{err}</p>}
          {result && (
            <p className="muted">
              导入完成：新建 {result.created} 人，已有主播加号 {result.bound} 个，重复跳过 {result.already} 条，失败 {result.failed} 条。
              {result.alreadyDetail.map((d) => `「${d.id}」已绑给 ${d.owner}`).join("；")}
              {result.failedDetail.map((d) => `「${d.name}」失败：${d.error}`).join("；")}
            </p>
          )}

          {items.length > 0 && (
            <>
              <table>
                <thead>
                  <tr>
                    <th style={{ width: 44 }}>选</th>
                    <th>CSV 原名</th>
                    <th>导入名字（可改）</th>
                    <th>主播 ID</th>
                    <th>抖音号</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.map((it) => {
                    const idx = items.indexOf(it);
                    return (
                      <tr key={`${it.rawIndex}-${it.anchorId}`} style={it.bound ? { opacity: 0.5 } : undefined}>
                        <td>
                          <input
                            type="checkbox"
                            checked={it.selected && !it.bound}
                            disabled={it.bound}
                            onChange={(e) => patch(idx, { selected: e.target.checked })}
                          />
                        </td>
                        <td className="muted">{it.name}</td>
                        <td>
                          {it.bound ? (
                            <span className="muted">已在库中{it.boundTo ? `（绑给 ${it.boundTo}）` : ""}</span>
                          ) : (
                            <input value={it.customName} onChange={(e) => patch(idx, { customName: e.target.value })} />
                          )}
                        </td>
                        <td className="muted">{it.anchorId}</td>
                        <td className="muted">{it.douyinNo || "—"}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
              <div className="toolbar" style={{ marginTop: 8 }}>
                <button onClick={run} disabled={busy || !selected.length}>
                  {busy ? "导入中…" : `导入所选（${selected.length}）`}
                </button>
              </div>
            </>
          )}
        </div>
      )}
    </>
  );
}

function BindAccount({ persons, onDone }: { persons: Person[]; onDone: () => void }) {
  const [personId, setPersonId] = useState("");
  const [anchorId, setAnchorId] = useState("");
  const [anchorName, setAnchorName] = useState("");
  const [msg, setMsg] = useState("");

  const bind = async () => {
    if (!personId || !anchorId.trim()) return;
    try {
      await api.bindAccount(Number(personId), {
        anchorId: anchorId.trim(),
        anchorName: anchorName.trim(),
        isPrimary: true,
      });
      setAnchorId("");
      setAnchorName("");
      setMsg("绑定成功");
      onDone();
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  return (
    <>
      <h4 style={{ marginBottom: 8 }}>绑定抖音账号</h4>
      <div className="toolbar">
        <select value={personId} onChange={(e) => setPersonId(e.target.value)}>
          <option value="">选择主播…</option>
          {persons.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <input placeholder="抖音采集 ID (anchorId)" value={anchorId} onChange={(e) => setAnchorId(e.target.value)} />
        <input placeholder="抖音昵称（可选）" value={anchorName} onChange={(e) => setAnchorName(e.target.value)} />
        <button onClick={() => void bind()} disabled={!personId || !anchorId.trim()}>
          绑定
        </button>
        {msg && <span className="muted">{msg}</span>}
      </div>
      <p className="muted" style={{ fontSize: 12 }}>
        导入快照时按 anchorId 认人。没绑过的主播在导入时会被跳过，并在结果里列出来。
      </p>
    </>
  );
}

/* -------------------------------- 日榜 -------------------------------- */

export function DailyPage() {
  const [date, setDate] = useState(todayLocal());
  const [gender, setGender] = useState("");
  const { data, error, loading } = useLoad(() => api.daily(date, gender || undefined), [date, gender]);

  const rows = data ?? [];
  const notLive = rows.filter((r) => !r.isLive).length;

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>日榜</h3>
      <div className="toolbar">
        <input type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        <select value={gender} onChange={(e) => setGender(e.target.value)}>
          <option value="">全部</option>
          <option value="male">男团</option>
          <option value="female">女队</option>
        </select>
        <span className="muted">
          共 {rows.length} 人 · 未开播 {notLive} 人
        </span>
      </div>

      <Status loading={loading} error={error} />

      <table>
        <thead>
          <tr>
            <th style={{ width: 56 }}>排名</th>
            <th>主播</th>
            <th>师傅</th>
            <th className="num">日音浪</th>
            <th className="num">累计总音浪</th>
            <th className="num">时长</th>
            <th style={{ width: 60 }}>等级</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={r.personId} className={r.isLive ? "" : "inactive"}>
              <td>{medal(i)}</td>
              <td>{r.name}</td>
              <td className="muted">{r.masterName ?? "—"}</td>
              <td className="num">
                {r.isLive ? fmtWave(r.wave) : "未开播"}
                {r.isLive && !r.waveReliable && (
                  <span className="muted" title={`跨 ${r.waveSpan} 天的区间增量，中间有漏采`}>
                    {" "}
                    *
                  </span>
                )}
              </td>
              <td className="num muted">{fmtWave(r.cumulativeWave)}</td>
              <td className="num">{fmtMinutes(r.minutes)}</td>
              <td>{r.tier ? <span className="tier">{r.tier}</span> : "—"}</td>
            </tr>
          ))}
          {rows.length === 0 && !loading && (
            <tr>
              <td colSpan={7} className="muted">
                这一天没有数据。先去「数据导入」导入快照，或改个日期试试。
              </td>
            </tr>
          )}
        </tbody>
      </table>
      <p className="muted" style={{ fontSize: 12 }}>
        * 表示这几天里有漏采，日音浪是跨天的区间增量，月/年总量仍然正确。
      </p>
    </div>
  );
}

/* -------------------------------- 月榜 -------------------------------- */

export function MonthlyPage() {
  const [period, setPeriod] = useState(todayLocal().slice(0, 7));
  const [gender, setGender] = useState("");
  const { data, error, loading } = useLoad(() => api.monthly(period, gender || undefined), [period, gender]);

  const rows = (data ?? []) as MonthlyRow[];
  const totalWave = rows.reduce((s, r) => s + r.wave, 0);
  const totalMinutes = rows.reduce((s, r) => s + r.minutes, 0);

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>月榜（月音浪 · 月直播时长）</h3>
      <div className="toolbar">
        <input type="month" value={period} onChange={(e) => setPeriod(e.target.value)} />
        <select value={gender} onChange={(e) => setGender(e.target.value)}>
          <option value="">全部</option>
          <option value="male">男团</option>
          <option value="female">女队</option>
        </select>
        <span className="muted">
          合计 {fmtWave(totalWave)} 音浪 · {fmtMinutes(totalMinutes)}
        </span>
      </div>

      <Status loading={loading} error={error} />

      <table>
        <thead>
          <tr>
            <th style={{ width: 56 }}>排名</th>
            <th>主播</th>
            <th className="num">月音浪</th>
            <th className="num">月时长</th>
            <th className="num">开播天数</th>
            <th className="num">未播天数</th>
            <th className="num">日均</th>
            <th className="num">最高单日</th>
            <th style={{ width: 60 }}>等级</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={r.personId}>
              <td>{medal(i)}</td>
              <td>{r.name}</td>
              <td className="num">{fmtWave(r.wave)}</td>
              <td className="num">{r.formattedDuration || fmtMinutes(r.minutes)}</td>
              <td className="num">{r.liveDays}</td>
              <td className="num muted">{r.absentDays}</td>
              <td className="num muted">{fmtWave(r.avgWavePerLiveDay)}</td>
              <td className="num">{fmtWave(r.bestDayWave)}</td>
              <td>{r.tier ? <span className="tier">{r.tier}</span> : "—"}</td>
            </tr>
          ))}
          {rows.length === 0 && !loading && (
            <tr>
              <td colSpan={9} className="muted">
                这个月还没有数据。
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/* ------------------------------- 年度汇总 ------------------------------- */

export function YearlyPage() {
  const [year, setYear] = useState(String(new Date().getFullYear()));
  const [gender, setGender] = useState("");
  const { data, error, loading } = useLoad(() => api.yearly(year, gender || undefined), [year, gender]);

  const rows = (data ?? []) as YearlyRow[];

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>年度汇总</h3>
      <div className="toolbar">
        <input
          type="number"
          value={year}
          onChange={(e) => setYear(e.target.value)}
          style={{ width: 110 }}
          min={2000}
          max={9999}
        />
        <select value={gender} onChange={(e) => setGender(e.target.value)}>
          <option value="">全部</option>
          <option value="male">男团</option>
          <option value="female">女队</option>
        </select>
      </div>

      <Status loading={loading} error={error} />

      <table>
        <thead>
          <tr>
            <th style={{ width: 56 }}>排名</th>
            <th>主播</th>
            <th className="num">全年音浪</th>
            <th className="num">全年时长</th>
            <th className="num">开播天数</th>
            <th className="num">活跃月数</th>
            <th>最佳月</th>
            <th className="num">最高单日</th>
            <th className="num">月均</th>
            <th style={{ width: 60 }}>等级</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={r.personId}>
              <td>{medal(i)}</td>
              <td>{r.name}</td>
              <td className="num">{fmtWave(r.wave)}</td>
              <td className="num">{r.formattedDuration || fmtMinutes(r.minutes)}</td>
              <td className="num">{r.liveDays}</td>
              <td className="num muted">{r.activeMonths}</td>
              <td className="muted">{r.bestMonth ?? "—"}</td>
              <td className="num">{fmtWave(r.bestDayWave)}</td>
              <td className="num muted">{fmtWave(r.avgMonthWave)}</td>
              <td>{r.tier ? <span className="tier">{r.tier}</span> : "—"}</td>
            </tr>
          ))}
          {rows.length === 0 && !loading && (
            <tr>
              <td colSpan={10} className="muted">
                这一年还没有数据。
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/* ------------------------------- 数据导入 ------------------------------- */

const sample = {
  date: todayLocal(),
  source: "manual",
  operator: "web",
  waves: [{ anchorId: "ANCHOR_ID_1", waveValue: 123456, rank: 1 }],
  durations: [{ anchorId: "ANCHOR_ID_1", minutes: 480 }],
};

export function ImportPage() {
  const [payload, setPayload] = useState(JSON.stringify(sample, null, 2));
  const [result, setResult] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setErr("");
    setResult("");
    try {
      let body: unknown;
      try {
        body = JSON.parse(payload);
      } catch {
        throw new Error("JSON 格式不对，检查一下括号和逗号");
      }
      const r = await api.importSnapshots(body);
      setResult(
        `导入成功：${r.imported} 条快照，覆盖 ${r.persons} 位主播` +
          (r.skipped.length ? `；未识别的账号 ${r.skipped.length} 个：${r.skipped.join("、")}` : ""),
      );
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const recompute = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await api.recompute({});
      setResult(`已重算最近 30 天，共 ${r.persons} 位主播`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>数据导入</h3>
      <p className="muted" style={{ marginTop: 0 }}>
        平台给的是<b>累计</b>音浪和累计时长，后端会自动差分出日音浪。重复导入同一天会覆盖旧值，
        导入完立即重算当天指标。
      </p>

      <textarea
        value={payload}
        onChange={(e) => setPayload(e.target.value)}
        rows={14}
        style={{ width: "100%", fontFamily: "ui-monospace, Menlo, Consolas, monospace", fontSize: 12 }}
      />

      <div className="toolbar" style={{ marginTop: 12 }}>
        <button onClick={() => void submit()} disabled={busy}>
          导入
        </button>
        <button className="ghost" onClick={() => void recompute()} disabled={busy}>
          重算最近 30 天
        </button>
        {result && <span className="ok">{result}</span>}
        {err && <span className="err">{err}</span>}
      </div>

      <div className="notice" style={{ marginTop: 12 }}>
        anchorId 必须先在主播管理页绑定，否则该行会被跳过（导入结果里会列出）。
      </div>
    </div>
  );
}


/* ------------------------------- 导入日志 ------------------------------- */

const kindLabel: Record<string, string> = { wave: "音浪", duration: "时长" };
const sourceLabel: Record<string, string> = { bot: "机器人", web: "网页" };

const friendlyDay = (iso: string | null | undefined) => {
  if (!iso) return "—";
  const text = String(iso).slice(0, 10);
  const [y, m, d] = text.split("-");
  if (!y || !m || !d) return text;
  return `${Number(m)}月${Number(d)}日`;
};

const friendlyTime = (iso: string | null | undefined) =>
  iso ? String(iso).replace("T", " ").slice(0, 16) : "—";

export function ImportLogsPage() {
  const { data, error, loading, reload } = useLoad(() => api.listImportLogs(100), []);

  return (
    <section className="panel">
      <h3 style={{ marginTop: 0 }}>导入日志</h3>
      <div className="toolbar">
        <button onClick={reload}>刷新</button>
      </div>
      <Status loading={loading} error={error} />
      {data && data.length === 0 && (
        <p className="muted">还没有导入记录。发 CSV 或在「数据导入」页导入即可生成日志。</p>
      )}
      {data && data.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>时间</th>
              <th>数据日期</th>
              <th>类型</th>
              <th>文件</th>
              <th>行数</th>
              <th>匹配</th>
              <th>未匹配</th>
              <th>来源</th>
            </tr>
          </thead>
          <tbody>
            {data.map((log: ImportLogRow) => (
              <tr key={log.id}>
                <td className="muted">{friendlyTime(log.created_at)}</td>
                <td>{friendlyDay(log.import_date)}</td>
                <td>{kindLabel[log.kind] ?? log.kind}</td>
                <td className="muted" style={{ maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis" }}>
                  {log.file_name || "—"}
                </td>
                <td>{log.row_count}</td>
                <td>{log.matched_count}</td>
                <td>
                  {log.unmatched_count}
                  {log.duplicate_rows > 0 ? <span className="muted">（重复 {log.duplicate_rows}）</span> : null}
                </td>
                <td className="muted">{sourceLabel[log.source] ?? log.source}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
