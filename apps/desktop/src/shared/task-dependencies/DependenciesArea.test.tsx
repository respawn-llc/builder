import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { vi } from "vitest";

import type { TaskSearchResult } from "@/app-facade";
import {
  insertPreparedTaskDependency,
  preparedTaskDependenciesProjection,
  type PreparedTaskDependency,
} from "./preparedDependencies";
import { DependenciesArea } from "./DependenciesArea";

const searchFixture = vi.hoisted<{ results: readonly TaskSearchResult[] }>(() => ({ results: [] }));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  taskSearchDebounceMs: 0,
  useDebouncedText: (value: string) => value,
  useTaskSearch: () => ({
    displayedQuery: null,
    normalizedTooShort: false,
    paginationUsesVisibleData: true,
    request: {
      data: undefined,
      error: null,
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isError: false,
      isFetchNextPageError: false,
      isFetching: false,
      isFetchingNextPage: false,
      refetch: vi.fn(),
    },
    results: searchFixture.results,
    searchable: searchFixture.results.length > 0,
  }),
}));

describe("DependenciesArea prepared limit transition", () => {
  it("accepts the 50th result, replaces the open picker with disabled Add, and never invokes a 51st callback", async () => {
    searchFixture.results = [candidate("task-50"), candidate("task-51")];
    const selected = vi.fn();
    const user = userEvent.setup();

    function Harness() {
      const [prepared, setPrepared] = useState<readonly PreparedTaskDependency[]>(
        Array.from({ length: 49 }, (_, index) => preparedEntry(`task-${index}`)),
      );
      const projection = preparedTaskDependenciesProjection(prepared);
      return (
        <DependenciesArea
          dependencies={projection}
          disabled={false}
          excludedTaskIDs={(direction) =>
            new Set(prepared.filter((entry) => entry.direction === direction).map((entry) => entry.taskID))
          }
          navigationDisabled={false}
          onAdd={vi.fn()}
          onRemove={vi.fn()}
          onSelectCandidate={async (direction, result) => {
            selected(result.group.taskID);
            setPrepared((current) =>
              insertPreparedTaskDependency(current, {
                direction,
                taskID: result.group.taskID,
                shortID: result.group.shortID,
                title: result.group.title,
                workflowID: result.group.workflowID,
                status: result.group.status,
              }),
            );
          }}
          onSelectTask={vi.fn()}
          previewProgress
          projectID="project-1"
        />
      );
    }

    render(<Harness />);
    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    await user.click(screen.getByTestId("dependency-candidate-task-50"));

    await waitFor(() => {
      expect(screen.queryByTestId("dependency-candidate-task-51")).not.toBeInTheDocument();
    });
    const add = screen.getByTestId("dependency-add-blocked-by");
    expect(add).toBeDisabled();
    expect(add).toHaveAttribute("aria-describedby");
    await user.click(add);
    expect(selected).toHaveBeenCalledTimes(1);
  });
});

function preparedEntry(taskID: string): PreparedTaskDependency {
  return {
    direction: "blocked-by",
    taskID,
    shortID: taskID,
    title: taskID,
    workflowID: "workflow-1",
    status: {
      kind: "backlog",
      nativeState: "active",
      nodeIDs: [],
      attentionTypes: [],
    },
  };
}

function candidate(taskID: string): TaskSearchResult {
  return {
    key: taskID,
    group: {
      projectID: "project-1",
      projectKey: "KENT",
      taskID,
      shortID: taskID,
      workflowID: "workflow-1",
      title: taskID,
      status: {
        kind: "backlog",
        nativeState: "active",
        nodeIDs: [],
        attentionTypes: [],
      },
      totalHitCount: 1,
      hits: [
        {
          ordinal: 1,
          source: { kind: "title" },
          literal: {
            before: "",
            match: taskID,
            after: "",
            leftTruncated: false,
            rightTruncated: false,
          },
        },
      ],
    },
  };
}
