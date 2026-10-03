<script setup lang="ts">
// SQLite VACUUM panel. Self-contained: it owns the size fetch and the
// optimize round-trip, so the parent only decides *whether* the section
// applies (see canOptimizeDB there) and never has to thread db state through
// props. The parent renders this behind a v-if, which is what keeps the
// on-mount size fetch from firing on non-sqlite backends.
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminDatabaseSize, adminOptimizeDatabase } from "@/composables/useApi";
import type { AdminDBOptimizeResult } from "@/composables/useApi";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import { useConfirm } from "@/composables/useConfirm";
import { Button } from "@/components/ui/button";

const { t } = useI18n();
const { confirm } = useConfirm();

const { busy: optimizing, error: optimizeErr, run: runOptimize } = useAsyncOperation();
const optimizeResult = ref<AdminDBOptimizeResult | null>(null);
// Current on-disk size, fetched on mount and updated to after_bytes after
// each optimization. null means we haven't fetched yet (or the backend
// reported 0 — see formatBytes for the unknown rendering).
const dbSize = ref<number | null>(null);

onMounted(async () => {
  try {
    dbSize.value = await adminDatabaseSize();
  } catch {
    // Non-fatal: the panel still works, the size just won't render.
  }
});

async function onOptimize() {
  if (!(await confirm({ message: t("settings.adminGlobal.optimizeConfirm") }))) {
    return;
  }
  optimizeResult.value = null;
  const result = await runOptimize(() => adminOptimizeDatabase());
  if (!result) return;
  optimizeResult.value = result;
  // The optimize response already carries the post-VACUUM size, so reuse it
  // instead of issuing a second /db/size round-trip.
  dbSize.value = result.after_bytes;
}

// Local to this panel rather than lib/uploadHelpers' formatBytes: database
// sizes are quoted to two decimals so an admin can see whether a VACUUM
// actually moved the needle, while upload progress rounds to one.
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 ? 1 : 2)} ${units[i]}`;
}
</script>

<template>
  <section class="border border-border rounded-md bg-card p-4" data-testid="database-maintenance">
    <h2 class="text-[14px] font-semibold mb-2">
      {{ t("settings.adminGlobal.databaseMaintenance") }}
    </h2>
    <p class="text-[12px] text-muted-foreground mb-2">
      {{ t("settings.adminGlobal.dbIntro") }}
    </p>
    <p class="text-[12px] text-muted-foreground mb-3">
      {{ t("settings.adminGlobal.currentSize") }}
      <code class="px-1 py-0.5 bg-accent rounded text-[11px] font-mono">{{
        dbSize === null ? "..." : dbSize > 0 ? formatBytes(dbSize) : t("common.unknown")
      }}</code>
    </p>
    <Button variant="outline" size="sm" :disabled="optimizing" @click="onOptimize">
      {{
        optimizing
          ? t("settings.adminGlobal.optimizing")
          : t("settings.adminGlobal.optimizeDatabase")
      }}
    </Button>
    <p v-if="optimizeResult" class="mt-3 text-[12px] text-muted-foreground">
      {{
        t("settings.adminGlobal.reclaimed", {
          bytes: formatBytes(optimizeResult.bytes_reclaimed),
          before: formatBytes(optimizeResult.before_bytes),
          after: formatBytes(optimizeResult.after_bytes),
        })
      }}
      <span v-if="optimizeResult.expired_sessions_deleted > 0">
        {{
          t("settings.adminGlobal.purgedSessions", {
            count: optimizeResult.expired_sessions_deleted,
          })
        }}
      </span>
      <span v-if="optimizeResult.purged_conversations > 0">
        {{
          t("settings.adminGlobal.purgedConversations", {
            count: optimizeResult.purged_conversations,
          })
        }}
      </span>
      <span v-if="optimizeResult.purged_users > 0">
        {{
          t("settings.adminGlobal.purgedUsers", {
            count: optimizeResult.purged_users,
          })
        }}
      </span>
      <span v-if="optimizeResult.purged_agents > 0">
        {{
          t("settings.adminGlobal.purgedAgents", {
            count: optimizeResult.purged_agents,
          })
        }}
      </span>
    </p>
    <p v-if="optimizeErr" class="mt-3 text-[12px] text-red-600">{{ optimizeErr }}</p>
  </section>
</template>
