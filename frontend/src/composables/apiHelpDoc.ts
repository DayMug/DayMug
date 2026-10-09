import { jsonRequestInit, request } from "./apiClient";

export interface HelpDoc {
  markdown: string;
}

// fetchHelpDoc loads the admin-configured help markdown for the chat
// sidebar popup. Empty `markdown` means no doc is configured — the UI
// hides the "?" button in that case.
export async function fetchHelpDoc(): Promise<HelpDoc> {
  return request<HelpDoc>(`/api/help-doc`, undefined, { label: "help doc" });
}

// adminSaveHelpDoc replaces the stored markdown. Passing an empty
// string clears the doc and hides the sidebar button for every user.
export async function adminSaveHelpDoc(markdown: string): Promise<HelpDoc> {
  return request<HelpDoc>(`/api/admin/help-doc`, jsonRequestInit("PUT", { markdown }), {
    label: "admin save help doc",
    errorBody: "message",
  });
}
