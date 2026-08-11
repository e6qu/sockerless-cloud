import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import {
  LabelsEditor,
  labelsToPairs,
  pairsToLabels,
  type LabelPair,
} from "../console/LabelsEditor.js";

afterEach(cleanup);

describe("labels conversion", () => {
  it("round-trips a labels map through pairs", () => {
    const labels = { env: "prod", team: "core" };
    expect(pairsToLabels(labelsToPairs(labels))).toEqual(labels);
  });

  it("drops blank keys and trims whitespace so no empty key is ever sent", () => {
    const pairs: LabelPair[] = [
      { key: "  keep ", value: "yes" },
      { key: "", value: "orphan" },
      { key: "   ", value: "blank" },
    ];
    expect(pairsToLabels(pairs)).toEqual({ keep: "yes" });
  });
});

function Harness({ initial }: { initial?: Record<string, string> }) {
  const [pairs, setPairs] = useState<LabelPair[]>(labelsToPairs(initial));
  return (
    <>
      <LabelsEditor pairs={pairs} onChange={setPairs} idPrefix="t" />
      <output data-testid="result">{JSON.stringify(pairsToLabels(pairs))}</output>
    </>
  );
}

describe("LabelsEditor", () => {
  it("adds a row, edits it, and reflects it in the produced map", () => {
    render(<Harness />);
    fireEvent.click(screen.getByTestId("t-label-add"));
    fireEvent.change(screen.getByTestId("t-label-key-0"), { target: { value: "region" } });
    fireEvent.change(screen.getByTestId("t-label-value-0"), { target: { value: "us" } });
    expect(screen.getByTestId("result").textContent).toBe(JSON.stringify({ region: "us" }));
  });

  it("removes a row, leaving the rest intact", () => {
    render(<Harness initial={{ a: "1", b: "2" }} />);
    fireEvent.click(screen.getByTestId("t-label-remove-0"));
    expect(screen.getByTestId("result").textContent).toBe(JSON.stringify({ b: "2" }));
  });

  it("gives each input an accessible name and the remove control names its label", () => {
    render(<Harness initial={{ team: "core" }} />);
    expect(screen.getByLabelText("Label 1 key")).toBeTruthy();
    expect(screen.getByLabelText("Label 1 value")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove label team" })).toBeTruthy();
  });
});
