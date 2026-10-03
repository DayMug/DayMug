import { createRouter, createWebHistory } from "vue-router";
import ChatPage from "@/pages/ChatPage.vue";

declare module "vue-router" {
  interface RouteMeta {
    layout?: "bare";
  }
}

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: "/",
      name: "home",
      redirect: () => ({ name: isMobileViewport() ? "conversations" : "chat" }),
    },
    {
      path: "/conversations",
      name: "conversations",
      component: ChatPage,
      beforeEnter: () => (isMobileViewport() ? true : { name: "chat" }),
    },
    {
      path: "/chat/:userId?/:conversationId?",
      name: "chat",
      component: ChatPage,
    },
    {
      path: "/file/:userId",
      name: "file",
      component: () => import("@/pages/PreviewPage.vue"),
      props: true,
      meta: { layout: "bare" },
    },
    {
      path: "/login",
      name: "login",
      component: () => import("@/pages/LoginPage.vue"),
      meta: { layout: "bare" },
    },
    {
      path: "/share/conversation/:token",
      name: "shared-conversation",
      component: () => import("@/pages/SharedConversationPage.vue"),
      meta: { layout: "bare" },
    },
    {
      path: "/setup/admin",
      name: "setup-admin",
      component: () => import("@/pages/SetupAdminPage.vue"),
      meta: { layout: "bare" },
    },
    {
      path: "/settings",
      component: () => import("@/pages/settings/SettingsLayout.vue"),
      children: [
        {
          path: "",
          name: "settings",
          component: () => import("@/pages/settings/SettingsHub.vue"),
        },
        {
          path: "system",
          name: "settings-system",
          component: () => import("@/pages/settings/SystemSettings.vue"),
        },
        {
          path: "users/add",
          name: "settings-users-add",
          component: () => import("@/pages/settings/UserAdd.vue"),
        },
        {
          path: "users/:id",
          name: "settings-users-edit",
          component: () => import("@/pages/settings/UserEdit.vue"),
        },
        {
          path: "account",
          name: "settings-account",
          component: () => import("@/pages/settings/AccountSettings.vue"),
        },
        {
          path: "advanced",
          name: "settings-advanced",
          component: () => import("@/pages/settings/AdvancedSettings.vue"),
        },
        {
          path: "notifications",
          name: "settings-notifications",
          component: () => import("@/pages/settings/NotificationsSettings.vue"),
        },
        {
          path: "crontab",
          name: "settings-crontab",
          component: () => import("@/pages/settings/CronSettings.vue"),
        },
        {
          path: "about",
          name: "settings-about",
          component: () => import("@/pages/settings/AboutSettings.vue"),
        },
        {
          path: "admin",
          name: "settings-admin",
          component: () => import("@/pages/settings/AdminPanel.vue"),
        },
        {
          path: "admin/users",
          name: "settings-admin-users",
          component: () => import("@/pages/settings/AdminUsers.vue"),
        },
        {
          path: "admin/global",
          name: "settings-admin-global",
          component: () => import("@/pages/settings/AdminGlobal.vue"),
        },
        {
          path: "admin/models",
          name: "settings-admin-models",
          component: () => import("@/pages/settings/AdminModels.vue"),
        },
        {
          path: "admin/terminal",
          name: "settings-admin-terminal",
          component: () => import("@/pages/settings/AdminTerminal.vue"),
        },
        {
          path: "usage",
          name: "settings-usage",
          component: () => import("@/pages/settings/UsageStats.vue"),
        },
      ],
    },
  ],
});

function isMobileViewport() {
  return window.matchMedia("(max-width: 767px)").matches;
}

export default router;
