import { describe, it, expect, afterEach, vi } from "vitest";
import { render, fireEvent, cleanup } from "@testing-library/react";
import { createColumnHelper } from "@tanstack/react-table";
import { DataTable } from "../components/DataTable.js";

afterEach(cleanup);

interface Row {
  name: string;
  age: number;
}

const col = createColumnHelper<Row>();

const columns = [
  col.accessor("name", { header: "Name" }),
  col.accessor("age", { header: "Age" }),
];

const data: Row[] = [
  { name: "Alice", age: 30 },
  { name: "Bob", age: 25 },
  { name: "Charlie", age: 35 },
];

describe("DataTable", () => {
  it("renders rows from data", () => {
    const { container } = render(<DataTable data={data} columns={columns} />);

    const rows = container.querySelectorAll("tbody tr");
    expect(rows).toHaveLength(3);

    const names = Array.from(rows).map(
      (row) => row.querySelectorAll("td")[0].textContent,
    );
    expect(names).toContain("Alice");
    expect(names).toContain("Bob");
    expect(names).toContain("Charlie");
  });

  it("sorts by column header click", () => {
    const { container } = render(<DataTable data={data} columns={columns} />);

    const headers = container.querySelectorAll("th");
    // Sort is on a child <button> for keyboard activation; the <th>
    // wrapper carries aria-sort.
    const ageSortButton = headers[1].querySelector("button")!;

    fireEvent.click(ageSortButton);
    let rows = container.querySelectorAll("tbody tr");
    let ageValues = Array.from(rows).map(
      (row) => row.querySelectorAll("td")[1].textContent,
    );
    expect(ageValues).toEqual(["35", "30", "25"]);

    fireEvent.click(ageSortButton);
    rows = container.querySelectorAll("tbody tr");
    ageValues = Array.from(rows).map(
      (row) => row.querySelectorAll("td")[1].textContent,
    );
    expect(ageValues).toEqual(["25", "30", "35"]);
  });

  it("filters with global search input", () => {
    const { container } = render(
      <DataTable data={data} columns={columns} filterPlaceholder="Search..." />,
    );

    const input = container.querySelector("input")!;
    fireEvent.change(input, { target: { value: "Ali" } });

    const rows = container.querySelectorAll("tbody tr");
    expect(rows).toHaveLength(1);
    expect(rows[0].querySelectorAll("td")[0].textContent).toBe("Alice");
  });

  it("has accessible label on filter input", () => {
    const { container } = render(<DataTable data={data} columns={columns} />);
    const input = container.querySelector("input");
    expect(input?.getAttribute("aria-label")).toBe("Filter table");
  });

  it("calls onRowClick with row data when a row is clicked", () => {
    const onClick = vi.fn();
    const { container } = render(
      <DataTable data={data} columns={columns} onRowClick={onClick} />,
    );

    const rows = container.querySelectorAll("tbody tr");
    fireEvent.click(rows[0]);
    expect(onClick).toHaveBeenCalledTimes(1);
    expect(onClick).toHaveBeenCalledWith(expect.objectContaining({ name: expect.any(String) }));

    // Cursor moved to inline style in the redesign so onRowClick rows
    // get cursor: pointer regardless of class composition.
    expect((rows[0] as HTMLElement).style.cursor).toBe("pointer");
  });
});
