import { beforeEach, describe, expect, it } from "vitest";

import { installTestStorage } from "../testSupport/storage";
import { readLastProjectRoute, writeLastProjectRoute } from "./projectRoutePersistence";

describe("project route persistence", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
  });

  it("preserves omitted and explicitly empty workflow selectors", () => {
    writeLastProjectRoute({ projectId: "project-unscoped" });
    expect(readLastProjectRoute()).toEqual({ projectId: "project-unscoped" });

    writeLastProjectRoute({ projectId: "project-empty", workflowId: "" });
    expect(readLastProjectRoute()).toEqual({ projectId: "project-empty", workflowId: "" });
  });
});
