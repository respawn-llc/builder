import { ellipsizeActionTarget } from "./ellipsizeActionTarget";

it("counts emoji and combining sequences as visible characters", () => {
  const combined = "e\u0301";
  const family = "👨‍👩‍👧‍👦";

  expect(ellipsizeActionTarget(`${family.repeat(31)}${combined}`)).toBe(`${family.repeat(31)}${combined}`);
  expect(ellipsizeActionTarget(`${family.repeat(31)}${combined}x`)).toBe(`${family.repeat(31)}…`);
});
