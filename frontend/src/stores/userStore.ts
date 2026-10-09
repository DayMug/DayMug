// Global singleton store: the agent roster the sidebar and settings screens
// render from. The CRUD/reorder actions that maintain it live in
// composables/useUsers.ts. See src/stores/index.ts for the conventions every
// store in this directory follows.
import { ref } from "vue";

import type { User } from "@/composables/apiTypes";

export const users = ref<User[]>([]);
export const currentUser = ref<User | null>(null);
// Flips true once the roster has been fetched, so the rail can hold back its
// "+" until the agents it sits under have arrived.
export const usersLoaded = ref(false);
// Archived agents, populated on demand by the settings restore view.
export const archivedAgents = ref<User[]>([]);

export function resetUserStore() {
  users.value = [];
  currentUser.value = null;
  usersLoaded.value = false;
  archivedAgents.value = [];
}
