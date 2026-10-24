import { useEffect, useRef, useState, type RefObject } from "react";
import { Link } from "react-router";
import { Icon } from "./icons.js";
import { CATEGORIES, PRODUCTS, findProduct, locate, productHome, type ConsoleProduct } from "./catalog.js";
import { usePinnedProducts } from "./pins.js";
import { useProjectPath } from "./project.js";

export function PinButton({ product }: { product: ConsoleProduct }) {
  const { pinned, toggle } = usePinnedProducts();
  const isPinned = pinned.includes(product.id);
  const label = isPinned ? `Unpin ${product.name}` : `Pin ${product.name}`;
  return (
    <button
      type="button"
      className={isPinned ? "gc-pin gc-pin-on" : "gc-pin"}
      aria-label={label}
      aria-pressed={isPinned}
      title={label}
      onClick={() => toggle(product.id)}
    >
      <Icon name={isPinned ? "keep" : "keep_off"} size="1.15em" />
    </button>
  );
}

function ProductRow({ product, current, onNavigate }: { product: ConsoleProduct; current: boolean; onNavigate: () => void }) {
  const path = useProjectPath();
  return (
    <li className="gc-drawer-row">
      <Link
        to={path(productHome(product))}
        className={current ? "gc-drawer-link gc-drawer-link-active" : "gc-drawer-link"}
        aria-current={current ? "page" : undefined}
        onClick={onNavigate}
      >
        <Icon name={product.icon} size="1.25em" />
        <span>{product.name}</span>
      </Link>
      <PinButton product={product} />
    </li>
  );
}

// The navigation menu the top bar's ☰ opens, as in the real console: Cloud
// overview, the pinned products, and every product by category under More
// products, each with its pin.
export function GcpNavDrawer({
  open,
  onClose,
  triggerRef,
  pathname,
}: {
  open: boolean;
  onClose: () => void;
  triggerRef: RefObject<HTMLButtonElement | null>;
  pathname: string;
}) {
  const closeRef = useRef<HTMLButtonElement>(null);
  const { pinned } = usePinnedProducts();
  const [moreOpen, setMoreOpen] = useState(false);
  const path = useProjectPath();
  const currentProduct = locate(pathname).product?.id;

  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const trigger = triggerRef.current;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      trigger?.focus();
    };
    // onClose and triggerRef keep their identity across renders; re-running on
    // them would pull focus back to the close button while the operator tabs.
  }, [open]);

  if (!open) return null;
  const pinnedProducts = pinned.map(findProduct).filter((product): product is ConsoleProduct => Boolean(product));

  return (
    <div
      className="gc-drawer-overlay"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="gc-drawer" role="dialog" aria-modal="true" aria-label="Navigation menu" data-testid="nav-drawer">
        <div className="gc-drawer-header">
          <button ref={closeRef} type="button" className="gc-icon-button" aria-label="Close navigation menu" onClick={onClose}>
            <Icon name="menu" />
          </button>
          <span className="gc-wordmark">Google Cloud</span>
        </div>
        <nav aria-label="Navigation menu">
          <h2 className="gc-drawer-group-label" id="drawer-overview">Cloud overview</h2>
          <ul aria-labelledby="drawer-overview">
            {[
              { label: "Welcome", to: "/ui/welcome", icon: "home" as const },
              { label: "Dashboard", to: "/ui/home/dashboard", icon: "dashboard" as const },
              { label: "Activity", to: "/ui/home/activity", icon: "history" as const },
            ].map((entry) => (
              <li key={entry.to} className="gc-drawer-row">
                <Link
                  to={path(entry.to)}
                  className={pathname === entry.to ? "gc-drawer-link gc-drawer-link-active" : "gc-drawer-link"}
                  aria-current={pathname === entry.to ? "page" : undefined}
                  onClick={onClose}
                >
                  <Icon name={entry.icon} size="1.25em" />
                  <span>{entry.label}</span>
                </Link>
              </li>
            ))}
          </ul>
          <h2 className="gc-drawer-group-label" id="drawer-pinned">Pinned products</h2>
          {pinnedProducts.length === 0 ? (
            <p className="gc-drawer-note">Pin a product to keep it here.</p>
          ) : (
            <ul aria-labelledby="drawer-pinned" data-testid="drawer-pinned">
              {pinnedProducts.map((product) => (
                <ProductRow key={product.id} product={product} current={product.id === currentProduct} onNavigate={onClose} />
              ))}
            </ul>
          )}
          <button
            type="button"
            className="gc-drawer-more"
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((value) => !value)}
          >
            <Icon name={moreOpen ? "expand_more" : "chevron_right"} size="1.25em" />
            More products
          </button>
          {moreOpen
            ? CATEGORIES.map((category) => {
                const products = PRODUCTS.filter((product) => product.category === category);
                if (products.length === 0) return null;
                const id = `drawer-category-${category.replace(/[^a-z]/gi, "")}`;
                return (
                  <div key={category} className="gc-drawer-group">
                    <h3 className="gc-drawer-group-label" id={id}>{category}</h3>
                    <ul aria-labelledby={id}>
                      {products.map((product) => (
                        <ProductRow key={product.id} product={product} current={product.id === currentProduct} onNavigate={onClose} />
                      ))}
                    </ul>
                  </div>
                );
              })
            : null}
          <Link className="gc-drawer-all" to={path("/ui/products")} onClick={onClose}>
            View all products
          </Link>
        </nav>
      </div>
    </div>
  );
}
