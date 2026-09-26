import { useCallback, useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { api, type BotChannelStatus, type BotLog, type ReminderTargets } from "./api";

const CHANNEL_META: Record<string, { label: string; icon: string; hint: string }> = {
  weixin: {
    label: "微信 iLink",
    icon: "💬",
    hint: "扫码登录后可收发消息与 CSV 文件",
  },
  qq: {
    label: "QQ 开放平台",
    icon: "🐧",
    hint: "填 AppID / ClientSecret 后挂载，群内外都能收发文件",
  },
};

const INTENT_LABEL: Record<string, string> = {
  help: "帮助",
  daily_report: "日报",
  monthly_report: "月报",
  yearly_report: "年报",
  daily_star: "每日之星",
  person_query: "查主播",
  push_toggle: "推送开关",
  push_status: "推送状态",
  pk_group: "PK 分组",
  import_date: "导入日期预告",
  import_csv: "导入 CSV",
  import_error: "导入失败",
  import_logs: "导入记录",
  rename_flow: "改名/改号",
  quit: "退出流程",
  unknown: "没听懂",
};

const LOGIN_PHASE_LABEL: Record<string, string> = {
  awaiting_scan: "等待扫码",
  scanned: "已扫码，请在手机上确认",
  running: "登录成功",
  session_expired: "二维码已过期",
  error: "登录异常",
};

// 快捷指令：都是群里真实会敲的话，点一下直接发
const QUICK_COMMANDS = [
  { label: "帮助", text: "帮助" },
  { label: "日报", text: "日报" },
  { label: "昨天", text: "昨天" },
  { label: "9.1（指定导入日）", text: "9.1" },
  { label: "导入记录", text: "导入记录" },
  { label: "q（退出）", text: "q" },
];

const COMMANDS: [string, string][] = [
  ["9.1 / 9月1日 / 1号", "记住导入日期，随后传的 CSV 全部进这一天"],
  ["直接发 CSV", "默认导入到昨天（T+1）"],
  ["q", "退出当前流程：作废导入日期、放弃改名改号"],
  ["日报 / 昨天 / 18号报告", "出榜单图"],
  ["9月 / 2026年3月", "月报、年报"],
  ["艺名 / 艺名 9月", "查单个主播"],
  ["姓名-抖音号", "新增主播，如 柚子-123456"],
  ["改名 / 改号", "对话式修改主播信息"],
  ["开启/关闭日报推送", "推送开关"],
];

interface Turn {
  id: number;
  dir: "in" | "out";
  text: string;
  file?: string;
  images?: { name: string; dataUrl?: string }[];
  files?: { name: string }[];
  at: string;
}

const nowLabel = () =>
  new Date().toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });

/**
 * 机器人页。
 *
 * 三层信息，从上到下：这台服务「现在能不能用」→ 两个通道「怎么接上」→
 * 「接上之后说什么样的话能干活」。中间那块会话控制台走的是与微信/QQ
 * 完全相同的 Handle 流程，所以不用真账号也能把导入链路跑通。
 */
export function BotPage() {
  const [status, setStatus] = useState<BotChannelStatus[]>([]);
  const [push, setPush] = useState(false);
  const [targets, setTargets] = useState<ReminderTargets>({ groups: true, private: true });
  const [err, setErr] = useState("");
  const [turns, setTurns] = useState<Turn[]>([]);
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [conversation] = useState("web-console");
  const [dragging, setDragging] = useState(false);
  const chatRef = useRef<HTMLDivElement>(null);
  const seq = useRef(0);

  const loadStatus = useCallback(async () => {
    try {
      const data = await api.botStatus();
      setStatus(data.channels ?? []);
      setPush(data.push);
      if (data.reminder) setTargets(data.reminder);
    } catch (e) {
      setErr((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void loadStatus();
    const t = window.setInterval(() => void loadStatus(), 15000);
    return () => window.clearInterval(t);
  }, [loadStatus]);

  useEffect(() => {
    chatRef.current?.scrollTo({ top: chatRef.current.scrollHeight, behavior: "smooth" });
  }, [turns, busy]);

  const send = async (raw: string, f: File | null) => {
    if (!raw.trim() && !f) return;
    setBusy(true);
    setErr("");
    seq.current += 1;
    setTurns((prev) => [
      ...prev,
      {
        id: seq.current,
        dir: "in",
        text: raw.trim() || (f ? `（发送文件 ${f.name}）` : ""),
        file: f?.name,
        at: nowLabel(),
      },
    ]);
    setText("");
    setFile(null);
    try {
      const r = await api.injectToBot({ text: raw, conversation, file: f });
      seq.current += 1;
      setTurns((prev) => [
        ...prev,
        {
          id: seq.current,
          dir: "out",
          text: r.text,
          images: r.images ?? [],
          files: r.files ?? [],
          at: nowLabel(),
        },
      ]);
    } catch (e) {
      seq.current += 1;
      setTurns((prev) => [
        ...prev,
        { id: seq.current, dir: "out", text: "❌ " + (e as Error).message, at: nowLabel() },
      ]);
    } finally {
      setBusy(false);
    }
  };

  const onlineCount = status.filter((c) => c.running).length;

  return (
    <>
      <div className="grid-main-side">
        {/* ---------------- 左：会话控制台 ---------------- */}
        <section className="card">
          <div className="card-head">
            <div>
              <h3>会话控制台</h3>
              <p className="card-desc" style={{ margin: "4px 0 0" }}>
                走的是与群里完全相同的处理流程：发日期口令 → 传 CSV → 看结果，
                <b>「q」随时退出</b>。
              </p>
            </div>
            <span className="badge badge-info">会话 {conversation}</span>
          </div>

          <div
            className={`dropzone${dragging ? " active" : ""}`}
            style={{ padding: dragging ? 18 : 14, marginBottom: 12 }}
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragging(false);
              const f = e.dataTransfer.files?.[0];
              if (f) setFile(f);
            }}
            onClick={() => !file && document.getElementById("bot-file-input")?.click()}
          >
            <input
              id="bot-file-input"
              type="file"
              accept=".csv,text/csv"
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              style={{ display: "none" }}
            />
            {file ? (
              <div className="file-pill" style={{ width: "100%" }}>
                <span>📄</span>
                <span className="grow">
                  {file.name}（{(file.size / 1024).toFixed(1)} KB）
                </span>
                <button
                  className="ghost btn-sm"
                  onClick={(e) => {
                    e.stopPropagation();
                    setFile(null);
                  }}
                >
                  移除
                </button>
              </div>
            ) : (
              <>
                <span className="dropzone-icon">📎</span>
                <span>
                  拖入音浪/时长 CSV，或点这里选文件
                  <br />
                  不传日期就是昨天；先发 <b>9.1</b> 再传文件则进 9 月 1 日
                </span>
              </>
            )}
          </div>

          <div className="chat-wrap" ref={chatRef}>
            {turns.length === 0 && (
              <div className="empty" style={{ border: "none", background: "transparent" }}>
                <span className="empty-emoji">🤖</span>
                还没说会话：先在下面输入一句话，或直接拖一个 CSV 进来。
                <span className="tiny">
                  例：「9.1」→ 传文件 → 出结果；中途敲「q」退出。
                </span>
              </div>
            )}
            {turns.map((t) => (
              <div key={t.id} className={t.dir === "out" ? "bubble-row out" : "bubble-row"}>
                <div>
                  <div className={t.dir === "out" ? "bubble out" : "bubble in"}>{t.text}</div>
                  {(t.images ?? []).map((img, i) => (
                    <div className="bubble-media" key={i}>
                      {img.dataUrl ? (
                        <img src={img.dataUrl} alt={img.name} />
                      ) : (
                        <div className="tiny muted" style={{ padding: 8 }}>
                          {img.name}
                        </div>
                      )}
                    </div>
                  ))}
                  {(t.files ?? []).length > 0 && (
                    <div className="bubble-meta">
                      附件：{t.files?.map((f) => f.name).join("、")}
                    </div>
                  )}
                  <div className="bubble-meta">
                    {t.dir === "out" ? "机器人" : "我"} · {t.at}
                  </div>
                </div>
              </div>
            ))}
            {busy && (
              <div className="bubble-row">
                <div className="bubble in">处理中…（重算指标大概要几秒）</div>
              </div>
            )}
          </div>

          <div className="composer">
            <div className="chip-row">
              {QUICK_COMMANDS.map((c) => (
                <button
                  key={c.text}
                  className="chip"
                  disabled={busy}
                  onClick={() => void send(c.text, null)}
                >
                  {c.label}
                </button>
              ))}
            </div>
            <div className="composer-row">
              <input
                type="text"
                value={text}
                placeholder={file ? "可留空直接发送文件，或补一句说明" : "输入指令，回车发送"}
                onChange={(e) => setText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    void send(text, file);
                  }
                }}
              />
              <button onClick={() => void send(text, file)} disabled={busy || (!text.trim() && !file)}>
                {busy ? "发送中…" : "发送"}
              </button>
            </div>
            {file && (
              <div className="field-hint" style={{ marginTop: 0 }}>
                待发送：<b>{file.name}</b>
                {text.trim() ? "（同时带上文字）" : "（不发文字，按默认/口令日期入库）"}
              </div>
            )}
          </div>

          {err && (
            <p className="err tiny" style={{ marginBottom: 0 }}>
              {err}
            </p>
          )}
        </section>

        {/* ---------------- 右：通道 ---------------- */}
        <div>
          <section className="card">
            <div className="card-head">
              <h4>通道状态</h4>
              <button className="ghost btn-sm" onClick={() => void loadStatus()}>
                刷新
              </button>
            </div>
            <div className="toolbar" style={{ marginBottom: 12 }}>
              <span className={onlineCount > 0 ? "badge badge-ok" : "badge badge-muted"}>
                {onlineCount > 0 ? `${onlineCount} 个通道运行中` : "全部离线"}
              </span>
              <span className="spacer" />
              <span className="tiny muted">每日 1 点索要 CSV</span>
              <button
                className={push ? "btn-sm ghost" : "btn-sm"}
                onClick={async () => {
                  try {
                    await api.botSetPush(!push);
                    await loadStatus();
                  } catch (e) {
                    setErr((e as Error).message);
                  }
                }}
              >
                {push ? "已开启" : "已关闭"}
              </button>
              <button
                className="btn-sm ghost"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  setErr("");
                  try {
                    const r = await api.botRemindNow();
                    setTurns((prev) => [
                      ...prev,
                      {
                        id: ++seq.current,
                        dir: "out" as const,
                        text: `📣 已按当前范围发送索要提醒：\n${r.text}\n${r.channels.join("；") || "（没有可发送的会话）"}`,
                        at: nowLabel(),
                      },
                    ]);
                  } catch (e) {
                    setErr((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                立即索要
              </button>
            </div>

            {/* 索要范围：群聊 / 私聊独立开关，管的是定时与「立即索要」两者 */}
            <div className="toolbar" style={{ marginBottom: 12 }}>
              <span className="tiny muted">索要范围</span>
              <button
                className={targets.groups ? "chip on" : "chip"}
                disabled={busy}
                onClick={async () => {
                  try {
                    const r = await api.botSetReminderTargets(!targets.groups, targets.private);
                    setTargets(r.reminder);
                  } catch (e) {
                    setErr((e as Error).message);
                  }
                }}
              >
                👥 群聊{targets.groups ? "：发" : "：不发"}
              </button>
              <button
                className={targets.private ? "chip on" : "chip"}
                disabled={busy}
                onClick={async () => {
                  try {
                    const r = await api.botSetReminderTargets(targets.groups, !targets.private);
                    setTargets(r.reminder);
                  } catch (e) {
                    setErr((e as Error).message);
                  }
                }}
              >
                🔒 私聊{targets.private ? "：发" : "：不发"}
              </button>
            </div>

            <div className="grid" style={{ gap: 10 }}>
              {status.map((c) => (
                <ChannelCard key={c.name} c={c} onChanged={loadStatus} onError={setErr} />
              ))}
            </div>
          </section>

          <WeixinLoginPanel onChanged={loadStatus} onError={setErr} />
          <QqCredentialsPanel onChanged={loadStatus} onError={setErr} />
        </div>
      </div>

      <div className="grid2">
        <section className="card">
          <div className="card-head">
            <h4>指令速查</h4>
            <span className="tiny muted">群里照这些话术敲就行</span>
          </div>
          <div className="table-wrap">
            <table style={{ width: "100%" }}>
              <thead>
                <tr>
                  <th>说法</th>
                  <th>效果</th>
                </tr>
              </thead>
              <tbody>
                {COMMANDS.map(([k, v]) => (
                  <tr key={k}>
                    <td style={{ whiteSpace: "normal" }}>
                      <code>{k}</code>
                    </td>
                    <td className="muted" style={{ whiteSpace: "normal" }}>
                      {v}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="notice notice-info" style={{ marginTop: 12 }}>
            <span>
              日期规则：没指定 → 昨天；指定了哪天（如 9.1）就写哪天，系统不会自动偏移。
              指定到未来的日期会给提醒，但仍然按你说的写。
            </span>
          </div>
        </section>

        <MessageLogCard turns={turns.length} />
      </div>
    </>
  );
}

/* ------------------------------ 通道卡片 ------------------------------ */

function ChannelCard({
  c,
  onChanged,
  onError,
}: {
  c: BotChannelStatus;
  onChanged: () => void;
  onError: (m: string) => void;
}) {
  const meta = CHANNEL_META[c.name] ?? { label: c.name, icon: "📡", hint: "" };
  const [busy, setBusy] = useState(false);

  const act = async (verb: "start" | "stop") => {
    setBusy(true);
    onError("");
    try {
      if (verb === "start") await api.botStart(c.name);
      else await api.botStop(c.name);
      onChanged();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      style={{
        border: "1px solid var(--border)",
        borderRadius: "var(--radius)",
        padding: 12,
        background: "var(--surface-2)",
      }}
    >
      <div className="card-head" style={{ marginBottom: 6 }}>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          <span style={{ marginRight: 6 }}>{meta.icon}</span>
          {meta.label}
        </span>
        <span className={c.running ? "badge badge-ok" : "badge badge-muted"}>
          {c.running ? "运行中" : "已停止"}
        </span>
      </div>
      <p className="tiny muted" style={{ margin: "0 0 10px" }}>
        {c.connected ? "已连接 · " : ""}
        {meta.hint}
        {c.note ? ` — ${c.note}` : ""}
      </p>
      <div style={{ display: "flex", gap: 6 }}>
        <button className="btn-sm" disabled={c.running || busy} onClick={() => void act("start")}>
          启动
        </button>
        <button className="ghost btn-sm" disabled={!c.running || busy} onClick={() => void act("stop")}>
          停止
        </button>
      </div>
    </div>
  );
}

/* ------------------------------ 消息日志 ------------------------------ */

function MessageLogCard({ turns }: { turns: number }) {
  const [logs, setLogs] = useState<BotLog[]>([]);
  const [err, setErr] = useState("");

  const load = async () => {
    try {
      setLogs(await api.botMessages(30));
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 12000);
    return () => window.clearInterval(t);
  }, [turns]); // 控制台有新对话就顺带刷新一次（涵盖 IM 通道的日志）

  return (
    <section className="card">
      <div className="card-head">
        <h4>消息日志</h4>
        <button className="ghost btn-sm" onClick={() => void load()}>
          刷新
        </button>
      </div>
      <p className="card-desc">两个通道最近收到的消息与机器人的回复。</p>
      {err && <p className="err tiny">{err}</p>}
      {logs.length === 0 ? (
        <div className="empty">
          <span className="empty-emoji">📭</span>
          还没有消息。机器人上线后，群里的每一句话都会出现在这里。
        </div>
      ) : (
        <div className="chat-wrap" style={{ height: 300 }}>
          {logs.map((l, i) => (
            <div key={i} className={l.dir === "out" ? "bubble-row out" : "bubble-row"}>
              <div>
                <div className={l.dir === "out" ? "bubble out" : "bubble in"}>{l.text}</div>
                <div className="bubble-meta">
                  {l.channel === "qq" ? "QQ" : l.channel === "weixin" ? "微信" : "网页"} ·{" "}
                  {l.intent ? INTENT_LABEL[l.intent] ?? l.intent : ""} ·{" "}
                  {new Date(l.at).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

/* ---------------------------- 微信扫码登录 ---------------------------- */

function WeixinLoginPanel({
  onChanged,
  onError,
}: {
  onChanged: () => void;
  onError: (msg: string) => void;
}) {
  const [qrDataUrl, setQrDataUrl] = useState<string | null>(null);
  const [login, setLogin] = useState<{ phase: string; nickname?: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const timerRef = useRef<number | null>(null);

  const stopPolling = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  useEffect(() => stopPolling, [stopPolling]);

  const poll = useCallback(
    (stopped: { value: boolean }) => {
      stopPolling();
      timerRef.current = window.setTimeout(async () => {
        if (stopped.value) return;
        try {
          const st = await api.botWeixinLoginStatus();
          setLogin(st);
          if (st.loggedIn) {
            stopPolling();
            setQrDataUrl(null);
            onChanged();
            return;
          }
          if (st.phase === "session_expired" || st.phase === "error") {
            stopPolling();
            return;
          }
        } catch (e) {
          // 单次轮询失败不终止（网络抖动），连续失败交给用户手动取消
          onError((e as Error).message);
        }
        if (!stopped.value) poll(stopped);
      }, 2500);
    },
    [onChanged, onError, stopPolling],
  );

  const startLogin = async () => {
    setBusy(true);
    onError("");
    const stopped = { value: false };
    try {
      const qr = await api.botWeixinQRCode();
      const dataUrl =
        qr.url && qr.url.startsWith("data:")
          ? qr.url
          : await QRCode.toDataURL(qr.qrcode, { width: 220, margin: 1 });
      setQrDataUrl(dataUrl);
      setLogin({ phase: "awaiting_scan" });
      poll(stopped);
    } catch (e) {
      onError((e as Error).message);
      setLogin(null);
    } finally {
      setBusy(false);
    }
  };

  const cancelLogin = () => {
    stopPolling();
    setQrDataUrl(null);
    setLogin(null);
  };

  const phaseText = login ? LOGIN_PHASE_LABEL[login.phase] ?? login.phase : "";

  return (
    <section className="card">
      <div className="card-head">
        <h4>微信扫码登录</h4>
        <span className={login?.phase === "running" ? "badge badge-ok" : "badge badge-muted"}>
          {phaseText || "未开始"}
        </span>
      </div>
      <p className="card-desc">
        用任意微信扫码即可登录 iLink 机器人；登录后群里就能收发 CSV 文件。
      </p>
      <div style={{ display: "flex", gap: 14, alignItems: "flex-start", flexWrap: "wrap" }}>
        {qrDataUrl ? (
          <img
            src={qrDataUrl}
            alt="微信登录二维码"
            width={150}
            height={150}
            style={{ borderRadius: 10, border: "1px solid var(--border)", background: "#fff", padding: 6 }}
          />
        ) : (
          <div
            className="empty"
            style={{ width: 150, height: 150, padding: 8, gridTemplateRows: "1fr auto" }}
          >
            <span className="empty-emoji">📱</span>
            <span className="tiny">尚未取码</span>
          </div>
        )}
        <div style={{ minWidth: 140, display: "grid", gap: 8 }}>
          {login?.nickname && <span className="tiny">{login.nickname}</span>}
          {qrDataUrl ? (
            <>
              <button className="ghost btn-sm" onClick={cancelLogin}>
                取消取码
              </button>
              <span className="tiny muted">二维码过期后请重新取</span>
            </>
          ) : (
            <button className="btn-sm" onClick={() => void startLogin()} disabled={busy}>
              {busy ? "取码中…" : "扫码连接"}
            </button>
          )}
        </div>
      </div>
    </section>
  );
}

/* ---------------------------- QQ 凭证配置 ---------------------------- */

function QqCredentialsPanel({
  onChanged,
  onError,
}: {
  onChanged: () => void;
  onError: (msg: string) => void;
}) {
  const [appId, setAppId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!appId.trim() || !clientSecret.trim()) {
      onError("AppID 与 ClientSecret 都不能为空");
      return;
    }
    setBusy(true);
    onError("");
    try {
      await api.botQqCredentials(appId.trim(), clientSecret.trim());
      setSaved(true);
      setClientSecret("");
      onChanged();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="card">
      <div className="card-head">
        <h4>QQ 机器人配置</h4>
        {saved && <span className="badge badge-ok">已保存</span>}
      </div>
      <p className="card-desc">
        在 <a href="https://q.qq.com" target="_blank" rel="noreferrer">q.qq.com</a>{" "}
        建好机器人、订阅「群聊@消息 / 私聊消息」后填凭证，保存即挂、不用重启。
      </p>
      <div style={{ display: "grid", gap: 10 }}>
        <label className="field">
          <span className="field-label">AppID</span>
          <input
            value={appId}
            onChange={(e) => setAppId(e.target.value)}
            placeholder="从 QQ 开放平台复制"
            autoComplete="off"
            style={{ width: "100%" }}
          />
        </label>
        <label className="field">
          <span className="field-label">ClientSecret</span>
          <input
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            placeholder={saved ? "已保存，重填可覆盖" : "请勿泄露"}
            autoComplete="off"
            style={{ width: "100%" }}
          />
        </label>
        <div>
          <button onClick={() => void save()} disabled={busy}>
            {busy ? "保存中…" : "保存凭证"}
          </button>
        </div>
      </div>
    </section>
  );
}
