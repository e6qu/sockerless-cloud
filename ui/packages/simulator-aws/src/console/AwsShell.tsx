import { useEffect, useId, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTheme } from "@sockerless/ui-core/hooks";
import { applyDensity, applyMode, Density, Mode } from "@cloudscape-design/global-styles";
import TopNavigation, { type TopNavigationProps } from "@cloudscape-design/components/top-navigation";
import Autosuggest, { type AutosuggestProps } from "@cloudscape-design/components/autosuggest";
import Button from "@cloudscape-design/components/button";
import Box from "@cloudscape-design/components/box";
import Input, { type InputProps } from "@cloudscape-design/components/input";
import Link from "@cloudscape-design/components/link";
import Badge from "@cloudscape-design/components/badge";
import {
  colorBackgroundContainerContent,
  colorBorderDividerDefault,
  colorBorderDividerSecondary,
} from "@cloudscape-design/design-tokens";
import { consoleConfig, currentRegion, selectRegion } from "./federation.js";
import { regionName, regionShortName } from "./regions.js";
import { useRecentlyVisited } from "./recent.js";
import {
  CATEGORIES,
  filterServices,
  findService,
  searchConsole,
  type ConsoleService,
} from "./serviceCatalog.js";
import { fetchCallerIdentity, fetchEC2Regions } from "../api.js";

const AWS_LOGO =
  "data:image/svg+xml," +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" width="56" height="34" viewBox="0 0 56 34"><text x="3" y="19" font-family="Arial,Helvetica,sans-serif" font-weight="700" font-size="20" fill="#ffffff">aws</text><path d="M5 24.5 Q27 34 49 23.5" stroke="#ff9900" stroke-width="3" fill="none" stroke-linecap="round"/><path d="M43.5 21 L49.5 23.5 L45.5 29" stroke="#ff9900" stroke-width="3" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  );

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

const DENSITY_STORAGE_KEY = "aws-console:density";

function storedDensity(): Density {
  try {
    return window.localStorage.getItem(DENSITY_STORAGE_KEY) === "compact" ? Density.Compact : Density.Comfortable;
  } catch {
    return Density.Comfortable;
  }
}

/** The visual mode and content density the operator chose in Settings,
 * applied through Cloudscape's own global styles and remembered in this
 * browser. */
function useDisplaySettings() {
  const { theme, setTheme } = useTheme("light");
  const [density, setDensityState] = useState<Density>(storedDensity);
  useEffect(() => {
    applyMode(theme === "dark" ? Mode.Dark : Mode.Light);
  }, [theme]);
  useEffect(() => {
    applyDensity(density);
  }, [density]);
  const setDensity = (next: Density) => {
    try {
      window.localStorage.setItem(DENSITY_STORAGE_KEY, next === Density.Compact ? "compact" : "comfortable");
    } catch {
      // Storage is unavailable (a private window); the choice lasts for this page.
    }
    setDensityState(next);
  };
  return { theme, setTheme, density, setDensity };
}

interface OperatorSession {
  name?: string;
  email?: string;
}

/** The signed-in operator and the console's sign-out coordinate, read from the
 * console's own authentication layer. Absent when the console runs with no
 * identity provider, where it reads AWS unsigned. */
function useOperatorSession() {
  const config = useQuery({ queryKey: ["console-config"], queryFn: consoleConfig, staleTime: Infinity, retry: false });
  const identityEndpoint = config.data?.identityEndpoint;
  const session = useQuery({
    queryKey: ["console-session", identityEndpoint],
    enabled: Boolean(identityEndpoint),
    retry: false,
    staleTime: 30_000,
    queryFn: async (): Promise<OperatorSession> => {
      const response = await fetch(identityEndpoint!, { credentials: "include", headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`${identityEndpoint} returned HTTP ${response.status}`);
      return (await response.json()) as OperatorSession;
    },
  });
  const caller = useQuery({
    queryKey: ["sts-caller-identity"],
    enabled: Boolean(identityEndpoint),
    retry: false,
    staleTime: 5 * 60_000,
    queryFn: fetchCallerIdentity,
  });
  return {
    signedIn: Boolean(identityEndpoint),
    logoutEndpoint: config.data?.logoutEndpoint,
    session: session.data,
    caller: caller.data,
  };
}

/** 123456789012 as the console prints it: 1234-5678-9012. */
export function formatAccountId(account: string): string {
  return /^\d{12}$/.test(account) ? `${account.slice(0, 4)}-${account.slice(4, 8)}-${account.slice(8)}` : account;
}

/** The principal the console shows in the account menu: `role/session` for an
 * assumed role, the user name for an IAM user. */
export function principalLabel(arn: string): string {
  const resource = arn.split(":").slice(5).join(":");
  if (resource.startsWith("assumed-role/")) return resource.slice("assumed-role/".length);
  if (resource.startsWith("user/")) return resource.slice(resource.lastIndexOf("/") + 1);
  return resource || arn;
}

function UnifiedSearch() {
  const navigate = useNavigate();
  const [value, setValue] = useState("");
  const ref = useRef<AutosuggestProps.Ref>(null);
  const wrapperRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (!event.altKey || event.code !== "KeyS") return;
      if (wrapperRef.current?.closest('[aria-hidden="true"]')) return;
      event.preventDefault();
      ref.current?.focus();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);
  const results = searchConsole(value);
  const services = results.filter((entry) => entry.kind === "service").slice(0, 8);
  const features = results.filter((entry) => entry.kind === "feature").slice(0, 8);
  const options: AutosuggestProps.Options = [
    ...(services.length ? [{ label: "Services", options: services.map((entry) => ({ value: entry.href, label: entry.label, description: entry.description })) }] : []),
    ...(features.length
      ? [{ label: "Features", options: features.map((entry) => ({ value: entry.href, label: entry.label, description: entry.description })) }]
      : []),
  ];
  return (
    <div ref={wrapperRef}>
    <Autosuggest
      ref={ref}
      value={value}
      onChange={(event) => setValue(event.detail.value)}
      onSelect={(event) => {
        if (!event.detail.selectedOption?.value) return;
        setValue("");
        navigate(event.detail.selectedOption.value);
      }}
      options={options}
      filteringType="manual"
      enteredTextLabel={(text) => `Search for "${text}"`}
      hideEnteredTextOption
      placeholder="Search [Alt+S]"
      ariaLabel="Search"
      empty="No services or features match your search."
      virtualScroll={false}
    />
    </div>
  );
}

// TopNavigation renders its search slot twice: once for real and once in an
// aria-hidden copy it measures for responsive layout. The trigger below is
// stateless so both copies stay in step, and the panel renders once, outside
// TopNavigation, where neither the copy nor the header's dark visual context
// reaches it.
const SERVICES_TRIGGER = "data-services-menu-trigger";

export function ServicesMenuTrigger({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  return (
    <Button
      variant="icon"
      iconName="grid-view"
      ariaLabel="Services"
      ariaExpanded={open}
      ariaHaspopup="dialog"
      nativeButtonAttributes={{ [SERVICES_TRIGGER]: "" }}
      onClick={() => onToggle()}
    />
  );
}

/** The visible copy of an element TopNavigation renders twice. */
function visibleOf<T extends Element>(selector: string): T | undefined {
  return Array.from(document.querySelectorAll<T>(selector)).find((element) => !element.closest('[aria-hidden="true"]'));
}

/**
 * The Services menu: a panel docked under the header that lists the catalog by
 * category, with Recently visited first, and narrows as the operator types.
 * Cloudscape's TopNavigation has no packaged mega-menu, so this is a labelled
 * dialog built from Cloudscape primitives, with the focus trap every dialog in
 * the console holds.
 */
export function ServicesMenu({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [query, setQuery] = useState("");
  const recent = useRecentlyVisited();
  const [category, setCategory] = useState<string>(recent.length ? "recent" : "all");
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<InputProps.Ref>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!open) return;
    const returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    searchRef.current?.focus();
    function close(restoreFocus: boolean) {
      onClose();
      if (restoreFocus) (visibleOf<HTMLElement>(`[${SERVICES_TRIGGER}]`) ?? returnFocus)?.focus();
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.stopPropagation();
        close(true);
        return;
      }
      if (event.key !== "Tab") return;
      const focusables = Array.from(panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? []);
      if (focusables.length === 0) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
    function onPointerDown(event: MouseEvent) {
      const target = event.target as Element;
      if (panelRef.current?.contains(target) || target.closest(`[${SERVICES_TRIGGER}]`)) return;
      close(false);
    }
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("mousedown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("mousedown", onPointerDown);
    };
  }, [open, onClose]);

  useEffect(() => {
    if (!open) {
      setQuery("");
      setCategory(recent.length ? "recent" : "all");
    }
  }, [open, recent.length]);

  if (!open) return null;

  const searching = query.trim() !== "";
  let shown: ConsoleService[];
  let heading: string;
  if (searching) {
    shown = filterServices(query);
    heading = "Search results";
  } else if (category === "recent") {
    shown = recent.map(findService).filter((service): service is ConsoleService => Boolean(service));
    heading = "Recently visited";
  } else if (category === "all") {
    shown = filterServices("");
    heading = "All services";
  } else {
    shown = filterServices("", category);
    heading = category;
  }

  function follow(to: string) {
    onClose();
    navigate(to);
  }

  const categoryLinks = [
    { id: "recent", label: "Recently visited" },
    { id: "all", label: "All services" },
    ...CATEGORIES.map((label) => ({ id: label, label })),
  ];

  return (
    <div
      ref={panelRef}
      role="dialog"
      aria-labelledby={titleId}
      className="aws-services-panel"
      style={{
        top: document.getElementById("aws-console-header")?.getBoundingClientRect().bottom ?? 0,
        background: colorBackgroundContainerContent,
        borderBottom: `1px solid ${colorBorderDividerDefault}`,
      }}
    >
      <div className="aws-services-panel-categories" style={{ borderInlineEnd: `1px solid ${colorBorderDividerSecondary}` }}>
        <Box variant="h2" fontSize="heading-s" id={titleId} padding={{ bottom: "s" }}>
          Services
        </Box>
        <ul>
          {categoryLinks.map((entry) => {
            const active = !searching && entry.id === category;
            return (
              <li key={entry.id}>
                <Link
                  href={`#${entry.id}`}
                  variant={active ? "primary" : "secondary"}
                  nativeAttributes={active ? { "aria-current": "true" } : undefined}
                  onFollow={(event) => {
                    event.preventDefault();
                    setQuery("");
                    setCategory(entry.id);
                  }}
                >
                  {entry.label}
                </Link>
              </li>
            );
          })}
        </ul>
      </div>
      <div className="aws-services-panel-services">
        <Box margin={{ bottom: "m" }}>
          <Input
            ref={searchRef}
            type="search"
            ariaLabel="Search services"
            placeholder="Search services"
            value={query}
            onChange={(event) => setQuery(event.detail.value)}
          />
        </Box>
        <Box variant="h3" fontSize="heading-xs" margin={{ bottom: "s" }}>
          {heading}
        </Box>
        {shown.length === 0 ? (
          <Box color="text-body-secondary">
            {searching ? `No services match "${query.trim()}".` : "You have not visited any services yet."}
          </Box>
        ) : (
          <ul className="aws-services-panel-list">
            {shown.map((service) => {
              const supported = service.supported !== false;
              return (
                <li key={service.id}>
                  <Link
                    href={service.home}
                    fontSize="body-m"
                    ariaLabel={supported ? undefined : `${service.name}, no page in this console`}
                    onFollow={(event) => {
                      event.preventDefault();
                      follow(service.home);
                    }}
                  >
                    {service.name}
                  </Link>
                  {!supported && (
                    <span className="aws-pill">
                      <Badge color="grey" nativeAttributes={{ "aria-hidden": true }}>
                        Not supported
                      </Badge>
                    </span>
                  )}
                  <Box fontSize="body-s" color="text-body-secondary">
                    {service.description}
                  </Box>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}

function SignOutForm({ endpoint, formRef }: { endpoint: string; formRef: React.RefObject<HTMLFormElement | null> }) {
  return <form ref={formRef} method="post" action={endpoint} hidden aria-hidden="true" />;
}

export function AwsTopNavigation({ servicesOpen, onToggleServices }: { servicesOpen: boolean; onToggleServices: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { theme, setTheme, density, setDensity } = useDisplaySettings();
  const { signedIn, logoutEndpoint, session, caller } = useOperatorSession();
  const signOutRef = useRef<HTMLFormElement>(null);
  const [region, setRegion] = useState(currentRegion);
  const regions = useQuery({ queryKey: ["ec2-regions"], queryFn: fetchEC2Regions, staleTime: 5 * 60_000, retry: false });

  const regionCodes = regions.data?.map((entry) => entry.regionName) ?? [];
  if (!regionCodes.includes(region)) regionCodes.unshift(region);

  const utilities: TopNavigationProps.Utility[] = [
    {
      type: "menu-dropdown",
      iconName: "settings",
      ariaLabel: "Settings",
      title: "Settings",
      items: [
        {
          text: "Visual mode",
          items: [
            { id: "mode-light", text: "Light", itemType: "checkbox", checked: theme === "light" },
            { id: "mode-dark", text: "Dark", itemType: "checkbox", checked: theme === "dark" },
          ],
        },
        {
          text: "Density",
          items: [
            { id: "density-comfortable", text: "Comfortable", itemType: "checkbox", checked: density === Density.Comfortable },
            { id: "density-compact", text: "Compact", itemType: "checkbox", checked: density === Density.Compact },
          ],
        },
      ],
      onItemClick: (event) => {
        const id = event.detail.id;
        if (id === "mode-light") setTheme("light");
        else if (id === "mode-dark") setTheme("dark");
        else if (id === "density-comfortable") setDensity(Density.Comfortable);
        else if (id === "density-compact") setDensity(Density.Compact);
      },
    },
    {
      type: "menu-dropdown",
      text: regionShortName(region),
      ariaLabel: `Region: ${regionShortName(region)}`,
      title: "Regions",
      items: regionCodes.map((code) => ({
        id: code,
        text: regionName(code),
        secondaryText: code,
        itemType: "checkbox" as const,
        checked: code === region,
      })),
      onItemClick: (event) => {
        const code = event.detail.id;
        if (code === region) return;
        selectRegion(code);
        setRegion(code);
        void queryClient.resetQueries();
      },
    },
  ];
  if (signedIn && caller) {
    const account = formatAccountId(caller.account);
    const principal = principalLabel(caller.arn);
    utilities.push({
      type: "menu-dropdown",
      text: `${principal} @ ${account}`,
      description: session?.email || session?.name,
      iconName: "user-profile",
      ariaLabel: `Account menu: ${principal} @ ${account}`,
      items: [
        { id: "account-id", text: `Account ID: ${account}`, secondaryText: "Copy the account ID", iconName: "copy" },
        { id: "principal", text: caller.arn, secondaryText: "Signed-in principal", disabled: true },
        ...(logoutEndpoint ? [{ id: "signout", text: "Sign out" }] : []),
      ],
      onItemClick: (event) => {
        if (event.detail.id === "account-id") void navigator.clipboard?.writeText(caller.account);
        if (event.detail.id === "signout") signOutRef.current?.requestSubmit();
      },
    });
  }

  return (
    <>
      <TopNavigation
        identity={{
          href: "/ui/",
          logo: { src: AWS_LOGO, alt: "AWS Console Home" },
          onFollow: (event) => {
            event.preventDefault();
            navigate("/ui/");
          },
        }}
        search={
          <div className="aws-topnav-search">
            <ServicesMenuTrigger open={servicesOpen} onToggle={onToggleServices} />
            <div className="aws-topnav-search-field">
              <UnifiedSearch />
            </div>
          </div>
        }
        utilities={utilities}
        i18nStrings={{
          searchIconAriaLabel: "Search",
          searchDismissIconAriaLabel: "Close search",
          overflowMenuDismissIconAriaLabel: "Close menu",
          overflowMenuBackIconAriaLabel: "Back",
          overflowMenuTriggerText: "More",
          overflowMenuTitleText: "All",
        }}
      />
      {logoutEndpoint && <SignOutForm endpoint={logoutEndpoint} formRef={signOutRef} />}
    </>
  );
}
