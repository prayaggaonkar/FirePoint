// Official OpenAI SDK against FirePoint and the mock. No real API key.
import { readFileSync } from "node:fs";

import OpenAI from "openai";

const MODEL = "test";
const MESSAGES = [{ role: "user", content: "hi" }];
const TOOLS = [
  {
    type: "function",
    function: {
      name: "lookup",
      description: "Look up a value",
      parameters: {
        type: "object",
        properties: { q: { type: "string" } },
        required: ["q"],
      },
    },
  },
];

function envInt(name) {
  const raw = process.env[name];
  if (!raw) fail(`${name} is required`);
  return Number(raw);
}

function expectedText() {
  let text = "";
  for (let i = 0; i < envInt("MOCK_TOKENS"); i++) text += `t${i}`;
  return text;
}

function fullStreamSeconds() {
  const tokens = envInt("MOCK_TOKENS");
  const first = envInt("MOCK_FIRST_MS") / 1000;
  const interval = envInt("MOCK_INTERVAL_MS") / 1000;
  if (tokens < 1) return first;
  return first + Math.max(tokens - 1, 0) * interval;
}

function deltaText(chunk) {
  return chunk.choices?.[0]?.delta?.content ?? "";
}

function fail(message) {
  console.error(`FAIL ${message}`);
  process.exit(1);
}

function disconnects() {
  const path = process.env.MOCK_LOG;
  if (!path) fail("MOCK_LOG is required");
  const text = readFileSync(path, "utf8");
  return text.split("client disconnected").length - 1;
}

async function waitDisconnect(before) {
  const deadline = performance.now() + 2000;
  while (performance.now() < deadline) {
    if (disconnects() > before) return true;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  return false;
}

function client() {
  const baseURL = process.env.FIREPOINT_BASE_URL;
  if (!baseURL) fail("FIREPOINT_BASE_URL is required");
  return new OpenAI({ baseURL, apiKey: "firepoint-compat", maxRetries: 0, timeout: 10_000 });
}

async function testNonStream(openai) {
  const resp = await openai.chat.completions.create({ model: MODEL, messages: MESSAGES });
  const content = resp.choices[0]?.message?.content;
  if (content !== expectedText()) fail(`non-stream content ${JSON.stringify(content)}`);
  if (resp.choices[0].finish_reason !== "stop") {
    fail(`non-stream finish_reason ${resp.choices[0].finish_reason}`);
  }
  if (resp.model !== MODEL) fail(`non-stream model ${resp.model}`);
  console.log("ok non-stream");
}

async function testStream(openai) {
  const parts = [];
  const stream = await openai.chat.completions.create({
    model: MODEL,
    messages: MESSAGES,
    stream: true,
  });
  for await (const chunk of stream) {
    const text = deltaText(chunk);
    if (text) parts.push(text);
  }
  const joined = parts.join("");
  if (joined !== expectedText()) fail(`stream content ${JSON.stringify(joined)}`);
  if (parts.length !== envInt("MOCK_TOKENS")) {
    fail(`stream chunks ${parts.length}, want ${envInt("MOCK_TOKENS")}`);
  }
  console.log("ok stream");
}

async function testTools(openai) {
  const resp = await openai.chat.completions.create({
    model: MODEL,
    messages: MESSAGES,
    tools: TOOLS,
    tool_choice: "auto",
  });
  const content = resp.choices[0]?.message?.content;
  if (content !== expectedText()) fail(`tools content ${JSON.stringify(content)}`);
  console.log("ok tools");
}

function isAbort(err) {
  const name = `${err?.name || ""} ${err?.constructor?.name || ""}`;
  return /abort/i.test(name) || /abort/i.test(String(err));
}

async function testCancel(openai) {
  const before = disconnects();
  const started = performance.now();
  const parts = [];
  const controller = new AbortController();
  const stream = await openai.chat.completions.create(
    { model: MODEL, messages: MESSAGES, stream: true },
    { signal: controller.signal },
  );
  try {
    for await (const chunk of stream) {
      const text = deltaText(chunk);
      if (!text) continue;
      parts.push(text);
      controller.abort();
    }
  } catch (err) {
    if (!parts.length || !isAbort(err)) throw err;
    console.log(`ok cancel closed the stream (${err.constructor?.name || err.name})`);
  }
  const elapsed = (performance.now() - started) / 1000;
  const joined = parts.join("");
  const full = expectedText();
  if (!joined || !full.startsWith(joined) || joined === full) {
    fail(`cancel received ${JSON.stringify(joined)}, full stream is ${JSON.stringify(full)}`);
  }
  const limit = fullStreamSeconds() * 0.75;
  if (elapsed >= limit) {
    fail(`cancel took ${elapsed.toFixed(3)}s, full stream is ${fullStreamSeconds().toFixed(3)}s`);
  }
  if (!(await waitDisconnect(before))) fail("mock did not see the client disconnect");
  console.log(`ok cancel after ${JSON.stringify(joined)} in ${elapsed.toFixed(3)}s`);
}

const openai = client();
await testNonStream(openai);
await testStream(openai);
await testTools(openai);
await testCancel(openai);
console.log("js compat ok");
