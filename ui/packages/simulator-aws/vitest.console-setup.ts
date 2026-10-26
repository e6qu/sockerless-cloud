import { configure } from "@testing-library/react";
import { inject } from "vitest";
import { installConsoleBrowser } from "@sockerless/ui-core/testing";

installConsoleBrowser(inject("consoleOrigin"));

// A query a page issues travels to the simulator and back, through the
// console's federated credentials, before the DOM a test waits on renders.
configure({ asyncUtilTimeout: 10_000 });
