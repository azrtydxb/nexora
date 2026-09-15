import { useUpdateAiFinding, type AiFinding } from "@/api/ai";
import { ErrorAlert, formatAgo } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const severityClass: Record<AiFinding["severity"], string> = {
  info: "border-border text-muted-foreground",
  warning: "border-warning/40 text-warning",
  critical: "border-destructive/40 text-destructive",
};

/** One anomaly or insight; operators acknowledge or dismiss an open one. */
export function FindingCard({
  finding,
  canEdit,
}: {
  finding: AiFinding;
  canEdit: boolean;
}) {
  const update = useUpdateAiFinding();
  const open = finding.status === "open";
  return (
    <Card
      data-testid={`ai-finding-${finding.candidate_id}`}
      className="space-y-2 p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="font-medium break-words">{finding.title}</h3>
          <p className="text-muted-foreground text-xs">
            {finding.type} · seen {formatAgo(finding.last_seen)} · confidence{" "}
            {Math.round(finding.confidence * 100)}%
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <Badge
            variant="outline"
            className={cn(severityClass[finding.severity])}
          >
            {finding.severity}
          </Badge>
          {!open && (
            <Badge variant="secondary" data-testid="ai-finding-state">
              {finding.status}
            </Badge>
          )}
        </div>
      </div>
      {finding.description && (
        <p className="text-sm break-words">{finding.description}</p>
      )}
      <ErrorAlert error={update.error} />
      {canEdit && open && (
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            data-testid="ai-finding-ack"
            disabled={update.isPending}
            onClick={() =>
              update.mutate({ id: finding.id, status: "acknowledged" })
            }
          >
            Acknowledge
          </Button>
          <Button
            size="sm"
            variant="ghost"
            data-testid="ai-finding-dismiss"
            disabled={update.isPending}
            onClick={() =>
              update.mutate({ id: finding.id, status: "dismissed" })
            }
          >
            Dismiss
          </Button>
        </div>
      )}
    </Card>
  );
}
