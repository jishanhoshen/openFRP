import { useState, type FormEvent } from "react";
import { ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ErrorNote } from "@/components/common";
import { api, errorText } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { go } from "@/lib/router";
import type { Me } from "@/lib/types";

export function LoginPage({ register }: { register: boolean }) {
  const { info, refresh } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      if (register) await api("POST", "/auth/register", { email, password });
      await api<Me>("POST", "/auth/login", { email, password });
      await refresh();
      go("#/");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-4">
      <div className="w-full max-w-sm space-y-6">
        <div className="flex flex-col items-center gap-2 text-center">
          <div className="flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
            <ShieldCheck className="size-6" />
          </div>
          <h1 className="text-xl font-semibold">open<span className="text-primary">FRP</span></h1>
        </div>
        <Card>
          <CardHeader>
            <CardTitle>{register ? "Create your account" : "Welcome back"}</CardTitle>
            <CardDescription>
              {register
                ? "Share games from your own computer through your own public address."
                : "Log in to manage your computers and tunnels."}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={submit} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="email">Email</Label>
                <Input id="email" type="email" autoComplete="username" required maxLength={254}
                  value={email} onChange={(e) => setEmail(e.target.value)} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">Password</Label>
                <Input id="password" type="password" required minLength={10} maxLength={256}
                  autoComplete={register ? "new-password" : "current-password"}
                  value={password} onChange={(e) => setPassword(e.target.value)} />
                {register && <p className="text-xs text-muted-foreground">At least 10 characters.</p>}
              </div>
              {error && <ErrorNote message={error} />}
              <Button type="submit" className="w-full" disabled={busy}>
                {busy ? "Please wait…" : register ? "Create account" : "Log in"}
              </Button>
            </form>
            <div className="mt-4 text-center text-sm text-muted-foreground">
              {register ? (
                <a className="text-primary hover:underline" href="#/login">Already have an account? Log in</a>
              ) : info?.open_registration ? (
                <a className="text-primary hover:underline" href="#/register">Create an account</a>
              ) : (
                <span>No account? Ask the administrator to create one.</span>
              )}
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
