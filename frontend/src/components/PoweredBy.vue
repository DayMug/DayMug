<script setup lang="ts">
import { PROJECT_URL } from "@/lib/project";

// Not translated and not configurable on purpose: the LICENSE requires this
// exact attribution to stay visible in every deployment.
</script>

<template>
  <span data-testid="powered-by">
    Powered by
    <a
      :href="PROJECT_URL"
      target="_blank"
      rel="noopener noreferrer"
      class="font-medium underline-offset-2 hover:underline"
      >DayMug</a
    >
  </span>
</template>
