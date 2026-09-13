import type { ReactNode } from "react";
import { ChevronLeft, Plus, X } from "lucide-react";
import { Link } from "react-router";

import {
  type Engine,
  type EngineGroup,
  type Rollout,
  useEngineGroups,
} from "@/api/fleet";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

type Tone = "success" | "warning" | "destructive" | "progress" | "muted";

const toneClass: Record<Tone, string> = {
  success: "border-success/40 text-success",
  warning: "border-warning/40 text-warning",
  destructive: "border-destructive/40 text-destructive",
  progress: "border-primary/40 text-primary",
  muted: "border-border text-muted-foreground",
};

const dotClass: Record<Tone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  destructive: "bg-destructive",
  progress: "bg-primary",
  muted: "bg-muted-foreground/50",
};

function Pill({
  tone,
  title,
  testId,
  children,
}: {
  tone: Tone;
  title?: string;
  testId?: string;
  children: string;
}) {
  return (
    <span
      data-testid={testId}
      title={title}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        toneClass[tone],
      )}
    >
      <span
        aria-hidden
        className={cn("h-1.5 w-1.5 shrink-0 rounded-full", dotClass[tone])}
      />
      {children}
    </span>
  );
}

const engineTone: Record<Engine["status"], Tone> = {
  current: "success",
  behind: "warning",
  ahead: "warning",
  rejected: "destructive",
  revoked: "destructive",
  disconnected: "muted",
};

export const engineStatusHelp: Record<Engine["status"], string> = {
  current: "Running its engine group's target configuration version",
  behind: "Connected but not yet on its engine group's target version",
  ahead:
    "Reports a configuration version newer than the database holds (restored backup?)",
  rejected: "Refused the target configuration version",
  disconnected: "No live control stream",
  revoked: "Certificate revoked; the engine is refused by the management plane",
};

export const engineStatuses = Object.keys(engineTone) as Engine["status"][];

export function EngineStatusBadge({
  status,
  testId,
}: {
  status: Engine["status"];
  testId?: string;
}) {
  return (
    <Pill
      tone={engineTone[status]}
      title={engineStatusHelp[status]}
      testId={testId}
    >
      {status}
    </Pill>
  );
}

const rolloutTone: Record<Rollout["state"], Tone> = {
  completed: "success",
  pending: "progress",
  canary: "progress",
  verifying: "progress",
  rolling: "progress",
  halted: "destructive",
  rolled_back: "muted",
  superseded: "muted",
};

const rolloutHelp: Record<Rollout["state"], string> = {
  pending: "Waiting to start",
  canary: "Pushing to the canary engines",
  verifying: "Watching the canary engines' health before rolling on",
  rolling: "Pushing to every engine in the group",
  completed: "Every engine applied the version; it is the group's stable one",
  halted: "Stopped by a failed health gate or rejection",
  rolled_back: "Replaced by a rollback",
  superseded: "Replaced by a newer version before it finished",
};

export function RolloutStateBadge({ state }: { state: Rollout["state"] }) {
  return (
    <Pill tone={rolloutTone[state]} title={rolloutHelp[state]}>
      {state.replace("_", " ")}
    </Pill>
  );
}

/** Engines that applied the rollout's version out of the engines it targets. */
export function RolloutProgress({
  rollout,
  className,
}: {
  rollout: Rollout;
  className?: string;
}) {
  const { total, applied, rejected } = rollout.progress;
  const pct = total === 0 ? 0 : Math.round((applied / total) * 100);
  const halted = rollout.state === "halted";
  return (
    <div className={cn("min-w-40", className)}>
      <div
        data-testid="rollout-progress"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct}
        aria-label={`Version ${rollout.version}: ${applied} of ${total} engines applied`}
        className="bg-muted flex h-1.5 overflow-hidden rounded-full"
      >
        <div
          className={cn(
            "h-full transition-[width]",
            halted ? "bg-destructive" : "bg-primary",
            rollout.state === "completed" && "bg-success",
          )}
          style={{ width: `${pct}%` }}
        />
        {rejected > 0 && total > 0 && (
          <div
            className="bg-destructive/60 h-full"
            style={{ width: `${Math.round((rejected / total) * 100)}%` }}
          />
        )}
      </div>
      <div className="mt-1 flex flex-wrap gap-x-2 text-xs tabular-nums">
        <span>
          {applied} / {total} applied
        </span>
        {rejected > 0 && (
          <span className="text-destructive">{rejected} rejected</span>
        )}
      </div>
      {halted && rollout.halt_reason && (
        <p className="text-destructive mt-0.5 text-xs">{rollout.halt_reason}</p>
      )}
    </div>
  );
}

const endStates: Partial<Record<Rollout["state"], string>> = {
  halted: "Halted",
  rolled_back: "Rolled back",
  superseded: "Superseded",
};

/** The rollout's phases in order with the current one marked; a halt, rollback or supersede is appended as the end state. */
export function RolloutStages({ rollout }: { rollout: Rollout }) {
  const phases: Rollout["state"][] =
    rollout.strategy === "canary"
      ? ["pending", "canary", "verifying", "rolling", "completed"]
      : ["pending", "rolling", "completed"];
  const end = endStates[rollout.state];
  const at = phases.indexOf(rollout.state);
  return (
    <ol
      aria-label="Rollout stages"
      data-testid="rollout-stages"
      className="flex flex-wrap items-center gap-1.5 text-xs"
    >
      {phases.map((p, i) => {
        const done = rollout.state === "completed" || (!end && i < at);
        const current = i === at;
        return (
          <li key={p} className="flex items-center gap-1.5">
            {i > 0 && (
              <span
                aria-hidden
                className={cn(
                  "h-px w-4",
                  done || current ? "bg-primary" : "bg-border",
                )}
              />
            )}
            <span
              aria-current={current ? "step" : undefined}
              className={cn(
                "rounded-full border px-2 py-0.5 whitespace-nowrap",
                current &&
                  "border-primary bg-primary/10 text-primary font-medium",
                done && !current && "border-success/40 text-success",
                !done && !current && "text-muted-foreground",
                end && "border-dashed",
              )}
            >
              {p}
            </span>
          </li>
        );
      })}
      {end && (
        <li className="flex items-center gap-1.5">
          <span aria-hidden className="bg-border h-px w-4" />
          <span
            aria-current="step"
            className={cn(
              "rounded-full border px-2 py-0.5 font-medium whitespace-nowrap",
              rollout.state === "halted"
                ? "border-destructive bg-destructive/10 text-destructive"
                : "text-muted-foreground",
            )}
          >
            {end}
          </span>
        </li>
      )}
    </ol>
  );
}

const allGroups = "__all__";

/** A Radix select over the engine groups; null is "All engine groups" unless allowAll is false. */
export function EngineGroupSelect({
  value,
  onChange,
  id,
  testId,
  allowAll = true,
  disabled,
  className,
}: {
  value: string | null;
  onChange(v: string | null): void;
  id?: string;
  testId: string;
  allowAll?: boolean;
  disabled?: boolean;
  className?: string;
}) {
  const groups = useEngineGroups();
  return (
    <Select
      value={value ?? (allowAll ? allGroups : undefined)}
      onValueChange={(v) => onChange(v === allGroups ? null : v)}
      disabled={disabled}
    >
      <SelectTrigger
        id={id}
        data-testid={testId}
        className={cn("h-9", className)}
      >
        <SelectValue placeholder="Select an engine group" />
      </SelectTrigger>
      <SelectContent>
        {allowAll && (
          <SelectItem value={allGroups}>All engine groups</SelectItem>
        )}
        {(groups.data ?? []).map((g) => (
          <SelectItem key={g.id} value={g.id}>
            {g.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/** The table cell text for a resource's engine group scope. */
export function EngineGroupName({ id }: { id: string | null }) {
  const groups = useEngineGroups();
  if (id === null)
    return <span className="text-muted-foreground">All engine groups</span>;
  return <>{groups.data?.find((g) => g.id === id)?.name ?? id.slice(0, 8)}</>;
}

export type LabelRow = { key: string; value: string };

export function labelRows(labels: Record<string, string>): LabelRow[] {
  return Object.entries(labels)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({ key, value }));
}

/** Rows with an empty key are dropped; the server validates the rest. */
export function labelsFromRows(rows: LabelRow[]): Record<string, string> {
  return Object.fromEntries(
    rows
      .map((r) => [r.key.trim(), r.value.trim()] as const)
      .filter(([k]) => k !== ""),
  );
}

export function LabelsEditor({
  rows,
  onChange,
  disabled,
  testIdPrefix = "engine-label",
}: {
  rows: LabelRow[];
  onChange: (rows: LabelRow[]) => void;
  disabled?: boolean;
  testIdPrefix?: string;
}) {
  const setRow = (i: number, patch: Partial<LabelRow>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="grid gap-2">
      {rows.length === 0 && (
        <p className="text-muted-foreground text-sm">No labels.</p>
      )}
      {rows.map((r, i) => (
        <div key={i} className="grid grid-cols-[1fr_1fr_auto] gap-2">
          <Input
            aria-label={`Label ${i + 1} key`}
            data-testid={`${testIdPrefix}-key-${i}`}
            className="h-9 font-mono text-[13px]"
            placeholder="nexora.io/rack"
            maxLength={63}
            disabled={disabled}
            value={r.key}
            onChange={(e) => setRow(i, { key: e.target.value })}
          />
          <Input
            aria-label={`Label ${i + 1} value`}
            data-testid={`${testIdPrefix}-value-${i}`}
            className="h-9 font-mono text-[13px]"
            placeholder="r3"
            maxLength={63}
            disabled={disabled}
            value={r.value}
            onChange={(e) => setRow(i, { value: e.target.value })}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="hover:text-destructive h-9 w-9"
            aria-label={`Remove label ${r.key || i + 1}`}
            data-testid={`${testIdPrefix}-remove-${i}`}
            disabled={disabled}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid={`${testIdPrefix}-add`}
          disabled={disabled || rows.length >= 32}
          onClick={() => onChange([...rows, { key: "", value: "" }])}
        >
          <Plus className="mr-1.5 h-4 w-4" />
          Add label
        </Button>
      </div>
    </div>
  );
}

/** A router link styled as a small outline button. */
export function LinkButton({
  to,
  testId,
  children,
}: {
  to: string;
  testId?: string;
  children: ReactNode;
}) {
  return (
    <Link
      to={to}
      data-testid={testId}
      className="border-input bg-background hover:bg-accent hover:text-accent-foreground focus-visible:ring-ring inline-flex h-8 items-center rounded-md border px-3 text-sm font-medium focus-visible:ring-2 focus-visible:outline-none"
    >
      {children}
    </Link>
  );
}

/** The "back to the list" link above a detail page. */
export function BackLink({
  to,
  children,
}: {
  to: string;
  children: ReactNode;
}) {
  return (
    <Link
      to={to}
      className="text-muted-foreground hover:text-foreground mb-3 inline-flex items-center gap-1 text-sm"
    >
      <ChevronLeft className="h-4 w-4" />
      {children}
    </Link>
  );
}

/** The canary size of a canary group, "2 engines or 10%". */
export function canarySize(g: EngineGroup): string {
  const parts = [];
  if (g.canary_count > 0) parts.push(`${g.canary_count} engines`);
  if (g.canary_percent > 0) parts.push(`${g.canary_percent}%`);
  return parts.join(" or ");
}
