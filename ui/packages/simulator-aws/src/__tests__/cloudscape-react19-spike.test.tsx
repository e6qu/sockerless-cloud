import { render, screen, fireEvent } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import Button from "@cloudscape-design/components/button";
import Table from "@cloudscape-design/components/table";
import Header from "@cloudscape-design/components/header";
import Tabs from "@cloudscape-design/components/tabs";

describe("Cloudscape under React 19", () => {
  it("renders a real Cloudscape Button and fires onClick", () => {
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Launch</Button>);
    const btn = screen.getByRole("button", { name: "Launch" });
    fireEvent.click(btn);
    expect(onClick).toHaveBeenCalled();
  });
  it("renders a real Cloudscape Table with rows", () => {
    render(
      <Table<{ id: string; state: string }>
        header={<Header>Instances</Header>}
        columnDefinitions={[
          { id: "id", header: "ID", cell: (i) => i.id },
          { id: "state", header: "State", cell: (i) => i.state },
        ]}
        items={[{ id: "i-1", state: "running" }, { id: "i-2", state: "pending" }]}
      />,
    );
    expect(screen.getByText("i-1")).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
  });
  it("renders Cloudscape Tabs and switches panels", () => {
    render(
      <Tabs
        tabs={[
          { id: "a", label: "Details", content: <div>details-panel</div> },
          { id: "b", label: "Logs", content: <div>logs-panel</div> },
        ]}
      />,
    );
    expect(screen.getByText("details-panel")).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "Logs" }));
    expect(screen.getByText("logs-panel")).toBeTruthy();
  });
});
