import { useQuery } from "@tanstack/react-query";

interface UIAuthConfig {
  identityEndpoint: string;
  logoutEndpoint: string;
}

interface OperatorIdentity {
  sub?: string;
  name?: string;
  login?: string;
  user?: string;
  email?: string;
  preferred_username?: string;
  preferredUsername?: string;
  picture?: string;
}

async function getJSON<T>(endpoint: string): Promise<T> {
  const response = await fetch(endpoint, {
    credentials: "include",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    throw new Error(`${endpoint} returned HTTP ${response.status}`);
  }
  return (await response.json()) as T;
}

function identityName(identity: OperatorIdentity): string {
  return (
    identity.name ||
    identity.preferred_username ||
    identity.preferredUsername ||
    identity.login ||
    identity.user ||
    identity.email ||
    identity.sub ||
    "Signed-in user"
  );
}

export function OperatorAccount() {
  const config = useQuery({
    queryKey: ["simulator-ui-config"],
    queryFn: () => getJSON<UIAuthConfig>("/ui/config.json"),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const identityEndpoint = config.data?.identityEndpoint;
  const identity = useQuery({
    queryKey: ["simulator-operator-identity", identityEndpoint],
    queryFn: () => getJSON<OperatorIdentity>(identityEndpoint!),
    enabled: Boolean(identityEndpoint),
    retry: false,
    staleTime: 30_000,
  });

  if (config.isPending) {
    return <div role="status" className="text-xs">Loading identity…</div>;
  }
  if (config.isError || identity.isError) {
    return (
      <div role="alert" className="text-xs" style={{ color: "var(--color-status-error)" }}>
        Signed-in identity is unavailable.
      </div>
    );
  }
  if (!identityEndpoint || !config.data?.logoutEndpoint) {
    return (
      <div className="text-xs" style={{ color: "var(--color-fg-subtle)" }}>
        Authentication not configured
      </div>
    );
  }
  if (identity.isPending || !identity.data) {
    return <div role="status" className="text-xs">Loading signed-in identity…</div>;
  }

  const name = identityName(identity.data);
  const detail = identity.data.email && identity.data.email !== name ? identity.data.email : "Authenticated by Shauth";
  return (
    <section className="flex items-center gap-2" aria-label="Signed-in user" data-shauth-user={name}>
      {identity.data.picture ? (
        <img
          src={identity.data.picture}
          alt=""
          className="h-8 w-8 shrink-0 rounded-full"
          referrerPolicy="no-referrer"
        />
      ) : (
        <span
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-xs font-semibold"
          aria-hidden="true"
          style={{ background: "var(--color-accent-soft)", color: "var(--color-fg)" }}
        >
          {name.slice(0, 1).toUpperCase()}
        </span>
      )}
      <div className="min-w-0 flex-1">
        <p className="truncate text-xs font-medium" style={{ color: "var(--color-fg)" }}>{name}</p>
        <p className="truncate text-[10px]" style={{ color: "var(--color-fg-subtle)" }}>{detail}</p>
      </div>
      <form method="post" action={config.data.logoutEndpoint}>
        <button
          type="submit"
          className="text-xs underline"
          aria-label={`Sign out ${name}`}
          data-shauth-sign-out
        >
          Sign out
        </button>
      </form>
    </section>
  );
}
