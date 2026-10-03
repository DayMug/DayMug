import { fetchMe } from "./useApi";
import type { AuthUser } from "./useApi";
import { authMe } from "@/stores/authStore";

async function loadAuthMe(): Promise<AuthUser | null> {
  try {
    authMe.value = await fetchMe();
    return authMe.value;
  } catch {
    authMe.value = null;
    return null;
  }
}

// applyAuthMe seeds the cached identity from a payload the caller already
// fetched (typically the /api/app-state aggregate). Lets the cold-start
// path skip the dedicated /api/auth/me round-trip when the aggregate
// already carried the same shape.
function applyAuthMe(user: AuthUser) {
  authMe.value = user;
}

function clearAuthMe() {
  authMe.value = null;
}

export function useAuth() {
  return {
    authMe,
    loadAuthMe,
    applyAuthMe,
    clearAuthMe,
  };
}
