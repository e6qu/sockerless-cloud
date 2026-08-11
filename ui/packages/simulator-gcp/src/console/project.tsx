import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { DEFAULT_CONSOLE_PROJECT } from "../api.js";

// The selected project is SPA state, the way the real console treats it: the
// header's project picker chip names it, the picker dialog switches it, and
// every page's queries read it. The choice persists across reloads in
// localStorage and defaults to the deployment's default project.

const STORAGE_KEY = "gcp-console-project";

interface ProjectContextValue {
  project: string;
  setProject: (projectId: string) => void;
}

const ProjectContext = createContext<ProjectContextValue | null>(null);

export function ProjectProvider({ children }: { children: ReactNode }) {
  const [project, setProjectState] = useState(
    () => window.localStorage.getItem(STORAGE_KEY) || DEFAULT_CONSOLE_PROJECT,
  );
  const value = useMemo<ProjectContextValue>(
    () => ({
      project,
      setProject: (projectId: string) => {
        window.localStorage.setItem(STORAGE_KEY, projectId);
        setProjectState(projectId);
      },
    }),
    [project],
  );
  return <ProjectContext.Provider value={value}>{children}</ProjectContext.Provider>;
}

// useProject reads the selected project. Components below GcpApp always have
// the provider; rendering one outside it is a programming error, surfaced
// loudly rather than silently defaulting.
export function useProject(): ProjectContextValue {
  const value = useContext(ProjectContext);
  if (!value) throw new Error("useProject requires a ProjectProvider ancestor");
  return value;
}
