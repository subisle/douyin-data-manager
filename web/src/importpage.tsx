import { useState } from "react";
import { api, fmtWave, type PreviewResponse, type PreviewRow } from "./api";

const todayLocal = () => {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

const STATUS_TEXT: Record<string, string> = {
  new: "新增",
  changed: "覆盖",
  unchanged: "无变化",
  unmatched: "未入列表",
  duplicate: "重复行",
  skipped: "跳过",
};

const STATUS_CLASS: Record<string, string> = {
  new: "ok",
  changed: "err",
  unmatched: "muted",
  duplicate: "err",
  skipped: "muted",
  unchanged: "muted",
};

/**
 * 数据导入——对齐 615 的流程：
 * 选 CSV → 自动识别是音浪表还是时长表 → 预览（新增/覆盖/无变化三态）→ 确认才写入。
 *
 * 之所以多一步预览：覆盖已有数据是危险操作，必须先看清楚会发生什么。
 */
export function ImportPage() {
  const [file, setFile] = useState<File | null>(null);
  const [date, setDate] = useState(todayLocal());
  const [preview, setPreview] = useState<PreviewResponse | null>(null);
  const [err, setErr] = useState("");
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const [filter, setFilter] = useState("all");
  const [dragging, setDragging] = useState(false);

  const runPreview = async (f: File | null) => {
    if (!f) return;
    setBusy(true);
    setErr("");
    setDone("");
    try {
      setPreview(await api.previewCSV(f, date));
    } catch (e) {
      setErr((e as Error).message);
      setPreview(null);
    } finally {
      setBusy(false);
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
    if (f) void runPreview(f); // 拖进来/选完就自动解析预览
  };

  const commit = async () => {
    if (!file) return;
    if (!confirm("确认导入？已存在的数据会被覆盖，这一步不可撤销。")) return;
    setBusy(true);
    setErr("");
    try {
      const r = await api.importCSV(file, date);
      setDone(
        `导入完成：${r.imported} 行，覆盖 ${r.persons} 位主播` +
          `（新增 ${r.counts.new} · 覆盖 ${r.counts.changed} · 无变化 ${r.counts.unchanged}）` +
          (r.skipped.length ? `；跳过 ${r.skipped.length} 个未识别账号` : ""),
      );
      setPreview(null);
      setFile(null);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const rows: PreviewRow[] = preview?.preview.rows ?? [];
  const visible = rows.filter((r) => filter === "all" || r.status === filter);
  const p = preview?.preview;

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>数据导入</h3>
      <p className="muted" style={{ marginTop: 0 }}>
        一次导入一个 CSV，靠表头自动识别是<b>音浪</b>还是<b>时长</b>表。
        音浪按日期（YYYY-MM-DD），时长可按月份（YYYY-MM）。
        表头支持「主播id / 抖音号 / anchor_id / uid」等别名，数值支持「12.5万」「2:30:45」「150分钟」。
      </p>

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
        onClick={() => document.getElementById("csv-file-input")?.click()}
      >
        <input
          id="csv-file-input"
          type="file"
          accept=".csv,text/csv"
          onChange={(e) => pickFile(e.target.files?.[0] ?? null)}
          style={{ display: "none" }}
        />
        {file ? (
          <span>
            📄 <b>{file.name}</b>（{(file.size / 1024).toFixed(1)} KB）
            {busy ? " — 解析中…" : preview ? " — 已解析，可直接确认导入" : ""}
          </span>
        ) : (
          <span className="muted">把 CSV 文件拖到这里，或点击选择文件</span>
        )}
      </div>

      <div className="toolbar">
        <input type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        <button onClick={() => void runPreview(file)} disabled={!file || busy}>
          重新解析
        </button>
        {preview && (
          <button className="ghost" onClick={() => void commit()} disabled={busy}>
            确认导入
          </button>
        )}
      </div>

      {err && <p className="err">{err}</p>}
      {done && <p className="ok">{done}</p>}
      {preview?.duplicate && (
        <p className="muted">
          ℹ️ 该文件在 {preview.duplicate.import_date} 已导入过（{preview.duplicate.row_count} 行）。
          支持重复导入：相同数据会覆盖更新，不会产生重复行。
        </p>
      )}

      {preview && p && (
        <>
          <div className="toolbar" style={{ marginTop: 8 }}>
            <span className="muted">
              识别为 <b>{p.kind === "duration" ? "时长表" : "音浪表"}</b> · 共 {p.rowCount} 行
            </span>
            <span className="ok">新增 {p.newCount}</span>
            <span className="err">覆盖 {p.changedCount}</span>
            <span className="muted">无变化 {p.unchangedCount}</span>
            <span className="muted">未入列表 {p.unmatchedCount}</span>
            {p.duplicateCount > 0 && <span className="err">重复行 {p.duplicateCount}</span>}
            {p.skippedCount > 0 && <span className="muted">跳过 {p.skippedCount}</span>}
          </div>

          <div className="toolbar">
            <select value={filter} onChange={(e) => setFilter(e.target.value)}>
              <option value="all">全部</option>
              <option value="new">仅新增</option>
              <option value="changed">仅覆盖</option>
              <option value="unchanged">仅无变化</option>
              <option value="unmatched">仅未入列表</option>
              <option value="duplicate">仅重复行</option>
            </select>
            <span className="muted">显示 {visible.length} / {rows.length} 行</span>
          </div>

          <table>
            <thead>
              <tr>
                <th style={{ width: 70 }}>状态</th>
                <th>主播</th>
                <th>抖音ID</th>
                <th className="num">已有</th>
                <th className="num">导入后</th>
              </tr>
            </thead>
            <tbody>
              {visible.slice(0, 200).map((r, i) => (
                <tr key={`${r.anchorId}-${i}`}>
                  <td className={STATUS_CLASS[r.status]}>{STATUS_TEXT[r.status]}</td>
                  <td>{r.name || "—"}</td>
                  <td className="muted">{r.anchorId}</td>
                  <td className="num muted">{r.current == null ? "—" : fmtWave(r.current)}</td>
                  <td className="num">{r.err ? <span className="err">{r.err}</span> : fmtWave(r.next)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length > 200 && (
            <p className="muted" style={{ fontSize: 12 }}>
              只显示前 200 行，导入不受影响。
            </p>
          )}
        </>
      )}

      <div className="notice" style={{ marginTop: 12 }}>
        未入列表的行仍会导入（快照按 anchorId 存），但不会出现在日榜里——
        先到「主播管理」绑定抖音号，再回来导入。
      </div>
    </div>
  );
}
