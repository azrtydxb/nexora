import { useEffect, useRef, useState, type FormEvent } from "react";
import { Sparkles } from "lucide-react";

import { useAiStatus, useAiTask, useStartAiQueryLogSearch } from "@/api/ai";
import type { Schemas } from "@/api/client";
import { AiTaskStatus } from "@/components/ai/AiTaskStatus";
import { ErrorAlert } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type Result = Schemas["AiQueryLogSearchResult"];

const maxQuestion = 500;

/**
 * Asks the query log a question in plain language. The model's filters become the page's filters
 * through onFilters; nothing is changed by the search itself.
 */
export function QueryLogAsk({
  onFilters,
}: {
  onFilters: (f: Schemas["AiQueryLogFilters"]) => void;
}) {
  const status = useAiStatus();
  const [question, setQuestion] = useState("");
  const [taskId, setTaskId] = useState<string | null>(null);
  // The task whose filters were handed to the page already, so a poll does not reapply them.
  const applied = useRef<string | null>(null);
  const start = useStartAiQueryLogSearch();
  const task = useAiTask(taskId);

  const done = task.data?.status === "succeeded" ? task.data : undefined;
  const result = (done?.result as Result | null | undefined) ?? undefined;

  useEffect(() => {
    if (!done || !result || applied.current === done.id) return;
    applied.current = done.id;
    onFilters(result.filters);
  }, [done, result, onFilters]);

  function ask(e: FormEvent) {
    e.preventDefault();
    const q = question.trim();
    if (q === "") return;
    setTaskId(null);
    start.mutate({ query: q }, { onSuccess: (t) => setTaskId(t.id) });
  }

  if (status.data?.features.querylog_search !== true) return null;

  return (
    <Card className="mb-4 p-4">
      <form onSubmit={ask} className="flex flex-wrap items-end gap-3">
        <div className="grid min-w-0 flex-1 gap-1.5">
          <div className="flex items-center gap-1.5">
            <Label
              htmlFor="querylog-ai-ask"
              className="text-muted-foreground text-xs"
            >
              Ask about the query log
            </Label>
            <HelpTip id="querylog-ai-ask" label="Ask about the query log" />
          </div>
          <Input
            id="querylog-ai-ask"
            data-testid="querylog-ai-ask"
            maxLength={maxQuestion}
            placeholder="Which clients were blocked for malware in the last hour?"
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
          />
        </div>
        <Button
          type="submit"
          data-testid="querylog-ai-ask-submit"
          className="h-9"
          disabled={question.trim() === "" || start.isPending}
        >
          <Sparkles className="mr-1.5 h-4 w-4" />
          Ask
        </Button>
      </form>

      <ErrorAlert
        error={start.error ?? task.error}
        prefix="Could not ask the query log"
        className="mt-3"
      />
      <div className="mt-3 empty:mt-0">
        <AiTaskStatus task={task.data} />
      </div>

      {result && (
        <div className="mt-3 space-y-2 text-sm">
          <p data-testid="querylog-ai-summary">{result.summary}</p>
          <p
            className="text-muted-foreground"
            data-testid="querylog-ai-explanation"
          >
            {result.explanation}
          </p>
          {result.suggestions.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {result.suggestions.map((s) => (
                <Button
                  key={s}
                  type="button"
                  variant="outline"
                  size="sm"
                  data-testid="querylog-ai-suggestion"
                  onClick={() => setQuestion(s)}
                >
                  {s}
                </Button>
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
