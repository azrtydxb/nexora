import { useState } from "react";
import { Play } from "lucide-react";

import {
  useAiStatus,
  useRunAiAgent,
  type AiAgentName,
  type AiStatus,
} from "@/api/ai";
import { useCan } from "@/auth/AuthProvider";
import { AiPage } from "@/components/ai/AiOff";
import {
  ErrorAlert,
  Fact,
  formatAgo,
  formatSeconds,
  StatusDot,
} from "@/components/common";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const tokens = new Intl.NumberFormat();

export function AiStatusPage() {
  const status = useAiStatus();
  return (
    <AiPage
      title="AI status"
      description="The model provider, today's token budget, the background agents and the MCP endpoint. The API key is never shown."
    >
      {status.data && <StatusBody status={status.data} />}
    </AiPage>
  );
}

function StatusBody({ status }: { status: AiStatus }) {
  return (
    <div className="space-y-6">
      <Card className="p-5">
        <dl className="grid gap-4 sm:grid-cols-3">
          <Fact label="Model">
            <span data-testid="ai-status-model" className="font-mono">
              {status.model}
            </span>
          </Fact>
          <Fact label="Endpoint host">
            <span data-testid="ai-status-endpoint" className="font-mono">
              {status.endpoint_host}
            </span>
          </Fact>
          <Fact label="Structured output">
            <span data-testid="ai-status-structured-output">
              {status.structured_output}
            </span>
          </Fact>
        </dl>
      </Card>
      <Budget budget={status.budget} />
      <Agents agents={status.agents} />
      <Card className="p-5">
        <p data-testid="ai-mcp-state" className="text-sm">
          {status.mcp.enabled ? (
            <StatusDot tone="success">
              MCP endpoint /mcp: on,{" "}
              {status.mcp.read_only ? "read-only" : "read-write"}
            </StatusDot>
          ) : (
            <StatusDot tone="muted">MCP: off</StatusDot>
          )}
        </p>
      </Card>
    </div>
  );
}

function Budget({ budget }: { budget: AiStatus["budget"] }) {
  const pct = (v: number) =>
    budget.limit_tokens > 0
      ? Math.min(100, (v / budget.limit_tokens) * 100)
      : 0;
  const used = pct(budget.used_tokens);
  return (
    <Card data-testid="ai-budget" className="space-y-3 p-5">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="font-medium">Token budget for {budget.day}</h2>
        <span className="text-sm tabular-nums">
          {tokens.format(budget.used_tokens)} /{" "}
          {tokens.format(budget.limit_tokens)} tokens
        </span>
      </div>
      <div
        role="progressbar"
        aria-label="Tokens used today"
        aria-valuemin={0}
        aria-valuemax={budget.limit_tokens}
        aria-valuenow={budget.used_tokens}
        className="bg-muted relative h-2.5 overflow-hidden rounded-full"
      >
        <div
          className={
            used >= 100
              ? "bg-destructive h-full"
              : used >= pct(budget.background_limit_tokens)
                ? "bg-warning h-full"
                : "bg-primary h-full"
          }
          style={{ width: `${used}%` }}
        />
        <div
          aria-hidden
          data-testid="ai-budget-background-marker"
          className="bg-foreground/60 absolute top-0 h-full w-0.5"
          style={{ left: `${pct(budget.background_limit_tokens)}%` }}
        />
      </div>
      <p className="text-muted-foreground text-xs">
        Background agents stop at{" "}
        {tokens.format(budget.background_limit_tokens)} tokens (the marker);
        interactive features use the rest. The budget resets at midnight UTC.
      </p>
    </Card>
  );
}

function Agents({ agents }: { agents: AiStatus["agents"] }) {
  const canRun = useCan("runAiAgent");
  const run = useRunAiAgent();
  const [requested, setRequested] = useState<AiAgentName | null>(null);
  return (
    <Card className="overflow-hidden">
      <div className="px-5 pt-4 pb-2">
        <h2 className="font-medium">Agents</h2>
      </div>
      <ErrorAlert error={run.error} className="mx-5 mb-2" />
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-10">Agent</TableHead>
            <TableHead className="h-10">Interval</TableHead>
            <TableHead className="h-10">Last run</TableHead>
            <TableHead className="h-10">Outcome</TableHead>
            <TableHead className="h-10">Next run</TableHead>
            {canRun && (
              <TableHead className="h-10 w-32">
                <span className="sr-only">Actions</span>
              </TableHead>
            )}
          </TableRow>
        </TableHeader>
        <TableBody>
          {agents.map((a) => (
            <TableRow key={a.name} data-testid={`ai-agent-${a.name}`}>
              <TableCell className="py-2.5 font-mono text-[13px] whitespace-nowrap">
                {a.name}
                {!a.enabled && (
                  <div className="text-muted-foreground font-sans text-xs">
                    disabled
                  </div>
                )}
              </TableCell>
              <TableCell className="py-2.5 whitespace-nowrap">
                {a.interval_seconds > 0
                  ? formatSeconds(a.interval_seconds)
                  : "—"}
              </TableCell>
              <TableCell className="py-2.5 whitespace-nowrap">
                {a.running ? (
                  <StatusDot tone="warning">running</StatusDot>
                ) : (
                  formatAgo(a.last_finished_at ?? a.last_started_at)
                )}
              </TableCell>
              <TableCell className="py-2.5">
                <span data-testid="ai-agent-outcome">
                  {a.last_outcome || "—"}
                </span>
                {a.last_error && (
                  <div className="text-destructive max-w-xs text-xs break-words">
                    {a.last_error}
                  </div>
                )}
              </TableCell>
              <TableCell className="py-2.5 whitespace-nowrap">
                {a.enabled && a.next_run_at ? formatAgo(a.next_run_at) : "—"}
              </TableCell>
              {canRun && (
                <TableCell className="py-2.5 text-right whitespace-nowrap">
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-8 min-w-24 gap-1.5 whitespace-nowrap"
                    data-testid="ai-agent-run"
                    disabled={!a.enabled || run.isPending}
                    onClick={() => {
                      setRequested(null);
                      run.mutate(a.name, {
                        onSuccess: () => setRequested(a.name),
                      });
                    }}
                  >
                    <Play aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
                    <span>Run now</span>
                  </Button>
                  {requested === a.name && (
                    <div
                      role="status"
                      className="text-success mt-1 text-xs whitespace-nowrap"
                    >
                      Requested
                    </div>
                  )}
                </TableCell>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
