import { callLegacyDb } from "@/server/db/legacy-db";
import { requireApiAccess } from "@/server/api/auth";
import { apiFail, apiOk, errorMessage, type ApiErrorCode } from "@/server/api/response";
import { handleBotsRequest } from "@/server/bots/http.js";


function botsApiFail(status: number, payload: { error?: string; code?: string }) {
  const code: ApiErrorCode = payload?.code === "BOTS_SKIPPED" ? "BOTS_SKIPPED" : "BOTS_ERROR";
  return apiFail(payload?.error || "机器人接口失败", status, code);
}

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ path?: string[] }>;
};

async function getPath(context: RouteContext) {
  const params = await context.params;
  return params.path ?? [];
}

async function readJson(request: Request) {
  const text = await request.text();
  if (!text.trim()) return {};
  return JSON.parse(text) as Record<string, unknown>;
}

function searchParams(request: Request) {
  return new URL(request.url).searchParams;
}

function num(value: string | null, fallback?: number) {
  if (value == null || value === "") return fallback;
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function str(value: string | null, fallback = "") {
  return value == null || value === "" ? fallback : value;
}

async function call(method: string, ...args: unknown[]) {
  return callLegacyDb(method, ...args);
}

function authGuard(request: Request, options: { public?: boolean } = {}) {
  return requireApiAccess(request, options);
}

export async function GET(request: Request, context: RouteContext) {
  try {
    const path = await getPath(context);
    const sp = searchParams(request);
    const [one, two, three] = path;

    if (path.length === 0) {
      return apiOk({
        name: "douyin-api",
        version: "v1",
        status: "ok",
        docs: "/docs/api-design.md",
      });
    }

    if (one === "health") return apiOk({ status: "ok" });

    const auth = authGuard(request);
    if (auth) return auth;

    if (one === "startup-health") return apiOk(await call("getStartupHealth"));

    if (one === "imports" && two === "logs") {
      return apiOk(await call("listImportLogs", num(sp.get("limit"), 50)));
    }

    if (one === "bots") {
      const result = await handleBotsRequest({
        method: "GET",
        path,
        searchParams: sp,
      });
      if (result.status >= 400) {
        const payload = result.body as { error?: string; code?: string };
        return botsApiFail(result.status, payload);
      }
      return apiOk(result.body);
    }

    if (one === "anchors" && path.length === 1) return apiOk(await call("getAnchors"));
    if (one === "anchors" && two === "duplicates") return apiOk(await call("findDuplicateAnchors"));
    if (one === "anchors" && two && three === "daily-snapshot") {
      return apiOk(await call("getAnchorDailySnapshot", two, str(sp.get("date"))));
    }
    if (one === "anchors" && two && three === "wave-trend") {
      return apiOk(await call("getAnchorWaveTrend", two));
    }
    if (one === "anchors-wave-trend") {
      const ids = str(sp.get("ids")).split(",").map((id) => id.trim()).filter(Boolean);
      return apiOk(await call("getAnchorsWaveTrend", ids));
    }

    if (one === "family-tree") return apiOk(await call("getFamilyTree"));
    if (one === "roster" && two) return apiOk(await call("getRosterBySurname", two));

    if (one === "dashboard" && two === "summary") return apiOk(await call("getDashboardSummary"));
    if (one === "dashboard" && two === "wave-ranking") return apiOk(await call("getWaveRanking", num(sp.get("limit"), 10)));
    if (one === "dashboard" && two === "wave-trend-by-gender") return apiOk(await call("getWaveTrendByGender"));
    if (one === "dashboard" && two === "wave-trend-total") return apiOk(await call("getWaveTrendTotal"));
    if (one === "dashboard" && two === "anchor-count-trend") return apiOk(await call("getAnchorCountTrend"));

    if (one === "exports" && two === "wave") return apiOk(await call("exportWaveSnapshots", sp.get("date") || undefined));
    if (one === "exports" && two === "duration") return apiOk(await call("exportDurationSnapshots", sp.get("date") || undefined));
    if (one === "exports" && two === "anchors") return apiOk(await call("exportAnchors"));
    if (one === "exports" && two === "family-roster") return apiOk(await call("exportFamilyRoster"));

    if (one === "reports" && two === "daily-wave") {
      return apiOk(await call("getDailyWaveReport", str(sp.get("date")), str(sp.get("gender"), "all")));
    }
    if (one === "reports" && two === "monthly") {
      return apiOk(await call("getMonthlyReport", str(sp.get("month")), str(sp.get("gender"), "all")));
    }
    if (one === "reports" && two === "tier-rules") return apiOk(await call("getTierRules"));

    if (one === "data-cleanup" && two === "preview") {
      return apiOk(await call("getDataCleanupSummary", str(sp.get("from")), str(sp.get("to"))));
    }

    if (one === "flags" && two === "groups") return apiOk(await call("getFlagGroups", str(sp.get("period"))));
    if (one === "flags" && two === "winner") return apiOk(await call("getFlagWinner", str(sp.get("period"))));
    if (one === "flags" && two === "person" && three) return apiOk(await call("getFlowingFlag", Number(three)));

    if (one === "pk" && two === "roster") return apiOk(await call("getPkRoster", sp.get("period") || undefined, num(sp.get("groupSize"), 8)));
    if (one === "star-battle" && two === "scores") return apiOk(await call("getStarBattleScores", str(sp.get("period"))));
    if (one === "rewards" && two === "report") return apiOk(await call("getRewardReport", str(sp.get("period"))));

    if (one === "income" && two === "periods") return apiOk(await call("getIncomePeriods"));
    if (one === "income" && path.length === 1) {
      return apiOk(await call("getAnchorIncome", str(sp.get("period"))));
    }

    return apiFail(`接口不存在：GET /api/v1/${path.join("/")}`, 404, "NOT_FOUND");
  } catch (error) {
    console.error("[api/v1][GET]", error);
    return apiFail(errorMessage(error), 500, "INTERNAL_ERROR");
  }
}

export async function POST(request: Request, context: RouteContext) {
  try {
    const auth = authGuard(request);
    if (auth) return auth;

    const path = await getPath(context);
    const body = await readJson(request);
    const [one, two] = path;

    if (one === "anchors" && path.length === 1) return apiOk(await call("addAnchor", body), 201);
    if (one === "anchors" && two === "batch-import") return apiOk(await call("batchImportAnchors", body.rows ?? body));
    if (one === "anchors" && two === "merge-accounts") return apiOk(await call("mergeAccounts", body));

    if (one === "imports" && two === "preview") {
      return apiOk(await call("getImportPreview", body.kind, body.date, body.anchorIds, body.meta));
    }
    if (one === "imports" && two === "wave") {
      const rawInfo = (body.info ?? {}) as Record<string, unknown>;
      const info = { ...rawInfo, source: (rawInfo.source as string) || "web" };
      return apiOk(await call("importWaveSnapshots", body.date, body.rows, body.meta, info));
    }
    if (one === "imports" && two === "duration") {
      const rawInfo = (body.info ?? {}) as Record<string, unknown>;
      const info = { ...rawInfo, source: (rawInfo.source as string) || "web" };
      return apiOk(await call("importDurationSnapshots", body.date, body.rows, body.meta, info));
    }

    if (one === "flags" && two === "settle") return apiOk(await call("settleFlagScores", body.period));

    if (one === "star-battle" && two === "scores") return apiOk(await call("saveStarBattleScore", body));
    if (one === "pk" && two === "groups") return apiOk(await call("buildPkGroups", body));

    if (one === "rewards" && two === "report") return apiOk(await call("getRewardReport", body.period, body.config));

    if (one === "income" && two === "import") return apiOk(await call("importAnchorIncome", body));
    if (one === "income" && two === "profile") return apiOk(await call("saveAnchorIncomeProfile", body));

    if (one === "bots") {
      const result = await handleBotsRequest({
        method: "POST",
        path,
        body,
        searchParams: searchParams(request),
      });
      if (result.status >= 400) {
        const payload = result.body as { error?: string; code?: string };
        return botsApiFail(result.status, payload);
      }
      return apiOk(result.body);
    }

    return apiFail(`接口不存在：POST /api/v1/${path.join("/")}`, 404, "NOT_FOUND");
  } catch (error) {
    console.error("[api/v1][POST]", error);
    return apiFail(errorMessage(error), 500, "INTERNAL_ERROR");
  }
}

export async function PATCH(request: Request, context: RouteContext) {
  try {
    const auth = authGuard(request);
    if (auth) return auth;

    const path = await getPath(context);
    const body = await readJson(request);
    const [one, two, three] = path;

    if (one === "anchors" && two && path.length === 2) {
      return apiOk(await call("updateAnchorInfo", { ...body, personId: Number(two) }));
    }
    if (one === "anchors" && two && three === "name") {
      return apiOk(await call("updateAnchorName", { ...body, personId: Number(two) }));
    }
    if (one === "anchors" && two && three === "master") {
      return apiOk(await call("updateAnchorMaster", { ...body, personId: Number(two) }));
    }

    return apiFail(`接口不存在：PATCH /api/v1/${path.join("/")}`, 404, "NOT_FOUND");
  } catch (error) {
    console.error("[api/v1][PATCH]", error);
    return apiFail(errorMessage(error), 500, "INTERNAL_ERROR");
  }
}

export async function PUT(request: Request, context: RouteContext) {
  try {
    const auth = authGuard(request);
    if (auth) return auth;

    const path = await getPath(context);
    const body = await readJson(request);
    const [one, two, three] = path;

    if (one === "anchors" && two && three === "daily-snapshot") {
      return apiOk(await call("saveAnchorDailySnapshot", { ...body, anchorId: two }));
    }
    if (one === "reports" && two === "tier-rules") return apiOk(await call("saveTierRules", body.rules ?? body));

    return apiFail(`接口不存在：PUT /api/v1/${path.join("/")}`, 404, "NOT_FOUND");
  } catch (error) {
    console.error("[api/v1][PUT]", error);
    return apiFail(errorMessage(error), 500, "INTERNAL_ERROR");
  }
}

export async function DELETE(request: Request, context: RouteContext) {
  try {
    const auth = authGuard(request);
    if (auth) return auth;

    const path = await getPath(context);
    const [one, two] = path;

    if (one === "anchors" && two) return apiOk(await call("deleteAnchors", [Number(two)]));
    if (one === "anchors" && path.length === 1) {
      const body = await readJson(request);
      const ids = Array.isArray(body.ids) ? body.ids.map(Number).filter(Number.isFinite) : [];
      if (ids.length === 0) return apiFail("缺少要删除的主播 ID", 400, "MISSING_IDS");
      return apiOk(await call("deleteAnchors", ids));
    }

    if (one === "data-cleanup" && path.length === 1) {
      const body = await readJson(request);
      const from = str(String(body.from ?? ""));
      const to = str(String(body.to ?? ""));
      if (!from || !to) return apiFail("缺少删除区间 from / to", 400, "BAD_REQUEST");
      return apiOk(await call("deleteDataByDateRange", from, to));
    }

    if (one === "income" && path.length === 1) {
      const body = await readJson(request);
      const ids = Array.isArray(body.personIds) ? body.personIds.map(Number).filter(Number.isFinite) : [];
      return apiOk(await call("deleteAnchorIncome", str(String(body.period ?? "")), ids));
    }

    return apiFail(`接口不存在：DELETE /api/v1/${path.join("/")}`, 404, "NOT_FOUND");
  } catch (error) {
    console.error("[api/v1][DELETE]", error);
    return apiFail(errorMessage(error), 500, "INTERNAL_ERROR");
  }
}
