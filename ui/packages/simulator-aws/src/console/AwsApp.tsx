import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { BrowserRouter, Routes, useLocation, useNavigate } from "react-router";
import { ErrorBoundary } from "@sockerless/ui-core/components";
import AppLayout from "@cloudscape-design/components/app-layout";
import BreadcrumbGroup from "@cloudscape-design/components/breadcrumb-group";
import SideNavigation, { type SideNavigationProps } from "@cloudscape-design/components/side-navigation";
import HelpPanel from "@cloudscape-design/components/help-panel";
import Link from "@cloudscape-design/components/link";
import { AwsTopNavigation, ServicesMenu } from "./AwsShell.js";
import { ConsoleFlashbar, NotificationsProvider } from "./notifications.js";
import { recordVisit } from "./recent.js";
import { navLinkForPath, serviceForPath, type ConsoleService } from "./serviceCatalog.js";

interface Crumb {
  text: string;
  href?: string;
}

function decodeSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/**
 * The breadcrumb trail the real console shows above a service page: the
 * service, the side-navigation page the route sits under, and the resource a
 * detail route names.
 */
export function crumbTrail(service: ConsoleService | undefined, pathname: string): Crumb[] {
  if (!service) return [];
  const trail: Crumb[] = [{ text: service.name, href: service.home }];
  if (service.supported === false) return trail;
  const match = navLinkForPath(service, pathname);
  if (!match) return trail;
  if (match.exact) {
    if (match.link.href !== service.home || trail[0].text !== match.link.text) trail.push({ text: match.link.text });
    return trail;
  }
  trail.push({ text: match.link.text, href: match.link.href });
  const rest = pathname.slice(match.link.href.length + 1).split("/").filter(Boolean).map(decodeSegment);
  if (match.link.create && rest.length === 1 && rest[0] === "create") {
    trail.push({ text: match.link.create });
  } else if (service.id === "ecs" && match.link.href === "/ui/ecs/services" && rest.length === 2) {
    trail.push({ text: rest[0] }, { text: rest[1] });
  } else if (service.id === "stepfunctions" && rest[1] === "executions" && rest.length === 3) {
    trail.push({ text: rest[0], href: `${match.link.href}/${encodeURIComponent(rest[0])}` }, { text: rest[2] });
  } else if (rest.length > 0) {
    trail.push({ text: rest[rest.length - 1] });
  }
  return trail;
}

function navigationItems(service: ConsoleService): SideNavigationProps.Item[] {
  const items: SideNavigationProps.Item[] = [];
  for (const section of service.nav ?? []) {
    const links = section.items.map((link) => ({ type: "link" as const, text: link.text, href: link.href }));
    if (section.text) {
      items.push({ type: "section", text: section.text, items: links, defaultExpanded: true });
    } else {
      items.push(...links);
    }
  }
  return items;
}

const HelpContext = createContext<() => void>(() => undefined);
const ServicesMenuContext = createContext<() => void>(() => undefined);

/** Opens the help panel, the way an "Info" link beside a page heading does. */
export function useOpenHelp(): () => void {
  return useContext(HelpContext);
}

/** Opens the header's Services menu, the way Console Home's "View all
 * services" does. */
export function useOpenServicesMenu(): () => void {
  return useContext(ServicesMenuContext);
}

function ServiceHelp({ service }: { service: ConsoleService }) {
  return (
    <HelpPanel
      header={<h2>{service.name}</h2>}
      footer={
        <div>
          <h3>Learn more</h3>
          <ul>
            <li>
              <Link href={service.docs} external>
                {service.name} documentation
              </Link>
            </li>
          </ul>
        </div>
      }
    >
      <p>{service.description}.</p>
    </HelpPanel>
  );
}

function ConsoleFrame({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const service = serviceForPath(pathname);
  const [navigationOpen, setNavigationOpen] = useState(true);
  const [toolsOpen, setToolsOpen] = useState(false);
  const [servicesOpen, setServicesOpen] = useState(false);
  const closeServices = useCallback(() => setServicesOpen(false), []);

  useEffect(() => {
    if (service && service.supported !== false) recordVisit(service.id);
  }, [service]);

  useEffect(() => {
    if (!service) document.title = "Console Home";
  }, [service]);

  const hasNavigation = Boolean(service?.nav?.length);
  const activeHref = service ? (navLinkForPath(service, pathname)?.link.href ?? pathname) : pathname;
  const trail = crumbTrail(service, pathname);

  return (
    <div className="aws">
      <a href="#main-content" className="sl-skip-link">
        Skip to main content
      </a>
      <div id="aws-console-header" className="aws-header">
        <AwsTopNavigation servicesOpen={servicesOpen} onToggleServices={() => setServicesOpen((current) => !current)} />
      </div>
      <ServicesMenu open={servicesOpen} onClose={closeServices} />
      <ServicesMenuContext.Provider value={() => setServicesOpen(true)}>
      <HelpContext.Provider value={() => setToolsOpen(true)}>
        <AppLayout
          headerSelector="#aws-console-header"
          navigationHide={!hasNavigation}
          navigation={
            service && hasNavigation ? (
              <SideNavigation
                header={{ text: service.name, href: service.home }}
                activeHref={activeHref}
                items={navigationItems(service)}
                onFollow={(event) => {
                  if (event.detail.external) return;
                  event.preventDefault();
                  navigate(event.detail.href);
                }}
              />
            ) : undefined
          }
          navigationOpen={navigationOpen}
          onNavigationChange={(event) => setNavigationOpen(event.detail.open)}
          breadcrumbs={
            trail.length > 0 ? (
              <BreadcrumbGroup
                ariaLabel="Breadcrumbs"
                items={trail.map((crumb) => ({ text: crumb.text, href: crumb.href ?? pathname }))}
                onFollow={(event) => {
                  event.preventDefault();
                  if (event.detail.href !== pathname) navigate(event.detail.href);
                }}
              />
            ) : undefined
          }
          notifications={<ConsoleFlashbar />}
          toolsHide={!service}
          tools={service ? <ServiceHelp service={service} /> : undefined}
          toolsOpen={toolsOpen}
          onToolsChange={(event) => setToolsOpen(event.detail.open)}
          content={
            <div id="main-content" tabIndex={-1}>
              {children}
            </div>
          }
          ariaLabels={{
            navigation: "Service navigation",
            navigationToggle: "Open navigation",
            navigationClose: "Close navigation",
            tools: "Help panel",
            toolsToggle: "Open help panel",
            toolsClose: "Close help panel",
            notifications: "Notifications",
          }}
        />
      </HelpContext.Provider>
      </ServicesMenuContext.Provider>
    </div>
  );
}

export function AwsApp({ children }: { children: ReactNode }) {
  return (
    <ErrorBoundary>
      <BrowserRouter>
        <NotificationsProvider>
          <ConsoleFrame>
            <Routes>{children}</Routes>
          </ConsoleFrame>
        </NotificationsProvider>
      </BrowserRouter>
    </ErrorBoundary>
  );
}
