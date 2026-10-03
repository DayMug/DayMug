import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ProviderAccountStatus from "./ProviderAccountStatus.vue";

const checkAccount = vi.fn();
vi.mock("@/composables/useApi", () => ({
  adminCheckAccount: (...args: unknown[]) => checkAccount(...args),
}));

// Minimal WebSocket stand-in: records what the login terminal sends and lets
// the test push server frames.
class FakeSocket {
  static last: FakeSocket | null = null;
  static OPEN = 1;
  readyState = 1;
  sent: Record<string, unknown>[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  url: string;
  constructor(url: string) {
    this.url = url;
    FakeSocket.last = this;
  }
  send(data: string) {
    this.sent.push(JSON.parse(data) as Record<string, unknown>);
  }
  close() {
    this.onclose?.();
  }
}

function okResult(overrides: Record<string, unknown> = {}) {
  return {
    account: "main",
    provider: "claude",
    transport: "agent-sdk",
    ok: true,
    auth: { checked: true, logged_in: true, method: "claude.ai", detail: "max" },
    run: { attempted: true, ok: true, model: "claude-sonnet-5", latency_ms: 2300, reply: "OK" },
    checked_at: "2026-09-24T00:00:00Z",
    ...overrides,
  };
}

function mountStatus(props: Record<string, unknown> = {}) {
  return mount(ProviderAccountStatus, {
    props: { account: "main", type: "claude", ...props },
    attachTo: document.body,
  });
}

describe("ProviderAccountStatus", () => {
  beforeEach(() => {
    checkAccount.mockReset();
    vi.stubGlobal("WebSocket", FakeSocket);
    FakeSocket.last = null;
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("shows the login state and the test call after a check", async () => {
    checkAccount.mockResolvedValue(okResult());
    const wrapper = mountStatus();

    await wrapper.find('[data-testid="account-check"]').trigger("click");
    await flushPromises();

    expect(checkAccount).toHaveBeenCalledWith("main");
    const result = wrapper.find('[data-testid="account-check-result"]');
    expect(result.text()).toContain("Account is usable");
    expect(result.text()).toContain("agent-sdk");
    expect(wrapper.find('[data-testid="account-check-auth"]').text()).toBe(
      "Logged in: claude.ai · max",
    );
    expect(wrapper.find('[data-testid="account-check-run"]').text()).toBe(
      "Test call succeeded (claude-sonnet-5, 2.3s)",
    );
  });

  it("tells the admin to log in when the account has no credentials", async () => {
    checkAccount.mockResolvedValue(
      okResult({
        ok: false,
        auth: { checked: true, logged_in: false },
        run: { attempted: false, ok: false },
      }),
    );
    const wrapper = mountStatus();
    await wrapper.find('[data-testid="account-check"]').trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("Account is not usable");
    expect(wrapper.text()).toContain("Not logged in. Click Log in to authorize it.");
    expect(wrapper.find('[data-testid="account-check-run"]').exists()).toBe(false);
  });

  it("has no login for env-key types", () => {
    const wrapper = mountStatus({ type: "openai-compatible" });
    expect(wrapper.find('[data-testid="account-login"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("API key");
  });

  it("runs the account's login flow and re-checks once the terminal closes", async () => {
    checkAccount.mockResolvedValue(okResult());
    const wrapper = mountStatus();

    await wrapper.find('[data-testid="account-login"]').trigger("click");
    const socket = FakeSocket.last!;
    socket.onopen?.();
    expect(socket.sent[0]).toMatchObject({ type: "start", account: "main", login: true });

    socket.onmessage?.({ data: JSON.stringify({ type: "exited", exit_code: 0 }) });
    await flushPromises();
    const close = document.body.querySelector<HTMLButtonElement>(".admin-terminal-close")!;
    close.click();
    await flushPromises();

    expect(checkAccount).toHaveBeenCalledWith("main");
    expect(wrapper.find('[data-testid="account-check-result"]').exists()).toBe(true);
  });

  it("disables both actions while the account has unsaved edits", () => {
    const wrapper = mountStatus({ disabled: true });
    expect(wrapper.find('[data-testid="account-check"]').attributes("disabled")).toBeDefined();
    expect(wrapper.find('[data-testid="account-login"]').attributes("disabled")).toBeDefined();
  });
});
