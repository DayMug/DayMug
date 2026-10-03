import type { User } from "@/composables/useApi";

// Qualifies another owner's agent as "owner/agent" so same-named agents from
// different people stay distinguishable in shared views.
export function agentDisplayName(user: User, usersById: Map<string, User>): string {
  if (!user.owner_id || user.owner_id === user.id) return user.name;
  const owner = usersById.get(user.owner_id);
  const ownerName = owner?.username.trim() || owner?.name.trim();
  const agentName = user.name.trim();
  if (!ownerName || ownerName === agentName) return user.name;
  return `${ownerName}/${user.name}`;
}
