import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { Icon } from "./icons.js";
import { searchCatalog } from "./catalog.js";
import { useProjectPath } from "./project.js";
import { useDismiss } from "./menu.js";

const RESULT_LIMIT = 8;

/** The top bar's search over products and their pages, focused with "/" as in the real console. */
export function ConsoleSearch() {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const wrapper = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const path = useProjectPath();
  const listId = useId();
  const results = useMemo(() => searchCatalog(query).slice(0, RESULT_LIMIT), [query]);
  useDismiss(wrapper, open, () => setOpen(false));

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.tagName === "SELECT" || target.isContentEditable)) {
        return;
      }
      event.preventDefault();
      input.current?.focus();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  function go(index: number) {
    const entry = results[index];
    if (!entry) return;
    navigate(path(entry.to));
    setQuery("");
    setOpen(false);
    input.current?.blur();
  }

  const expanded = open && query.trim() !== "";
  return (
    <div className="gc-search" ref={wrapper}>
      <Icon name="search" size="1.25em" />
      <input
        ref={input}
        type="text"
        role="combobox"
        aria-label="Search"
        aria-expanded={expanded}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={expanded && results[active] ? `${listId}-${active}` : undefined}
        placeholder="Search (/) for resources, docs, products, and more"
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          setActive(0);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown") {
            event.preventDefault();
            setActive((value) => Math.min(value + 1, results.length - 1));
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            setActive((value) => Math.max(value - 1, 0));
          } else if (event.key === "Enter") {
            event.preventDefault();
            go(active);
          } else if (event.key === "Escape") {
            setOpen(false);
          }
        }}
      />
      {expanded ? (
        <div className="gc-search-results">
          <p className="gc-search-heading" id={`${listId}-heading`}>Products &amp; pages</p>
          <ul role="listbox" id={listId} aria-labelledby={`${listId}-heading`}>
            {results.length === 0 ? (
              <li className="gc-search-empty" role="option" aria-selected={false} aria-disabled>
                No products or pages match “{query}”
              </li>
            ) : (
              results.map((entry, index) => (
                <li
                  key={`${entry.to}-${entry.label}`}
                  id={`${listId}-${index}`}
                  role="option"
                  aria-selected={index === active}
                  className={index === active ? "gc-search-option gc-search-option-active" : "gc-search-option"}
                  onMouseDown={(event) => event.preventDefault()}
                  onMouseEnter={() => setActive(index)}
                  onClick={() => go(index)}
                >
                  <Icon name={entry.icon} size="1.25em" />
                  <span className="gc-search-label">{entry.label}</span>
                  <span className="gc-search-detail">{entry.detail}</span>
                </li>
              ))
            )}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
