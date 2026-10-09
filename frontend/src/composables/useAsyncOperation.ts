import { ref, type Ref } from "vue";
import { errorMessage } from "@/lib/errorMessage";

export interface RunOptions {
  /**
   * Busy ref toggled while the operation runs. Pass a page-owned ref when
   * several operations share one error line but need distinct busy flags
   * (AdminUsers), or `false` when the flow never tracked busy at all
   * (pricing table load). Defaults to the operation's own `busy` ref.
   */
  busy?: Ref<boolean> | false;
  /**
   * Localized copy for a rejection that isn't an Error (see errorMessage).
   * An Error's own message always wins.
   */
  errorFallback?: string;
}

export interface AsyncOperation {
  busy: Ref<boolean>;
  error: Ref<string>;
  message: Ref<string>;
  run: <T>(fn: () => Promise<T>, opts?: RunOptions) => Promise<T | undefined>;
  reset: () => void;
}

// useAsyncOperation centralises the loading/error/success-message refs and
// the try/catch/finally choreography that every settings-page action used
// to hand-roll. It deliberately stays small: callers keep their own data
// refs and set `message` from inside `fn` when the success copy depends on
// the response — abstracting those proved to cost more than the
// boilerplate it saved.
//
// `run` resolves to the callback's value, or `undefined` when it threw (the
// error text is already surfaced in `error`), so success paths read as
// `const res = await run(...); if (!res) return;`.
export function useAsyncOperation(): AsyncOperation {
  const busy = ref(false);
  const error = ref("");
  const message = ref("");

  function reset(): void {
    error.value = "";
    message.value = "";
  }

  async function run<T>(fn: () => Promise<T>, opts?: RunOptions): Promise<T | undefined> {
    reset();
    const busyRef = opts?.busy === false ? null : (opts?.busy ?? busy);
    if (busyRef) busyRef.value = true;
    try {
      return await fn();
    } catch (e) {
      error.value = errorMessage(e, opts?.errorFallback);
      return undefined;
    } finally {
      if (busyRef) busyRef.value = false;
    }
  }

  return { busy, error, message, run, reset };
}
