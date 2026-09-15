import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import {
  useAiProposal,
  useDismissAiProposals,
  type AiProposal,
} from "@/api/ai";
import { ErrorAlert, formatAgo } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { JsonDiff } from "@/components/ai/JsonDiff";
import { ProposalApplyDialog } from "@/components/ai/ProposalApplyDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

const priorityClass: Record<AiProposal["priority"], string> = {
  low: "text-muted-foreground",
  medium: "border-warning/40 text-warning",
  high: "border-destructive/40 text-destructive",
};

/** A suggested configuration change: apply and dismiss for operators, details for everyone. */
export function ProposalCard({
  proposal,
  canApply,
  selectable = false,
  selected = false,
  onSelect,
}: {
  proposal: AiProposal;
  canApply: boolean;
  selectable?: boolean;
  selected?: boolean;
  onSelect?: (v: boolean) => void;
}) {
  const [details, setDetails] = useState(false);
  const [applying, setApplying] = useState(false);
  const [dismissing, setDismissing] = useState(false);
  const open = proposal.status === "open";

  return (
    <Card data-testid={`ai-proposal-${proposal.id}`} className="space-y-3 p-4">
      <div className="flex flex-wrap items-start gap-3">
        {selectable && (
          <div className="flex items-center gap-1.5 pt-0.5">
            <input
              type="checkbox"
              data-help="ai-proposal-select"
              data-testid="ai-proposal-select"
              aria-label={`Select ${proposal.title}`}
              className="accent-primary h-4 w-4"
              checked={selected}
              onChange={(e) => onSelect?.(e.target.checked)}
            />
            <HelpTip id="ai-proposal-select" label="Select proposal" />
          </div>
        )}
        <div className="min-w-0 flex-1">
          <h3 className="font-medium break-words">{proposal.title}</h3>
          <p className="text-muted-foreground text-xs">
            {proposal.source} · {proposal.actions.length} action
            {proposal.actions.length === 1 ? "" : "s"} · created{" "}
            {formatAgo(proposal.created_at)}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <Badge variant="outline" className={priorityClass[proposal.priority]}>
            {proposal.priority}
          </Badge>
          <Badge variant="secondary" data-testid="ai-proposal-status">
            {proposal.status}
          </Badge>
        </div>
      </div>
      {proposal.description && (
        <p className="text-sm break-words">{proposal.description}</p>
      )}
      {proposal.dismiss_reason && (
        <p className="text-muted-foreground text-xs break-words">
          Dismissed: {proposal.dismiss_reason}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        {canApply && open && (
          <>
            <Button
              size="sm"
              data-testid="ai-proposal-apply"
              onClick={() => setApplying(true)}
            >
              Apply…
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="ai-proposal-dismiss"
              onClick={() => setDismissing(true)}
            >
              Dismiss…
            </Button>
          </>
        )}
        <Button
          size="sm"
          variant="ghost"
          data-testid="ai-proposal-details"
          aria-expanded={details}
          onClick={() => setDetails((d) => !d)}
        >
          {details ? (
            <ChevronDown className="mr-1 h-4 w-4" />
          ) : (
            <ChevronRight className="mr-1 h-4 w-4" />
          )}
          Details
        </Button>
      </div>
      {details && <ProposalDetails id={proposal.id} />}
      <ProposalApplyDialog
        ids={[proposal.id]}
        open={applying}
        onClose={() => setApplying(false)}
      />
      {dismissing && (
        <DismissDialog
          ids={[proposal.id]}
          onClose={() => setDismissing(false)}
        />
      )}
    </Card>
  );
}

/** Each action with its explanation and the live resource beside the proposed body. */
function ProposalDetails({ id }: { id: string }) {
  const q = useAiProposal(id);
  if (q.isPending) {
    return <p className="text-muted-foreground text-sm">Loading…</p>;
  }
  if (q.error) return <ErrorAlert error={q.error} />;
  const p = q.data;
  return (
    <div data-testid="ai-proposal-detail" className="space-y-4">
      {p.actions.map((a, i) => (
        <div key={i} className="space-y-1.5">
          <p className="text-sm">
            <code className="font-mono text-xs">{a.operation_id}</code>
            {Object.entries(a.path_params).map(([k, v]) => (
              <span key={k} className="text-muted-foreground ml-2 text-xs">
                {k}={v}
              </span>
            ))}
          </p>
          {a.explanation && (
            <p className="text-muted-foreground text-sm break-words">
              {a.explanation}
            </p>
          )}
          <JsonDiff before={a.current} after={a.body} />
        </div>
      ))}
      {Object.keys(p.evidence).length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer font-medium">Evidence</summary>
          <pre className="bg-muted mt-1 overflow-x-auto rounded-md p-2 font-mono">
            {JSON.stringify(p.evidence, null, 2)}
          </pre>
        </details>
      )}
    </div>
  );
}

/** Dismisses proposals with an optional reason. */
export function DismissDialog({
  ids,
  onClose,
}: {
  ids: string[];
  onClose: () => void;
}) {
  const dismiss = useDismissAiProposals();
  const [reason, setReason] = useState("");
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Dismiss {ids.length === 1 ? "proposal" : `${ids.length} proposals`}
          </DialogTitle>
          <DialogDescription>
            The same change is not suggested again for 7 days.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="ai-dismiss-reason">Reason (optional)</Label>
            <HelpTip id="ai-dismiss-reason" label="Dismiss reason" />
          </div>
          <Textarea
            id="ai-dismiss-reason"
            data-testid="ai-dismiss-reason"
            maxLength={500}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
        <ErrorAlert error={dismiss.error} />
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            data-testid="ai-dismiss-confirm"
            disabled={dismiss.isPending}
            onClick={() =>
              dismiss.mutate(
                { ids, ...(reason.trim() !== "" && { reason: reason.trim() }) },
                { onSuccess: onClose },
              )
            }
          >
            {dismiss.isPending ? "Dismissing…" : "Dismiss"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
