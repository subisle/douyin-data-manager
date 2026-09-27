import { useEffect, useRef, useState } from "react";
import { api, fmtMinutes } from "./api";
import type { NavParams } from "./nav";

// 2026-09-27：不再分页——全部人进一张图，只分男女（后端 /exports/report.svg 缺省即全量）。

// 数据 T+1：能看到的最新一天是昨天
const dataDayLocal = () => {
  const d = new Date();
  d.setDate(d.getDate() - 1);
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
 * 一个性别一张整图（不分页），女队 classic 样式，男团 apple 样式。
 */
export function ExportPage({ params }: { params?: NavParams }) {
  // 默认昨天：数据 T+1，今天还没数，默认今天会让预览直接空掉
  const [date, setDate] = useState(() => params?.date ?? dataDayLocal());
  const [gender, setGender] = useState(() => params?.gender ?? "female");
  const [style, setStyle] = useState("auto");
  const [visible, setVisible] = useState<string[]>(DEFAULT_VISIBLE);
  const [svg, setSvg] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  // 自定义日报图标题（男女各一个），存后端 app_setting，机器人与导出共用
  const DEFAULT_TITLES = { male: "ST-001", female: "主播数据统计" };
  const [titleMale, setTitleMale] = useState("");
  const [titleFemale, setTitleFemale] = useState("");
  const [titleSaved, setTitleSaved] = useState(false);

  useEffect(() => {
    fetch("/api/v1/exports/report-titles")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`后端返回 ${r.status}`))))
      .then((d: { male?: string; female?: string }) => {
        setTitleMale(d.male ?? "");
        setTitleFemale(d.female ?? "");
      })
      .catch(() => {/* 读不到就用默认，不阻塞页面 */});
    // 列勾选也是共享设置：进页面回显已保存的勾选
    fetch("/api/v1/exports/report-columns")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`后端返回 ${r.status}`))))
      .then((d: { columns?: string }) => {
        if (!d.columns) return;
        const keys = d.columns.split(",").map((s) => s.trim()).filter(Boolean);
        if (keys.length) setVisible(keys);
      })
      .catch(() => {/* 读不到就用默认列 */});
  }, []);

  const saveTitles = async () => {
    setErr("");
    try {
      const res = await fetch("/api/v1/exports/report-titles", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ male: titleMale.trim(), female: titleFemale.trim() }),
      });
      if (!res.ok) throw new Error(`保存失败：后端返回 ${res.status}`);
      setTitleSaved(true);
      setTimeout(() => setTitleSaved(false), 2000);
      void load();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const toggleCol = (key: string) => {
    setVisible((v) => {
      const next = v.includes(key) ? v.filter((k) => k !== key) : [...v, key];
      if (next.length === 0 || next.length === v.length) return v; // 至少保留一列 / 无变化不保存
      // 勾选即保存：机器人发图与手动导出共用这一份（后端 report_columns）
      void fetch("/api/v1/exports/report-columns", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          columns: ALL_COLUMNS.filter((c) => next.includes(c.key)).map((c) => c.key).join(","),
        }),
      }).catch(() => {/* 保存失败不阻塞预览，下次勾选会再存 */});
      return next;
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
        // 列顺序按 615 的固定顺序输出，勾选只决定去留；缺省不分页
        cols: ALL_COLUMNS.filter((c) => visible.includes(c.key)).map((c) => c.key).join(","),
      });
      // 预览即时生效：输入框有值就显式传 title（后端显式传参优先级最高），
      // 空则交给后端用已保存的设置
      const t = (g === "male" ? titleMale : titleFemale).trim();
      if (t) usp.set("title", t);
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

  // 总排名 CSV：按所选月份的累计音浪排名。
  // 字段：排名 / 主播姓名 / X月音浪 / 时长 / 未播天数（不含等级）。
  // 姓名列是给 PSD 导入脚本用的（脚本按 排名+姓名 两列工作）。
  // UTF-8 带 BOM，Excel 双击打开不乱码。
  const downloadRankCSV = async () => {
    setBusy(true);
    setErr("");
    try {
      const period = date.slice(0, 7);
      const g = gender === "female" ? "female" : gender === "male" ? "male" : undefined;
      const rows = (await api.monthly(period, g)) ?? [];
      const sorted = [...rows].sort((a, b) => b.wave - a.wave);
      const monthLabel = `${parseInt(period.slice(5), 10)}月音浪`;
      const lines: string[] = [["排名", "主播姓名", monthLabel, "时长", "未播天数"].join(",")];
      sorted.forEach((r, i) => {
        lines.push(
          [
            String(i + 1),
            r.name || "",
            String(r.wave),
            r.formattedDuration || fmtMinutes(r.minutes),
            String(r.absentDays),
          ].join(","),
        );
      });
      download(
        new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }),
        `总排名-${period}-${gender === "female" ? "女团" : "男团"}.csv`,
      );
    } catch (e) {
      setErr((e as Error).message);
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
        <span className="tiny muted">勾选自动保存，机器人发图同步生效</span>
      </div>

      <div className="toolbar" style={{ flexWrap: "wrap" }}>
        <span className="tiny muted">日报图标题：</span>
        <label style={{ display: "flex", alignItems: "center", gap: 4 }}>
          男团
          <input
            value={titleMale}
            placeholder={DEFAULT_TITLES.male}
            onChange={(e) => setTitleMale(e.target.value)}
            style={{ width: 140 }}
          />
        </label>
        <label style={{ display: "flex", alignItems: "center", gap: 4 }}>
          女队
          <input
            value={titleFemale}
            placeholder={DEFAULT_TITLES.female}
            onChange={(e) => setTitleFemale(e.target.value)}
            style={{ width: 140 }}
          />
        </label>
        <button onClick={() => void saveTitles()}>保存标题</button>
        {titleSaved && <span className="tiny muted">已保存，机器人发图同步生效</span>}
        <span className="tiny muted">留空 = 恢复默认</span>
      </div>

      <div className="toolbar">
        <input
          type="date"
          value={date}
          onChange={(e) => {
            setDate(e.target.value);
            void load({ date: e.target.value });
          }}
        />
        <select
          value={gender}
          onChange={(e) => {
            setGender(e.target.value);
            void load({ gender: e.target.value });
          }}
        >
          <option value="female">女队</option>
          <option value="male">男团</option>
        </select>
        <select
          value={style}
          onChange={(e) => {
            setStyle(e.target.value);
            void load();
          }}
        >
          <option value="auto">跟随性别</option>
          <option value="classic">classic（样式一）</option>
          <option value="apple">apple（样式二）</option>
        </select>
        <button onClick={() => void load()} disabled={busy}>
          生成预览
        </button>
        <button className="ghost" disabled={busy} onClick={() => void downloadRankCSV()}>
          下载总排名 CSV
        </button>
        <span className="tiny muted">按所选月份：排名 / X月音浪 / 时长 / 未播天数</span>
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
          下载 PNG
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
