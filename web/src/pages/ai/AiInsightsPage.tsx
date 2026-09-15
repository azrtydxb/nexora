import { Link, useSearchParams } from "react-router";

import { useAiFindings, type AiFinding } from "@/api/ai";
import { useCan } from "@/auth/AuthProvider";
import { AiPage } from "@/components/ai/AiOff";
import { FindingCard } from "@/components/ai/FindingCard";
import { ErrorAlert } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Card } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

type Kind = "anomaly" | "insight";

const kinds: { value: Kind; label: string }[] = [
  { value: "anomaly", label: "Anomalies" },
  { value: "insight", label: "Insights" },
];

const statuses = ["open", "acknowledged", "dismissed", "resolved", "all"];

/** A cause the insight agent proposed, with how confident the model was in it. */
type Cause = { cause: string; confidence: number };

function strings(detail: AiFinding["detail"], key: string): string[] {
  const v = detail[key];
  return Array.isArray(v)
    ? v.filter((e): e is string => typeof e === "string")
    : [];
}

function causes(detail: AiFinding["detail"]): Cause[] {
  const v = detail["possible_causes"];
  if (!Array.isArray(v)) return [];
  const out: Cause[] = [];
  for (const e of v) {
    if (e === null || typeof e !== "object") continue;
    const { cause, confidence } = e as Record<string, unknown>;
    if (typeof cause === "string" && cause !== "")
      out.push({
        cause,
        confidence: typeof confidence === "number" ? confidence : 0,
      });
  }
  return out;
}

export function AiInsightsPage() {
  return (
    <AiPage
      title="Insights"
      description="Query-log anomalies and fleet insights the AI agents found, to acknowledge or dismiss."
    >
      <Findings />
    </AiPage>
  );
}

function Findings() {
  const [params, setParams] = useSearchParams();
  const kind: Kind = params.get("kind") === "insight" ? "insight" : "anomaly";
  const raw = params.get("status") ?? "open";
  const status = statuses.includes(raw) ? raw : "open";
  const canEdit = useCan("updateAiFinding");
  const q = useAiFindings(kind, status === "all" ? undefined : status);

  function update(key: string, value: string) {
    const next = new URLSearchParams(params);
    next.set(key, value);
    setParams(next, { replace: true });
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-1.5">
          <Tabs value={kind} onValueChange={(v) => update("kind", v)}>
            <TabsList
              data-testid="ai-findings-kind"
              data-help="ai-findings-kind"
            >
              {kinds.map((k) => (
                <TabsTrigger
                  key={k.value}
                  value={k.value}
                  data-testid={`ai-insights-tab-${k.value}`}
                >
                  {k.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <HelpTip id="ai-findings-kind" label="Findings" />
        </div>
        <div className="flex items-center gap-1.5">
          <Select value={status} onValueChange={(v) => update("status", v)}>
            <SelectTrigger
              id="ai-findings-status"
              data-testid="ai-findings-status"
              aria-label="Status"
              className="h-9 w-44"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {statuses.map((s) => (
                <SelectItem
                  key={s}
                  value={s}
                  data-testid={`ai-findings-status-${s}`}
                >
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <HelpTip id="ai-findings-status" label="Status" />
        </div>
      </div>
      <ErrorAlert error={q.error} prefix="Could not load the findings" />
      {q.data?.length === 0 && (
        <Card className="text-muted-foreground p-10 text-center text-sm">
          No {kind === "anomaly" ? "anomalies" : "insights"} with status{" "}
          {status}.
        </Card>
      )}
      {q.data?.map((f) => (
        <FindingCard key={f.id} finding={f} canEdit={canEdit}>
          {kind === "insight" ? (
            <Causes causes={causes(f.detail)} />
          ) : (
            <AnomalyDetail detail={f.detail} />
          )}
        </FindingCard>
      ))}
    </div>
  );
}

function Causes({ causes }: { causes: Cause[] }) {
  if (causes.length === 0) return null;
  return (
    <div className="space-y-2">
      <h4 className="text-muted-foreground text-xs font-medium">
        Possible causes
      </h4>
      {causes.map((c) => (
        <div key={c.cause} data-testid="ai-insight-cause" className="space-y-1">
          <div className="flex items-baseline justify-between gap-2 text-sm">
            <span className="break-words">{c.cause}</span>
            <span className="text-muted-foreground shrink-0 text-xs tabular-nums">
              {Math.round(c.confidence * 100)}%
            </span>
          </div>
          <div
            role="progressbar"
            aria-label={`Confidence in: ${c.cause}`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(c.confidence * 100)}
            className="bg-muted h-1.5 overflow-hidden rounded-full"
          >
            <div
              className="bg-primary h-full"
              style={{ width: `${Math.round(c.confidence * 100)}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

function AnomalyDetail({ detail }: { detail: AiFinding["detail"] }) {
  const clients = strings(detail, "affected_clients");
  const domains = strings(detail, "sample_domains");
  return (
    <div className="space-y-1 text-xs">
      {clients.length > 0 && (
        <p className="break-words">
          <span className="text-muted-foreground">Clients: </span>
          <span className="font-mono">{clients.join(", ")}</span>
        </p>
      )}
      {domains.length > 0 && (
        <p className="break-words">
          <span className="text-muted-foreground">Sample domains: </span>
          <span className="font-mono">{domains.join(", ")}</span>
        </p>
      )}
      {clients.length > 0 && (
        <Link
          to={`/query-log?client=${encodeURIComponent(clients[0])}`}
          data-testid="ai-anomaly-query-log"
          className="text-primary inline-block underline-offset-4 hover:underline"
        >
          Find in query log
        </Link>
      )}
    </div>
  );
}
