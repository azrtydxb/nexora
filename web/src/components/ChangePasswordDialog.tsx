import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";

import { api, ApiError, unwrap } from "@/api/client";
import { errorMessage } from "@/components/common";
import { HelpTip } from "@/components/HelpTip";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const minPassword = 12;

/** The message for a failed password change; the server's own message for anything else. */
function passwordError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 403 && err.code === "invalid_current_password")
      return "Current password is wrong";
    if (err.status === 429)
      return "Too many attempts. Try again in 15 minutes.";
  }
  return errorMessage(err);
}

/** Changes the signed-in local user's password; onDone runs after a successful change. */
export function ChangePasswordDialog({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: () => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [revokeOthers, setRevokeOthers] = useState(true);
  const [problem, setProblem] = useState("");

  const change = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/auth/me/password", {
          body: {
            current_password: current,
            new_password: next,
            revoke_other_sessions: revokeOthers,
          },
        }),
      ),
    onError: (err) => setProblem(passwordError(err)),
    onSuccess: () => {
      onDone();
      onClose();
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (next.length < minPassword) {
      setProblem(`The new password needs at least ${minPassword} characters.`);
      return;
    }
    if (next !== confirm) {
      setProblem("The new passwords do not match.");
      return;
    }
    setProblem("");
    change.mutate();
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Change password</DialogTitle>
          <DialogDescription>
            You stay signed in here after the change.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          {problem && (
            <Alert variant="destructive">
              <AlertDescription data-testid="password-error">
                {problem}
              </AlertDescription>
            </Alert>
          )}
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="password-current">Current password</Label>
              <HelpTip id="password-current" label="Current password" />
            </div>
            <Input
              id="password-current"
              data-testid="password-current"
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="password-new">New password</Label>
              <HelpTip id="password-new" label="New password" />
            </div>
            <Input
              id="password-new"
              data-testid="password-new"
              type="password"
              autoComplete="new-password"
              required
              aria-describedby="password-new-hint"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            <p id="password-new-hint" className="text-muted-foreground text-xs">
              At least {minPassword} characters.
            </p>
          </div>
          <div className="grid gap-1.5">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="password-confirm">Confirm new password</Label>
              <HelpTip id="password-confirm" label="Confirm new password" />
            </div>
            <Input
              id="password-confirm"
              data-testid="password-confirm"
              type="password"
              autoComplete="new-password"
              required
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </div>
          <div className="flex items-center gap-1.5">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                id="password-revoke-others"
                data-testid="password-revoke-others"
                className="accent-primary h-4 w-4"
                checked={revokeOthers}
                onChange={(e) => setRevokeOthers(e.target.checked)}
              />
              Sign out my other sessions
            </label>
            <HelpTip
              id="password-revoke-others"
              label="Sign out my other sessions"
            />
          </div>
          <DialogFooter className="gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="password-save"
              disabled={change.isPending}
            >
              {change.isPending ? "Changing…" : "Change password"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
