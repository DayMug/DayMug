import { afterEach, describe, expect, it } from "vitest";
import { desktopClient, dirLabel } from "./desktopClient";

describe("desktopClient", () => {
  afterEach(() => {
    delete window.__DAYMUG_CLIENT__;
  });

  it("is null in a plain browser and for a malformed marker", () => {
    expect(desktopClient()).toBeNull();
    window.__DAYMUG_CLIENT__ = { nonce: "short" };
    expect(desktopClient()).toBeNull();
  });

  it("reads the marker the client injects", () => {
    window.__DAYMUG_CLIENT__ = { nonce: "n".repeat(40), os: "windows", hostname: "pc" };
    expect(desktopClient()).toMatchObject({ os: "windows", hostname: "pc", version: "" });
  });

  it("labels POSIX and Windows directories by their last segment", () => {
    expect(dirLabel("/Users/a/proj/")).toBe("proj");
    expect(dirLabel("C:\\code\\app")).toBe("app");
  });
});
