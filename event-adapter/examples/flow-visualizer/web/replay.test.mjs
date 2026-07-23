// web/replay.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { normalizeConfig } from "./config.js";
import { createTrace, reduceLiveEvent } from "./state.js";
import { renderFlow } from "./render.js";

test("replays the normalized request-reply preset to eight completed steps", async () => {
  const config = normalizeConfig(JSON.parse(
    await fs.readFile(new URL("./generated-request-reply.json", import.meta.url), "utf8"),
  ));
  const events = (await fs.readFile(new URL("../fixtures/request-reply.jsonl", import.meta.url), "utf8"))
    .trim().split("\n").map(line => JSON.parse(line));
  const trace = events.reduce(
    (current, event) => reduceLiveEvent(current, event, config),
    createTrace(config, "req-demo-001"),
  );
  assert.equal([...trace.steps.values()].filter(step => step.status === "completed").length, 8);
  const html = renderFlow(config, trace, "live");
  assert.match(html, /Focused request: <code>req-demo-001<\/code>/);
  assert.equal((html.match(/state-completed/g) ?? []).length, 8);
  assert.match(html, /elapsedMs/);
});
