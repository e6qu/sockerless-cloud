import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, cleanup, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ColumnDef } from "@tanstack/react-table";
import { ResourceListPage } from "../components/ResourceListPage.js";

interface Row {
  name: string;
  size: number;
}

const columns: ColumnDef<Row, unknown>[] = [
  { accessorKey: "name", header: "Name" },
  { accessorKey: "size", header: "Size" },
];

afterEach(() => cleanup());

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
  );
}

describe("ResourceListPage", () => {
  it("renders heading + rows on success", async () => {
    const fetchFn = vi.fn(async (): Promise<Row[]> => [
      { name: "alpha", size: 1 },
      { name: "beta", size: 2 },
    ]);
    renderWithClient(
      <ResourceListPage<Row>
        kicker="aws · simulator · s3"
        title={<>Buckets</>}
        countNoun="bucket"
        columns={columns}
        queryKey={["t1"]}
        queryFn={fetchFn}
        refetchInterval={false}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText("Buckets")).toBeInTheDocument();
      expect(screen.getByText("alpha")).toBeInTheDocument();
      expect(screen.getByText("beta")).toBeInTheDocument();
    });
    // 2 rows → "2 buckets"
    expect(screen.getByText(/2 buckets/)).toBeInTheDocument();
  });

  it("singular meta when count is 1", async () => {
    const fetchFn = vi.fn(async (): Promise<Row[]> => [
      { name: "only", size: 9 },
    ]);
    renderWithClient(
      <ResourceListPage<Row>
        title={<>Things</>}
        countNoun="thing"
        columns={columns}
        queryKey={["t2"]}
        queryFn={fetchFn}
        refetchInterval={false}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText("1 thing")).toBeInTheDocument();
    });
  });

  it("renders an English plural for nouns ending in y", async () => {
    const fetchFn = vi.fn(async (): Promise<Row[]> => [
      { name: "one", size: 1 },
      { name: "two", size: 2 },
    ]);
    renderWithClient(
      <ResourceListPage<Row>
        title={<>Repositories</>}
        countNoun="repository"
        columns={columns}
        queryKey={["plural-y"]}
        queryFn={fetchFn}
        refetchInterval={false}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText("2 repositories")).toBeInTheDocument();
    });
  });

  it("renders InlineError + retry on failure", async () => {
    let calls = 0;
    const fetchFn = vi.fn(async (): Promise<Row[]> => {
      calls++;
      if (calls === 1) throw new Error("boom");
      return [{ name: "after-retry", size: 0 }];
    });
    renderWithClient(
      <ResourceListPage<Row>
        title={<>Things</>}
        columns={columns}
        queryKey={["t3"]}
        queryFn={fetchFn}
        refetchInterval={false}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText("Failed to load")).toBeInTheDocument();
    });
    expect(screen.getByText(/boom/)).toBeInTheDocument();

    const retry = screen.getByRole("button", { name: /retry/i });
    fireEvent.click(retry);

    await waitFor(() => {
      expect(screen.getByText("after-retry")).toBeInTheDocument();
    });
  });

  it("honours a custom meta override", async () => {
    const fetchFn = vi.fn(async (): Promise<Row[]> => [{ name: "x", size: 1 }]);
    renderWithClient(
      <ResourceListPage<Row>
        title={<>X</>}
        meta={<span>region us-east-1</span>}
        columns={columns}
        queryKey={["t4"]}
        queryFn={fetchFn}
        refetchInterval={false}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText(/region us-east-1/)).toBeInTheDocument();
    });
    // Default count meta should not appear when meta is overridden.
    expect(screen.queryByText(/1 row/)).not.toBeInTheDocument();
  });

  it("shows actions slot in the heading", async () => {
    const fetchFn = vi.fn(async (): Promise<Row[]> => []);
    renderWithClient(
      <ResourceListPage<Row>
        title={<>Things</>}
        columns={columns}
        queryKey={["t5"]}
        queryFn={fetchFn}
        refetchInterval={false}
        actions={<button type="button">refresh</button>}
      />,
    );
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: /refresh/i }),
      ).toBeInTheDocument();
    });
  });
});
