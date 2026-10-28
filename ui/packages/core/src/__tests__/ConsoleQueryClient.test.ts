import { describe, expect, it } from "vitest";
import { QueryObserver } from "@tanstack/react-query";
import { ConsoleQueryClient } from "../query/index.js";

describe("ConsoleQueryClient", () => {
  it("answers an invalidation during a list's first load with a fetch that starts after the write", async () => {
    const client = new ConsoleQueryClient({ defaultOptions: { queries: { retry: false } } });
    const store: string[] = [];
    let finishFirstLoad = () => {};
    const firstLoad = new Promise<void>((resolve) => (finishFirstLoad = resolve));
    let calls = 0;
    const observer = new QueryObserver<string[]>(client, {
      queryKey: ["repos"],
      queryFn: async () => {
        const snapshot = [...store];
        if (calls++ === 0) await firstLoad;
        return snapshot;
      },
    });
    const unsubscribe = observer.subscribe(() => {});

    expect(calls).toBe(1);
    store.push("created-during-first-load");
    const invalidated = client.invalidateQueries({ queryKey: ["repos"] });
    finishFirstLoad();
    await invalidated;

    expect(client.getQueryData(["repos"])).toEqual(["created-during-first-load"]);
    unsubscribe();
    client.clear();
  });
});
