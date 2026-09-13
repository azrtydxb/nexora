import { useEffect, useRef, useState, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { Check, Copy } from "lucide-react";

import { ApiError, type Schemas } from "@/api/client";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { TableCell, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

/** A readable message for an API or network error; a 409 names what changed underneath. */
export function errorMessage(err: unknown, thing = "This item"): string {
  if (err instanceof ApiError && err.status === 409) {
    return err.code === "conflict" && /revision/i.test(err.message)
      ? `${thing} was changed by someone else — reload to see the latest version`
      : err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

export function ErrorAlert({
  error,
  prefix,
  thing,
  className,
}: {
  error: unknown;
  prefix?: string;
  thing?: string;
  className?: string;
}) {
  if (!error) return null;
  return (
    <Alert variant="destructive" className={className}>
      <AlertDescription>
        {prefix && `${prefix}: `}
        {errorMessage(error, thing)}
      </AlertDescription>
    </Alert>
  );
}

/** A full-width table row for the loading and empty states. */
export function MessageRow({
  colSpan,
  children,
}: {
  colSpan: number;
  children: ReactNode;
}) {
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell
        colSpan={colSpan}
        className="text-muted-foreground py-10 text-center"
      >
        {children}
      </TableCell>
    </TableRow>
  );
}

/** A destructive confirmation dialog; the confirm button carries `confirm-delete`. */
export function ConfirmDialog({
  title,
  description,
  confirmLabel,
  pendingLabel,
  thing,
  onConfirm,
  onClose,
}: {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  pendingLabel: string;
  thing?: string;
  onConfirm: () => Promise<unknown>;
  onClose: () => void;
}) {
  const run = useMutation({ mutationFn: onConfirm, onSuccess: onClose });
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <ErrorAlert error={run.error} thing={thing} />
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            data-testid="confirm-delete"
            disabled={run.isPending}
            onClick={() => run.mutate()}
          >
            {run.isPending ? pendingLabel : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** A secret shown once, with a copy button. */
export function SecretValue({
  value,
  testId,
}: {
  value: string;
  testId: string;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access can be denied; the value stays selectable.
    }
  };
  return (
    <div className="bg-muted flex items-start gap-2 rounded-md border p-3">
      <code
        data-testid={testId}
        className="min-w-0 flex-1 font-mono text-xs leading-relaxed break-all select-all"
      >
        {value}
      </code>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="h-7 w-7 shrink-0"
        aria-label={copied ? "Copied" : "Copy to clipboard"}
        onClick={() => void copy()}
      >
        {copied ? (
          <Check className="text-success h-4 w-4" />
        ) : (
          <Copy className="h-4 w-4" />
        )}
      </Button>
    </div>
  );
}

/** A small dot-and-label state indicator. */
export function StatusDot({
  tone,
  children,
}: {
  tone: "success" | "warning" | "destructive" | "muted";
  children: ReactNode;
}) {
  return (
    <span className="inline-flex items-center gap-1.5 text-sm whitespace-nowrap">
      <span
        aria-hidden
        className={cn("h-1.5 w-1.5 shrink-0 rounded-full", {
          "bg-success": tone === "success",
          "bg-warning": tone === "warning",
          "bg-destructive": tone === "destructive",
          "bg-muted-foreground/50": tone === "muted",
        })}
      />
      {children}
    </span>
  );
}

/** A transient "saved" note for forms that stay on screen after saving. */
export function SavedNote({
  show,
  children,
}: {
  show: boolean;
  children: ReactNode;
}) {
  return (
    <span
      role="status"
      className={cn(
        "text-success inline-flex items-center gap-1 text-sm",
        !show && "sr-only",
      )}
    >
      {show && (
        <>
          <Check className="h-4 w-4" />
          {children}
        </>
      )}
    </span>
  );
}

/** A ref that scrolls its element into view whenever key changes (a newly opened detail panel). */
export function useRevealRef<T extends HTMLElement>(key: unknown) {
  const ref = useRef<T>(null);
  useEffect(() => {
    ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [key]);
  return ref;
}

/** One label/value pair inside a <dl>. */
export function Fact({
  label,
  className,
  children,
}: {
  label: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={className}>
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="mt-0.5 tabular-nums">{children}</dd>
    </div>
  );
}

/** The roles in ascending order of privilege, with what each adds. */
export const roles: { value: Schemas["Role"]; help: string }[] = [
  { value: "viewer", help: "Reads configuration, engines and the query log." },
  { value: "operator", help: "Also changes the resolver configuration." },
  {
    value: "admin",
    help: "Also manages users, tokens, engines and the audit log.",
  },
];

const dateTime = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

export function formatDateTime(v: string | null | undefined): string {
  return v ? dateTime.format(new Date(v)) : "—";
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

/** "3 minutes ago" style text for a timestamp, or "never". */
export function formatAgo(v: string | null | undefined): string {
  if (!v) return "never";
  const s = (new Date(v).getTime() - Date.now()) / 1000;
  const abs = Math.abs(s);
  if (abs < 45) return relative.format(Math.round(s), "second");
  if (abs < 2700) return relative.format(Math.round(s / 60), "minute");
  if (abs < 64800) return relative.format(Math.round(s / 3600), "hour");
  return relative.format(Math.round(s / 86400), "day");
}

/** A duration in seconds as "1 h", "30 min", "2 d" or "45 s". */
export function formatSeconds(s: number): string {
  if (s % 86400 === 0) return `${s / 86400} d`;
  if (s % 3600 === 0) return `${s / 3600} h`;
  if (s % 60 === 0) return `${s / 60} min`;
  return `${s} s`;
}
