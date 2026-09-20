"use client";

import { useEffect, useState } from "react";
import { FileText, Upload, Download, Trash2, History } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { ImportPage, type DroppedImportFile } from "./import-page";
import { ImportLogsPage } from "./import-logs-page";
import { ExportPage } from "./export-page";
import { DailyReportPage } from "./daily-report-page";
import { DataCleanupPage } from "./data-cleanup-page";

type Tab = "report" | "import" | "logs" | "export" | "cleanup";

const TABS: { id: Tab; label: string; icon: typeof FileText }[] = [
  { id: "report", label: "每日报告", icon: FileText },
  { id: "import", label: "数据导入", icon: Upload },
  { id: "logs", label: "导入日志", icon: History },
  { id: "export", label: "数据导出", icon: Download },
  { id: "cleanup", label: "数据清理", icon: Trash2 },
];

export function DataPage({
  incomingFile,
  onIncomingFileConsumed,
}: {
  incomingFile?: DroppedImportFile | null;
  onIncomingFileConsumed?: () => void;
}) {
  const [tab, setTab] = useState<Tab>("report");

  useEffect(() => {
    if (incomingFile) setTab("import");
  }, [incomingFile]);

  return (
    <div className="space-y-6">
      {/* 子功能标签切换 */}
      <div className="inline-flex rounded-full border border-border bg-card p-1">
        {TABS.map((t) => {
          const Icon = t.icon;
          return (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className={cn(
                "flex items-center gap-2 rounded-full px-5 py-2 text-sm font-medium transition-colors",
                tab === t.id
                  ? "bg-primary text-primary-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              <Icon className="size-4" />
              {t.label}
            </button>
          );
        })}
      </div>

      {tab === "report" ? (
        <DailyReportPage />
      ) : tab === "cleanup" ? (
        <DataCleanupPage />
      ) : tab === "import" ? (
        <Card>
          <CardContent className="space-y-6 pt-6 pb-8">
            <ImportPage
              incomingFile={incomingFile}
              onIncomingFileConsumed={onIncomingFileConsumed}
            />
          </CardContent>
        </Card>
      ) : tab === "logs" ? (
        <ImportLogsPage />
      ) : (
        <Card>
          <CardContent className="space-y-6 pt-6 pb-8">
            <ExportPage />
          </CardContent>
        </Card>
      )}
    </div>
  );
}
