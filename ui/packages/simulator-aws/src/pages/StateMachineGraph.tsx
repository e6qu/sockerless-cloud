import { useMemo } from "react";

interface AslState {
  Type?: string;
  Next?: string;
  End?: boolean;
  Default?: string;
  Choices?: { Next?: string }[];
}

interface AslDefinition {
  StartAt?: string;
  States?: Record<string, AslState>;
}

export function StateMachineGraph({
  definition,
  activeStates = [],
}: {
  definition: string;
  activeStates?: string[];
}) {
  const parsed = useMemo(() => {
    try {
      return JSON.parse(definition) as AslDefinition;
    } catch {
      return null;
    }
  }, [definition]);

  if (!parsed?.States || !parsed.StartAt) {
    return <div className="aws-sfn-graph-error">The definition is not valid Amazon States Language JSON.</div>;
  }

  const ordered: string[] = [];
  const visited = new Set<string>();
  const visit = (name: string) => {
    if (visited.has(name) || !parsed.States?.[name]) return;
    visited.add(name);
    ordered.push(name);
    const state = parsed.States[name];
    if (state.Next) visit(state.Next);
    for (const choice of state.Choices ?? []) if (choice.Next) visit(choice.Next);
    if (state.Default) visit(state.Default);
  };
  visit(parsed.StartAt);
  for (const name of Object.keys(parsed.States)) visit(name);

  return (
    <div className="aws-sfn-workflow-canvas" aria-label="Workflow graph">
      <div className="aws-sfn-graph-start">Start</div>
      {ordered.map((name, index) => {
        const state = parsed.States![name];
        const next = [
          ...(state.Choices ?? []).flatMap((choice) => (choice.Next ? [choice.Next] : [])),
          ...(state.Default ? [state.Default] : []),
          ...(state.Next ? [state.Next] : []),
        ];
        return (
          <div className="aws-sfn-graph-step" key={name}>
            <div className="aws-sfn-graph-arrow" aria-hidden="true">↓</div>
            <div
              className={`aws-sfn-state-card aws-sfn-state-${(state.Type ?? "unknown").toLowerCase()} ${
                activeStates.includes(name) ? "aws-sfn-state-active" : ""
              }`}
            >
              <span className="aws-sfn-state-icon">{stateIcon(state.Type)}</span>
              <span>
                <strong>{name}</strong>
                <small>{state.Type ?? "Unknown"}</small>
              </span>
            </div>
            {next.length > 1 && (
              <div className="aws-sfn-branches">
                {next.map((target) => (
                  <span key={target}>{target}</span>
                ))}
              </div>
            )}
            {index === ordered.length - 1 && (state.End || state.Type === "Succeed") && (
              <>
                <div className="aws-sfn-graph-arrow" aria-hidden="true">↓</div>
                <div className="aws-sfn-graph-end">End</div>
              </>
            )}
          </div>
        );
      })}
    </div>
  );
}

function stateIcon(type = ""): string {
  switch (type) {
    case "Task": return "λ";
    case "Choice": return "◇";
    case "Parallel": return "⑂";
    case "Map": return "↻";
    case "Wait": return "◷";
    case "Succeed": return "✓";
    case "Fail": return "!";
    default: return "→";
  }
}
