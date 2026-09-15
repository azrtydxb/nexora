import { Link } from "react-router";
import { Sparkles } from "lucide-react";

import { useAiInsights, type AiFinding } from "@/api/ai";
import { ErrorAlert, formatAgo } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const severityRank: Record<AiFinding["severity"], number> = {
  critical: 0,
  warning: 1,
  info: 2,
};

const severityClass: Record<AiFinding["severity"], string> = {
  info: "border-border text-muted-foreground",
  warning: "border-warning/40 text-warning",
  critical: "border-destructive/40 text-destructive",
};

/**
 * The dashboard's AI summary: the fleet insight score (0 is quiet, 10 needs attention), the one-line
 * summary and the three most severe open insights. Rendered only while AI is on.
 */
export function DashboardAiCard() {
  const q = useAiInsights();
  const top = [...(q.data?.insights ?? [])]
    .filter((f) => f.status === "open")
    .sort(
      (a, b) =>
        severityRank[a.severity] - severityRank[b.severity] ||
        b.last_seen.localeCompare(a.last_seen),
    )
    .slice(0, 3);
  return (
    <Card data-testid="dashboard-ai-card" className="mb-4 space-y-3 p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 text-sm font-semibold">
          <Sparkles className="text-muted-foreground h-4 w-4" />
          AI insights
        </h2>
        <div className="flex items-center gap-3">
          <span className="text-sm">
            Score{" "}
            <span data-testid="ai-score" className="font-medium tabular-nums">
              {q.data?.score ?? 0}/10
            </span>
          </span>
          <Link
            to="/ai/insights?kind=insight"
            data-testid="dashboard-ai-card-all"
            className="text-primary text-sm underline-offset-4 hover:underline"
          >
            All insights
          </Link>
        </div>
      </div>
      <ErrorAlert error={q.error} prefix="Could not load the AI insights" />
      {q.data && (
        <p className="text-muted-foreground text-sm">
          {q.data.summary}
          {q.data.generated_at &&
            ` Last updated ${formatAgo(q.data.generated_at)}.`}
        </p>
      )}
      {top.length > 0 && (
        <ul className="space-y-1.5">
          {top.map((f) => (
            <li
              key={f.id}
              data-testid="ai-insight-top"
              className="flex items-start gap-2 text-sm"
            >
              <Badge
                variant="outline"
                className={cn("shrink-0", severityClass[f.severity])}
              >
                {f.severity}
              </Badge>
              <Link
                to="/ai/insights?kind=insight"
                className="min-w-0 break-words underline-offset-4 hover:underline"
              >
                {f.title}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
