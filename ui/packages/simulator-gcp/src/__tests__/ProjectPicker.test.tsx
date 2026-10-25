import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, useLocation } from "react-router";
import { ProjectProvider, recentProjects, useProject } from "../console/project.js";
import { ProjectPickerList } from "../console/ProjectPicker.js";
import { NewProjectForm, deriveProjectId } from "../pages/CreateProjectPage.js";
import type { CrmProject } from "../api.js";

// Without vitest globals, Testing Library's automatic between-test cleanup
// never registers; unmount explicitly so screen queries see one render.
afterEach(() => {
  cleanup();
  window.localStorage.clear();
});

const PROJECTS: CrmProject[] = [
  { name: "projects/123456789012", projectId: "sockerless", displayName: "sockerless", state: "ACTIVE" },
  { name: "projects/584739201746", projectId: "payments-prod", displayName: "Payments", state: "ACTIVE" },
];

function Probe() {
  const { project, setProject } = useProject();
  const location = useLocation();
  return (
    <>
      <button type="button" data-testid="probe" onClick={() => setProject("payments-prod")}>
        {project}
      </button>
      <span data-testid="location">{`${location.pathname}${location.search}`}</span>
    </>
  );
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ProjectProvider>
        <Probe />
      </ProjectProvider>
    </MemoryRouter>,
  );
}

describe("ProjectProvider", () => {
  it("puts the deployment's default project on a URL that names none", () => {
    renderAt("/ui/storage/browser");
    expect(screen.getByTestId("probe").textContent).toBe("sockerless");
    expect(screen.getByTestId("location").textContent).toBe("/ui/storage/browser?project=sockerless");
  });

  it("reads the project the URL names and remembers it as recent", () => {
    renderAt("/ui/storage/browser?project=payments-prod");
    expect(screen.getByTestId("probe").textContent).toBe("payments-prod");
    expect(recentProjects()).toEqual(["payments-prod"]);
  });

  it("switches the project on the URL and keeps the page", () => {
    renderAt("/ui/run?project=sockerless");
    fireEvent.click(screen.getByTestId("probe"));
    expect(screen.getByTestId("location").textContent).toBe("/ui/run?project=payments-prod");
    expect(window.localStorage.getItem("gcp-console-project")).toBe("payments-prod");
  });
});

describe("ProjectPickerList", () => {
  it("lists each project with its ID and number, marks the current one, and selects on click", () => {
    const onSelect = vi.fn();
    render(<ProjectPickerList projects={PROJECTS} current="sockerless" filter="" onSelect={onSelect} />);
    const current = screen.getByTestId("project-row-sockerless");
    expect(current.getAttribute("aria-current")).toBe("true");
    expect(screen.getByText("123456789012")).toBeDefined();
    fireEvent.click(screen.getByTestId("project-row-payments-prod"));
    expect(onSelect).toHaveBeenCalledWith("payments-prod");
  });

  it("filters by project ID and display name and explains an empty match", () => {
    const { rerender } = render(
      <ProjectPickerList projects={PROJECTS} current="sockerless" filter="payments" onSelect={() => {}} />,
    );
    expect(screen.queryByTestId("project-row-sockerless")).toBeNull();
    expect(screen.getByTestId("project-row-payments-prod")).toBeDefined();
    rerender(<ProjectPickerList projects={PROJECTS} current="sockerless" filter="no-such" onSelect={() => {}} />);
    expect(screen.getByText("No projects match.")).toBeDefined();
  });
});

describe("NewProjectForm", () => {
  it("derives the project ID from the typed name and submits both", () => {
    const onCreate = vi.fn();
    render(<NewProjectForm creating={false} error={null} onCancel={() => {}} onCreate={onCreate} />);
    fireEvent.change(screen.getByTestId("project-create-name"), { target: { value: "My New Project" } });
    expect((screen.getByTestId("project-create-id") as HTMLInputElement).value).toBe("my-new-project");
    fireEvent.click(screen.getByTestId("project-create-submit"));
    expect(onCreate).toHaveBeenCalledWith("my-new-project", "My New Project");
  });

  it("refuses an ID outside the Resource Manager contract", () => {
    render(<NewProjectForm creating={false} error={null} onCancel={() => {}} onCreate={() => {}} />);
    fireEvent.change(screen.getByTestId("project-create-id"), { target: { value: "ab" } });
    expect((screen.getByTestId("project-create-submit") as HTMLButtonElement).disabled).toBe(true);
  });

  it("reports the API's failure", () => {
    render(<NewProjectForm creating={false} error="Requested entity already exists" onCancel={() => {}} onCreate={() => {}} />);
    expect(screen.getByRole("alert").textContent).toContain("Requested entity already exists");
  });

  it("derives IDs the way the real page does", () => {
    expect(deriveProjectId("  Payments  Prod!! ")).toBe("payments-prod");
  });
});
