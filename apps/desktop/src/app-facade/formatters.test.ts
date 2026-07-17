import { describe, expect, it } from "vitest";

import { formatHomeRelativePath } from "./formatters";

describe("formatHomeRelativePath", () => {
  it("replaces Unix home descendants with a tilde path", () => {
    expect(formatHomeRelativePath("/Users/nek/Developer/kent", "/Users/nek", "macos")).toBe(
      "~/Developer/kent",
    );
  });

  it("formats the home directory itself as tilde", () => {
    expect(formatHomeRelativePath("/home/nek/", "/home/nek", "linux")).toBe("~");
    expect(formatHomeRelativePath("C:\\Users\\Nikita\\", "c:\\users\\nikita", "windows")).toBe("~");
  });

  it("leaves non-home Unix paths absolute", () => {
    expect(formatHomeRelativePath("/var/tmp/project", "/Users/nek", "macos")).toBe("/var/tmp/project");
    expect(formatHomeRelativePath("/Users/nek-sibling/project", "/Users/nek", "macos")).toBe(
      "/Users/nek-sibling/project",
    );
  });

  it("replaces Windows home descendants with a user-relative path", () => {
    expect(formatHomeRelativePath("C:\\Users\\Nikita\\Developer\\kent", "c:\\users\\nikita", "windows")).toBe(
      "~\\Developer\\kent",
    );
  });

  it("leaves Windows sibling directories absolute", () => {
    expect(formatHomeRelativePath("C:\\Users\\Nikita2\\project", "C:\\Users\\Nikita", "windows")).toBe(
      "C:\\Users\\Nikita2\\project",
    );
  });

  it("handles UNC paths with the same home boundary rules", () => {
    expect(
      formatHomeRelativePath(
        "\\\\server\\share\\Users\\Nikita\\project",
        "\\\\server\\share\\Users\\Nikita",
        "windows",
      ),
    ).toBe("~\\project");
    expect(
      formatHomeRelativePath(
        "\\\\server\\share\\Users\\Nikita2\\project",
        "\\\\server\\share\\Users\\Nikita",
        "windows",
      ),
    ).toBe("\\\\server\\share\\Users\\Nikita2\\project");
  });

  it("leaves paths unchanged when the home path is unavailable", () => {
    expect(formatHomeRelativePath("/Users/nek/Developer/kent", "", "unknown")).toBe(
      "/Users/nek/Developer/kent",
    );
  });
});
