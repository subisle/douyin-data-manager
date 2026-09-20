"use client";

import { getDataApi } from "@/client/http-electron-api";
import { useCallback, useEffect, useState } from "react";
import { History, Loader2, RefreshCw } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

type ImportLogRow = {
  id: number;
  kind: string;
  importDate: string | null;
  fileName: string;
  rowCount: number;
  source: string;
  matchedCount: number;
  unmatchedCount: number;
  duplicateRows: number;
  createdAt: string | null;
};

const KIND_LABEL: Record<string, string> = { wave: "音浪", duration: "时长" };
const SOURCE_LABEL: Record<string, string> = { bot: "机器人", web: "网页" };

function friendlyDate(iso: string | null) {
  if (!iso) return "—";
  const text = String(iso).slice(0, 10);
  const [y, m, d] = text.split("-");
  if (!y || !m || !d) return text;
  return `${Number(m)}月${Number(d)}日`;
}

function friendlyTime(iso: string | null) {
  if (!iso) return "—";
  return String(iso).replace("T", " ").slice(0, 16);
}

export function ImportLogsPage() {
  const [logs, setLogs] = useState<ImportLogRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const api = typeof window !== "undefined" ? getDataApi() : null;

  const load = useCallback(async () => {
    if (!api) return;
    setLoading(true);
    setError(null);
    try {
      const res = await api.listImportLogs(100);
      if (res.success) {
        setLogs((res.data as ImportLogRow[]) || []);
      } else {
        setError(res.error || "读取失败");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [api]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">导入日志</h2>
          <p className="text-sm text-muted-foreground">
            每次 CSV 导入的留痕：来源（机器人/网页）、匹配与未匹配数。音浪 + 时长两个文件算一次导入的两个条目。
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
          刷新
        </Button>
      </div>

      {error ? (
        <Card>
          <CardContent className="py-6 text-sm text-destructive">{error}</CardContent>
        </Card>
      ) : loading ? (
        <Card>
          <CardContent className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" /> 加载中…
          </CardContent>
        </Card>
      ) : logs.length === 0 ? (
        <Card>
          <CardContent className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <History className="h-4 w-4" /> 还没有导入记录。发 CSV 或在「数据导入」页导入即可生成日志。
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent className="p-0">
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-left text-xs text-muted-foreground">
                    <th className="px-4 py-3 font-medium">时间</th>
                    <th className="px-4 py-3 font-medium">数据日期</th>
                    <th className="px-4 py-3 font-medium">类型</th>
                    <th className="px-4 py-3 font-medium">文件</th>
                    <th className="px-4 py-3 text-right font-medium">行数</th>
                    <th className="px-4 py-3 text-right font-medium">匹配</th>
                    <th className="px-4 py-3 text-right font-medium">未匹配</th>
                    <th className="px-4 py-3 font-medium">来源</th>
                  </tr>
                </thead>
                <tbody>
                  {logs.map((log) => (
                    <tr key={log.id} className="border-b border-border/60 last:border-0">
                      <td className="whitespace-nowrap px-4 py-2.5 text-xs text-muted-foreground">
                        {friendlyTime(log.createdAt)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5">{friendlyDate(log.importDate)}</td>
                      <td className="px-4 py-2.5">
                        <Badge variant={log.kind === "wave" ? "default" : "secondary"}>
                          {KIND_LABEL[log.kind] || log.kind}
                        </Badge>
                      </td>
                      <td className="max-w-56 truncate px-4 py-2.5" title={log.fileName}>
                        {log.fileName || "—"}
                      </td>
                      <td className="px-4 py-2.5 text-right tabular-nums">{log.rowCount}</td>
                      <td className="px-4 py-2.5 text-right tabular-nums text-emerald-600">{log.matchedCount}</td>
                      <td className="px-4 py-2.5 text-right tabular-nums text-amber-600">
                        {log.unmatchedCount}
                        {log.duplicateRows > 0 ? (
                          <span className="ml-1 text-xs text-muted-foreground">（重复 {log.duplicateRows}）</span>
                        ) : null}
                      </td>
                      <td className="px-4 py-2.5">
                        <span className="text-xs text-muted-foreground">
                          {SOURCE_LABEL[log.source] || log.source}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
