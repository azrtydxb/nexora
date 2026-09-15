import { useState } from "react";
import { Link, useSearchParams } from "react-router";

import { useAiProposals, type AiProposal } from "@/api/ai";
import { useCan } from "@/auth/AuthProvider";
import { AiPage } from "@/components/ai/AiOff";
import { ProposalApplyDialog } from "@/components/ai/ProposalApplyDialog";
import { DismissDialog, ProposalCard } from "@/components/ai/ProposalCard";
import { ErrorAlert } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

type Source = AiProposal["source"];

const sources: { value: Source | "all"; label: string }[] = [
  { value: "all", label: "All" },
  { value: "filter_recommendations", label: "Filtering" },
  { value: "config_assistant", label: "Assistant" },
  { value: "upstream_prediction", label: "Upstreams" },
  { value: "rollout_risk", label: "Rollouts" },
  { value: "capacity_forecast", label: "Capacity" },
  { value: "rpz_suggestions", label: "RPZ" },
];

const statuses = [
  "open",
  "applied",
  "failed",
  "stale",
  "dismissed",
  "superseded",
  "all",
];

export function AiRecommendationsPage() {
  return (
    <AiPage
      title="Recommendations"
      description="Configuration changes the AI suggests. Nothing changes until an operator applies a proposal."
    >
      <Proposals />
    </AiPage>
  );
}

function Proposals() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("source") ?? "all";
  const source = sources.some((s) => s.value === raw)
    ? (raw as Source | "all")
    : "all";
  const rawStatus = params.get("status") ?? "open";
  const status = statuses.includes(rawStatus) ? rawStatus : "open";
  const canApply = useCan("applyAiProposals");
  const [selected, setSelected] = useState<string[]>([]);
  const [applying, setApplying] = useState(false);
  const [dismissing, setDismissing] = useState(false);
  // The RPZ tab links to the RPZ page, which reviews suggested rules record by record.
  const rpz = source === "rpz_suggestions";
  const q = useAiProposals(
    source === "all" || rpz ? undefined : source,
    status === "all" ? undefined : (status as AiProposal["status"]),
  );
  const proposals = q.data ?? [];
  const chosen = selected.filter((id) =>
    proposals.some((p) => p.id === id && p.status === "open"),
  );

  function update(key: string, value: string) {
    const next = new URLSearchParams(params);
    next.set(key, value);
    setParams(next, { replace: true });
    setSelected([]);
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs value={source} onValueChange={(v) => update("source", v)}>
          <TabsList className="h-auto flex-wrap">
            {sources.map((s) => (
              <TabsTrigger
                key={s.value}
                value={s.value}
                data-testid={`ai-recommendations-tab-${s.value}`}
              >
                {s.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        <div className="flex items-center gap-1.5">
          <Select value={status} onValueChange={(v) => update("status", v)}>
            <SelectTrigger
              id="ai-proposals-status"
              data-testid="ai-proposals-status"
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
                  data-testid={`ai-proposals-status-${s}`}
                >
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <HelpTip id="ai-proposals-status" label="Status" />
        </div>
      </div>

      {rpz ? (
        <Card className="space-y-2 p-5 text-sm">
          <p>
            Suggested RPZ rules are reviewed on the RPZ page, where each record
            is checked against its zone before it is applied.
          </p>
          <Link
            to="/rpz?tab=ai"
            data-testid="ai-recommendations-rpz-link"
            className="text-primary inline-block underline-offset-4 hover:underline"
          >
            Open the suggested RPZ rules
          </Link>
        </Card>
      ) : (
        <>
          <ErrorAlert
            error={q.error}
            prefix="Could not load the recommendations"
          />
          {chosen.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm">{chosen.length} selected</span>
              <Button
                size="sm"
                data-testid="ai-proposals-apply-selected"
                onClick={() => setApplying(true)}
              >
                Apply…
              </Button>
              <Button
                size="sm"
                variant="outline"
                data-testid="ai-proposals-dismiss-selected"
                onClick={() => setDismissing(true)}
              >
                Dismiss…
              </Button>
            </div>
          )}
          {q.data?.length === 0 && (
            <Card className="text-muted-foreground p-10 text-center text-sm">
              No {source === "all" ? "" : `${source} `}proposals with status{" "}
              {status}.
            </Card>
          )}
          {proposals.map((p) => (
            <div key={p.id} className="space-y-1.5">
              <ProposalCard
                proposal={p}
                canApply={canApply}
                selectable={canApply && p.status === "open"}
                selected={selected.includes(p.id)}
                onSelect={(v) =>
                  setSelected((ids) =>
                    v ? [...ids, p.id] : ids.filter((id) => id !== p.id),
                  )
                }
              />
              <Impact impact={p.impact} />
            </div>
          ))}
          <ProposalApplyDialog
            ids={chosen}
            open={applying && chosen.length > 0}
            onClose={() => setApplying(false)}
          />
          {dismissing && chosen.length > 0 && (
            <DismissDialog ids={chosen} onClose={() => setDismissing(false)} />
          )}
        </>
      )}
    </div>
  );
}

/** What the agent expects the change to do, as the agent measured it. */
function Impact({ impact }: { impact: AiProposal["impact"] }) {
  const facts = Object.entries(impact).filter(
    ([, v]) =>
      typeof v === "string" || typeof v === "number" || typeof v === "boolean",
  );
  if (facts.length === 0) return null;
  return (
    <p
      data-testid="ai-proposal-impact"
      className="text-muted-foreground px-4 text-xs break-words"
    >
      Impact:{" "}
      {facts
        .map(([k, v]) => `${k.replaceAll("_", " ")} ${String(v)}`)
        .join(" · ")}
    </p>
  );
}
