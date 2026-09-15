import { useState } from "react";

import { useAiProposals, type AiProposal } from "@/api/ai";
import { useCan } from "@/auth/AuthProvider";
import { ProposalApplyDialog } from "@/components/ai/ProposalApplyDialog";
import { DismissDialog } from "@/components/ai/ProposalCard";
import { ErrorAlert, MessageRow } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Badge } from "@/components/ui/badge";
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

/** One suggested RPZ record, as an `appendAiRpzRules` action carries it. */
type RpzRule = {
  record: string;
  policy: string;
  category: string;
  reason: string;
  confidence: number;
};

type Row = RpzRule & { proposalId: string };

function rowsOf(proposals: AiProposal[]): Row[] {
  return proposals.flatMap((p) =>
    p.actions.flatMap((a) => {
      if (a.operation_id !== "appendAiRpzRules") return [];
      const body = a.body as unknown as { rules?: unknown } | null;
      const rules = Array.isArray(body?.rules) ? body.rules : [];
      return rules.flatMap((r: unknown) => {
        const rule = r as Partial<RpzRule> | null;
        if (typeof rule?.record !== "string") return [];
        return [
          {
            proposalId: p.id,
            record: rule.record,
            policy: String(rule.policy ?? ""),
            category: String(rule.category ?? ""),
            reason: String(rule.reason ?? ""),
            confidence:
              typeof rule.confidence === "number" ? rule.confidence : 0,
          },
        ];
      });
    }),
  );
}

/**
 * Open RPZ suggestions, one row per suggested record. Applying the selected rows writes them into
 * the zone the management plane keeps for suggested rules; rejecting dismisses their proposals.
 */
export function RpzSuggestionsTab() {
  const canApply = useCan("applyAiProposals");
  const q = useAiProposals("rpz_suggestions", "open");
  const rows = rowsOf(q.data ?? []);
  const [selected, setSelected] = useState<string[]>([]);
  // The dialogs keep the ids chosen when they opened: an apply refetches the open list, which
  // drops the applied rows (and with them the selection) while the dialog still shows the results.
  const [applying, setApplying] = useState<string[] | null>(null);
  const [rejecting, setRejecting] = useState<string[] | null>(null);
  const ids = [...new Set(rows.map((r) => r.proposalId))];
  const chosen = selected.filter((id) => ids.includes(id));
  const cols = canApply ? 6 : 5;

  function select(id: string, on: boolean) {
    setSelected((s) => (on ? [...s, id] : s.filter((x) => x !== id)));
  }

  return (
    <div className="space-y-4">
      <p
        data-testid="ai-rpz-zone-notice"
        className="text-muted-foreground text-sm"
      >
        Applied rules are written to the zone ai-suggested.rpz. Manual uploads
        to that zone are replaced by the next apply.
      </p>
      <ErrorAlert error={q.error} prefix="Could not load the RPZ suggestions" />
      {canApply && (
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              id="ai-rpz-select-all"
              data-testid="ai-rpz-select-all"
              className="accent-primary h-4 w-4"
              checked={ids.length > 0 && chosen.length === ids.length}
              disabled={ids.length === 0}
              onChange={(e) => setSelected(e.target.checked ? ids : [])}
            />
            Select all
          </label>
          <HelpTip id="ai-rpz-select-all" label="Select all suggestions" />
          <span className="text-muted-foreground text-sm">
            {chosen.length} selected
          </span>
          <Button
            size="sm"
            data-testid="ai-rpz-apply-selected"
            disabled={chosen.length === 0}
            onClick={() => setApplying(chosen)}
          >
            Apply selected…
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="ai-rpz-reject-selected"
            disabled={chosen.length === 0}
            onClick={() => setRejecting(chosen)}
          >
            Reject selected…
          </Button>
        </div>
      )}
      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {canApply && (
                <TableHead className="w-10">
                  <HelpTip id="ai-rpz-select" label="Select suggestion" />
                </TableHead>
              )}
              <TableHead>Record</TableHead>
              <TableHead>Policy</TableHead>
              <TableHead>Category</TableHead>
              <TableHead className="text-right">Confidence</TableHead>
              <TableHead>Reason</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow
                key={`${r.proposalId}-${r.record}`}
                data-testid={`ai-rpz-row-${r.record}`}
              >
                {canApply && (
                  <TableCell className="py-3">
                    <input
                      type="checkbox"
                      data-help="ai-rpz-select"
                      aria-label={`Select ${r.record}`}
                      className="accent-primary h-4 w-4"
                      checked={chosen.includes(r.proposalId)}
                      onChange={(e) => select(r.proposalId, e.target.checked)}
                    />
                  </TableCell>
                )}
                <TableCell className="py-3 font-mono text-[13px] break-all">
                  {r.record}
                </TableCell>
                <TableCell className="py-3">
                  <Badge variant="secondary">{r.policy}</Badge>
                </TableCell>
                <TableCell className="py-3">{r.category}</TableCell>
                <TableCell className="py-3 text-right tabular-nums">
                  {Math.round(r.confidence * 100)}%
                </TableCell>
                <TableCell className="text-muted-foreground py-3 text-sm break-words">
                  {r.reason}
                </TableCell>
              </TableRow>
            ))}
            {q.isPending && <MessageRow colSpan={cols}>Loading…</MessageRow>}
            {q.isSuccess && rows.length === 0 && (
              <MessageRow colSpan={cols}>
                <span data-testid="ai-rpz-empty">No open RPZ suggestions.</span>
              </MessageRow>
            )}
          </TableBody>
        </Table>
      </Card>
      <ProposalApplyDialog
        ids={applying ?? []}
        open={applying !== null}
        onClose={() => {
          setApplying(null);
          setSelected([]);
        }}
      />
      {rejecting && (
        <DismissDialog
          ids={rejecting}
          onClose={() => {
            setRejecting(null);
            setSelected([]);
          }}
        />
      )}
    </div>
  );
}
