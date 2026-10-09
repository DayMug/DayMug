import { createApp } from "vue";
import App from "./App.vue";
import router from "./router";
import { i18n, loadLocaleMessages, toSupportedLocale } from "./i18n";
import { setUnauthorizedHandler } from "./composables/apiClient";
import { vMermaid } from "./directives/mermaid";
import { installAppWiring } from "./composables/appWiring";
import { installUploadLeaveGuard } from "./composables/useWorkspaceUpload";
import "./assets/index.css";

setUnauthorizedHandler(() => {
  const current = router.currentRoute.value;
  if (current.name === "login") return;
  const redirect = current.fullPath && current.fullPath !== "/" ? current.fullPath : undefined;
  router.replace({ name: "login", query: redirect ? { redirect } : {} });
});

installAppWiring();
installUploadLeaveGuard(router);

const app = createApp(App).use(router).use(i18n);
app.directive("mermaid", vMermaid);
// Wait for the initial navigation to resolve before the first render.
// Without this the app mounts with `route.path === "/"` (the router's
// START_LOCATION) and only updates to the real URL a tick later — on
// /settings/* that flashes the chat sidebar + conversation list before
// the route-aware `v-if`s flip them off.
// The initial language's messages load alongside it, so a non-English UI
// doesn't paint in English first; a failed fetch still mounts (in English).
const initialMessages = loadLocaleMessages(toSupportedLocale(i18n.global.locale.value)).catch(
  (err: unknown) => console.error("[i18n] failed to load initial messages", err),
);
Promise.all([router.isReady(), initialMessages]).then(() => app.mount("#app"));
