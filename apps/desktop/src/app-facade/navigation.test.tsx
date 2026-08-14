import { act, renderHook } from "@testing-library/react";

import { useAppNavigation } from "./navigation";

const fixture = vi.hoisted(() => ({
  navigate: vi.fn(async () => undefined),
  pathname: "/",
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => fixture.navigate,
  useRouter: () => ({
    history: {
      back: vi.fn(),
      forward: vi.fn(),
    },
  }),
  useRouterState: ({ select }: Readonly<{ select: (state: unknown) => unknown }>) =>
    select({ location: { pathname: fixture.pathname } }),
}));

vi.mock("./navigationTransitions", () => ({
  runNavigationTransition: async (action: () => Promise<void>) => action(),
}));

vi.mock("./useAppServices", () => ({
  useAppServices: () => ({
    logger: { append: vi.fn(async () => undefined) },
  }),
}));

describe("useAppNavigation", () => {
  beforeEach(() => {
    fixture.navigate.mockClear();
    fixture.pathname = "/";
  });

  it("does not push another Home entry when Home is already active", async () => {
    const { result } = renderHook(() => useAppNavigation());

    await act(async () => {
      await result.current.openHome();
    });

    expect(fixture.navigate).not.toHaveBeenCalled();
  });

  it("pushes Home from another destination", async () => {
    fixture.pathname = "/projects/project-1";
    const { result } = renderHook(() => useAppNavigation());

    await act(async () => {
      await result.current.openHome();
    });

    expect(fixture.navigate).toHaveBeenCalledWith({ to: "/" });
  });
});
