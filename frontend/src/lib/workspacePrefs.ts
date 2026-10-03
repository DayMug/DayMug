const UI_PREFS_KEY = "daymug-ui-prefs-v1";

export type WorkspaceViewMode = "grid" | "list";
export type WorkspaceUIPrefs = {
  showHiddenFiles?: boolean;
  viewMode?: WorkspaceViewMode;
};

export function loadWorkspaceUIPrefs(): WorkspaceUIPrefs {
  try {
    const raw = localStorage.getItem(UI_PREFS_KEY);
    return raw ? (JSON.parse(raw) as WorkspaceUIPrefs) : {};
  } catch {
    return {};
  }
}

export function saveWorkspaceUIPrefs(prefs: WorkspaceUIPrefs) {
  try {
    localStorage.setItem(UI_PREFS_KEY, JSON.stringify(prefs));
  } catch {
    // Quota / private mode: ignore.
  }
}
