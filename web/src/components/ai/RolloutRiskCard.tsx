import { Link } from "react-router";
import { Sparkles } from "lucide-react";

import { useAiRolloutRisk } from "@/api/ai";
import { ErrorAlert, formatAgo } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const levelClass: Record<string, string> = {
  low: "border-success/40 text-success",
  medium: "border-warning/40 text-warning",
  high: "border-destructive/40 text-destructive",
};

/**
 * One rollout's AI risk assessment: the score, the level, why the model scored it that way, the past
 * rollouts it compared against and the rollout settings it suggests. The assessment is advice about a
 * rollout that already happened or is running; it never changes the rollout.
 */
export function RolloutRiskCard({ rolloutId }: { rolloutId: string }) {
  const q = useAiRolloutRisk(rolloutId);
  const r = q.data;
  return (
    <Card data-testid="ai-rollout-risk" className="space-y-3 p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 text-sm font-semibold">
          <Sparkles className="text-muted-foreground h-4 w-4" />
          AI rollout risk
        </h2>
        {r?.status === "assessed" && (
          <div className="flex items-center gap-3">
            <span className="text-sm">
              Risk{" "}
              <span
                data-testid="ai-risk-score"
                className="font-medium tabular-nums"
              >
                {r.risk_score ?? 0}/10
              </span>
            </span>
            {r.risk_level && (
              <Badge
                variant="outline"
                data-testid="ai-risk-level"
                className={cn(levelClass[r.risk_level])}
              >
                {r.risk_level}
              </Badge>
            )}
          </div>
        )}
      </div>
      <ErrorAlert error={q.error} prefix="Could not load the risk assessment" />
      {r?.status === "pending" && (
        <p className="text-muted-foreground text-sm">Assessing…</p>
      )}
      {r?.status === "skipped" && (
        <p className="text-muted-foreground text-sm">
          Not assessed (rollback or republish).
        </p>
      )}
      {r?.status === "failed" && (
        <p className="text-muted-foreground text-sm">
          Assessment failed{r.error ? `: ${r.error}` : "."}
        </p>
      )}
      {r?.status === "assessed" && (
        <>
          <p data-testid="ai-risk-analysis" className="text-sm break-words">
            {r.analysis}
            {r.assessed_at && (
              <span className="text-muted-foreground">
                {" "}
                Assessed {formatAgo(r.assessed_at)}.
              </span>
            )}
          </p>
          {r.historical_patterns.length > 0 && (
            <div className="space-y-1.5">
              <h3 className="text-muted-foreground text-xs font-medium">
                Comparable past rollouts
              </h3>
              <ul className="space-y-1">
                {r.historical_patterns.map((h) => (
                  <li
                    key={h.config_version}
                    data-testid="ai-risk-history"
                    className="flex flex-wrap items-baseline gap-x-2 text-sm"
                  >
                    <span className="font-medium tabular-nums">
                      v{h.config_version}
                    </span>
                    <span className="break-words">{h.description}</span>
                    <span className="text-muted-foreground">
                      {h.outcome}
                      {h.canary_rejected && ", canary rejected"}, SERVFAIL up to{" "}
                      {Math.round(h.max_servfail_ratio * 100)}%
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {r.recommendation && (
            <p data-testid="ai-risk-recommendation" className="text-sm">
              <span className="text-muted-foreground">
                Suggested settings:{" "}
              </span>
              <span className="font-mono text-[13px]">
                {r.recommendation.strategy}
              </span>
              , {r.recommendation.canary_count} canary, at least{" "}
              {r.recommendation.min_health_queries} healthy queries, SERVFAIL
              under {Math.round(r.recommendation.max_servfail_ratio * 100)}%.{" "}
              <span className="break-words">{r.recommendation.reasoning}</span>
            </p>
          )}
          {r.proposal_id && (
            <Link
              to="/ai/recommendations?source=rollout_risk&status=all"
              data-testid="ai-risk-proposal"
              className="text-primary inline-block text-sm underline-offset-4 hover:underline"
            >
              Review the proposal
            </Link>
          )}
        </>
      )}
    </Card>
  );
}
