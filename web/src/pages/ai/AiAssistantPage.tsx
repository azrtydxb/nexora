import { useEffect, useRef, useState, type FormEvent } from "react";
import { Navigate, useSearchParams } from "react-router";
import { Plus, Sparkles } from "lucide-react";

import {
  useAiAssistantSession,
  useCreateAiAssistantSession,
  usePostAiAssistantMessage,
} from "@/api/ai";
import { useCan } from "@/auth/AuthProvider";
import { AiPage } from "@/components/ai/AiOff";
import { AiTaskStatus } from "@/components/ai/AiTaskStatus";
import { ProposalCard } from "@/components/ai/ProposalCard";
import { ErrorAlert } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

const storageKey = "nexora-ai-assistant-session";
const maxMessage = 2000;

// The last session id is a convenience: unreadable or unwritable storage simply starts without one.
function readStored(): string | null {
  try {
    return localStorage.getItem(storageKey);
  } catch {
    return null;
  }
}

function writeStored(id: string) {
  try {
    localStorage.setItem(storageKey, id);
  } catch {
    // Storage unavailable (private mode, quota): the session lives in the URL for this visit only.
  }
}

/** The configuration assistant: a conversation whose plan is a proposal to review and apply. */
export function AiAssistantPage() {
  // The assistant plans configuration changes: viewers have no session to talk to.
  if (!useCan("createAiAssistantSession")) return <Navigate to="/ai" replace />;
  return (
    <AiPage
      title="Assistant"
      description="Describe a configuration change; the assistant answers with a proposal to review and apply."
    >
      <Assistant />
    </AiPage>
  );
}

function Assistant() {
  const [params, setParams] = useSearchParams();
  const sessionId = params.get("session");
  const [message, setMessage] = useState("");
  const create = useCreateAiAssistantSession();
  const post = usePostAiAssistantMessage(sessionId ?? "");
  const session = useAiAssistantSession(sessionId);
  const canApply = useCan("applyAiProposals");

  // Reopening the page returns to the session of the last visit, once.
  const restored = useRef(false);
  useEffect(() => {
    if (restored.current) return;
    restored.current = true;
    if (sessionId !== null) return;
    const stored = readStored();
    if (stored !== null) setParams({ session: stored }, { replace: true });
  }, [sessionId, setParams]);

  function open(id: string) {
    writeStored(id);
    setParams({ session: id });
  }

  function send(e: FormEvent) {
    e.preventDefault();
    const content = message.trim();
    if (content === "" || sessionId === null) return;
    post.mutate(content, { onSuccess: () => setMessage("") });
  }

  const messages = session.data?.messages ?? [];
  const proposal = session.data?.proposal;

  return (
    <div className="space-y-4">
      <ErrorAlert
        error={session.error}
        prefix="Could not load the session"
        thing="session"
      />

      <Card className="space-y-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="min-w-0 truncate font-medium">
            {session.data?.title || "New session"}
          </h2>
          <Button
            size="sm"
            variant="outline"
            data-testid="ai-assistant-new"
            disabled={create.isPending}
            onClick={() =>
              create.mutate(undefined, { onSuccess: (s) => open(s.id) })
            }
          >
            <Plus className="mr-1 h-4 w-4" />
            New session
          </Button>
        </div>
        <ErrorAlert error={create.error} prefix="Could not start a session" />

        {messages.length > 0 && (
          <ol className="space-y-3">
            {messages.map((m) => (
              <li
                key={m.id}
                data-testid={`ai-assistant-msg-${m.role}`}
                className={
                  m.role === "user"
                    ? "bg-muted ml-auto max-w-[85%] rounded-md p-3"
                    : "max-w-[85%] rounded-md border p-3"
                }
              >
                <p className="text-sm break-words whitespace-pre-wrap">
                  {m.content}
                </p>
              </li>
            ))}
          </ol>
        )}

        <form onSubmit={send} className="space-y-2">
          <div className="flex items-center gap-1.5">
            <Label
              htmlFor="ai-assistant-message"
              className="text-muted-foreground text-xs"
            >
              Describe the change
            </Label>
            <HelpTip id="ai-assistant-message" label="Message" />
          </div>
          <Textarea
            id="ai-assistant-message"
            data-testid="ai-assistant-message"
            rows={3}
            maxLength={maxMessage}
            placeholder="Block malware for the guest network 10.99.0.0/24"
            value={message}
            onChange={(e) => setMessage(e.target.value)}
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-muted-foreground text-xs">
              Nothing changes until you apply the plan.
            </p>
            <Button
              type="submit"
              size="sm"
              data-testid="ai-assistant-send"
              disabled={
                message.trim() === "" || sessionId === null || post.isPending
              }
            >
              <Sparkles className="mr-1.5 h-4 w-4" />
              Send
            </Button>
          </div>
          {sessionId === null && (
            <p className="text-muted-foreground text-xs">
              Start a new session to send a message.
            </p>
          )}
        </form>

        <ErrorAlert error={post.error} prefix="Could not send the message" />
        <div className="empty:hidden">
          <AiTaskStatus task={session.data?.task ?? undefined} />
        </div>
      </Card>

      {proposal && (
        <section
          data-testid="ai-assistant-plan"
          aria-label="Plan"
          className="space-y-2"
        >
          <p className="text-muted-foreground text-xs">
            The plan runs{" "}
            {proposal.actions.map((a, i) => (
              <span key={i}>
                {i > 0 && ", "}
                <code className="font-mono">{a.operation_id}</code>
              </span>
            ))}{" "}
            when you apply it.
          </p>
          <ProposalCard proposal={proposal} canApply={canApply} />
        </section>
      )}
    </div>
  );
}
