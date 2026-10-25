import { useState } from "react";
import { Link as RouterLink } from "react-router";
import { Tab, TabList, Table, TableBody, TableCell, TableHeader, TableHeaderCell, TableRow, Text } from "@fluentui/react-components";
import { HOME_SERVICES, SERVICE_BY_ID, SERVICES } from "../catalog.js";
import { bladeForResource } from "../services.js";
import { usePageTitle } from "../portal/AzurePortal.js";
import { AzureIcon, ServiceTile } from "../portal/icons.js";
import { usePreferences } from "../portal/preferences.js";
import { resourceRoute } from "../portal/routes.js";

function timeAgo(time: number): string {
  const minutes = Math.round((Date.now() - time) / 60_000);
  if (minutes < 1) return "Just now";
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

/** Home: the Azure services row, recently viewed and favorite resources, and
 *  the Navigate links, as the portal lays out its start page. */
export function HomePage() {
  usePageTitle("Home");
  const { recentResources, favorites } = usePreferences();
  const [tab, setTab] = useState<"recent" | "favorite">("recent");
  const favoriteServices = favorites.map((id) => SERVICE_BY_ID.get(id)).filter((service) => service !== undefined);
  return (
    <div className="az-home">
      <h1 className="sr-only">Home</h1>
      <section aria-labelledby="home-services" className="az-home-section">
        <Text as="h2" id="home-services" size={500} weight="semibold" block>
          Azure services
        </Text>
        <ul className="az-home-services">
          <li>
            <RouterLink to="/ui/create" className="az-home-service">
              <span className="az-home-create" aria-hidden>
                <AzureIcon name="add" size={28} />
              </span>
              <span>Create a resource</span>
            </RouterLink>
          </li>
          {HOME_SERVICES.map((id) => SERVICE_BY_ID.get(id))
            .filter((service) => service !== undefined)
            .map((service) => (
              <li key={service.id}>
                <RouterLink to={service.route} className="az-home-service">
                  <ServiceTile icon={service.icon} color={service.color} size={48} />
                  <span>{service.label}</span>
                </RouterLink>
              </li>
            ))}
          <li>
            <RouterLink to="/ui/allservices" className="az-home-service">
              <span className="az-home-more" aria-hidden>
                <AzureIcon name="chevron_right" size={28} />
              </span>
              <span>More services</span>
            </RouterLink>
          </li>
        </ul>
      </section>

      <section aria-labelledby="home-resources" className="az-home-section">
        <Text as="h2" id="home-resources" size={500} weight="semibold" block>
          Resources
        </Text>
        <TabList selectedValue={tab} onTabSelect={(_, data) => setTab(data.value as "recent" | "favorite")} aria-label="Resources">
          <Tab value="recent">Recent</Tab>
          <Tab value="favorite">Favorite</Tab>
        </TabList>
        {tab === "recent" ? (
          recentResources.length === 0 ? (
            <div className="az-home-empty">
              <Text block weight="semibold">
                No resources have been viewed recently
              </Text>
              <RouterLink to="/ui/browse/all">View all resources</RouterLink>
            </div>
          ) : (
            <Table aria-label="Recent resources" size="small" className="az-grid" data-testid="home-recent">
              <TableHeader>
                <TableRow>
                  <TableHeaderCell>Name</TableHeaderCell>
                  <TableHeaderCell>Type</TableHeaderCell>
                  <TableHeaderCell>Last Viewed</TableHeaderCell>
                </TableRow>
              </TableHeader>
              <TableBody>
                {recentResources.slice(0, 10).map((resource) => (
                  <TableRow key={resource.id}>
                    <TableCell>
                      <RouterLink to={resourceRoute(resource.id)}>{resource.name}</RouterLink>
                    </TableCell>
                    <TableCell>{bladeForResource(resource.type)?.kind ?? resource.type}</TableCell>
                    <TableCell>{timeAgo(resource.viewedAt)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )
        ) : (
          <ul className="az-home-favorites" aria-label="Favorite services">
            {favoriteServices.map((service) => (
              <li key={service.id}>
                <RouterLink to={service.route}>
                  <ServiceTile icon={service.icon} color={service.color} size={20} />
                  {service.label}
                </RouterLink>
              </li>
            ))}
          </ul>
        )}
        <RouterLink to="/ui/browse/all" className="az-home-see-all">
          See all
        </RouterLink>
      </section>

      <section aria-labelledby="home-navigate" className="az-home-section">
        <Text as="h2" id="home-navigate" size={500} weight="semibold" block>
          Navigate
        </Text>
        <ul className="az-home-navigate">
          {["subscriptions", "resource-groups", "all-resources", "monitor"].map((id) => {
            const service = SERVICES.find((entry) => entry.id === id)!;
            return (
              <li key={id}>
                <RouterLink to={service.route}>
                  <ServiceTile icon={service.icon} color={service.color} size={24} />
                  {service.label}
                </RouterLink>
              </li>
            );
          })}
        </ul>
      </section>
    </div>
  );
}
