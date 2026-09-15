import { useState, type FormEvent } from "react";

import { useAiTask, useStartAiThreatCheck } from "@/api/ai";
import type { Schemas } from "@/api/client";
import { AiTaskStatus } from "@/components/ai/AiTaskStatus";
import { ErrorAlert, formatAgo } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
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
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

type Verdict = Schemas["AiThreatVerdict"];

/** The most names one check takes; the API answers 400 above it. */
export const maxThreatDomains = 100;

const integer = new Intl.NumberFormat();

/** The names of the textarea: one per line, blank lines ignored. */
export function threatDomains(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

/**
 * Asks the model whether domain names are threats, with what the query log knows about each.
 * Advice only: nothing is blocked by a check.
 */
export function ThreatCheckDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      {open && <CheckContent onClose={onClose} />}
    </Dialog>
  );
}

function CheckContent({ onClose }: { onClose: () => void }) {
  const [text, setText] = useState("");
  const [taskId, setTaskId] = useState<string | null>(null);
  const start = useStartAiThreatCheck();
  const task = useAiTask(taskId);

  const domains = threatDomains(text);
  const tooMany = domains.length > maxThreatDomains;
  const running =
    start.isPending ||
    task.data?.status === "queued" ||
    task.data?.status === "running";
  const result =
    task.data?.status === "succeeded"
      ? (task.data.result as Schemas["AiThreatCheckResult"] | null)
      : null;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (domains.length === 0 || tooMany) return;
    setTaskId(null);
    start.mutate({ domains }, { onSuccess: (t) => setTaskId(t.id) });
  }

  return (
    <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>Check domains</DialogTitle>
        <DialogDescription>
          The model judges each name with what the query log knows about it.
          Advice only — nothing is blocked by a check.
        </DialogDescription>
      </DialogHeader>
      <form onSubmit={submit} className="grid gap-3">
        <div className="grid gap-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="ai-threat-domains">Domains</Label>
            <HelpTip id="ai-threat-domains" label="Domains" />
          </div>
          <Textarea
            id="ai-threat-domains"
            data-testid="ai-threat-domains"
            className="font-mono"
            rows={5}
            placeholder={"login-bank.example\ncdn.tracker.example"}
            aria-describedby="ai-threat-domains-count"
            aria-invalid={tooMany}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <p
            id="ai-threat-domains-count"
            data-testid="ai-threat-count"
            className={cn(
              "text-xs tabular-nums",
              tooMany ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {domains.length} / {maxThreatDomains} names
            {tooMany && " — remove some to check the rest"}
          </p>
        </div>
        <ErrorAlert
          error={start.error ?? task.error}
          prefix="Could not check the domains"
        />
        <AiTaskStatus task={task.data} />
        <DialogFooter className="gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            Close
          </Button>
          <Button
            type="submit"
            data-testid="ai-threat-submit"
            disabled={domains.length === 0 || tooMany || running}
          >
            {running ? "Checking…" : "Check"}
          </Button>
        </DialogFooter>
      </form>
      {result && (
        <ul className="grid gap-2" aria-label="Verdicts">
          {result.results.map((v) => (
            <VerdictRow key={v.name} v={v} />
          ))}
        </ul>
      )}
    </DialogContent>
  );
}

function VerdictRow({ v }: { v: Verdict }) {
  return (
    <li
      data-testid="ai-threat-result"
      className="border-border/60 grid gap-1.5 rounded-md border p-3 text-sm"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono break-all">{v.name}</span>
        <Badge
          variant="outline"
          className={cn(
            v.is_threat
              ? "border-destructive/40 text-destructive"
              : "border-success/40 text-success",
          )}
        >
          {v.is_threat ? "Threat" : "No threat"}
        </Badge>
        {v.categories.map((c) => (
          <Badge key={c} variant="secondary" className="font-normal">
            {c}
          </Badge>
        ))}
        <span className="text-muted-foreground text-xs tabular-nums">
          {Math.round(v.confidence * 100)}% confidence
        </span>
        {v.cached && (
          <span className="text-muted-foreground text-xs">cached</span>
        )}
      </div>
      {v.reasoning && (
        <p className="text-muted-foreground break-words">{v.reasoning}</p>
      )}
      <p className="text-muted-foreground text-xs tabular-nums">
        {integer.format(v.query_count)}{" "}
        {v.query_count === 1 ? "query" : "queries"} from{" "}
        {integer.format(v.client_count)}{" "}
        {v.client_count === 1 ? "client" : "clients"} in 7 days
        {v.last_seen && ` · last seen ${formatAgo(v.last_seen)}`}
        {" · "}
        {v.blocked_by ? `blocked by ${v.blocked_by}` : "not blocked"}
      </p>
    </li>
  );
}
