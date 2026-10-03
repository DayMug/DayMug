import { ref } from "vue";
import { fetchHelpDoc } from "./apiHelpDoc";

// Module-level singletons: every component that imports useHelpDoc shares
// the same markdown ref, so a save from AdminGlobal.vue propagates to the
// sidebar's "?" button without a page reload. The fetch happens at most
// once per page load; admin saves update the ref directly via
// setHelpDocMarkdown.
const markdown = ref<string>("");
let loaded = false;
let loadingPromise: Promise<void> | null = null;

async function load(): Promise<void> {
  if (loaded) return;
  if (loadingPromise) return loadingPromise;
  loadingPromise = (async () => {
    try {
      const doc = await fetchHelpDoc();
      markdown.value = doc.markdown;
    } catch {
      // Non-fatal — the sidebar simply hides the help button.
    } finally {
      loaded = true;
      loadingPromise = null;
    }
  })();
  return loadingPromise;
}

function setHelpDocMarkdown(value: string) {
  markdown.value = value;
  loaded = true;
}

export function useHelpDoc() {
  return { markdown, loadHelpDoc: load, setHelpDocMarkdown };
}
