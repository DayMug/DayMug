import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

const controlType = "daymug_control";

// The SDK expands mcpServers into `--mcp-config {json}`. Environment-backed
// MCP headers therefore become visible in /proc/<pid>/cmdline to every process
// running as the DayMug account. The supported spawn hook is late enough to
// replace that one argument with a private file before Claude is created.
function protectMcpConfigArg(args) {
  const protectedArgs = [...args];
  let configDir = "";

  for (let index = 0; index < protectedArgs.length; index += 1) {
    let valueIndex = -1;
    let value = "";
    if (protectedArgs[index] === "--mcp-config" && index + 1 < protectedArgs.length) {
      valueIndex = index + 1;
      value = protectedArgs[valueIndex];
    } else if (protectedArgs[index].startsWith("--mcp-config=")) {
      valueIndex = index;
      value = protectedArgs[index].slice("--mcp-config=".length);
    }
    if (valueIndex < 0 || !value.trimStart().startsWith("{")) continue;

    configDir ||= mkdtempSync(join(tmpdir(), "daymug-mcp-"));
    const configPath = join(configDir, "config.json");
    writeFileSync(configPath, value, { encoding: "utf8", mode: 0o600 });
    protectedArgs[valueIndex] =
      valueIndex === index ? `--mcp-config=${configPath}` : configPath;
  }

  return {
    args: protectedArgs,
    cleanup() {
      if (configDir) rmSync(configDir, { recursive: true, force: true });
    },
  };
}

function spawnClaudeCodeProcess(options) {
  const protectedConfig = protectMcpConfigArg(options.args);
  let child;
  try {
    child = spawn(options.command, protectedConfig.args, {
      cwd: options.cwd,
      env: options.env,
      signal: options.signal,
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });
  } catch (error) {
    protectedConfig.cleanup();
    throw error;
  }

  // The custom-spawn interface does not expose stderr to the SDK. Forward it
  // to the bridge's stderr so the Go parent still drains diagnostics and the
  // child can never block on a full pipe.
  child.stderr.pipe(process.stderr, { end: false });
  let cleaned = false;
  const cleanup = () => {
    if (cleaned) return;
    cleaned = true;
    protectedConfig.cleanup();
  };
  child.once("exit", cleanup);
  child.once("error", cleanup);
  return child;
}

// DayMug resolves the SDK in Go and passes an absolute path, so the fallbacks
// below only run for hosts whose layout that search missed. `npm root -g`
// forks a process and is invisible inside a sandbox, hence last.
async function loadSDK() {
  const override = process.env.DAYMUG_CLAUDE_AGENT_SDK_MODULE;
  if (override) {
    return import(override.startsWith("/") ? pathToFileURL(override).href : override);
  }
  try {
    return await import("@anthropic-ai/claude-agent-sdk");
  } catch (localError) {
    try {
      const root = execFileSync("npm", ["root", "-g"], { encoding: "utf8" }).trim();
      return await import(pathToFileURL(join(root, "@anthropic-ai", "claude-agent-sdk", "sdk.mjs")).href);
    } catch {
      throw new Error(
        `cannot load @anthropic-ai/claude-agent-sdk; install it with npm install -g @anthropic-ai/claude-agent-sdk or set DAYMUG_CLAUDE_AGENT_SDK_MODULE (${localError.message})`,
      );
    }
  }
}

// Claude Code redirects a shell command's stdin to /dev/null only when the
// command text contains no `<` at all — a `<=` inside an rg pattern is enough
// to skip it. The command then inherits the CLI's own stdin, which under the
// SDK is the stream-json channel and never reaches EOF, so anything that falls
// back to reading stdin (rg or grep given no path) blocks forever. The CLI
// backgrounds it at the tool timeout and the conversation stays parked as
// "running" until the silence reaper fires. No tool command may legitimately
// read that channel, so every one gets /dev/null up front. Hooks fire for
// subagent tool calls too, which is where most of these hangs came from.
const stdinGuard = "exec 0</dev/null\n";

async function guardShellStdin(input) {
  const command = input?.tool_input?.command;
  if (typeof command !== "string" || command.startsWith(stdinGuard)) return {};
  return {
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      updatedInput: { ...input.tool_input, command: stdinGuard + command },
    },
  };
}

function buildOptions(req, abortController, questions) {
  const options = {
    abortController,
    includePartialMessages: true,
    permissionMode: "bypassPermissions",
    allowDangerouslySkipPermissions: true,
    settingSources: ["user", "project", "local"],
    hooks: { PreToolUse: [{ matcher: "Bash|Monitor", hooks: [guardShellStdin] }] },
  };
  // canUseTool, despite the CLAUDE_SDK_CAN_USE_TOOL_SHADOWED warning node
  // prints for this combination. The warning is about permission gating, which
  // bypassPermissions really does settle before the callback; AskUserQuestion
  // is not gated but routed — the callback IS the answer channel, and dropping
  // it (for a PreToolUse hook, as the warning suggests) removes the tool from
  // the model's registry entirely. Verified against SDK 0.3.232, both ways.
  if (req.enableUserQuestions) {
    options.canUseTool = async (toolName, input, toolOptions) => {
      if (toolName === "AskUserQuestion") return questions.ask(input, toolOptions);
      return { behavior: "allow", updatedInput: input };
    };
  } else {
    options.disallowedTools = ["AskUserQuestion"];
  }
  if (req.cwd) options.cwd = req.cwd;
  if (req.sessionId) options[req.resume ? "resume" : "sessionId"] = req.sessionId;
  if (req.model) options.model = req.model;
  if (req.systemPrompt) {
    options.systemPrompt = { type: "preset", preset: "claude_code", append: req.systemPrompt };
  }
  if (req.effort) options.effort = req.effort;
  if (req.readOnly) options.tools = [];
  if (req.mcpServers) {
    options.mcpServers = req.mcpServers;
    options.spawnClaudeCodeProcess = spawnClaudeCodeProcess;
  }

  // Minimal turns (title generation) answer one throwaway question. Loading
  // settings, MCP schemas, CLAUDE.md and the full tool registry for that costs
  // orders of magnitude more input tokens than the answer is worth, and the
  // session file it would leave behind is noise in the conversation's history.
  if (req.minimal) {
    options.settingSources = [];
    options.tools = [];
    options.mcpServers = {};
    options.persistSession = false;
    options.includePartialMessages = false;
    if (req.systemPrompt) options.systemPrompt = req.systemPrompt;
  }
  return options;
}

function sdkUserMessage(content, messageId) {
  return {
    type: "user",
    message: { role: "user", content },
    parent_tool_use_id: null,
    ...(messageId ? { uuid: messageId } : {}),
    origin: { kind: "human" },
  };
}

function createMessageQueue(initial) {
  const values = [initial];
  const waiters = [];
  let closed = false;
  return {
    push(value) {
      if (closed) return false;
      const waiter = waiters.shift();
      if (waiter) waiter({ value, done: false });
      else values.push(value);
      return true;
    },
    close() {
      if (closed) return;
      closed = true;
      while (waiters.length > 0) waiters.shift()({ value: undefined, done: true });
    },
    async *messages() {
      while (true) {
        if (values.length > 0) {
          yield values.shift();
          continue;
        }
        if (closed) return;
        const next = await new Promise((resolve) => waiters.push(resolve));
        if (next.done) return;
        yield next.value;
      }
    },
  };
}

function writeControl(frame) {
  process.stdout.write(`${JSON.stringify({ type: controlType, ...frame })}\n`);
}

function createQuestionBroker(signal) {
  const pending = new Map();
  let sequence = 0;

  function answer(frame) {
    const waiting = pending.get(frame.requestId);
    if (!waiting) {
      writeControl({
        action: "answer_ack",
        requestId: frame.requestId,
        accepted: false,
        error: "question is no longer active",
      });
      return;
    }
    pending.delete(frame.requestId);
    const answers = {};
    waiting.questions.forEach((question, index) => {
      answers[question.question] = (frame.answers?.[`q${index + 1}`] || []).join(", ");
    });
    waiting.resolve({ behavior: "allow", updatedInput: { ...waiting.input, answers } });
    writeControl({ action: "answer_ack", requestId: frame.requestId, accepted: true });
  }

  function close() {
    for (const waiting of pending.values()) {
      waiting.resolve({ behavior: "deny", message: "interactive question was cancelled" });
    }
    pending.clear();
  }

  signal.addEventListener("abort", close, { once: true });
  return {
    ask(input, toolOptions = {}) {
      const requestId = toolOptions.requestId || `question-${++sequence}`;
      const sourceQuestions = input.questions || [];
      const normalized = sourceQuestions.map((question, index) => ({
        id: `q${index + 1}`,
        header: question.header || "Question",
        question: question.question || "",
        multi_select: Boolean(question.multiSelect),
        allow_other: true,
        options: (question.options || []).map((option) => ({
          label: option.label,
          description: option.description || "",
        })),
      }));
      writeControl({ action: "question", requestId, questions: normalized });
      return new Promise((resolve) =>
        pending.set(requestId, { input, questions: sourceQuestions, resolve }),
      );
    },
    answer,
    close,
  };
}

// interrupt aborts the running turn without ending the query, so a resident
// bridge survives a Stop and its still-useful background tasks are cancelled
// with it. Each fallback is one step closer to today's behaviour: without
// interrupt_cancel_queued_v1 the queued wakeups survive, and without
// interrupt at all the abort ends the query and the process exits — which is
// exactly what a Stop did before residency existed.
async function interruptTurn(q) {
  try {
    await q.interrupt({ cancel_queued: true });
    return { accepted: true };
  } catch {
    try {
      await q.interrupt();
      return { accepted: true };
    } catch (error) {
      return { accepted: false, error: error?.message || String(error) };
    }
  }
}

async function readControls(lines, queue, questions, getQuery, abortController) {
  for await (const line of lines) {
    let frame;
    try {
      frame = JSON.parse(line);
    } catch {
      continue;
    }
    if (frame.type !== controlType) continue;
    if (frame.action === "answer") {
      questions.answer(frame);
      continue;
    }
    if (frame.action === "interrupt") {
      const q = getQuery();
      let result = { accepted: false, error: "no query is running" };
      if (q) {
        result = await interruptTurn(q);
        if (!result.accepted) abortController.abort();
      }
      writeControl({
        action: "interrupt_ack",
        messageId: frame.messageId,
        accepted: result.accepted,
        ...(result.accepted ? {} : { error: result.error }),
      });
      continue;
    }
    if (frame.action === "input") {
      const accepted = queue.push(sdkUserMessage(frame.content, frame.messageId));
      writeControl({
        action: "input_ack",
        messageId: frame.messageId,
        accepted,
        ...(accepted ? {} : { error: "input stream is no longer active" }),
      });
    }
  }
  queue.close();
}

async function main() {
  const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
  const iterator = lines[Symbol.asyncIterator]();
  const first = await iterator.next();
  if (first.done) throw new Error("missing bridge request");
  let req;
  try {
    req = JSON.parse(first.value);
  } catch (error) {
    throw new Error(`invalid bridge request: ${error.message}`);
  }

  const abortController = new AbortController();
  // DayMug cancels a turn by signalling the process group. Without a handler,
  // SIGTERM kills node outright and the SDK's own child is left to be reaped by
  // the group signal mid-write; aborting first lets it close the session file
  // cleanly, which is what the next --resume reads.
  const stop = () => abortController.abort();
  process.on("SIGTERM", stop);
  process.on("SIGINT", stop);

  const { query } = await loadSDK();
  const queue = createMessageQueue(sdkUserMessage(req.prompt, req.messageId));
  const questions = createQuestionBroker(abortController.signal);
  let running = null;
  const controls = readControls(
    { [Symbol.asyncIterator]: () => iterator },
    queue,
    questions,
    () => running,
    abortController,
  );
  try {
    running = query({ prompt: queue.messages(), options: buildOptions(req, abortController, questions) });
    for await (const message of running) {
      process.stdout.write(`${JSON.stringify(message)}\n`);
      // `result` is a TURN boundary, not the end of the query. Closing the
      // input stream here is what makes the process exit after one turn —
      // which also kills any background task the turn armed, so the wakeup
      // it was waiting for never arrives. A resident request leaves the
      // stream open and lets DayMug decide when the process should go.
      if (message.type === "result" && !req.resident) queue.close();
    }
  } finally {
    questions.close();
    queue.close();
    lines.close();
    await controls;
  }
}

// A rejected promise would otherwise print a raw V8 stack trace, which DayMug
// surfaces verbatim as the assistant's error. One line, no stack.
main().catch((error) => {
  process.stderr.write(`CAS bridge: ${error?.message || error}\n`);
  process.exitCode = 1;
});
