import { useState } from "react";
import Tabs from "@cloudscape-design/components/tabs";

/**
 * The Cloudscape "Details page with tabs" pattern, via the real `Tabs`
 * component: a persistent summary container (the caller's own key-value
 * block, rendered above this component) followed by task-oriented tabs —
 * "start with the details of the resource, followed by the most frequently
 * accessed information type" is Cloudscape's own ordering guidance
 * (cloudscape.design/patterns/resource-management/details/details-page-with-tabs/).
 */
export interface AwsTab {
  id: string;
  label: string;
  content: React.ReactNode;
}

export function AwsTabs({ tabs, ariaLabel }: { tabs: AwsTab[]; ariaLabel: string }) {
  const [activeTabId, setActiveTabId] = useState(tabs[0]?.id);
  return (
    <Tabs
      ariaLabel={ariaLabel}
      activeTabId={activeTabId}
      onChange={(event) => setActiveTabId(event.detail.activeTabId)}
      tabs={tabs.map((tab) => ({ id: tab.id, label: tab.label, content: tab.content }))}
    />
  );
}
