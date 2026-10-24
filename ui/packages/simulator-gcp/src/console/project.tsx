import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { Navigate, useLocation, useNavigate } from "react-router";
import { DEFAULT_CONSOLE_PROJECT } from "../api.js";

// The selected project rides on every console URL as ?project=, the way the
// real console carries it, so a link names the project it shows. A URL
// without one takes the last project this browser used, or the deployment's
// default project, and gains the parameter. The last project and the recently
// used ones are per-browser conveniences kept in localStorage.

const STORAGE_KEY = "gcp-console-project";
const RECENT_KEY = "gcp-console-recent-projects";
const RECENT_LIMIT = 10;

function readStorage(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // A browser that refuses storage keeps the selection for this page only.
  }
}

export function recentProjects(): string[] {
  const raw = readStorage(RECENT_KEY);
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((entry): entry is string => typeof entry === "string") : [];
  } catch {
    return [];
  }
}

function remember(projectId: string) {
  writeStorage(STORAGE_KEY, projectId);
  const recent = [projectId, ...recentProjects().filter((entry) => entry !== projectId)].slice(0, RECENT_LIMIT);
  writeStorage(RECENT_KEY, JSON.stringify(recent));
}

interface ProjectContextValue {
  project: string;
  /** Switches the project, staying on the current page or moving to `path`. */
  setProject: (projectId: string, path?: string) => void;
}

const ProjectContext = createContext<ProjectContextValue | null>(null);

export function ProjectProvider({ children }: { children: ReactNode }) {
  const location = useLocation();
  const navigate = useNavigate();
  const fromUrl = new URLSearchParams(location.search).get("project");
  const [stored, setStored] = useState(() => readStorage(STORAGE_KEY) || DEFAULT_CONSOLE_PROJECT);
  const project = fromUrl || stored;

  useEffect(() => {
    if (fromUrl && fromUrl !== stored) {
      remember(fromUrl);
      setStored(fromUrl);
    }
  }, [fromUrl, stored]);


  const setProject = useCallback(
    (projectId: string, path?: string) => {
      remember(projectId);
      setStored(projectId);
      const params = new URLSearchParams(path ? "" : location.search);
      params.set("project", projectId);
      navigate({ pathname: path ?? location.pathname, search: `?${params}` });
    },
    [location.pathname, location.search, navigate],
  );

  const value = useMemo<ProjectContextValue>(() => ({ project, setProject }), [project, setProject]);
  if (!fromUrl) {
    const params = new URLSearchParams(location.search);
    params.set("project", project);
    return <Navigate replace to={{ pathname: location.pathname, search: `?${params}`, hash: location.hash }} />;
  }
  return <ProjectContext.Provider value={value}>{children}</ProjectContext.Provider>;
}

// useProject reads the selected project. Components below GcpApp always have
// the provider; rendering one outside it is a programming error.
export function useProject(): ProjectContextValue {
  const value = useContext(ProjectContext);
  if (!value) throw new Error("useProject requires a ProjectProvider ancestor");
  return value;
}

/** A console path carrying the selected project, for links and navigation. */
export function useProjectPath(): (path: string) => string {
  const { project } = useProject();
  return useCallback(
    (path: string) => {
      const [pathname, query = ""] = path.split("?");
      const params = new URLSearchParams(query);
      params.set("project", project);
      return `${pathname}?${params}`;
    },
    [project],
  );
}
