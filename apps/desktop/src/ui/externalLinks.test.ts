import { describe, expect, it } from "vitest";

import { compactExternalUrlLabel, safeExternalUrl } from "./externalLinks";

describe("safeExternalUrl", () => {
  it("returns a normalized URL for allowed protocols", () => {
    expect(safeExternalUrl("https://github.com/respawn-llc/kent")).toBe(
      "https://github.com/respawn-llc/kent",
    );
    expect(safeExternalUrl("http://example.com")).toBe("http://example.com/");
    expect(safeExternalUrl("mailto:support@respawn.pro")).toBe("mailto:support@respawn.pro");
  });

  it("returns undefined for empty or whitespace input", () => {
    expect(safeExternalUrl(undefined)).toBeUndefined();
    expect(safeExternalUrl("")).toBeUndefined();
    expect(safeExternalUrl("   ")).toBeUndefined();
  });

  it("returns undefined for non-URL or disallowed-protocol input", () => {
    expect(safeExternalUrl("not a url")).toBeUndefined();
    expect(safeExternalUrl("JIRA-1234")).toBeUndefined();
    expect(safeExternalUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeExternalUrl("file:///etc/passwd")).toBeUndefined();
  });
});

describe("compactExternalUrlLabel", () => {
  it("returns the bare host for http(s) URLs", () => {
    expect(compactExternalUrlLabel("https://github.com/respawn-llc/kent/issues/1")).toBe("github.com");
    expect(compactExternalUrlLabel("http://docs.example.com:8080/path?q=1")).toBe("docs.example.com");
  });

  it("strips a leading www.", () => {
    expect(compactExternalUrlLabel("https://www.linear.app/issue/ABC-1")).toBe("linear.app");
  });

  it("falls back to the full value when there is no host", () => {
    expect(compactExternalUrlLabel("mailto:support@respawn.pro")).toBe("mailto:support@respawn.pro");
  });
});
