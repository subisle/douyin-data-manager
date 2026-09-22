import { useState } from "react";

const todayLocal = () => {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

const TYPE_OPTIONS = [
  { value: "wave", label: "音浪数据" },
  { value: "duration", label: "时长数据" },
  { value: "both", label: "音浪 + 时长" },
];

const typeText = (t: string) =>
  TYPE_OPTIONS.find((o) => o.value === t)?.label ?? t;

/**
 * 数据清理：按日期或区间删除音浪 / 时长采集数据。
 * 后端会连带清掉本地 staging 副本与去重账本（该区间可重新导入），
 * 然后按整年重算指标。
 */
export function DataCleanupPage() {
  const [type, setType] = useState("wave");
  const [from, setFrom] = useState(todayLocal());
  const [to, setTo] = useState(todayLocal());
  const [result, setResult] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const purge = async () => {
    const range = from === to ? from : `${from} ~ ${to}`;
    const ok = window.confirm(
      `⚠️ 确认删除 ${range} 的${typeText(type)}？\n\n` +
        `将删除：采集快照、导入账本（该区间可重新导入），并重算指标。\n` +
        `此操作不可恢复！`
    );
    if (!ok) return;

    setBusy(true);
    setErr("");
    setResult("");
    try {
      const res = await fetch("/api/v1/imports/purge", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ type, from, to }),
      });
      const json = await res.json();
      if (!res.ok) throw new Error(json.error?.message || `后端返回 ${res.status}`);
      const d = json.data ?? json;
      const parts: string[] = [];
      if (d.waveDeleted > 0) parts.push(`音浪快照 ${d.waveDeleted} 行`);
      if (d.durationDeleted > 0) parts.push(`时长快照 ${d.durationDeleted} 行`);
      if (d.ledgerDeleted > 0) parts.push(`导入账本 ${d.ledgerDeleted} 条`);
      setResult(
        (parts.length ? `已删除：${parts.join("、")}。` : "该区间没有可删的数据。") +
          `指标已重算（${d.recomputedFrom} ~ ${d.recomputedTo}）。`
      );
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>数据清理</h3>
      <p className="muted" style={{ marginTop: 0 }}>
        删除某一日或区间的音浪 / 时长采集数据。会连带清掉导入账本（该区间可重新导入），
        并自动重算日 / 月 / 年指标。远端 615 staging 不在清理范围内，同步前请确认。
      </p>

      <div className="toolbar">
        <select value={type} onChange={(e) => setType(e.target.value)}>
          {TYPE_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        <span className="muted">至</span>
        <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        <button
          onClick={() => void purge()}
          disabled={busy}
          style={{ color: "#B42318", borderColor: "#F04438" }}
        >
          {busy ? "清理中…" : "删除数据"}
        </button>
      </div>

      {err && <p className="err">{err}</p>}
      {result && <p style={{ color: "#047857" }}>{result}</p>}

      <p className="muted" style={{ fontSize: 12 }}>
        只删采集快照，主播档案、账号绑定、等级规则不受影响。删错可以从清理前备份恢复。
      </p>
    </div>
  );
}
