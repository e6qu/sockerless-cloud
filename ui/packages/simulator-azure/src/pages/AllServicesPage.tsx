import { useState } from "react";
import { Link as RouterLink, useSearchParams } from "react-router";
import { Button, Input, Text, Tooltip } from "@fluentui/react-components";
import { CATEGORIES, SERVICES, SERVICE_BY_ID, matchesService, type PortalService } from "../catalog.js";
import { Blade } from "../portal/AzurePortal.js";
import { AzureIcon, ServiceTile } from "../portal/icons.js";
import { toggleFavorite, usePreferences } from "../portal/preferences.js";

function ServiceCard({ service, favorite }: { service: PortalService; favorite: boolean }) {
  return (
    <li className="az-service-card">
      <RouterLink to={service.route} className="az-service-card-link">
        <ServiceTile icon={service.icon} color={service.color} size={32} />
        <span className="az-service-card-text">
          <span className="az-service-card-name">{service.label}</span>
          <span className="az-service-card-description">{service.description}</span>
        </span>
      </RouterLink>
      <Tooltip content={favorite ? `Remove ${service.label} from favorites` : `Add ${service.label} to favorites`} relationship="label">
        <Button
          appearance="transparent"
          size="small"
          aria-pressed={favorite}
          icon={<AzureIcon name={favorite ? "star_filled" : "star"} />}
          onClick={() => toggleFavorite(service.id)}
        />
      </Tooltip>
    </li>
  );
}

/** All services: every service the console serves, by category, with the
 *  Favorites and Recently used views, a filter, and a star on each. */
export function AllServicesPage() {
  const [params, setParams] = useSearchParams();
  const view = params.get("category") ?? "All";
  const [filter, setFilter] = useState("");
  const { favorites, recentServices } = usePreferences();
  const views = ["All", "Favorites", "Recently used", ...CATEGORIES];
  let services: PortalService[];
  if (view === "Favorites") services = favorites.map((id) => SERVICE_BY_ID.get(id)).filter((service) => service !== undefined);
  else if (view === "Recently used") services = recentServices.map((id) => SERVICE_BY_ID.get(id)).filter((service) => service !== undefined);
  else if (view === "All") services = SERVICES;
  else services = SERVICES.filter((service) => service.category === view);
  services = services.filter((service) => matchesService(service, filter));
  const byCategory = CATEGORIES.map((category) => ({ category, items: services.filter((service) => service.category === category) })).filter(
    (group) => group.items.length > 0,
  );
  return (
    <Blade title="All services" trail={[{ label: "Home", to: "/ui/home" }, { label: "All services" }]}>
      <div className="az-allservices">
        <nav aria-label="Service categories" className="az-allservices-nav">
          <ul>
            {views.map((entry) => (
              <li key={entry}>
                <button
                  type="button"
                  className={entry === view ? "az-menu-link az-menu-link-active" : "az-menu-link"}
                  aria-current={entry === view ? "page" : undefined}
                  onClick={() => setParams(entry === "All" ? {} : { category: entry })}
                >
                  {entry}
                </button>
              </li>
            ))}
          </ul>
        </nav>
        <div className="az-allservices-main">
          <Input
            className="az-filter-text"
            value={filter}
            onChange={(_, data) => setFilter(data.value)}
            placeholder="Filter services"
            aria-label="Filter services"
            contentBefore={<AzureIcon name="search" size={14} />}
          />
          {services.length === 0 ? (
            <Text block className="az-pane-empty">
              {view === "Recently used" ? "No services have been used recently." : "No services match."}
            </Text>
          ) : view === "Favorites" || view === "Recently used" ? (
            <ul className="az-service-cards">
              {services.map((service) => (
                <ServiceCard key={service.id} service={service} favorite={favorites.includes(service.id)} />
              ))}
            </ul>
          ) : (
            byCategory.map((group) => (
              <section key={group.category} aria-labelledby={`category-${group.category}`}>
                <Text as="h2" size={400} weight="semibold" block id={`category-${group.category}`} className="az-allservices-heading">
                  {group.category}
                </Text>
                <ul className="az-service-cards">
                  {group.items.map((service) => (
                    <ServiceCard key={service.id} service={service} favorite={favorites.includes(service.id)} />
                  ))}
                </ul>
              </section>
            ))
          )}
        </div>
      </div>
    </Blade>
  );
}
