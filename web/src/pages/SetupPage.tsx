import { useState, type ChangeEvent, type FormEvent } from "react";
import { Navigate, useNavigate } from "react-router";

import { api, unwrap } from "@/api/client";
import {
  useCurrentUser,
  useSetupStatus,
  useSignedIn,
} from "@/auth/AuthProvider";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AuthLayout } from "@/pages/AuthLayout";

export function SetupPage() {
  const setup = useSetupStatus();
  const navigate = useNavigate();
  const signedIn = useSignedIn();
  const { user } = useCurrentUser();
  const [form, setForm] = useState({
    token: "",
    username: "",
    email: "",
    password: "",
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (setup.isPending) return null;
  // Completing setup signs the new admin in and clears the query cache, so this page can see
  // `required: false` before its navigation to / commits; a signed-in user goes to / then too.
  if (setup.data && !setup.data.required)
    return <Navigate to={user ? "/" : "/login"} replace />;

  const field = (key: keyof typeof form) => ({
    value: form[key],
    onChange: (e: ChangeEvent<HTMLInputElement>) =>
      setForm({ ...form, [key]: e.target.value }),
  });

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      signedIn(unwrap(await api.POST("/setup", { body: form })));
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Setup failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout
      title="Create the first administrator"
      description="Paste the setup token that nexora-mgmt printed to its log on first start. It works once."
    >
      <form onSubmit={submit} className="space-y-4">
        {error && (
          <Alert variant="destructive" data-testid="setup-error">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="space-y-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="setup-token">Setup token</Label>
            <HelpTip id="setup-token" label="Setup token" />
          </div>
          <Input
            id="setup-token"
            data-testid="setup-token"
            className="font-mono"
            autoComplete="off"
            required
            {...field("token")}
          />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="setup-username">Username</Label>
            <HelpTip id="setup-username" label="Username" />
          </div>
          <Input
            id="setup-username"
            data-testid="setup-username"
            autoComplete="username"
            required
            {...field("username")}
          />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="setup-email">Email</Label>
            <HelpTip id="setup-email" label="Email" />
          </div>
          <Input
            id="setup-email"
            data-testid="setup-email"
            type="email"
            autoComplete="email"
            required
            {...field("email")}
          />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="setup-password">Password</Label>
            <HelpTip id="setup-password" label="Password" />
          </div>
          <Input
            id="setup-password"
            data-testid="setup-password"
            type="password"
            autoComplete="new-password"
            minLength={12}
            required
            {...field("password")}
          />
          <p className="text-muted-foreground text-xs">
            At least 12 characters.
          </p>
        </div>
        <Button
          type="submit"
          data-testid="setup-submit"
          className="w-full"
          disabled={busy}
        >
          {busy ? "Creating…" : "Create administrator"}
        </Button>
      </form>
    </AuthLayout>
  );
}
