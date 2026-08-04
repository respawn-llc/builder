import { render, screen, waitFor } from "@testing-library/react";
import type * as TanStackVirtual from "@tanstack/react-virtual";

const scrollCalls = vi.hoisted(() => ({ values: Array<number>() }));

vi.mock("@tanstack/react-virtual", async () => {
  const actual = await vi.importActual<typeof TanStackVirtual>("@tanstack/react-virtual");
  return {
    ...actual,
    useVirtualizer: (options: Parameters<typeof actual.useVirtualizer>[0]) => {
      const instance = actual.useVirtualizer(options);
      const scrollToOffset = instance.scrollToOffset.bind(instance);
      instance.scrollToOffset = (offset, scrollOptions) => {
        scrollCalls.values.push(offset);
        scrollToOffset(offset, scrollOptions);
      };
      return instance;
    },
  };
});

import { VirtualizedInfiniteList } from "./VirtualizedInfiniteList";

describe("VirtualizedInfiniteList restoration", () => {
  it("restores through the virtualizer offset API after the typed request arrives", async () => {
    render(
      <VirtualizedInfiniteList
        estimateSize={() => 80}
        getItemKey={(item) => item.id}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={Array.from({ length: 20 }, (_unused, index) => ({ id: `item-${index.toString()}` }))}
        initialScrollOffset={240}
        initialScrollOffsetRequestKey="task-1:restored"
        loadingLabel="Loading"
        onLoadMore={() => {
          return;
        }}
        renderItem={(item) => <div>{item.id}</div>}
        testId="virtualized-list"
      />,
    );

    await waitFor(() => {
      expect(scrollCalls.values).toContain(240);
    });
    expect(screen.getByTestId("virtualized-list")).toHaveProperty("scrollTop", 240);
  });
});
