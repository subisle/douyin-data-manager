import { useState } from "react";
import { api, fmtWave, type AbsentRow, type DayPoint, type Dashboard, type TopRow } from "./api";
import { useLoad } from "./pages";

const WAN = 10000;

// 音浪统一按"万"展示，位数太多读不准
const wan = (n: number) => `${(n / WAN).toFixed(1)}万`;
const plainWan = (n: number) => (n / WAN).toFixed(1);

// 把最大值向上取整到好读的刻度，避免 Y 轴出现 37.4 万这种值
function niceTop(max: number) {
  if (max <= 0) return 10 * WAN;
  const raw = max / WAN;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  for (const m of [1, 2, 2.5, 5, 10]) {
    if (raw <= m * mag) return m * mag * WAN;
  }
  return 10 * mag * WAN;
}

/* ------------------------------ 趋势曲线 ------------------------------ */

const W = 720;
const H = 240;
const PAD = { left: 52, right: 14, top: 14, bottom: 28 };

function TrendChart({ points }: { points: DayPoint[] }) {
  const [hover, setHover] = useState<number | null>(null);

  if (points.length === 0) {
    return <p className="muted">这段时间还没有数据，导入快照后这里会出现曲线。</p>;
  }

  const innerW = W - PAD.left - PAD.right;
  const innerH = H - PAD.top - PAD.bottom;

  const maxVal = Math.max(...points.map((p) => Math.max(p.female, p.male)));
  const top = niceTop(maxVal);

  const xAt = (i: number) =>
    points.length === 1 ? PAD.left + innerW / 2 : PAD.left + (i / (points.length - 1)) * innerW;
  const yAt = (v: number) => PAD.top + innerH - (v / top) * innerH;

  const pathOf = (key: "female" | "male") =>
    points.map((p, i) => `${i === 0 ? "M" : "L"}${xAt(i).toFixed(1)},${yAt(p[key]).toFixed(1)}`).join(" ");

  const areaOf = (key: "female" | "male") =>
    `${pathOf(key)} L${xAt(points.length - 1).toFixed(1)},${(PAD.top + innerH).toFixed(1)} L${xAt(0).toFixed(1)},${(PAD.top + innerH).toFixed(1)} Z`;

  const gridCount = 4;
  const ticks = Array.from({ length: gridCount + 1 }, (_, i) => (top / gridCount) * i);

  // X 轴只标 6 个左右，全标会糊成一片
  const labelEvery = Math.max(1, Math.ceil(points.length / 6));

  const active = hover != null ? points[hover] : null;

  return (
    <div>
      <div style={{ display: "flex", gap: 16, fontSize: 12, color: "var(--muted)", marginBottom: 4 }}>
        <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
          <span style={{ width: 12, height: 2, background: "#DC2626" }} />女队
        </span>
        <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
          <span style={{ width: 12, height: 2, background: "#2563EB" }} />男团
        </span>
        {active && (
          <span style={{ marginLeft: "auto", color: "var(--text)" }}>
            {active.date.slice(5)}　女队 {plainWan(active.female)}万 · 男团 {plainWan(active.male)}万
          </span>
        )}
      </div>

      <div className="chart-wrap">
        <svg
          viewBox={`0 0 ${W} ${H}`}
          preserveAspectRatio="none"
          role="img"
          aria-label="全团日音浪趋势曲线，女队与男团两条线"
        >
        {ticks.map((t) => (
          <g key={t}>
            <line x1={PAD.left} x2={W - PAD.right} y1={yAt(t)} y2={yAt(t)}
                  stroke="#E2E8F0" strokeWidth={1} />
            <text x={PAD.left - 8} y={yAt(t) + 4} textAnchor="end" fontSize={11} fill="#94A3B8">
              {plainWan(t)}
            </text>
          </g>
        ))}
        <text x={PAD.left - 8} y={PAD.top - 2} textAnchor="end" fontSize={10} fill="#CBD5E1">万</text>

        <path d={areaOf("male")} fill="#2563EB" opacity={0.07} />
        <path d={areaOf("female")} fill="#DC2626" opacity={0.07} />
        <path d={pathOf("male")} fill="none" stroke="#2563EB" strokeWidth={2} strokeLinejoin="round" />
        <path d={pathOf("female")} fill="none" stroke="#DC2626" strokeWidth={2} strokeLinejoin="round" />

        {points.map((p, i) =>
          i % labelEvery === 0 || i === points.length - 1 ? (
            <text key={p.date} x={xAt(i)} y={H - 8} textAnchor="middle" fontSize={11} fill="#94A3B8">
              {p.date.slice(5).replace("-", "/")}
            </text>
          ) : null,
        )}

        {hover != null && (
          <g>
            <line x1={xAt(hover)} x2={xAt(hover)} y1={PAD.top} y2={PAD.top + innerH}
                  stroke="#94A3B8" strokeWidth={1} strokeDasharray="3 3" />
            <circle cx={xAt(hover)} cy={yAt(points[hover].female)} r={3.5} fill="#DC2626" />
            <circle cx={xAt(hover)} cy={yAt(points[hover].male)} r={3.5} fill="#2563EB" />
          </g>
        )}

        {points.map((_, i) => (
          <rect
            key={i}
            x={i === 0 ? PAD.left : xAt(i) - innerW / (points.length - 1) / 2}
            y={PAD.top}
            width={Math.max(4, innerW / Math.max(1, points.length - 1))}
            height={innerH}
            fill="transparent"
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
            onTouchStart={() => setHover(i)}
          />
        ))}
      </svg>
      </div>
    </div>
  );
}

/* ------------------------------- 小组件 ------------------------------- */

function Kpi({ label, value, hint, tone }: { label: string; value: string; hint: string; tone?: "up" | "down" }) {
  return (
    <div style={{ background: "#F1F5F9", borderRadius: 8, padding: 14 }}>
      <p style={{ margin: "0 0 6px", fontSize: 13, color: "var(--muted)" }}>{label}</p>
      <p style={{ margin: 0, fontSize: 24, fontWeight: 500 }}>{value}</p>
      <p style={{
        margin: "4px 0 0",
        fontSize: 12,
        color: tone === "up" ? "#DC2626" : tone === "down" ? "#15803D" : "var(--muted)",
      }}>
        {hint}
      </p>
    </div>
  );
}

function TopList({ rows }: { rows: TopRow[] }) {
  if (rows.length === 0) return <p className="muted">今天还没有数据。</p>;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10, fontSize: 13 }}>
      {rows.map((r, i) => (
        <div key={r.personId} style={{ display: "flex", alignItems: "center", gap: 10 }}>
          <span style={{ width: 20, color: "var(--muted)", fontVariantNumeric: "tabular-nums" }}>{i + 1}</span>
          <span style={{ flex: 1 }}>{r.name}</span>
          {r.tier && <span className="tier">{r.tier}</span>}
          <span style={{ fontVariantNumeric: "tabular-nums" }}>{wan(r.wave)}</span>
        </div>
      ))}
    </div>
  );
}

function AbsentList({ rows }: { rows: AbsentRow[] }) {
  if (rows.length === 0) return <p className="muted">今天全员开播。</p>;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10, fontSize: 13 }}>
      {rows.map((r) => (
        <div key={r.personId}
             style={{ display: "flex", alignItems: "center", gap: 10, paddingLeft: 8, borderLeft: "3px solid #DC2626" }}>
          <span style={{ flex: 1 }}>{r.name}</span>
          <span className="muted">连续 {r.days} 天</span>
        </div>
      ))}
    </div>
  );
}

/* ------------------------------- 首页 ------------------------------- */

export function DashboardPage() {
  const [days, setDays] = useState("30");
  const { data, error, loading } = useLoad(() => api.dashboard({ days }), [days]);

  const d = data as Dashboard | null;
  const s = d?.summary;

  const delta = s && s.prevWave > 0 ? ((s.totalWave - s.prevWave) / s.prevWave) * 100 : null;

  return (
    <div className="panel">
      <div className="toolbar" style={{ justifyContent: "space-between" }}>
        <h3 style={{ margin: 0 }}>数据概览</h3>
        <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
          <span className="muted">{s?.date ?? ""}</span>
          <select value={days} onChange={(e) => setDays(e.target.value)}>
            <option value="7">近 7 天</option>
            <option value="30">近 30 天</option>
            <option value="90">近 90 天</option>
          </select>
        </div>
      </div>

      {loading && <p className="muted">加载中…</p>}
      {error && <p className="err">出错了：{error}</p>}

      {s && (
        <>
          <div
            className="kpi-grid"
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))",
              gap: 12,
              marginBottom: 20,
            }}
          >
            <Kpi
              label="今日总音浪"
              value={wan(s.totalWave)}
              hint={delta == null ? "无昨日数据对比" : `较昨日 ${delta >= 0 ? "+" : ""}${delta.toFixed(1)}%`}
              tone={delta == null ? undefined : delta >= 0 ? "up" : "down"}
            />
            <Kpi
              label="开播人数"
              value={`${s.liveCount} / ${s.totalCount}`}
              hint={`未开播 ${Math.max(0, s.totalCount - s.liveCount)} 人`}
            />
            <Kpi
              label="本月音浪"
              value={wan(s.monthWave)}
              hint={`月进度 ${(s.monthProgress * 100).toFixed(0)}%`}
            />
            <Kpi
              label="本月时长"
              value={`${Math.round(s.monthMinutes / 60).toLocaleString("zh-CN")}h`}
              hint={s.totalCount > 0 ? `人均 ${Math.round(s.monthMinutes / 60 / s.totalCount)}h` : "—"}
            />
          </div>

          <div style={{ marginBottom: 20 }}>
            <TrendChart points={d?.trend ?? []} />
          </div>

          <div className="grid2">
            <div>
              <h4 style={{ margin: "0 0 10px" }}>今日榜单前五</h4>
              <TopList rows={d?.top ?? []} />
            </div>
            <div>
              <h4 style={{ margin: "0 0 10px" }}>未开播预警</h4>
              <AbsentList rows={d?.absent ?? []} />
            </div>
          </div>

          <p className="muted" style={{ fontSize: 12, marginTop: 16 }}>
            曲线纵轴单位为万音浪。未开播天数按"截至统计日连续未开播"计算，中间缺数据的天不计数。
          </p>
        </>
      )}

      {!s && !loading && !error && (
        <p className="muted">还没有数据。先到「主播管理」加人、「数据导入」导入快照。</p>
      )}

      <p className="muted" style={{ fontSize: 12, marginTop: 8 }}>
        今日总音浪合计 {fmtWave(s?.totalWave ?? 0)}
      </p>
    </div>
  );
}
