<script setup lang="ts">
// Thin shell for the /settings/* routes. The settings flow is a hub-and-
// spoke experience now: SettingsHub is the index and every detail page
// renders its own SettingsDetailHeader. There is no longer a persistent
// nav rail — the same layout serves desktop and mobile.
//
// The shell also owns the pull-to-refresh gesture for the whole settings
// section. A pull past the threshold remounts the active route by bumping
// its key, which re-runs that page's onMounted data fetches — a single,
// uniform refresh that works for every sub-page without per-page wiring.
import { nextTick, ref } from "vue";
import PullToRefresh from "@/components/PullToRefresh.vue";

// Minimum time the spinner stays up so a remount that resolves instantly
// (cached imports, synchronous render) still reads as a deliberate
// refresh rather than a flicker.
const MIN_SPINNER_MS = 450;

const routeKey = ref(0);

async function handleRefresh() {
  routeKey.value += 1;
  await nextTick();
  await new Promise((resolve) => setTimeout(resolve, MIN_SPINNER_MS));
}
</script>

<template>
  <PullToRefresh
    :on-refresh="handleRefresh"
    class="settings-shell flex-1 min-h-0 overflow-y-auto overflow-x-hidden bg-background"
  >
    <RouterView :key="routeKey" />
  </PullToRefresh>
</template>
