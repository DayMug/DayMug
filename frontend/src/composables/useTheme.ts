import { ref } from "vue";

const isDark = ref(window.matchMedia("(prefers-color-scheme: dark)").matches);

function toggleTheme() {
  isDark.value = !isDark.value;
}

export function useTheme() {
  return { isDark, toggleTheme };
}
