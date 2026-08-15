import { projectSummarySchema, workspaceUnlinkResponseSchema } from "./project";

const project = {
  project_id: "project-1",
  project_key: "KNT",
  display_name: "Kent",
  primary_workspace: {
    workspace_id: "workspace-1",
    display_name: "Kent",
    root_path: "/workspace/kent",
    availability: "available",
    is_primary: true,
    updated_at_unix_ms: 1,
  },
  default_workflow_name: "",
  default_workflow_valid: false,
  updated_at_unix_ms: 1,
  task_count: 0,
  attention_count: 0,
  workflow_count: 0,
};

describe("projectSummarySchema", () => {
  it("propagates a null default Workflow identity", () => {
    expect(projectSummarySchema.parse({ ...project, default_workflow_id: null }).defaultWorkflowID).toBeNull();
  });

  it("rejects a prefixed default Workflow identity", () => {
    expect(() =>
      projectSummarySchema.parse({
        ...project,
        default_workflow_id: "workflow-11111111-1111-4111-8111-111111111111",
      }),
    ).toThrow();
  });

  it("rejects an upper-case default Workflow identity", () => {
    expect(() =>
      projectSummarySchema.parse({
        ...project,
        default_workflow_id: "11111111-1111-4111-8111-11111111111A",
      }),
    ).toThrow();
  });

  it("uses blockers as the unlink outcome discriminator", () => {
    expect(
      workspaceUnlinkResponseSchema.parse({
        project_id: "project-1",
        workspace_id: "workspace-1",
      }),
    ).toMatchObject({
      projectID: "project-1",
      workspaceID: "workspace-1",
      blockers: [],
      project: null,
    });
    expect(
      workspaceUnlinkResponseSchema.parse({
        project_id: "project-1",
        workspace_id: "workspace-1",
        blockers: [{ code: "default_workspace", message: "blocked" }],
      }).blockers,
    ).toHaveLength(1);
  });
});
