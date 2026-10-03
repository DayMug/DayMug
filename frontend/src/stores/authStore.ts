// Global singleton store: the identity of the logged-in user. The fetch /
// seed / clear actions live in composables/useAuth.ts. See src/stores/index.ts
// for the conventions every store in this directory follows.
import { ref } from "vue";

import type { AuthUser } from "@/composables/apiTypes";

// The currently-logged-in user as returned by /api/auth/me, kept at module
// scope so any component can read it without prop drilling. Populated on app
// mount by loadAuthMe(); cleared on logout.
export const authMe = ref<AuthUser | null>(null);

export function resetAuthStore() {
  authMe.value = null;
}
