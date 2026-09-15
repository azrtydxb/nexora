import type { ReactNode } from "react";
import { Sparkles } from "lucide-react";

import { useAiStatus } from "@/api/ai";
import { useCurrentUser } from "@/auth/AuthProvider";
import { ErrorAlert } from "@/components/common";
import { PageHeader } from "@/components/layout/AppShell";
import { Card } from "@/components/ui/card";

/** "AI is off" with the status reason; admins also see how to configure it. */
export function AiOff({ reason }: { reason: string }) {
  const { user } = useCurrentUser();
  return (
    <Card data-testid="ai-off" className="flex items-start gap-3 p-5">
      <Sparkles className="text-muted-foreground mt-0.5 h-5 w-5 shrink-0" />
      <div className="min-w-0 space-y-1 text-sm">
        <p className="font-medium">AI is off</p>
        <p className="text-muted-foreground">
          Reason: <code className="font-mono">{reason || "unknown"}</code>
        </p>
        {user?.role === "admin" && (
          <p className="text-muted-foreground">
            Set NEXORA_AI_BASE_URL and NEXORA_AI_MODEL (Secret nexora-ai on kw).
          </p>
        )}
      </div>
    </Card>
  );
}

/**
 * The frame of every /ai* page: the header, then AiOff when the status reports AI off, the load
 * error when the status cannot be read, or the page body.
 */
export function AiPage({
  title,
  description,
  actions,
  children,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const status = useAiStatus();
  const on = status.data?.enabled === true;
  return (
    <>
      <PageHeader
        title={title}
        description={description}
        actions={on ? actions : undefined}
      />
      <ErrorAlert error={status.error} prefix="Could not load the AI status" />
      {status.data?.enabled === false && <AiOff reason={status.data.reason} />}
      {on && children}
    </>
  );
}
