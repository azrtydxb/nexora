import { Link, useSearchParams } from "react-router";

import { useAiForecasts } from "@/api/ai";
import type { Schemas } from "@/api/client";
import { AiPage } from "@/components/ai/AiOff";
import {
  rtt,
  UpstreamTrendBadge,
  type UpstreamPrediction,
} from "@/components/ai/UpstreamPredictionsPanel";
import { ErrorAlert, formatAgo } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Card } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

type Forecast = Schemas["AiForecast"];
type Capacity = NonNullable<Forecast["capacity"]>;
type Kind = Forecast["kind"];

const kinds: { value: Kind; label: string }[] = [
  { value: "upstream", label: "Upstream predictions" },
  { value: "capacity", label: "Capacity" },
];

const clock = new Intl.DateTimeFormat(undefined, { timeStyle: "short" });
const compact = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
});

export function AiForecastsPage() {
  return (
    <AiPage
      title="Forecasts"
      description="Upstream health predictions and capacity forecasts, with when each was generated."
    >
      <Forecasts />
    </AiPage>
  );
}

function Forecasts() {
  const [params, setParams] = useSearchParams();
  const kind: Kind =
    params.get("kind") === "capacity" ? "capacity" : "upstream";
  const q = useAiForecasts(kind);

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-1.5">
        <Select
          value={kind}
          onValueChange={(v) => {
            const next = new URLSearchParams(params);
            next.set("kind", v);
            setParams(next, { replace: true });
          }}
        >
          <SelectTrigger
            id="ai-forecasts-kind"
            data-testid="ai-forecasts-kind"
            aria-label="Forecast kind"
            className="h-9 w-56"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {kinds.map((k) => (
              <SelectItem
                key={k.value}
                value={k.value}
                data-testid={`ai-forecasts-kind-${k.value}`}
              >
                {k.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <HelpTip id="ai-forecasts-kind" label="Forecast kind" />
      </div>
      <ErrorAlert error={q.error} prefix="Could not load the forecasts" />
      {q.data?.length === 0 && (
        <Card className="text-muted-foreground p-10 text-center text-sm">
          No{" "}
          {kind === "upstream" ? "upstream predictions" : "capacity forecasts"}{" "}
          yet. The agent stores one the next time it runs.
        </Card>
      )}
      {q.data?.map((f) => (
        <Card
          key={f.id}
          data-testid={`ai-forecast-${f.subject}`}
          className="space-y-2 p-4"
        >
          {f.upstream ? (
            <UpstreamBody p={f.upstream} />
          ) : f.capacity ? (
            <CapacityBody c={f.capacity} />
          ) : null}
          <p
            data-testid="ai-forecast-freshness"
            className="text-muted-foreground text-xs"
          >
            {freshness(f)}
          </p>
          {f.proposal_id !== null && (
            <Link
              to={`/ai/recommendations?proposal=${f.proposal_id}`}
              data-testid="ai-forecast-proposal"
              className="text-primary inline-block text-sm underline-offset-4 hover:underline"
            >
              Review the suggested change
            </Link>
          )}
        </Card>
      ))}
    </div>
  );
}

/**
 * "Updated 3 hours ago · valid until 18:00", with " · stale" once valid_until has passed — a stale
 * forecast is still shown, because it is what the agent last knew.
 */
function freshness(f: Forecast): string {
  const until = new Date(f.valid_until);
  const stale = until.getTime() < Date.now() ? " · stale" : "";
  return `Updated ${formatAgo(f.generated_at)} · valid until ${clock.format(until)}${stale}`;
}

function UpstreamBody({ p }: { p: UpstreamPrediction }) {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-medium break-all">{p.upstream_name}</h3>
        <UpstreamTrendBadge trend={p.trend} />
        <span className="text-muted-foreground text-xs tabular-nums">
          confidence {Math.round(p.confidence * 100)}% ·{" "}
          {p.data_points_analyzed} samples
        </span>
      </div>
      <p className="text-sm tabular-nums">
        p50 {rtt(p.current_rtt_p50_ms)} · p99 {rtt(p.current_rtt_p99_ms)} ·{" "}
        {p.slope_ms_per_hour >= 0 ? "+" : ""}
        {p.slope_ms_per_hour.toFixed(1)} ms/h
      </p>
      {p.reasoning && (
        <p className="text-muted-foreground text-sm break-words">
          {p.reasoning}
        </p>
      )}
      {p.recommendation.type !== "none" && (
        <p className="text-sm break-words">{p.recommendation.description}</p>
      )}
    </>
  );
}

function CapacityBody({ c }: { c: Capacity }) {
  const max = c.max_value !== null && c.max_value > 0 ? c.max_value : null;
  const percent =
    max === null ? 0 : Math.min(100, Math.round((c.current_value / max) * 100));
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-medium break-all">{c.resource}</h3>
        <span className="text-muted-foreground text-xs tabular-nums">
          {c.trend} · confidence {Math.round(c.confidence * 100)}% ·{" "}
          {compact.format(c.growth_per_day)}/day
        </span>
      </div>
      {max !== null ? (
        <div className="space-y-1">
          <div className="flex items-baseline justify-between gap-2 text-sm tabular-nums">
            <span>
              {compact.format(c.current_value)} of {compact.format(max)}
            </span>
            <span
              data-testid="ai-capacity-days"
              className="text-muted-foreground shrink-0 text-xs"
            >
              {c.days_remaining === null
                ? "no exhaustion in sight"
                : `${c.days_remaining} days`}
            </span>
          </div>
          <div
            data-testid="ai-capacity-bar"
            role="progressbar"
            aria-label={`${c.resource} used`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent}
            className="bg-muted h-1.5 overflow-hidden rounded-full"
          >
            <div
              className="bg-primary h-full"
              style={{ width: `${percent}%` }}
            />
          </div>
        </div>
      ) : (
        <p className="text-sm tabular-nums">
          {compact.format(c.current_value)} ·{" "}
          <span data-testid="ai-capacity-days">No limit</span>
        </p>
      )}
      {c.recommendation && (
        <p className="text-sm break-words">{c.recommendation}</p>
      )}
    </>
  );
}
