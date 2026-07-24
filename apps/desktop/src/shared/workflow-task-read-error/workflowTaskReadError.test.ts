import i18next from "i18next";

import { RpcError } from "@/api";
import { workflowTaskReadError } from "./workflowTaskReadError";

const translations = i18next.createInstance();
await translations.init({
  lng: "en",
  resources: {
    en: {
      translation: {
        states: {
          error: "error-title",
          workflowTaskContractError: "contract-error:{{taskID}}",
        },
      },
    },
  },
});
const translate = translations.getFixedT("en");

describe("workflow task read errors", () => {
  it("uses client-owned contract copy for typed lifecycle corruption", () => {
    const error = workflowTaskReadError(
      new RpcError({
        code: -32049,
        message: "backend diagnostic must not become UI copy",
        method: "workflow.task.get",
        data: {
          type: "workflow_task_integrity_error",
          reason: "agent_session_missing",
          task_id: "task-1",
          placement_id: "placement-1",
          node_id: "node-1",
          node_kind: "agent",
          run_id: "run-1",
          generation: 3,
          status_kind: "running",
          durable: {
            run_present: true,
            started: true,
            completed: false,
            interrupted: false,
            waiting_question: false,
          },
          exact: {
            present: false,
            waiting_question: false,
          },
          actions: {
            can_interrupt: false,
            can_resume: false,
          },
        },
      }),
      translate,
    );

    expect(error).toEqual({
      body: "contract-error:task-1",
      title: "error-title",
    });
  });

  it("preserves the generic error path for untyped failures", () => {
    expect(workflowTaskReadError(new Error("network failed"), translate)).toEqual({
      body: "network failed",
      title: "error-title",
    });
  });
});
