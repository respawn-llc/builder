import { defaultDesktopSettings, parseDesktopSettings } from "@app/native-bridge";

describe("parseDesktopSettings", () => {
  it("returns defaults for non-object input", () => {
    expect(parseDesktopSettings(null)).toEqual(defaultDesktopSettings);
    expect(parseDesktopSettings(undefined)).toEqual(defaultDesktopSettings);
    expect(parseDesktopSettings("disabled")).toEqual(defaultDesktopSettings);
    expect(parseDesktopSettings(42)).toEqual(defaultDesktopSettings);
    expect(parseDesktopSettings([])).toEqual(defaultDesktopSettings);
  });

  it("defaults selfUpdate to enabled when missing or unrecognized", () => {
    expect(parseDesktopSettings({}).selfUpdate).toBe("enabled");
    expect(parseDesktopSettings({ selfUpdate: "on" }).selfUpdate).toBe("enabled");
    expect(parseDesktopSettings({ selfUpdate: true }).selfUpdate).toBe("enabled");
    expect(parseDesktopSettings({ selfUpdate: null }).selfUpdate).toBe("enabled");
  });

  it("reads an explicit disabled gate", () => {
    expect(parseDesktopSettings({ selfUpdate: "disabled" }).selfUpdate).toBe("disabled");
  });

  it("reads an explicit enabled gate", () => {
    expect(parseDesktopSettings({ selfUpdate: "enabled" }).selfUpdate).toBe("enabled");
  });

  it("normalizes version and ignores unknown fields for forward compatibility", () => {
    const parsed = parseDesktopSettings({ version: 7, selfUpdate: "disabled", futureFlag: "x" });
    expect(parsed.version).toBe(1);
    expect(parsed.selfUpdate).toBe("disabled");
    expect("futureFlag" in parsed).toBe(false);
  });
});
