import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Icon } from "./icons.js";

// The account control the console keeps at the top right: a circular avatar
// that opens a menu with the signed-in identity and a sign-out. It carries the
// `data-shauth-*` markers the browser qualification uses — the identity on the
// always-visible trigger, sign-out inside the menu — so the console reads as
// Google Cloud's while still being driven by the same qualification.
//
// When no identity provider is configured — a local or test simulator — it
// shows a neutral avatar rather than an error, so an unauthenticated console
// still looks like a console instead of a broken one.

interface UIConfig {
  identityEndpoint?: string;
  logoutEndpoint?: string;
}

interface Identity {
  name?: string;
  login?: string;
  preferred_username?: string;
  email?: string;
  picture?: string;
}

async function getJSON<T>(endpoint: string): Promise<T> {
  const response = await fetch(endpoint, { credentials: "include", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`${endpoint} returned HTTP ${response.status}`);
  return (await response.json()) as T;
}

function displayName(identity: Identity): string {
  return identity.name || identity.preferred_username || identity.login || identity.email || "Operator";
}

export function GcpAccount() {
  const [open, setOpen] = useState(false);
  const config = useQuery({
    queryKey: ["console-ui-config"],
    queryFn: () => getJSON<UIConfig>("/ui/config.json"),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const identityEndpoint = config.data?.identityEndpoint;
  const identity = useQuery({
    queryKey: ["console-operator-identity", identityEndpoint],
    queryFn: () => getJSON<Identity>(identityEndpoint!),
    enabled: Boolean(identityEndpoint),
    retry: false,
    staleTime: 30_000,
  });

  // A neutral avatar whenever there is no established identity — no provider
  // configured, still loading, or the identity could not be read. The console
  // stays presentable; it never renders an error into its own chrome.
  if (!identityEndpoint || !config.data?.logoutEndpoint || !identity.data) {
    return (
      <span className="gc-avatar gc-avatar-anonymous" role="img" aria-label="Account">
        <Icon name="account_circle" size="1.5em" />
      </span>
    );
  }

  const name = displayName(identity.data);
  const initial = name.slice(0, 1).toUpperCase();

  return (
    <div className="gc-account" data-shauth-user={name}>
      <button
        type="button"
        className="gc-avatar"
        data-shauth-account-trigger
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Google Account: ${name}`}
        onClick={() => setOpen((value) => !value)}
      >
        {identity.data.picture ? (
          <img src={identity.data.picture} alt="" referrerPolicy="no-referrer" />
        ) : (
          <span aria-hidden>{initial}</span>
        )}
      </button>
      {open ? (
        <div className="gc-account-menu" role="menu">
          <div className="gc-account-identity">
            <span className="gc-avatar gc-avatar-large" aria-hidden>
              {identity.data.picture ? <img src={identity.data.picture} alt="" referrerPolicy="no-referrer" /> : initial}
            </span>
            <div>
              <p className="gc-account-name">{name}</p>
              {identity.data.email ? <p className="gc-account-email">{identity.data.email}</p> : null}
            </div>
          </div>
          <form method="post" action={config.data.logoutEndpoint} className="gc-account-signout">
            <button type="submit" data-shauth-sign-out>Sign out</button>
          </form>
        </div>
      ) : null}
    </div>
  );
}
