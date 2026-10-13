import { describe, expect, it } from "vitest";
import { duration, relativeTime } from "./format";

describe("format", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  it("relativeTime", () => {
    expect(relativeTime("2026-10-13T11:59:58Z", now)).toBe("just now");
    expect(relativeTime("2026-10-13T11:59:00Z", now)).toBe("1m ago");
    expect(relativeTime("2026-10-13T09:00:00Z", now)).toBe("3h ago");
    expect(relativeTime("2026-10-10T12:00:00Z", now)).toBe("3d ago");
    expect(relativeTime(null, now)).toBe("—");
  });
  it("duration", () => {
    expect(duration(0.13)).toBe("130ms");
    expect(duration(5.24)).toBe("5.2s");
    expect(duration(125)).toBe("2m 5s");
    expect(duration(null)).toBe("—");
  });
});
