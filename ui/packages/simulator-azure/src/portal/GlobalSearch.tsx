import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Spinner } from "@fluentui/react-components";
import { SERVICES, matchesService } from "../catalog.js";
import { listResourceGroups, listSubscriptions, searchResources } from "../azure.js";
import { bladeForResource } from "../services.js";
import { AzureIcon } from "./icons.js";
import { resourceRoute } from "./routes.js";

interface Result {
  key: string;
  group: "Services" | "Resources" | "Resource Groups";
  label: string;
  detail: string;
  to: string;
}

function useDebounced(value: string, ms: number): string {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return debounced;
}

/**
 * The header's global search: services by name, and resources and resource
 * groups by name through Resources_List's substringof filter and the resource
 * group list, across every subscription the operator can reach. Keyboard: the
 * arrow keys move through results, Enter opens one, Escape closes the list,
 * and G then / focuses the box from anywhere.
 */
export function GlobalSearch() {
  const navigate = useNavigate();
  const listboxId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const needle = useDebounced(text.trim(), 250);

  // Attached before paint, so a shortcut pressed right after load lands.
  useLayoutEffect(() => {
    let armed = false;
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
      if (event.key === "g" || event.key === "G") {
        armed = true;
        return;
      }
      if (armed && event.key === "/") {
        event.preventDefault();
        inputRef.current?.focus();
      }
      armed = false;
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const subscriptions = useQuery({ queryKey: ["subscriptions"], queryFn: listSubscriptions, enabled: needle.length > 0 });
  const resources = useQuery({
    queryKey: ["search", "resources", needle, subscriptions.data?.map((sub) => sub.subscriptionId)],
    queryFn: async () => (await Promise.all((subscriptions.data ?? []).map((sub) => searchResources(sub.subscriptionId, needle)))).flat(),
    enabled: needle.length > 0 && Boolean(subscriptions.data),
  });
  const groups = useQuery({
    queryKey: ["search", "resource-groups", subscriptions.data?.map((sub) => sub.subscriptionId)],
    queryFn: async () => (await Promise.all((subscriptions.data ?? []).map((sub) => listResourceGroups(sub.subscriptionId)))).flat(),
    enabled: needle.length > 0 && Boolean(subscriptions.data),
  });

  const results = useMemo<Result[]>(() => {
    if (!text.trim()) return [];
    const services: Result[] = SERVICES.filter((service) => matchesService(service, text))
      .slice(0, 6)
      .map((service) => ({ key: `s:${service.id}`, group: "Services", label: service.label, detail: "", to: service.route }));
    const lower = needle.toLowerCase();
    const found: Result[] = needle
      ? (resources.data ?? []).map((resource) => ({
          key: `r:${resource.id}`,
          group: "Resources",
          label: resource.name,
          detail: bladeForResource(resource.type, resource.kind)?.kind ?? resource.type,
          to: resourceRoute(resource.id),
        }))
      : [];
    const rgs: Result[] = needle
      ? (groups.data ?? [])
          .filter((group) => group.name.toLowerCase().includes(lower))
          .slice(0, 6)
          .map((group) => ({ key: `g:${group.id}`, group: "Resource Groups", label: group.name, detail: "Resource group", to: resourceRoute(group.id) }))
      : [];
    return [...services, ...found, ...rgs];
  }, [text, needle, resources.data, groups.data]);

  useEffect(() => setActive(0), [results.length]);

  const go = (result: Result) => {
    setOpen(false);
    setText("");
    navigate(result.to);
  };
  const loading = needle.length > 0 && (subscriptions.isFetching || resources.isFetching || groups.isFetching);
  const showList = open && text.trim().length > 0;
  const grouped = (["Services", "Resources", "Resource Groups"] as const).map((group) => ({
    group,
    items: results.map((result, index) => ({ result, index })).filter(({ result }) => result.group === group),
  }));

  return (
    <div className="az-search" role="search">
      <div className="az-search-box">
        <AzureIcon name="search" size={16} />
        <input
          ref={inputRef}
          type="text"
          role="combobox"
          aria-label="Search resources, services, and docs"
          aria-expanded={showList}
          aria-controls={listboxId}
          aria-autocomplete="list"
          aria-activedescendant={showList && results[active] ? `${listboxId}-${active}` : undefined}
          placeholder="Search resources, services, and docs (G+/)"
          value={text}
          onChange={(event) => {
            setText(event.target.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown") {
              event.preventDefault();
              setActive((index) => Math.min(index + 1, results.length - 1));
            } else if (event.key === "ArrowUp") {
              event.preventDefault();
              setActive((index) => Math.max(index - 1, 0));
            } else if (event.key === "Enter" && results[active]) {
              event.preventDefault();
              go(results[active]);
            } else if (event.key === "Escape") {
              setOpen(false);
            }
          }}
        />
      </div>
      {showList ? (
        <div className="az-search-results">
          <div id={listboxId} role="listbox" aria-label="Search results">
            {grouped.map(({ group, items }) =>
              items.length ? (
                <div role="group" aria-label={group} key={group}>
                  <div className="az-search-group" aria-hidden>
                    {group}
                  </div>
                  {items.map(({ result, index }) => (
                    <div
                      key={result.key}
                      id={`${listboxId}-${index}`}
                      role="option"
                      aria-selected={index === active}
                      className={index === active ? "az-search-option az-search-option-active" : "az-search-option"}
                      onMouseDown={(event) => {
                        event.preventDefault();
                        go(result);
                      }}
                      onMouseEnter={() => setActive(index)}
                    >
                      <span>{result.label}</span>
                      {result.detail ? <span className="az-search-detail">{result.detail}</span> : null}
                    </div>
                  ))}
                </div>
              ) : null,
            )}
          </div>
          <div className="az-search-status" role="status">
            {loading ? (
              <>
                <Spinner size="extra-tiny" aria-labelledby={`${listboxId}-searching`} />
                <span id={`${listboxId}-searching`}>Searching resources…</span>
              </>
            ) : results.length === 0 ? (
              "No results were found."
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}
