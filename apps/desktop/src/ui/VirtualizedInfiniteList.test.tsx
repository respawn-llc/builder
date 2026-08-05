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
  it("restores through the virtualizer and drives the restored range to load more", async () => {
    const onLoadMore = vi.fn();
    render(
      <VirtualizedInfiniteList
        estimateSize={() => 80}
        getItemKey={(item) => item.id}
        hasNextPage
        initialScrollOffset={240}
        initialScrollOffsetRequestKey="task-1:restored"
        isFetchingNextPage={false}
        items={Array.from({ length: 10 }, (_unused, index) => ({ id: `item-${index.toString()}` }))}
        loadingLabel="Loading"
        onLoadMore={onLoadMore}
        renderItem={(item) => <div>{item.id}</div>}
        testId="virtualized-list"
      />,
    );

    await waitFor(() => {
      expect(scrollCalls.values).toContain(240);
      expect(onLoadMore).toHaveBeenCalledTimes(1);
    });
    expect(screen.getByText("item-9")).toBeVisible();
  });
});
