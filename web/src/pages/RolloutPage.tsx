import { Link, useParams } from "react-router";

import { type RolloutDetail, useRollout } from "@/api/fleet";
import {
  ErrorAlert,
  Fact,
  formatDateTime,
  MessageRow,
  StatusDot,
} from "@/components/common";
import {
  BackLink,
  LinkButton,
  RolloutProgress,
  RolloutStages,
  RolloutStateBadge,
} from "@/components/fleet";
import { PageHeader } from "@/components/layout/AppShell";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type EngineProgress = RolloutDetail["engines"][number]["progress"];

const progressTone: Record<
  EngineProgress,
  "success" | "warning" | "destructive" | "muted"
> = {
  applied: "success",
  waiting: "warning",
  rejected: "destructive",
  disconnected: "muted",
};

// Engines needing attention first.
const progressRank: Record<EngineProgress, number> = {
  rejected: 0,
  disconnected: 1,
  waiting: 2,
  applied: 3,
};

export function RolloutPage() {
  const { id = "" } = useParams();
  const q = useRollout(id);
  const r = q.data;
  const engines = [...(r?.engines ?? [])].sort(
    (a, b) =>
      Number(b.canary) - Number(a.canary) ||
      progressRank[a.progress] - progressRank[b.progress] ||
      a.node_name.localeCompare(b.node_name),
  );

  return (
    <div data-testid="rollout-detail">
      <BackLink to={r ? `/engines/groups/${r.engine_group_id}` : "/engines"}>
        {r ? r.engine_group_name : "Engines"}
      </BackLink>
      <PageHeader
        title={r ? `Rollout of v${r.version}` : "Rollout"}
        description={
          r
            ? `A ${r.kind} rollout to the ${r.engine_group_name} engine group, ${r.strategy === "canary" ? "canaries first" : "all engines at once"}.`
            : undefined
        }
      />
      <ErrorAlert
        error={q.error}
        prefix="Could not load the rollout"
        className="mb-4"
      />
      {q.isPending && <p className="text-muted-foreground text-sm">Loading…</p>}
      {r && (
        <div className="grid gap-6">
          <Card className="grid gap-4 px-5 py-4">
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div className="flex flex-wrap items-center gap-3">
                <RolloutStateBadge state={r.state} />
                <RolloutStages rollout={r} />
              </div>
              <RolloutProgress rollout={r} className="w-64" />
            </div>
            <dl className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
              <Fact label="Version">v{r.version}</Fact>
              <Fact label="From version">
                {r.from_version ? `v${r.from_version}` : "—"}
              </Fact>
              <Fact label="Kind">{r.kind}</Fact>
              <Fact label="Strategy">
                <span className="font-mono text-[13px]">{r.strategy}</span>
              </Fact>
              <Fact label="Engine group">
                <Link
                  to={`/engines/groups/${r.engine_group_id}`}
                  className="text-primary hover:underline"
                >
                  {r.engine_group_name}
                </Link>
              </Fact>
              <Fact label="Created">
                {formatDateTime(r.created_at)}
                <span className="text-muted-foreground">
                  {" "}
                  by {r.created_by}
                </span>
              </Fact>
              <Fact label="Phase started">
                {formatDateTime(r.phase_started_at)}
              </Fact>
              <Fact label="Finished">{formatDateTime(r.finished_at)}</Fact>
            </dl>
          </Card>

          <section aria-labelledby="rollout-engines-heading">
            <h2
              id="rollout-engines-heading"
              className="mb-3 text-sm font-semibold"
            >
              Engines
            </h2>
            <Card data-testid="rollout-engines" className="overflow-hidden">
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="h-10">Node</TableHead>
                    <TableHead className="h-10">Progress</TableHead>
                    <TableHead className="h-10">Connection</TableHead>
                    <TableHead className="h-10 text-right">
                      Applied version
                    </TableHead>
                    <TableHead className="h-10">Problem</TableHead>
                    <TableHead className="h-10 w-24" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {engines.map((e) => (
                    <TableRow key={e.engine_id}>
                      <TableCell className="py-2.5 font-medium whitespace-nowrap">
                        {e.node_name}
                        {e.canary && (
                          <Badge variant="outline" className="ml-2 font-normal">
                            canary
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="py-2.5">
                        <StatusDot tone={progressTone[e.progress]}>
                          {e.progress}
                        </StatusDot>
                      </TableCell>
                      <TableCell className="py-2.5">
                        {e.connected ? "connected" : "disconnected"}
                      </TableCell>
                      <TableCell className="py-2.5 text-right tabular-nums">
                        v{e.applied_version}
                      </TableCell>
                      <TableCell
                        className="text-destructive max-w-[20rem] truncate py-2.5 text-sm"
                        title={e.rejected_reason}
                      >
                        {e.rejected_reason}
                      </TableCell>
                      <TableCell className="py-2 text-right">
                        <LinkButton to={`/engines/nodes/${e.engine_id}`}>
                          Details
                        </LinkButton>
                      </TableCell>
                    </TableRow>
                  ))}
                  {engines.length === 0 && (
                    <MessageRow colSpan={6}>
                      No engines in this engine group.
                    </MessageRow>
                  )}
                </TableBody>
              </Table>
            </Card>
          </section>
        </div>
      )}
    </div>
  );
}
