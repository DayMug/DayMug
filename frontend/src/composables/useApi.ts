// Barrel re-export for the API surface. Existing callers keep importing
// from `@/composables/useApi`; the per-domain modules below are the
// authoritative homes for each function. Add new endpoints to the
// matching domain file, not here.

export * from "./apiTypes";
export * from "./apiAuth";
export * from "./apiUsers";
export * from "./apiConversations";
export * from "./apiAdmin";
export * from "./apiUploads";
export * from "./apiServerInfo";
export * from "./apiUsage";
export * from "./apiHelpDoc";
export * from "./apiBootstrap";
export * from "./apiAppState";
export * from "./apiCron";
export * from "./apiMarketplace";
