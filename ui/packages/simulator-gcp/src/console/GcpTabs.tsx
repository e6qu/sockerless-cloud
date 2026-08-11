import { useRef, useState, type KeyboardEvent, type ReactNode } from "react";

export interface GcpTab {
  id: string;
  label: string;
  content: ReactNode;
}

// GcpTabs is the console's resource-detail tab strip — Material 3 primary
// tabs, an underline indicator beneath the active label — following the
// WAI-ARIA Tabs pattern: a `tablist` of `tab` buttons with a roving tabindex,
// Left/Right/Home/End move and activate the tab (automatic activation, the
// pattern Material tabs use), and a single `tabpanel` labelled by its tab.
export function GcpTabs({ tabs, label, defaultTab }: { tabs: GcpTab[]; label: string; defaultTab?: string }) {
  const [active, setActive] = useState(defaultTab ?? tabs[0]?.id ?? "");
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  function activate(id: string, focus: boolean) {
    setActive(id);
    if (focus) tabRefs.current[id]?.focus();
  }

  function onKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    if (event.key === "ArrowRight") {
      event.preventDefault();
      activate(tabs[(index + 1) % tabs.length].id, true);
    } else if (event.key === "ArrowLeft") {
      event.preventDefault();
      activate(tabs[(index - 1 + tabs.length) % tabs.length].id, true);
    } else if (event.key === "Home") {
      event.preventDefault();
      activate(tabs[0].id, true);
    } else if (event.key === "End") {
      event.preventDefault();
      activate(tabs[tabs.length - 1].id, true);
    }
  }

  const activeTab = tabs.find((tab) => tab.id === active) ?? tabs[0];
  if (!activeTab) return null;

  return (
    <div className="gc-tabs">
      <div role="tablist" aria-label={label} className="gc-tablist">
        {tabs.map((tab, index) => (
          <button
            key={tab.id}
            ref={(node) => {
              tabRefs.current[tab.id] = node;
            }}
            type="button"
            role="tab"
            id={`gc-tab-${tab.id}`}
            aria-selected={tab.id === active}
            aria-controls={`gc-tabpanel-${tab.id}`}
            tabIndex={tab.id === active ? 0 : -1}
            data-testid={`gc-tab-${tab.id}`}
            className={tab.id === active ? "gc-tab gc-tab-active" : "gc-tab"}
            onClick={() => activate(tab.id, false)}
            onKeyDown={(event) => onKeyDown(event, index)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div
        role="tabpanel"
        id={`gc-tabpanel-${activeTab.id}`}
        aria-labelledby={`gc-tab-${activeTab.id}`}
        className="gc-tabpanel"
        tabIndex={0}
      >
        {activeTab.content}
      </div>
    </div>
  );
}
