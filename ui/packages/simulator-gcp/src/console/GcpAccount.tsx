import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Icon } from "./icons.js";
import { useDismiss } from "./menu.js";

// The account control at the top right: the signed-in operator's avatar,
// opening a card with their identity, the workforce pool provider the console
// federates them through, and Sign out. It carries the `data-shauth-*` markers
// the browser qualification drives. A console with no identity provider — a
// local instance — runs unauthenticated and shows no account control at all.

interface UIConfig {
  identityEndpoint?: string;
  logoutEndpoint?: string;
  federationAudience?: string;
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

export function useConsoleConfig() {
  return useQuery({
    queryKey: ["console-ui-config"],
    queryFn: () => getJSON<UIConfig>("/ui/config.json"),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
}

function displayName(identity: Identity): string {
  return identity.name || identity.preferred_username || identity.login || identity.email || "Operator";
}

/** The workforce pool provider the federation audience names, as `pool / provider`. */
export function workforceProvider(audience: string | undefined): string | null {
  const match = audience?.match(/workforcePools\/([^/]+)\/providers\/([^/]+)$/);
  return match ? `${match[1]} / ${match[2]}` : null;
}

export function GcpAccount() {
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useDismiss(wrapper, open, () => setOpen(false), trigger);
  const config = useConsoleConfig();
  const identityEndpoint = config.data?.identityEndpoint;
  const identity = useQuery({
    queryKey: ["console-operator-identity", identityEndpoint],
    queryFn: () => getJSON<Identity>(identityEndpoint!),
    enabled: Boolean(identityEndpoint),
    retry: false,
    staleTime: 30_000,
  });

  if (!identityEndpoint || !config.data?.logoutEndpoint || !identity.data) return null;

  const name = displayName(identity.data);
  const initial = name.slice(0, 1).toUpperCase();
  const provider = workforceProvider(config.data.federationAudience);
  const avatar = (large: boolean) => (
    <span className={large ? "gc-avatar gc-avatar-large" : "gc-avatar"} aria-hidden>
      {identity.data.picture ? <img src={identity.data.picture} alt="" referrerPolicy="no-referrer" /> : initial}
    </span>
  );

  return (
    <div className="gc-menu-anchor" ref={wrapper} data-shauth-user={name}>
      <button
        ref={trigger}
        type="button"
        className="gc-avatar-button"
        data-shauth-account-trigger
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`Google Account: ${name}`}
        title={`Google Account: ${name}`}
        onClick={() => setOpen((value) => !value)}
      >
        {avatar(false)}
      </button>
      {open ? (
        <div className="gc-menu gc-menu-end gc-account-card" role="dialog" aria-label="Account">
          {identity.data.email ? <p className="gc-account-email">{identity.data.email}</p> : null}
          {avatar(true)}
          <p className="gc-account-name">Hi, {name}!</p>
          {provider ? (
            <p className="gc-account-provider">
              <Icon name="badge" size="1.1em" />
              Signed in through Workforce Identity Federation ({provider})
            </p>
          ) : null}
          <form method="post" action={config.data.logoutEndpoint} className="gc-account-signout">
            <button type="submit" data-shauth-sign-out>
              <Icon name="logout" size="1.25em" />
              Sign out
            </button>
          </form>
        </div>
      ) : null}
    </div>
  );
}
