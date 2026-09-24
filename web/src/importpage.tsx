import { useEffect, useMemo, useState } from "react";
import { api, fmtWave, type PreviewResponse, type PreviewRow } from "./api";
import { ImportLogsPage } from "./pages";
import { useNav, type NavParams } from "./nav";

const pad = (n: number) => String(n).padStart(2, "0");
const iso = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

const todayISO = () => iso(new Date());
const shiftISO = (base: string, days: number) => {
  const [y, m, d] = base.split("-").map(Number);
  return iso(new Date(y, m - 1, d + days));
};

const STATUS_TEXT: Record<string, string> = {
  new: "新增",
  changed: "覆盖",
  unchanged: "无变化",
  unmatched: "未建档·已存档",
  duplicate: "重复行",
  skipped: "跳过",
};

const STATUS_CLASS: Record<string, string> = {
  new: "badge badge-ok",
  changed: "badge badge-warn",
  unmatched: "badge badge-info",
  duplicate: "badge badge-warn",
  skipped: "badge badge-muted",
  unchanged: "badge badge-muted",
};

/**
 * 数据导入——对齐 615 的流程，但把顺序显式成四步：
 * 选日期 → 选文件 → 看预览 → 完成。
 *
 * 之所以强制先看预览：覆盖已有数据是危险操作。先把「谁会变、变成多少」
 * 摆出来再让用户确认，比事后回滚便宜得多。
 */
export function ImportPage({ params }: { params?: NavParams }) {
  const [date, setDate] = useState(() => params?.date ?? shiftISO(todayISO(), -1));
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<PreviewResponse | null>(null);
  const [err, setErr] = useState("");
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const [busyHint, setBusyHint] = useState("");
  const [filter, setFilter] = useState("all");
  const [dragging, setDragging] = useState(false);
  const { navigate } = useNav();

  // 别的页带日期跳进来（如清理完带日期回来看）
  useEffect(() => {
    if (params?.date) setDate(params.date);
  }, [params?.date]);

  const step = done ? 4 : preview ? 3 : file ? 2 : 1;
  const isFuture = date >= todayISO();

  // 快捷日期：数据是 T+1 出的，所以默认是昨天；月底回看常用前天/昨天切换
  const quickDates = useMemo(() => {
    const t = todayISO();
    return [
      { label: "前天", value: shiftISO(t, -2) },
      { label: "昨天（默认）", value: shiftISO(t, -1) },
      { label: "今天", value: t },
    ];
  }, []);

  const runPreview = async (f: File | null, d: string) => {
    if (!f) return;
    setBusy(true);
    setBusyHint("解析中…");
    setErr("");
    setDone("");
    try {
      setPreview(await api.previewCSV(f, d));
    } catch (e) {
      setErr((e as Error).message);
      setPreview(null);
    } finally {
      setBusy(false);
      setBusyHint("");
    }
  };

  const pickFile = (f: File | null) => {
    if (f && !/\.(csv|txt)$/i.test(f.name) && f.type !== "text/csv") {
      setErr("只支持 CSV 文件");
      return;
    }
    setFile(f);
    setPreview(null);
    setDone("");
    setErr("");
    if (f) void runPreview(f, date); // 选完自动解析，少点一次按钮
  };

  const commit = async () => {
    if (!file) return;
    const p = preview?.preview;
    const warn = p
      ? `确认导入 ${date} 的${p.kind === "duration" ? "时长" : "音浪"}数据？\n` +
        `${p.changedCount} 行会覆盖已有数据，这一步不可撤销。`
      : "确认导入？";
    if (!window.confirm(warn)) return;
    setBusy(true);
    setBusyHint("写入并重算指标中…");
    setErr("");
    try {
      const r = await api.importCSV(file, date);
      setDone(
        `已导入 ${r.imported} 行 · 影响 ${r.persons} 位主播（新增 ${r.counts.new} / 覆盖 ${r.counts.changed} / 无变化 ${r.counts.unchanged}）` +
          (r.skipped.length ? ` · 跳过 ${r.skipped.length} 个未识别账号` : ""),
      );
      setPreview(null);
      setFile(null);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
      setBusyHint("");
    }
  };

  const rows: PreviewRow[] = preview?.preview.rows ?? [];
  const visible = rows.filter((r) => filter === "all" || r.status === filter);
  const p = preview?.preview;

  return (
    <>
      <div className="steps">
        {[
          { n: 1, label: "选日期" },
          { n: 2, label: "选文件" },
          { n: 3, label: "看预览" },
          { n: 4, label: "完成" },
        ].map((s, i) => (
          <div key={s.n} style={{ display: "flex", alignItems: "center", gap: 4 }}>
            {i > 0 && <span className="step-line" />}
            <span className={step === s.n ? "step on" : step > s.n ? "step done" : "step"}>
              <span className="step-num">{step > s.n ? "✓" : s.n}</span>
              {s.label}
            </span>
          </div>
        ))}
      </div>

      {/* ---------------- 第 1 步：日期 ---------------- */}
      <section className="card">
        <div className="card-head">
          <h4>① 这批数据属于哪一天</h4>
          <span className="tiny muted">音浪与时长是 T+1 出的，所以默认是昨天</span>
        </div>

        <div className="chip-row" style={{ marginBottom: 12 }}>
          {quickDates.map((q) => (
            <button
              key={q.value}
              className={date === q.value ? "chip on" : "chip"}
              onClick={() => {
                setDate(q.value);
                if (file) void runPreview(file, q.value);
              }}
            >
              {q.label}
              <span className="tiny muted">{q.value.slice(5)}</span>
            </button>
          ))}
        </div>

        <div className="toolbar" style={{ marginBottom: 0 }}>
          <label className="field" style={{ minWidth: 200 }}>
            <span className="field-label">精确日期（群里发「9.1」就是直接指定某天）</span>
            <input
              type="date"
              value={date}
              onChange={(e) => {
                setDate(e.target.value);
                if (file) void runPreview(file, e.target.value);
              }}
            />
          </label>
          <span className="spacer" />
          {isFuture && (
            <span className="badge badge-warn">该日期还没到，数据通常次日才出</span>
          )}
        </div>
      </section>

      {/* ---------------- 第 2 步：文件 ---------------- */}
      <section className="card">
        <div className="card-head">
          <h4>② 上传 CSV</h4>
          <span className="tiny muted">靠表头自动识别是音浪表还是时长表</span>
        </div>

        <div
          className={`dropzone${dragging ? " active" : ""}`}
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault();
            setDragging(false);
            pickFile(e.dataTransfer.files?.[0] ?? null);
          }}
          onClick={() => !file && document.getElementById("csv-file-input")?.click()}
        >
          <input
            id="csv-file-input"
            type="file"
            accept=".csv,text/csv"
            onChange={(e) => pickFile(e.target.files?.[0] ?? null)}
            style={{ display: "none" }}
          />
          {file ? (
            <div className="file-pill" style={{ width: "100%" }}>
              <span style={{ fontSize: 16 }}>📄</span>
              <span className="grow">
                <b>{file.name}</b> · {(file.size / 1024).toFixed(1)} KB
              </span>
              <button
                className="ghost btn-sm"
                onClick={(e) => {
                  e.stopPropagation();
                  pickFile(null);
                }}
              >
                换一个
              </button>
            </div>
          ) : (
            <>
              <span className="dropzone-icon">⬇</span>
              <span>把 CSV 拖到这里，或点击选择文件</span>
              <span className="tiny">
                表头支持「主播id / 抖音号 / anchor_id / uid」；数值支持「12.5万」「2:30:45」
              </span>
            </>
          )}
        </div>

        {file && (
          <div className="toolbar" style={{ marginTop: 12, marginBottom: 0 }}>
            <button className="ghost" onClick={() => void runPreview(file, date)} disabled={busy}>
              重新解析
            </button>
            {busy && <span className="tiny muted">{busyHint}</span>}
          </div>
        )}
      </section>

      {/* ---------------- 反馈 ---------------- */}
      {err && <div className="notice notice-danger">{err}</div>}
      {busy && preview === null && <div className="skeleton" style={{ height: 120 }} />}

      {done && (
        <section className="card">
          <div className="card-head">
            <h4>✅ 导入完成</h4>
            <span className="badge badge-ok">{date}</span>
          </div>
          <p style={{ margin: "0 0 12px" }}>{done}</p>
          <div className="chip-row">
            <button className="chip" onClick={() => navigate("daily", { date })}>
              查看 {date} 日榜 →
            </button>
            <button
              className="chip"
              onClick={() => {
                setDone("");
                setPreview(null);
                setFile(null);
              }}
            >
              继续导下一个文件
            </button>
          </div>
        </section>
      )}

      {preview?.duplicate && (
        <div className="notice notice-info">
          <span>
            该文件在 {preview.duplicate.import_date} 已导入过（{preview.duplicate.row_count} 行）。
            支持重复导入：相同数据覆盖更新，不会产生重复行。
          </span>
        </div>
      )}

      {/* ---------------- 第 3 步：预览 ---------------- */}
      {preview && p && (
        <section className="card">
          <div className="card-head">
            <h4>③ 预览后确认</h4>
            <span className="badge badge-info">
              {p.kind === "duration" ? "时长表" : "音浪表"} · {p.rowCount} 行
            </span>
          </div>

          <div className="chip-row" style={{ marginBottom: 12 }}>
            <span className="badge badge-ok">新增 {p.newCount}</span>
            <span className="badge badge-warn">覆盖 {p.changedCount}</span>
            <span className="badge badge-muted">无变化 {p.unchangedCount}</span>
            <span className="badge badge-info">未建档 {p.unmatchedCount}</span>
            {p.duplicateCount > 0 && (
              <span className="badge badge-warn">重复行 {p.duplicateCount}</span>
            )}
            {p.skippedCount > 0 && <span className="badge badge-muted">跳过 {p.skippedCount}</span>}
          </div>

          <div className="toolbar">
            <div className="seg">
              {[
                ["all", "全部"],
                ["new", "新增"],
                ["changed", "覆盖"],
                ["unmatched", "未建档"],
                ["unchanged", "无变化"],
              ].map(([k, label]) => (
                <button key={k} className={filter === k ? "on" : ""} onClick={() => setFilter(k)}>
                  {label}
                </button>
              ))}
            </div>
            <span className="spacer" />
            <span className="tiny muted">
              显示 {Math.min(visible.length, 300)} / {rows.length} 行
            </span>
          </div>

          {visible.length === 0 ? (
            <div className="empty">
              <span className="empty-emoji">🔍</span>
              这个筛选下没有行。
            </div>
          ) : (
            <div className="table-wrap">
              <table style={{ width: "100%" }}>
                <thead>
                  <tr>
                    <th>状态</th>
                    <th>主播</th>
                    <th>抖音ID</th>
                    <th className="num">已有</th>
                    <th className="num">导入后</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.slice(0, 300).map((r, i) => (
                    <tr key={`${r.anchorId}-${i}`}>
                      <td>
                        <span className={STATUS_CLASS[r.status]}>{STATUS_TEXT[r.status]}</span>
                      </td>
                      <td>{r.name || "—"}</td>
                      <td className="muted">{r.anchorId}</td>
                      <td className="num muted">{r.current == null ? "—" : fmtWave(r.current)}</td>
                      <td className="num">
                        {r.err ? <span className="err">{r.err}</span> : fmtWave(r.next)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {rows.length > 300 && (
            <p className="tiny muted" style={{ marginBottom: 0 }}>
              只显示前 300 行，实际导入不受影响。
            </p>
          )}

          <div className="toolbar" style={{ marginTop: 14, marginBottom: 0 }}>
            <button onClick={() => void commit()} disabled={busy}>
              {busy ? busyHint || "写入中…" : `确认导入到 ${date}`}
            </button>
            <button
              className="ghost"
              disabled={busy}
              onClick={() => {
                setPreview(null);
                setFile(null);
              }}
            >
              取消
            </button>
            <span className="spacer" />
            {p.unmatchedCount > 0 && (
              <span className="tiny muted">
                有 {p.unmatchedCount} 行没匹配到主播：会照常存档但暂不进榜；之后在主播管理加上这些人，历史数据自动显示。
              </span>
            )}
          </div>
        </section>
      )}

      <details className="logs-fold">
        <summary>导入日志（最近 100 条）</summary>
        <ImportLogsPage />
      </details>
    </>
  );
}
