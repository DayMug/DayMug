<script setup lang="ts">
// "Version & updates" section of the global admin page: the self-upgrade
// check/apply flow, the graceful-restart button, and the busy banner that
// explains which conversations a restart would interrupt.
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  adminUpgradeApply,
  adminUpgradeBusy,
  adminUpgradeCheck,
  adminUpgradeRestart,
  adminUpgradeStatus,
} from "@/composables/useApi";
import type { AdminUpgradeCheck, AdminUpgradeJob, AdminUpgradeStatus } from "@/composables/useApi";
import { Button } from "@/components/ui/button";
import { useConfirm } from "@/composables/useConfirm";
import { useTimeFormat } from "@/composables/useTimeFormat";
import { formatDateTime } from "@/lib/format";
import { errorMessage } from "@/lib/errorMessage";

const props = defineProps<{ currentVersion: string; enabled: boolean }>();
// The page owns the public config, so it refetches the running version once
// the watchdog reports a successful upgrade.
const emit = defineEmits<{ "upgrade-succeeded": [] }>();

// The version the *running* process reports. Seeded from the page's public
// config and refreshed from every check response, so a tab left open across
// a restart stops showing the version the config carried when it loaded.
const runningVersion = ref(props.currentVersion);
watch(
  () => props.currentVersion,
  (v) => {
    if (v) runningVersion.value = v;
  },
);
function noteRunningVersion(version: string | undefined) {
  if (version) runningVersion.value = version;
}

const { t } = useI18n();
const { confirm } = useConfirm();
const { timeHour12 } = useTimeFormat();

const upgradeCheck = ref<AdminUpgradeCheck | null>(null);
const upgradeStatus = ref<AdminUpgradeStatus | null>(null);
const upgradeMsg = ref<string>("");
const upgradeErr = ref<string>("");
const checking = ref(false);
const applying = ref(false);
const restarting = ref(false);
// Live in-flight job count. Updated on Check, on Busy poll, and on a
// failed Apply (the server returns the freshly-observed count in the 409
// body so the banner sticks around even when nothing has been refetched).
const inFlightJobs = ref(0);
const busyJobs = ref<AdminUpgradeJob[]>([]);
let pollHandle: number | null = null;
// Busy poller — runs while the upgrade panel is mounted so the gate
// updates in near-real-time as users finish their prompts. Distinct from
// the validating-phase pollHandle (which only runs during Apply).
let busyPollHandle: number | null = null;

const isValidating = computed(() => upgradeStatus.value?.phase === "validating");
const isBusy = computed(() => inFlightJobs.value > 0 || backgroundJobCount.value > 0);
const visibleBusyJobs = computed(() =>
  busyJobs.value.filter((job) => job.username || job.account_name || job.provider_type),
);
// A job is only "running" once it holds an account concurrency slot; jobs
// still parked in the pool's FIFO queue report status "queued".
const isJobQueued = (job: AdminUpgradeJob) => job.status === "queued";
const queuedJobCount = computed(() => busyJobs.value.filter(isJobQueued).length);
// A turn parked on a question holds no slot and no longer blocks a graceful
// restart: the drain ends it and the question comes back after the restart.
const isJobWaiting = (job: AdminUpgradeJob) => job.status === "waiting";
const waitingJobCount = computed(() => busyJobs.value.filter(isJobWaiting).length);
// Resident background work (subagents still running after the turn ended)
// is not an in-flight job, but a graceful restart still waits for it.
const isJobBackground = (job: AdminUpgradeJob) => job.status === "background";
const backgroundJobCount = computed(() => busyJobs.value.filter(isJobBackground).length);
const runningJobCount = computed(() =>
  Math.max(inFlightJobs.value - queuedJobCount.value - waitingJobCount.value, 0),
);
// The server schedules graceful upgrades after an idle window, so running
// conversations do not block the button. New work can still start while the
// scheduled operation waits for the service to become idle.
const canApply = computed(
  () => upgradeCheck.value?.has_update && !applying.value && !isValidating.value,
);

onMounted(async () => {
  if (!props.enabled) return;
  await refreshStatus();
  await refreshBusy();
  await restorePendingOperation();
  startBusyPolling();
});

onUnmounted(() => {
  stopPolling();
  stopBusyPolling();
});

async function refreshStatus() {
  try {
    upgradeStatus.value = await adminUpgradeStatus();
  } catch (e) {
    upgradeErr.value = errorMessage(e);
  }
}

async function restorePendingOperation() {
  if (upgradeStatus.value?.phase !== "waiting_for_conversations") return;
  if (upgradeStatus.value.pending_operation === "upgrade") {
    try {
      upgradeCheck.value = await adminUpgradeCheck();
      inFlightJobs.value = upgradeCheck.value.in_flight_jobs ?? inFlightJobs.value;
      busyJobs.value = upgradeCheck.value.jobs ?? busyJobs.value;
    } catch (e) {
      upgradeErr.value = errorMessage(e);
    }
    upgradeMsg.value = t("settings.adminGlobal.upgradeWaiting", {
      version: upgradeStatus.value.new_version,
    });
    startPolling();
    return;
  }
  if (upgradeStatus.value.pending_operation === "restart") {
    upgradeMsg.value = t("settings.adminGlobal.restartWaiting");
  }
}

async function refreshBusy() {
  try {
    const res = await adminUpgradeBusy();
    inFlightJobs.value = res.in_flight_jobs;
    busyJobs.value = res.jobs ?? [];
  } catch {
    // Polling errors are non-fatal — the panel still works, the banner
    // just won't update until the next successful poll.
  }
}

// startBusyPolling refreshes the in-flight job count every 5s while the
// panel is mounted. Cheap server-side (atomic counter read) and lets the
// Update button re-enable itself the moment running prompts finish.
function startBusyPolling() {
  stopBusyPolling();
  busyPollHandle = window.setInterval(() => {
    void refreshBusy();
  }, 5000);
}

function stopBusyPolling() {
  if (busyPollHandle != null) {
    window.clearInterval(busyPollHandle);
    busyPollHandle = null;
  }
}

async function onCheck() {
  upgradeErr.value = "";
  upgradeMsg.value = "";
  checking.value = true;
  try {
    upgradeCheck.value = await adminUpgradeCheck();
    // The check response carries the live in-flight count; mirror it
    // into the banner state so we don't have to wait for the next 5s
    // poll tick to disable the Update button. It also carries the version
    // the running binary actually reports, which is the authoritative
    // answer to "what am I on?" — refresh the header from it.
    noteRunningVersion(upgradeCheck.value.current_version);
    inFlightJobs.value = upgradeCheck.value.in_flight_jobs ?? 0;
    busyJobs.value = upgradeCheck.value.jobs ?? [];
    if (upgradeCheck.value.platform_supported === false) {
      // Saying "already latest" here would be a lie — a newer release
      // exists, it just shipped nothing for this OS/arch.
      upgradeMsg.value = t("settings.adminGlobal.noPlatformBuild", {
        version: upgradeCheck.value.latest_version,
      });
    } else if (!upgradeCheck.value.has_update) {
      upgradeMsg.value = t("settings.adminGlobal.alreadyLatest", {
        version: upgradeCheck.value.latest_version,
      });
    }
  } catch (e) {
    upgradeErr.value = errorMessage(e);
  } finally {
    checking.value = false;
  }
}

async function onApply() {
  if (!upgradeCheck.value?.has_update) return;
  await doApply(false);
}

async function onRestartService() {
  const ok = await confirm({
    message: t("settings.adminGlobal.restartConfirm"),
    variant: "destructive",
  });
  if (!ok) return;
  upgradeErr.value = "";
  upgradeMsg.value = "";
  restarting.value = true;
  try {
    const res = await adminUpgradeRestart();
    inFlightJobs.value = res.in_flight_jobs ?? inFlightJobs.value;
    busyJobs.value = res.jobs ?? busyJobs.value;
    upgradeMsg.value =
      res.status === "waiting_for_conversations"
        ? t("settings.adminGlobal.restartWaiting")
        : t("settings.adminGlobal.restartRequested");
  } catch (e) {
    upgradeErr.value = errorMessage(e);
    void refreshBusy();
  } finally {
    restarting.value = false;
  }
}

async function doApply(force: boolean) {
  upgradeErr.value = "";
  upgradeMsg.value = "";
  applying.value = true;
  try {
    const res = await adminUpgradeApply(force);
    if (res.status === "up_to_date") {
      upgradeMsg.value = t("settings.adminGlobal.alreadyUpToDate");
      applying.value = false;
      return;
    }
    if (res.status === "waiting_for_conversations") {
      upgradeMsg.value = t("settings.adminGlobal.upgradeWaiting", {
        version: res.new_version,
      });
      void refreshBusy();
      startPolling();
      return;
    }
    upgradeMsg.value = t("settings.adminGlobal.upgradePolling", { version: res.new_version });
    startPolling();
  } catch (e) {
    // The server returns 409 with a JSON body carrying the live job
    // count when it refuses on busy grounds. apiFetch surfaces that
    // body in the error message; we also kick a busy refresh so the
    // banner catches up if the count changed between Check and Apply.
    upgradeErr.value = errorMessage(e);
    applying.value = false;
    void refreshBusy();
  }
}

// startPolling refreshes /upgrade/status every 3s. Stops once the watchdog
// has reported a terminal phase (ok / rolled_back / failed) or once we've
// been polling for over 3 minutes.
function startPolling() {
  stopPolling();
  const startedAt = Date.now();
  pollHandle = window.setInterval(async () => {
    if (Date.now() - startedAt > 3 * 60 * 1000) {
      stopPolling();
      applying.value = false;
      upgradeErr.value = t("settings.adminGlobal.upgradeTimedOut");
      return;
    }
    try {
      upgradeStatus.value = await adminUpgradeStatus();
      const phase = upgradeStatus.value?.phase;
      if (phase === "ok" || phase === "rolled_back" || phase === "failed") {
        stopPolling();
        applying.value = false;
        if (phase === "ok") {
          upgradeMsg.value = t("settings.adminGlobal.upgradeSucceeded", {
            version: upgradeStatus.value?.new_version,
          });
          // Let the page refresh the public config so the displayed current
          // version catches up with reality.
          emit("upgrade-succeeded");
          // Clear the stale check result so the "Update to vX" button
          // disappears now that we are on vX. Next "Check for updates"
          // click will repopulate it if a newer build exists.
          upgradeCheck.value = null;
        } else if (phase === "rolled_back") {
          upgradeErr.value = t("settings.adminGlobal.upgradeRolledBack", {
            reason: upgradeStatus.value?.error || t("settings.adminGlobal.unknownReason"),
          });
        } else {
          upgradeErr.value = t("settings.adminGlobal.upgradeFailed", {
            reason: upgradeStatus.value?.error || t("settings.adminGlobal.unknownReason"),
          });
        }
      }
    } catch {
      // Polling errors during a restart are expected — the server is
      // bouncing. Swallow and let the next tick try again.
    }
  }, 3000);
}

function stopPolling() {
  if (pollHandle != null) {
    window.clearInterval(pollHandle);
    pollHandle = null;
  }
}

// Render a server-supplied ISO-8601 timestamp in the viewer's local time.
// Falls back to the raw value if Date parsing fails so admins still see
// something meaningful when the manifest publisher emits a non-standard date.
function formatLocalTime(iso: string): string {
  if (!iso) return "";
  const out = formatDateTime(iso, timeHour12.value);
  return out || iso;
}

function busyJobOwner(job: AdminUpgradeJob): string {
  return job.username || job.user_id || t("settings.adminGlobal.busyUnknownUser");
}

function busyJobAccount(job: AdminUpgradeJob): string {
  if (job.provider_type && job.account_name) return `${job.provider_type}/${job.account_name}`;
  return job.account_name || job.provider_type || t("settings.adminGlobal.busyUnknownAccount");
}

const phaseLabel = computed(() => {
  switch (upgradeStatus.value?.phase) {
    case "validating":
      return { text: t("settings.adminGlobal.phaseValidating"), tone: "text-amber-600" };
    case "ok":
      return { text: t("settings.adminGlobal.phaseOk"), tone: "text-green-700" };
    case "rolled_back":
      return { text: t("settings.adminGlobal.phaseRolledBack"), tone: "text-red-600" };
    case "failed":
      return { text: t("settings.adminGlobal.phaseFailed"), tone: "text-red-600" };
    default:
      return null;
  }
});
</script>

<template>
  <section class="border border-border rounded-md bg-card p-4">
    <h2 class="text-[14px] font-semibold mb-2">
      {{ t("settings.adminGlobal.versionUpdates") }}
    </h2>
    <p class="text-[12px] text-muted-foreground mb-3">
      {{ t("settings.adminGlobal.currentlyRunning") }}
      <code
        class="px-1 py-0.5 bg-accent rounded text-[11px] font-mono"
        data-testid="running-version"
        >{{ runningVersion || t("settings.adminGlobal.devVersion") }}</code
      >
    </p>

    <p v-if="!enabled" class="text-[12px] text-muted-foreground italic">
      {{ t("settings.adminGlobal.upgradeNotConfigured") }}
    </p>

    <div v-else class="space-y-3">
      <!-- Busy banner: shown whenever a Claude/Codex job is in flight.
           The watchdog restart that an upgrade triggers would kill
           those processes, so we block the Update button (the Force
           button below is the explicit override). -->
      <div
        v-if="isBusy"
        class="rounded-md border border-amber-300/60 bg-amber-50 px-3 py-2 text-[12px] text-amber-900 dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-100"
      >
        <p class="font-medium">
          {{ t("settings.adminGlobal.busyCount", { count: runningJobCount }) }}
          <span v-if="queuedJobCount" class="font-normal">
            {{ t("settings.adminGlobal.busyQueued", { count: queuedJobCount }) }}
          </span>
          <span v-if="backgroundJobCount" class="font-normal">
            {{ t("settings.adminGlobal.busyBackground", { count: backgroundJobCount }) }}
          </span>
        </p>
        <ul v-if="visibleBusyJobs.length" class="mt-1.5 space-y-1">
          <li
            v-for="job in visibleBusyJobs"
            :key="job.id"
            class="flex flex-wrap items-center gap-x-2 gap-y-0.5"
          >
            <span class="font-medium">{{ busyJobOwner(job) }}</span>
            <code class="rounded bg-amber-100 px-1 py-0.5 text-[11px] dark:bg-amber-900/70">{{
              busyJobAccount(job)
            }}</code>
            <span
              v-if="isJobQueued(job)"
              class="rounded bg-amber-200/80 px-1 py-0.5 text-[11px] font-medium text-amber-900 dark:bg-amber-800/70 dark:text-amber-100"
            >
              {{ t("settings.adminGlobal.busyStatusQueued") }}
            </span>
            <span
              v-else-if="isJobWaiting(job)"
              class="rounded bg-sky-100 px-1 py-0.5 text-[11px] font-medium text-sky-900 dark:bg-sky-900/60 dark:text-sky-100"
            >
              {{ t("settings.adminGlobal.busyStatusWaiting") }}
            </span>
            <span
              v-else-if="isJobBackground(job)"
              class="rounded bg-violet-100 px-1 py-0.5 text-[11px] font-medium text-violet-900 dark:bg-violet-900/60 dark:text-violet-100"
            >
              {{ t("settings.adminGlobal.busyStatusBackground") }}
            </span>
            <span v-if="job.started_at" class="text-amber-800/80 dark:text-amber-200/80">
              {{ t("settings.adminGlobal.busySince", { date: formatLocalTime(job.started_at) }) }}
            </span>
          </li>
        </ul>
        <p class="mt-0.5 text-amber-800/90 dark:text-amber-200/90">
          {{ t("settings.adminGlobal.busyHint") }}
        </p>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          :disabled="checking || applying || isValidating"
          @click="onCheck"
        >
          {{
            checking
              ? t("settings.adminGlobal.checking")
              : t("settings.adminGlobal.checkForUpdates")
          }}
        </Button>
        <Button v-if="upgradeCheck?.has_update" size="sm" :disabled="!canApply" @click="onApply">
          {{
            applying
              ? t("settings.adminGlobal.upgrading")
              : t("settings.adminGlobal.updateTo", { version: upgradeCheck.latest_version })
          }}
        </Button>
        <Button
          variant="outline"
          size="sm"
          :disabled="checking || applying || restarting || isValidating"
          @click="onRestartService"
        >
          {{
            restarting
              ? t("settings.adminGlobal.restarting")
              : t("settings.adminGlobal.restartService")
          }}
        </Button>
      </div>

      <div v-if="upgradeCheck && upgradeCheck.has_update" class="text-[12px] text-muted-foreground">
        {{ t("settings.adminGlobal.latestAvailable") }}
        <code class="px-1 py-0.5 bg-accent rounded text-[11px]">{{
          upgradeCheck.latest_version
        }}</code>
        <span v-if="upgradeCheck.released_at">
          {{
            t("settings.adminGlobal.releasedAt", {
              date: formatLocalTime(upgradeCheck.released_at),
            })
          }}</span
        >
      </div>

      <!-- Recent releases — the 5 newest tags plus the newest tag of each of
           the 5 newest minor lines, with filtered commit subjects.
           chore:/docs:/ci: were dropped at release time; when a tag
           ends up with no remaining notes we show a "general
           improvements" placeholder so the row still says something. -->
      <div
        v-if="upgradeCheck && upgradeCheck.recent_releases?.length"
        class="rounded-md border border-border bg-accent/30 p-3"
        data-testid="recent-releases"
      >
        <h3 class="text-[12px] font-semibold mb-2">
          {{ t("settings.adminGlobal.recentReleases") }}
        </h3>
        <ul class="space-y-3">
          <li
            v-for="release in upgradeCheck.recent_releases"
            :key="release.version"
            class="text-[12px]"
          >
            <div class="flex items-baseline gap-2">
              <code class="px-1 py-0.5 bg-accent rounded text-[11px] font-mono">{{
                release.version
              }}</code>
              <span v-if="release.released_at" class="text-muted-foreground">
                {{ formatLocalTime(release.released_at) }}
              </span>
            </div>
            <p v-if="!release.notes?.length" class="mt-1 text-muted-foreground italic">
              {{ t("settings.adminGlobal.generalImprovements") }}
            </p>
            <ul v-else class="mt-1 space-y-0.5 pl-4 list-disc text-foreground/90">
              <li v-for="note in release.notes" :key="note.hash">
                <span>{{ note.subject }}</span>
                <code
                  class="ml-1 px-1 py-0.5 bg-accent rounded text-[10px] font-mono text-muted-foreground"
                  >{{ note.hash }}</code
                >
              </li>
            </ul>
          </li>
        </ul>
      </div>

      <p v-if="upgradeMsg" class="text-[12px] text-muted-foreground">{{ upgradeMsg }}</p>
      <p v-if="upgradeErr" class="text-[12px] text-red-600">{{ upgradeErr }}</p>
      <p v-if="phaseLabel" class="text-[12px]" :class="phaseLabel.tone">
        {{ phaseLabel.text }}
      </p>
    </div>
  </section>
</template>
