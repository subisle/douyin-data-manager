import { useState } from "react";
import { DashboardPage } from "./dashboard";
import { BotPage } from "./botpage";
import { DataCleanupPage } from "./cleanuppage";
import { ExportPage } from "./exportpage";
import { ImportPage } from "./importpage";
import { DailyPage, ImportLogsPage, MonthlyPage, PersonsPage, YearlyPage } from "./pages";

const TABS = [
  { key: "home", label: "首页", Comp: DashboardPage },
  { key: "daily", label: "日榜", Comp: DailyPage },
  { key: "monthly", label: "月榜", Comp: MonthlyPage },
  { key: "yearly", label: "年度汇总", Comp: YearlyPage },
  { key: "persons", label: "主播管理", Comp: PersonsPage },
  { key: "import", label: "数据导入", Comp: ImportPage },
  { key: "logs", label: "导入日志", Comp: ImportLogsPage },
  { key: "cleanup", label: "数据清理", Comp: DataCleanupPage },
  { key: "export", label: "导出图片", Comp: ExportPage },
  { key: "bots", label: "机器人", Comp: BotPage },
] as const;

export default function App() {
  const [tab, setTab] = useState<string>("home");
  const Current = TABS.find((t) => t.key === tab)?.Comp ?? DashboardPage;

  return (
    <>
      <header className="topbar">
        <span className="brand">主播数据管理</span>
        <nav className="tabs">
          {TABS.map((t) => (
            <button
              key={t.key}
              className={tab === t.key ? "tab active" : "tab"}
              onClick={() => setTab(t.key)}
            >
              {t.label}
            </button>
          ))}
        </nav>
      </header>

      <main className="layout">
        <Current />
      </main>
    </>
  );
}
