import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProjectProvider, useProject } from "../console/project.js";
import { NewProjectForm, ProjectPickerList } from "../console/ProjectPicker.js";
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

describe("ProjectProvider", () => {
  function Probe() {
    const { project, setProject } = useProject();
    return (
      <button type="button" data-testid="probe" onClick={() => setProject("payments-prod")}>
        {project}
      </button>
    );
  }

  it("defaults to the deployment's default project and persists a switch", () => {
    render(
      <ProjectProvider>
        <Probe />
      </ProjectProvider>,
    );
    const probe = screen.getByTestId("probe");
    expect(probe.textContent).toBe("sockerless");
    fireEvent.click(probe);
    expect(probe.textContent).toBe("payments-prod");
    expect(window.localStorage.getItem("gcp-console-project")).toBe("payments-prod");
  });

  it("restores the persisted selection on the next mount", () => {
    window.localStorage.setItem("gcp-console-project", "payments-prod");
    render(
      <ProjectProvider>
        <Probe />
      </ProjectProvider>,
    );
    expect(screen.getByTestId("probe").textContent).toBe("payments-prod");
  });
});

describe("ProjectPickerList", () => {
  it("lists each project with its ID and number, marks the current one, and selects on click", () => {
    const onSelect = vi.fn();
    render(<ProjectPickerList projects={PROJECTS} current="sockerless" filter="" onSelect={onSelect} />);
    const current = screen.getByTestId("project-row-sockerless");
    expect(current.className).toContain("gc-picker-row-current");
    expect(current.textContent).toContain("123456789012");
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
    render(<NewProjectForm creating={false} error={null} onBack={() => {}} onCreate={onCreate} />);
    fireEvent.change(screen.getByTestId("project-create-name"), { target: { value: "My New Project" } });
    expect((screen.getByTestId("project-create-id") as HTMLInputElement).value).toBe("my-new-project");
    fireEvent.click(screen.getByTestId("project-create-submit"));
    expect(onCreate).toHaveBeenCalledWith("my-new-project", "My New Project");
  });

  it("refuses an ID outside the Resource Manager contract", () => {
    const onCreate = vi.fn();
    render(<NewProjectForm creating={false} error={null} onBack={() => {}} onCreate={onCreate} />);
    fireEvent.change(screen.getByTestId("project-create-id"), { target: { value: "ab" } });
    const submit = screen.getByTestId("project-create-submit") as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
  });

  it("shows the operation progressing while the create runs and its failure loudly", () => {
    const { rerender } = render(
      <NewProjectForm creating error={null} onBack={() => {}} onCreate={() => {}} />,
    );
    expect(screen.getByTestId("project-create-progress")).toBeDefined();
    expect((screen.getByTestId("project-create-submit") as HTMLButtonElement).disabled).toBe(true);
    rerender(
      <NewProjectForm creating={false} error="Requested entity already exists" onBack={() => {}} onCreate={() => {}} />,
    );
    expect(screen.getByRole("alert").textContent).toContain("Requested entity already exists");
  });
});
