import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { ConsoleQueryClient } from "@sockerless/ui-core/query";
import { expect } from "vitest";
import { uniqueName } from "@sockerless/ui-core/testing";
import { createIAMRole, createLambdaFunction, fetchIAMRole } from "../api.js";

// Resources the console tests arrange through the console's own API client
// against the real simulator, and the renderers they mount pages with.

/** Creates an IAM role AWS Lambda may assume, as a function's execution role must be, and returns its ARN. */
export async function createLambdaExecutionRole(): Promise<string> {
  const roleName = uniqueName("lambda-role");
  await createIAMRole({
    roleName,
    description: "",
    assumeRolePolicyDocument: JSON.stringify({
      Version: "2012-10-17",
      Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
    }),
    policyArns: [],
  });
  return (await fetchIAMRole(roleName)).arn;
}

export const IMAGE_URI = "123456789012.dkr.ecr.us-east-1.amazonaws.com/app:latest";

/** Creates a container-image AWS Lambda function and returns its name. */
export async function createImageFunction(): Promise<string> {
  const functionName = uniqueName("fn");
  await createLambdaFunction({
    functionName,
    role: await createLambdaExecutionRole(),
    memorySize: 128,
    timeout: 3,
    packageType: "Image",
    imageUri: IMAGE_URI,
  });
  return functionName;
}

/** Saves the operator's table preference for 100 rows a page on each named
 * table, so a resource this run created is on the first page however many
 * resources the run's other tests created beside it. */
export function preferHundredRowPages(...tableTitles: string[]) {
  for (const title of tableTitles) {
    window.localStorage.setItem(`aws-console:table:${title}`, JSON.stringify({ pageSize: 100 }));
  }
}

/** Waits for the button to become enabled — a page enables an action once the read it acts on has loaded — and returns it. */
export async function enabledButton(testId: string): Promise<HTMLButtonElement> {
  const button = await screen.findByTestId(testId);
  await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
  return button as HTMLButtonElement;
}

function queryClient() {
  return new ConsoleQueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
}

export function renderWithQuery(ui: React.ReactElement) {
  return render(
    <QueryClientProvider client={queryClient()}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

/** Mounts a list page beside a marker on its detail route, so a test proves a
 * navigation landed rather than that a control was merely enabled. */
export function renderWithNavigation(listPath: string, list: React.ReactElement, detailPath: string) {
  return render(
    <QueryClientProvider client={queryClient()}>
      <MemoryRouter initialEntries={[listPath]}>
        <Routes>
          <Route path={listPath} element={list} />
          <Route path={detailPath} element={<div data-testid="landed-on-detail">landed</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** Mounts a detail page with the identifier bound to the route parameter it reads. */
export function renderDetail(ui: React.ReactElement, param: string, id: string) {
  return render(
    <QueryClientProvider client={queryClient()}>
      <MemoryRouter initialEntries={[`/detail/${encodeURIComponent(id)}`]}>
        <Routes>
          <Route path={`/detail/:${param}`} element={ui} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
