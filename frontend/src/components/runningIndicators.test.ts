import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The two "an agent is working" indicators — the avatar's rotating ring and the
// conversation row's progress bar — are pure CSS, so nothing a mounted
// component exposes can tell whether they are still visible on the other theme.
// The regression that prompted these tests was a literal `#000` ring: correct on
// a light row, invisible on a dark avatar in dark mode. Assert instead that both
// take their paint from theme tokens, and that every token is restated under
// `.dark` — a var() inside a custom property is substituted where the property
// is declared, so a token defined only under `:root` silently keeps its light
// value on a dark surface.

const componentsDir = dirname(fileURLToPath(import.meta.url));
const read = (path: string) => readFileSync(join(componentsDir, path), "utf8");

const avatar = read("AgentAvatar.vue");
const progressBar = read("RunningProgressBar.vue");
const theme = read(join("..", "assets", "index.css"));

const TOKENS = ["--running-highlight", "--running-track", "--running-glow", "--running-ring-track"];

function declarationsIn(selector: string): string {
  const block = new RegExp(`\\n${selector}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(theme);
  expect(block, `${selector} block not found in index.css`).not.toBeNull();
  return block![1];
}

describe("running indicator theming", () => {
  it("paints the avatar's rotating ring from theme tokens", () => {
    const flow = /\.agent-running-border-flow\s*\{([\s\S]*?)\}/.exec(avatar)?.[1] ?? "";

    expect(flow).toContain("var(--running-highlight)");
    // A gradient stop written as a literal colour is the exact bug this guards.
    expect(flow).not.toMatch(/#[0-9a-f]{3,8}\b/i);
  });

  it("backs the ring's segments with a track so they never sit on bare avatar art", () => {
    const ring = /\.agent-running-border\s*\{([\s\S]*?)\n\}/.exec(avatar)?.[1] ?? "";

    expect(ring).toContain("background: var(--running-ring-track)");
  });

  it("paints the conversation progress bar from theme tokens", () => {
    expect(progressBar).toContain("background: var(--running-track)");
    expect(progressBar).toContain("background: var(--running-highlight)");
    expect(progressBar).toContain("var(--running-glow)");
  });

  it("defines every running-indicator token under both themes", () => {
    const light = declarationsIn(":root");
    const dark = declarationsIn("\\.dark");

    for (const token of TOKENS) {
      expect(light, `${token} missing from :root`).toContain(`${token}:`);
      expect(dark, `${token} missing from .dark`).toContain(`${token}:`);
    }
  });

  it("gives the dark theme a denser progress track than the light theme", () => {
    const percentOf = (declarations: string) =>
      Number(/--running-track:[^;]*?(\d+)%/.exec(declarations)![1]);

    expect(percentOf(declarationsIn("\\.dark"))).toBeGreaterThan(
      percentOf(declarationsIn(":root")),
    );
  });
});
