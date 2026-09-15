import { useAiForecasts } from "@/api/ai";
import type { Schemas } from "@/api/client";
import { ErrorAlert } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export type UpstreamPrediction = NonNullable<Schemas["AiForecast"]["upstream"]>;

const trendClass: Record<UpstreamPrediction["trend"], string> = {
  stable: "border-border text-muted-foreground",
  improving: "border-success/40 text-success",
  degrading: "border-warning/40 text-warning",
  periodic: "border-warning/40 text-warning",
  failing: "border-destructive/40 text-destructive",
  insufficient_data: "border-border text-muted-foreground",
};

/** The trend of one upstream prediction, coloured by how bad it is. */
export function UpstreamTrendBadge({
  trend,
}: {
  trend: UpstreamPrediction["trend"];
}) {
  return (
    <Badge variant="outline" className={cn(trendClass[trend])}>
      {trend.replaceAll("_", " ")}
    </Badge>
  );
}

export function rtt(ms: number): string {
  return `${Math.round(ms)} ms`;
}

/**
 * The newest upstream prediction per upstream, under the upstream table on Forwarding & recursion.
 * Read-only: acting on a prediction means applying its proposal from Recommendations.
 * The caller mounts this only when AI is enabled, so the request is never made with AI off.
 */
export function UpstreamPredictionsPanel() {
  const q = useAiForecasts("upstream");
  const rows = (q.data ?? []).flatMap((f) =>
    f.upstream ? [{ id: f.id, p: f.upstream }] : [],
  );
  if (q.error) return <ErrorAlert error={q.error} prefix="AI predictions" />;
  if (rows.length === 0) return null;
  return (
    <Card data-testid="ai-upstream-predictions" className="mt-4 space-y-3 p-4">
      <div>
        <h3 className="text-sm font-medium">AI upstream predictions</h3>
        <p className="text-muted-foreground text-xs">
          Where each upstream's response time is heading. Suggestions only —
          nothing changes until you apply one from Recommendations.
        </p>
      </div>
      <ul className="space-y-2">
        {rows.map(({ id, p }) => (
          <li
            key={id}
            className="border-border/60 space-y-1 border-t pt-2 first:border-t-0 first:pt-0"
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm break-all">
                {p.upstream_name}
              </span>
              <UpstreamTrendBadge trend={p.trend} />
              <span className="text-muted-foreground text-xs tabular-nums">
                p50 {rtt(p.current_rtt_p50_ms)} · p99{" "}
                {rtt(p.current_rtt_p99_ms)}
              </span>
            </div>
            {p.recommendation.type !== "none" && (
              <p className="text-muted-foreground text-xs break-words">
                {p.recommendation.description}
              </p>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}
