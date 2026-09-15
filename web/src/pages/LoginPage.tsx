import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { KeyRound } from "lucide-react";

import { api, unwrap } from "@/api/client";
import { safeReturnTo, useSetupStatus, useSignedIn } from "@/auth/AuthProvider";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { HelpTip } from "@/components/HelpTip";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AuthLayout } from "@/pages/AuthLayout";

export function LoginPage() {
  const [params] = useSearchParams();
  const returnTo = safeReturnTo(params.get("return_to"));
  const navigate = useNavigate();
  const signedIn = useSignedIn();
  const setup = useSetupStatus();
  const providers = useQuery({
    queryKey: ["auth-providers"],
    queryFn: async () => unwrap(await api.GET("/auth/providers")),
  });
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (setup.data?.required) return <Navigate to="/setup" replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await api.POST("/auth/login", { body: { username, password } });
      if (r.response.status === 401) {
        setError("Invalid username or password");
        return;
      }
      signedIn(unwrap(r));
      navigate(returnTo, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign-in failed");
    } finally {
      setBusy(false);
    }
  }

  async function startOidc() {
    setError(null);
    const url = `/api/v1/auth/oidc/start?return_to=${encodeURIComponent(returnTo)}`;
    try {
      // Probe first: a redirect means the provider is reachable, an error body says why not.
      const r = await fetch(url, {
        redirect: "manual",
        credentials: "same-origin",
      });
      if (r.type === "opaqueredirect") {
        window.location.assign(url);
        return;
      }
      const body = (await r.json().catch(() => null)) as {
        message?: string;
      } | null;
      setError(body?.message ?? `Single sign-on failed (${r.status})`);
    } catch {
      setError("Single sign-on failed: the management API is unreachable");
    }
  }

  return (
    <AuthLayout
      title="Sign in"
      description="Use your Nexora account or your organisation's single sign-on."
    >
      <form onSubmit={submit} className="space-y-4">
        {error && (
          <Alert variant="destructive" data-testid="login-error">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="space-y-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="username">Username</Label>
            <HelpTip id="username" label="Username" />
          </div>
          <Input
            id="username"
            data-testid="login-username"
            autoComplete="username"
            autoFocus
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="password">Password</Label>
            <HelpTip id="password" label="Password" />
          </div>
          <Input
            id="password"
            type="password"
            data-testid="login-password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
        <Button
          type="submit"
          data-testid="login-submit"
          className="w-full"
          disabled={busy}
        >
          {busy ? "Signing in…" : "Sign in"}
        </Button>
      </form>
      {providers.data?.oidc && (
        <>
          <div className="text-muted-foreground my-6 flex items-center gap-3 text-xs">
            <span className="bg-border h-px flex-1" />
            or
            <span className="bg-border h-px flex-1" />
          </div>
          <Button
            type="button"
            variant="outline"
            data-testid="login-oidc"
            className="w-full"
            onClick={() => void startOidc()}
          >
            <KeyRound className="mr-2 h-4 w-4" />
            Sign in with single sign-on
          </Button>
        </>
      )}
    </AuthLayout>
  );
}
