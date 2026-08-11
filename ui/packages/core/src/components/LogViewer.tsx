export interface LogViewerProps {
  lines: string[];
  maxHeight?: string;
}

/** Map common ANSI SGR codes to CSS colors. */
const ansiColors: Record<string, string> = {
  "30": "#000", "31": "#c00", "32": "#0a0", "33": "#a50",
  "34": "#00c", "35": "#c0c", "36": "#0cc", "37": "#ccc",
  "90": "#555", "91": "#f55", "92": "#5f5", "93": "#ff5",
  "94": "#55f", "95": "#f5f", "96": "#5ff", "97": "#fff",
};

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function ansiToHtml(text: string): string {
  const escaped = escapeHtml(text);
  let open = false;
  const result = escaped.replace(
    /\x1b\[([0-9;]*)m/g,
    (_, codes: string) => {
      const parts = codes.split(";");
      const styles: string[] = [];
      let hasReset = false;
      for (const code of parts) {
        if (code === "0" || code === "") {
          hasReset = true;
          continue;
        }
        if (code === "1") styles.push("font-weight:bold");
        const color = ansiColors[code];
        if (color) styles.push(`color:${color}`);
      }
      let out = "";
      if (open && (hasReset || styles.length > 0)) {
        out += "</span>";
        open = false;
      }
      if (styles.length > 0) {
        out += `<span style="${styles.join(";")}">`;
        open = true;
      }
      return out;
    },
  );
  return open ? result + "</span>" : result;
}

export function LogViewer({ lines, maxHeight = "24rem" }: LogViewerProps) {
  if (lines.length === 0) {
    return (
      <div
        className="px-4 py-6 font-mono uppercase tracking-[0.2em] text-center"
        style={{
          background: "var(--color-bg-subtle)",
          border: "1px solid var(--color-border)",
          borderRadius: "var(--radius-sm)",
          color: "var(--color-fg-subtle)",
          fontSize: "0.7rem",
        }}
      >
        — no log output —
      </div>
    );
  }

  return (
    <div
      className="overflow-auto"
      style={{
        maxHeight,
        background: "oklch(0.13 0.01 60)",
        border: "1px solid var(--color-border)",
        borderRadius: "var(--radius-sm)",
      }}
    >
      <pre
        className="font-mono"
        style={{
          padding: "0.85rem 1rem",
          fontSize: "0.72rem",
          lineHeight: 1.55,
          color: "oklch(0.92 0.005 80)",
          margin: 0,
        }}
      >
        {lines.map((line, i) => (
          <div key={i} className="flex">
            <span
              className="mr-4 inline-block select-none text-right"
              style={{
                width: "2.5rem",
                color: "oklch(0.45 0.005 60)",
                borderRight: "1px solid oklch(0.25 0.01 60)",
                paddingRight: "0.5rem",
                marginRight: "0.6rem",
              }}
            >
              {String(i + 1).padStart(3, " ")}
            </span>
            <span dangerouslySetInnerHTML={{ __html: ansiToHtml(line) }} />
          </div>
        ))}
      </pre>
    </div>
  );
}
