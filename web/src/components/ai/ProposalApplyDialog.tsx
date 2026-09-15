import { useState } from "react";

import { useAiProposalsById, useApplyAiProposals } from "@/api/ai";
import type { Schemas } from "@/api/client";
import { ErrorAlert } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { JsonDiff } from "@/components/ai/JsonDiff";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

// Actions that can enable a category whose source list carries a non-commercial license.
const licensedOperations = new Set([
  "updateFilterCategory",
  "updatePolicyGroup",
]);

/**
 * Confirms applying proposals: every action with its diff, the license acknowledgement when an
 * action can enable a category, then each proposal's result.
 */
export function ProposalApplyDialog({
  ids,
  open,
  onClose,
}: {
  ids: string[];
  open: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      {open && <ApplyContent ids={ids} onClose={onClose} />}
    </Dialog>
  );
}

function ApplyContent({
  ids,
  onClose,
}: {
  ids: string[];
  onClose: () => void;
}) {
  const proposals = useAiProposalsById(ids);
  const apply = useApplyAiProposals();
  const [acknowledge, setAcknowledge] = useState(false);
  const loading = proposals.some((q) => q.isPending);
  const loadError = proposals.find((q) => q.error)?.error;
  const actions = proposals.flatMap(({ data: proposal }) =>
    proposal
      ? proposal.actions.map((action, i) => ({ proposal, action, i }))
      : [],
  );
  const needsLicense = actions.some((a) =>
    licensedOperations.has(a.action.operation_id),
  );
  const results = apply.data?.results;

  return (
    <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
      <DialogHeader>
        <DialogTitle>
          Apply {ids.length === 1 ? "proposal" : `${ids.length} proposals`}
        </DialogTitle>
        <DialogDescription>
          Each action runs as you, through the normal API, and is audited. A
          proposal whose target changed since it was made is not applied.
        </DialogDescription>
      </DialogHeader>
      {loading && <p className="text-muted-foreground text-sm">Loading…</p>}
      <ErrorAlert error={loadError} prefix="Could not load the proposal" />
      {!results && (
        <ol className="space-y-4">
          {actions.map(({ proposal, action, i }) => (
            <li
              key={`${proposal.id}-${i}`}
              data-testid="ai-apply-action"
              className="space-y-1.5"
            >
              <p className="text-sm">
                <code className="font-mono text-xs">{action.operation_id}</code>
                {Object.entries(action.path_params).map(([k, v]) => (
                  <span key={k} className="text-muted-foreground ml-2 text-xs">
                    {k}={v}
                  </span>
                ))}
                {ids.length > 1 && (
                  <span className="text-muted-foreground ml-2 text-xs">
                    ({proposal.title})
                  </span>
                )}
              </p>
              {action.explanation && (
                <p className="text-muted-foreground text-sm break-words">
                  {action.explanation}
                </p>
              )}
              <JsonDiff before={action.current} after={action.body} />
            </li>
          ))}
        </ol>
      )}
      {!results && needsLicense && (
        <div className="flex items-start gap-1.5">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              id="ai-apply-acknowledge-license"
              data-testid="ai-apply-acknowledge-license"
              className="accent-primary mt-0.5 h-4 w-4 shrink-0"
              checked={acknowledge}
              onChange={(e) => setAcknowledge(e.target.checked)}
            />
            I accept the license terms of any non-commercial source list these
            actions enable.
          </label>
          <HelpTip
            id="ai-apply-acknowledge-license"
            label="Acknowledge license"
          />
        </div>
      )}
      {results && (
        <ul className="space-y-2">
          {results.map((r) => (
            <ApplyResult
              key={r.id}
              result={r}
              title={proposals.find((q) => q.data?.id === r.id)?.data?.title}
            />
          ))}
        </ul>
      )}
      <ErrorAlert error={apply.error} />
      <DialogFooter className="gap-2">
        <Button variant="outline" onClick={onClose}>
          {results ? "Close" : "Cancel"}
        </Button>
        {!results && (
          <Button
            data-testid="ai-apply-confirm"
            disabled={loading || !!loadError || apply.isPending}
            onClick={() =>
              apply.mutate({ ids, acknowledge_license: acknowledge })
            }
          >
            {apply.isPending ? "Applying…" : "Apply"}
          </Button>
        )}
      </DialogFooter>
    </DialogContent>
  );
}

function ApplyResult({
  result,
  title,
}: {
  result: Schemas["AiApplyResponse"]["results"][number];
  title: string | undefined;
}) {
  const failed = result.actions.filter((a) => a.http_status >= 300);
  return (
    <li data-testid="ai-apply-result" className="rounded-md border p-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-medium break-words">{title ?? result.id}</span>
        <Badge
          variant={result.status === "applied" ? "secondary" : "destructive"}
        >
          {result.status}
        </Badge>
      </div>
      {failed.map((a, i) => (
        <p key={i} className="text-destructive mt-1 text-xs break-words">
          {a.operation_id}: {a.http_status} {a.code} {a.message}
        </p>
      ))}
    </li>
  );
}
