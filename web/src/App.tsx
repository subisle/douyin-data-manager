import { useEffect, useMemo, useState, type ComponentType } from "react";
import { DashboardPage } from "./dashboard";
import { BotPage } from "./botpage";
import { DataCleanupPage } from "./cleanuppage";
import { ExportPage } from "./exportpage";
import { ImportPage } from "./importpage";
import { DailyPage, MonthlyPage, PersonsPage, YearlyPage } from "./pages";
import { buildHash, parseHash, NavContext, type NavParams } from "./nav";

/*
 * 导航按语义分四组，组间加分隔线；页面用 keep-alive 挂载
 * （首次访问才挂、之后隐藏不卸），切 tab 不丢已选的日期/性别/勾选。
 * 当前 tab 与跳转参数都在 location.hash 里，刷新和分享都不丢。
 */
const NAV_GROUPS: {
  tabs: { key: string; label: string; Comp: ComponentType<{ params?: NavParams }> }[];
}[] = [
  {
    tabs: [{ key: "home", label: "首页", Comp: DashboardPage }],
  },
  {
    tabs: [
      { key: "daily", label: "日榜", Comp: DailyPage },
      { key: "monthly", label: "月榜", Comp: MonthlyPage },
      { key: "yearly", label: "年度汇总", Comp: YearlyPage },
    ],
  },
  {
    tabs: [{ key: "persons", label: "主播管理", Comp: PersonsPage }],
  },
  {
    tabs: [
      { key: "import", label: "数据导入", Comp: ImportPage },
      { key: "export", label: "导出图片", Comp: ExportPage },
      { key: "cleanup", label: "数据清理", Comp: DataCleanupPage },
    ],
  },
  {
    tabs: [{ key: "bots", label: "机器人", Comp: BotPage }],
  },
];

const ALL_TABS = NAV_GROUPS.flatMap((g) => g.tabs);

export default function App() {
  const [route, setRoute] = useState(parseHash);
  const [visited, setVisited] = useState<Set<string>>(() => new Set([route.tab]));

  useEffect(() => {
    const onHash = () => setRoute(parseHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    setVisited((v) => (v.has(route.tab) ? v : new Set(v).add(route.tab)));
  }, [route.tab]);

  const nav = useMemo(
    () => ({
      tab: route.tab,
      params: route.params,
      navigate: (tab: string, params?: NavParams) => {
        const next = buildHash(tab, params);
        if (window.location.hash === next) {
          // 同 hash 不触发 hashchange，手动同步一次参数（如重复跳同一页）
          setRoute({ tab, params: params ?? {} });
        } else {
          window.location.hash = next;
        }
      },
    }),
    [route],
  );

  return (
    <NavContext.Provider value={nav}>
      <header className="topbar">
        <span className="brand">主播数据管理</span>
        <nav className="tabs" aria-label="主导航">
          {NAV_GROUPS.map((group, gi) => (
            <div className="nav-group" key={gi}>
              {gi > 0 && (
                <span className="nav-divider" aria-hidden="true" />
              )}
              {group.tabs.map((t) => (
                <button
                  key={t.key}
                  className={route.tab === t.key ? "tab active" : "tab"}
                  onClick={() => nav.navigate(t.key)}
                >
                  {t.label}
                </button>
              ))}
            </div>
          ))}
        </nav>
      </header>

      <main className="layout">
        {/* keep-alive：访问过的页面保持挂载（隐藏不卸载），状态不丢。
            必须始终用同一层 div 包裹（hidden 切换），否则 React 按
            元素类型 diff 会卸载重建，keep-alive 失效。 */}
        {ALL_TABS.filter((t) => visited.has(t.key)).map((t) => (
          <div key={t.key} hidden={t.key !== route.tab}>
            <t.Comp params={t.key === route.tab ? route.params : undefined} />
          </div>
        ))}
      </main>
    </NavContext.Provider>
  );
}
