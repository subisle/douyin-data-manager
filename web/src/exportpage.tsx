import { useEffect, useRef, useState } from "react";
import type { NavParams } from "./nav";

const todayLocal = () => {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

// 把 SVG 画到 canvas 再导出 PNG。
// 走浏览器渲染是刻意的：emoji 奖牌、中文字体、水印只有真实浏览器能画对。
async function svgToPng(svgText: string, scale = 2): Promise<Blob> {
  const svgBlob = new Blob([svgText], { type: "image/svg+xml;charset=utf-8" });
  const url = URL.createObjectURL(svgBlob);

  const img = new Image();
  await new Promise<void>((resolve, reject) => {
    img.onload = () => resolve();
    img.onerror = () => reject(new Error("SVG 加载失败"));
    img.src = url;
  });

  const w = img.naturalWidth || 1440;
  const h = img.naturalHeight || 800;
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(w * scale);
  canvas.height = Math.round(h * scale);
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("无法创建 canvas");
  ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
  URL.revokeObjectURL(url);

  return new Promise<Blob>((resolve, reject) => {
    canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("PNG 生成失败"))), "image/png");
  });
}

function download(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

// 列定义与 615 的 ALL_COLUMNS 一致：key 与后端 cols 参数、标签与列头一致
const ALL_COLUMNS: { key: string; label: string }[] = [
  { key: "rank", label: "排名" },
  { key: "name", label: "主播姓名" },
  { key: "notLiveDays", label: "未播天数" },
  { key: "dailyWave", label: "日音浪" },
  { key: "totalWave", label: "累计总音浪" },
  { key: "duration", label: "当月时长" },
  { key: "master", label: "师傅" },
  { key: "tier", label: "等级" },
];
const DEFAULT_VISIBLE = ["rank", "name", "notLiveDays", "dailyWave", "totalWave"];

/**
 * 导出图片——字段与样式对齐 615：
 * 8 列自由勾选（默认排名/姓名/未播天数/日音浪/累计总音浪），
 * 女队用 classic 样式，男团用 apple 样式。
 */
export function ExportPage({ params }: { params?: NavParams }) {
  const [date, setDate] = useState(() => params?.date ?? todayLocal());
  const [gender, setGender] = useState(() => params?.gender ?? "female");
  const [style, setStyle] = useState("auto");
  const [visible, setVisible] = useState<string[]>(DEFAULT_VISIBLE);
  const [svg, setSvg] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const toggleCol = (key: string) => {
    setVisible((v) => {
      const next = v.includes(key) ? v.filter((k) => k !== key) : [...v, key];
      return next.length ? next : v; // 至少保留一列
    });
  };

  const load = async (o?: { date?: string; gender?: string }) => {
    const d = o?.date ?? date;
    const g = o?.gender ?? gender;
    setBusy(true);
    setErr("");
    try {
      const usp = new URLSearchParams({
        date: d,
        gender: g,
        style,
        // 列顺序按 615 的固定顺序输出，勾选只决定去留
        cols: ALL_COLUMNS.filter((c) => visible.includes(c.key)).map((c) => c.key).join(","),
      });
      const res = await fetch(`/api/v1/exports/report.svg?${usp}`);
      if (!res.ok) throw new Error(`后端返回 ${res.status}`);
      setSvg(await res.text());
    } catch (e) {
      setErr((e as Error).message);
      setSvg("");
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 从日榜等页面带着日期/性别跳进来：直接按参数重新生成
  const appliedNav = useRef("");
  useEffect(() => {
    if (!params?.date && !params?.gender) return;
    const key = `${params.date ?? ""}|${params.gender ?? ""}`;
    if (appliedNav.current === key) return;
    appliedNav.current = key;
    if (params.date) setDate(params.date);
    if (params.gender) setGender(params.gender);
    void load({ date: params.date, gender: params.gender });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params?.date, params?.gender]);

  const previewUrl = svg ? URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" })) : "";

  return (
    <div className="panel">
      <h3 style={{ marginTop: 0 }}>导出图片</h3>
      <p className="muted" style={{ marginTop: 0 }}>
        字段自由勾选（与 615 桌面端一致），女队默认 classic 样式，男团默认 apple 样式。
      </p>

      <div className="toolbar" style={{ flexWrap: "wrap" }}>
        {ALL_COLUMNS.map((c) => (
          <label key={c.key} style={{ display: "flex", alignItems: "center", gap: 4 }}>
            <input
              type="checkbox"
              checked={visible.includes(c.key)}
              onChange={() => toggleCol(c.key)}
            />
            {c.label}
          </label>
        ))}
      </div>

      <div className="toolbar">
        <input type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        <select value={gender} onChange={(e) => setGender(e.target.value)}>
          <option value="female">女队</option>
          <option value="male">男团</option>
        </select>
        <select value={style} onChange={(e) => setStyle(e.target.value)}>
          <option value="auto">跟随性别</option>
          <option value="classic">classic（样式一）</option>
          <option value="apple">apple（样式二）</option>
        </select>
        <button onClick={() => void load()} disabled={busy}>
          生成预览
        </button>
      </div>

      <div className="toolbar">
        <button
          className="ghost"
          disabled={!svg}
          onClick={() => download(new Blob([svg], { type: "image/svg+xml" }), `report-${date}-${gender}.svg`)}
        >
          下载 SVG
        </button>
        <button
          className="ghost"
          disabled={!svg}
          onClick={async () => {
            try {
              download(await svgToPng(svg, 2), `report-${date}-${gender}.png`);
            } catch (e) {
              setErr((e as Error).message);
            }
          }}
        >
          下载 PNG（2 倍图）
        </button>
        {busy && <span className="muted">生成中…</span>}
      </div>

      {err && <p className="err">{err}</p>}

      {svg ? (
        <div style={{ marginTop: 12, border: "1px solid var(--border)", borderRadius: 8, padding: 12, background: "#fff" }}>
          <img src={previewUrl} alt="导出图预览" style={{ width: "100%", display: "block" }} />
        </div>
      ) : (
        !busy && <p className="muted">还没有预览，点「生成预览」。</p>
      )}

      <p className="muted" style={{ fontSize: 12 }}>
        PNG 由浏览器渲染 SVG 得到，字体与 emoji 与最终 bot 发送的一致。
      </p>
    </div>
  );
}
