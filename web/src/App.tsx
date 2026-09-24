import { useEffect, useMemo, useState, type ComponentType } from "react";
import { DashboardPage } from "./dashboard";
import { BotPage } from "./botpage";
import { DataCleanupPage } from "./cleanuppage";
import { ExportPage } from "./exportpage";
import { ImportPage } from "./importpage";
import { DailyPage, MonthlyPage, PersonsPage, YearlyPage } from "./pages";
import { buildHash, parseHash, NavContext, type NavParams } from "./nav";

/*
 * 桌面走左侧分组导航，窄屏走底部 tab——同一份 NAV_GROUPS 渲染两遍。
 * 页面 keep-alive 挂载：首次访问才挂、之后隐藏不卸，切 tab 不丢已选的
 * 日期/性别/勾选。当前 tab 与跳转参数都在 location.hash 里，刷新和分享都不丢。
 */
const NAV_GROUPS: {
  label?: string;
  tabs: { key: string; label: string; icon: string; Comp: ComponentType<{ params?: NavParams }> }[];
}[] = [
  {
    tabs: [{ key: "home", label: "首页", icon: "◎", Comp: DashboardPage }],
  },
  {
    label: "榜单",
    tabs: [
      { key: "daily", label: "日榜", icon: "☀", Comp: DailyPage },
      { key: "monthly", label: "月榜", icon: "▤", Comp: MonthlyPage },
      { key: "yearly", label: "年度汇总", icon: "✦", Comp: YearlyPage },
    ],
  },
  {
    label: "名单",
    tabs: [{ key: "persons", label: "主播管理", icon: "☺", Comp: PersonsPage }],
  },
  {
    label: "数据",
    tabs: [
      { key: "import", label: "数据导入", icon: "⇩", Comp: ImportPage },
      { key: "export", label: "导出图片", icon: "⇧", Comp: ExportPage },
      { key: "cleanup", label: "数据清理", icon: "⌫", Comp: DataCleanupPage },
    ],
  },
  {
    label: "自动化",
    tabs: [{ key: "bots", label: "机器人", icon: "⚙", Comp: BotPage }],
  },
];

const ALL_TABS = NAV_GROUPS.flatMap((g) => g.tabs);

// 每页一句话说明：告诉新用户这页能干嘛，别让人猜
const PAGE_DESC: Record<string, string> = {
  home: "音浪走势、当日概览与预警，一屏看完今天发生了什么。",
  daily: "按天查看音浪与时长榜单，可切换性别、回看任意历史日期。",
  monthly: "整月累计；月榜的「累计音浪」取当月 1 号到数据日的日音浪总和。",
  yearly: "全年汇总：每个人的年度音浪、开播天数与最佳月份。",
  persons: "主播名单、绑抖音号、设师傅与世代，也支持 CSV 批量建档。",
  import: "导 CSV 入库。默认进昨天；指定任意一天请先选日期再传文件。",
  export: "生成与群内日报同款样式的图片，用于对外汇报。",
  cleanup: "按日期或区间删除音浪/时长数据，删完自动重算受影响的主播。",
  bots: "微信与 QQ 双通道机器人：扫码登录、收发文件、命令与调测。",
};

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

  const current = ALL_TABS.find((t) => t.key === route.tab);

  return (
    <NavContext.Provider value={nav}>
      <div className="app-shell">
        <aside className="sidebar">
          <div className="brand">
            <span className="brand-mark">抖</span>
            <span>
              主播数据管理
              <span className="brand-sub">抖音数据管理台</span>
            </span>
          </div>

          {NAV_GROUPS.map((group, gi) => (
            <div key={gi}>
              {group.label && <div className="nav-group-label">{group.label}</div>}
              {group.tabs.map((t) => (
                <button
                  key={t.key}
                  className={route.tab === t.key ? "nav-item active" : "nav-item"}
                  onClick={() => nav.navigate(t.key)}
                >
                  <span className="nav-icon" aria-hidden="true">
                    {t.icon}
                  </span>
                  {t.label}
                </button>
              ))}
            </div>
          ))}

          <div className="sidebar-foot">
            音浪与时长按天 T+1 入库，
            <br />
            今天看到的通常是昨天的数据。
          </div>
        </aside>

        <main className="content">
          <div className="page-head">
            <div>
              <h1 className="page-title">{current?.label ?? "首页"}</h1>
              <p className="page-desc">{PAGE_DESC[route.tab] ?? ""}</p>
            </div>
          </div>

          {/* keep-alive：访问过的页面保持挂载（隐藏不卸载），状态不丢。
              必须始终用同一层 div 包裹（hidden 切换），否则 React 按
              元素类型 diff 会卸载重建，keep-alive 失效。 */}
          {ALL_TABS.filter((t) => visited.has(t.key)).map((t) => (
            <div key={t.key} hidden={t.key !== route.tab}>
              <t.Comp params={t.key === route.tab ? route.params : undefined} />
            </div>
          ))}
        </main>
      </div>

      {/* 移动端底部 tab：全部入口可横滑 */}
      <nav className="tabbar" aria-label="主导航">
        {ALL_TABS.map((t) => (
          <button
            key={t.key}
            className={route.tab === t.key ? "tabbar-item active" : "tabbar-item"}
            onClick={() => nav.navigate(t.key)}
          >
            <span className="tab-ico" aria-hidden="true">
              {t.icon}
            </span>
            {t.label}
          </button>
        ))}
      </nav>
    </NavContext.Provider>
  );
}
