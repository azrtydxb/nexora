import { useEffect, useState } from "react";
import { LoaderCircle } from "lucide-react";

import type { AiTask } from "@/api/ai";

/** "Thinking… 12 s" while a task is queued or running, its error once failed, nothing otherwise. */
export function AiTaskStatus({ task }: { task: AiTask | undefined }) {
  const active = task?.status === "queued" || task?.status === "running";
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [active]);

  if (!task) return null;
  if (active) {
    const since = new Date(task.started_at ?? task.created_at).getTime();
    const s = Math.max(0, Math.round((now - since) / 1000));
    return (
      <span
        data-testid="ai-task-status"
        role="status"
        className="text-muted-foreground inline-flex items-center gap-1.5 text-sm"
      >
        <LoaderCircle className="h-4 w-4 animate-spin" aria-hidden />
        Thinking… {s} s
      </span>
    );
  }
  if (task.status === "failed") {
    return (
      <span
        data-testid="ai-task-status"
        role="alert"
        className="text-destructive text-sm"
      >
        {task.error_message || task.error_code || "The AI task failed"}
      </span>
    );
  }
  return null;
}
