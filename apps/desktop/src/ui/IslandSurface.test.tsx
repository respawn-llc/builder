import { render, screen } from "@testing-library/react";
import { createRef } from "react";

import { IslandSurface } from "./IslandSurface";

describe("IslandSurface", () => {
  it("forwards refs to the rendered surface element", () => {
    const ref = createRef<HTMLElement>();

    render(<IslandSurface ref={ref}>Ref island</IslandSurface>);

    expect(ref.current).toBe(screen.getByText("Ref island"));
  });
});
