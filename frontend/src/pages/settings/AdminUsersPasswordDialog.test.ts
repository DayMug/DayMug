import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import AdminUsersPasswordDialog from "./AdminUsersPasswordDialog.vue";

function mountDialog(props: Partial<InstanceType<typeof AdminUsersPasswordDialog>["$props"]> = {}) {
  return mount(AdminUsersPasswordDialog, {
    props: { open: true, busy: false, ...props },
  });
}

function submitButton(wrapper: ReturnType<typeof mountDialog>) {
  return wrapper.findAll("button").find((b) => b.text() === "Set password");
}

describe("AdminUsersPasswordDialog", () => {
  it("stays out of the DOM until it is opened", () => {
    expect(mountDialog({ open: false }).find("input").exists()).toBe(false);
  });

  it("keeps the submit button disabled while the field is empty", () => {
    expect(submitButton(mountDialog())?.attributes("disabled")).toBeDefined();
  });

  it("emits the typed password on submit", async () => {
    const wrapper = mountDialog();
    await wrapper.find("input").setValue("hunter2");
    await submitButton(wrapper)?.trigger("click");

    expect(wrapper.emitted("submit")).toEqual([["hunter2"]]);
  });

  it("submits on Enter so the admin doesn't have to reach for the button", async () => {
    const wrapper = mountDialog();
    await wrapper.find("input").setValue("hunter2");
    await wrapper.find("input").trigger("keyup.enter");

    expect(wrapper.emitted("submit")).toEqual([["hunter2"]]);
  });

  it("keeps a failed attempt's password on screen for correction", async () => {
    const wrapper = mountDialog();
    await wrapper.find("input").setValue("typo");
    await submitButton(wrapper)?.trigger("click");

    expect((wrapper.find("input").element as HTMLInputElement).value).toBe("typo");
  });

  it("clears the field when reopened for another user", async () => {
    const wrapper = mountDialog();
    await wrapper.find("input").setValue("hunter2");

    await wrapper.setProps({ open: false });
    await wrapper.setProps({ open: true });

    expect((wrapper.find("input").element as HTMLInputElement).value).toBe("");
  });

  it("renders the failure inside the dialog it would otherwise hide", () => {
    const wrapper = mountDialog({ error: "password too short" });

    expect(wrapper.find('[data-testid="password-reset-error"]').text()).toBe("password too short");
  });

  it("shows no error line when there is nothing to report", () => {
    expect(mountDialog().find('[data-testid="password-reset-error"]').exists()).toBe(false);
  });

  it("emits close from the backdrop", async () => {
    const wrapper = mountDialog();
    await wrapper.find('[data-testid="password-reset-dialog"]').trigger("click");

    expect(wrapper.emitted("close")).toHaveLength(1);
  });
});
