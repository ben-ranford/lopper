import * as assert from "node:assert/strict";
import { suite, test } from "mocha";

import { RefreshSessionStore } from "../../refreshSession";

suite("refresh session store", () => {
  test("tracks latest run id so stale runs can be rejected", () => {
    const sessions = new RefreshSessionStore<string, void>();
    const folderKey = "workspace://one";

    const firstRunId = sessions.reserveRun(folderKey);
    const secondRunId = sessions.reserveRun(folderKey);

    assert.equal(firstRunId, 1);
    assert.equal(secondRunId, 2);
    assert.equal(sessions.isLatestRun(folderKey, firstRunId), false);
    assert.equal(sessions.isLatestRun(folderKey, secondRunId), true);
  });

  test("stores and retrieves in-flight runs by session key", async () => {
    const sessions = new RefreshSessionStore<string, string>();
    const folderKey = "workspace://one";
    const sessionKey = "workspace://one|js-ts|package";

    let resolve: (value: string) => void = () => undefined;
    const inFlightPromise = new Promise<string>((resolver) => {
      resolve = resolver;
    });

    const runId = sessions.reserveRun(folderKey);
    sessions.setInFlight(folderKey, sessionKey, runId, inFlightPromise);

    const inFlight = sessions.inFlight(folderKey, sessionKey);
    assert.ok(inFlight, "expected in-flight entry");
    assert.equal(inFlight?.runId, runId);

    resolve("done");
    assert.equal(await inFlight?.promise, "done");

    sessions.clearInFlight(folderKey, sessionKey, runId);
    assert.equal(sessions.inFlight(folderKey, sessionKey), undefined);
  });

  test("invalidates cache when input version changes", () => {
    const sessions = new RefreshSessionStore<{ id: string }, void>();
    const folderKey = "workspace://one";
    const sessionKey = "workspace://one|js-ts|repo";

    sessions.setCache(folderKey, sessionKey, { id: "report-1" });
    assert.equal(sessions.getCache(folderKey, sessionKey)?.value.id, "report-1");

    sessions.bumpInputVersion(folderKey);
    assert.equal(sessions.getCache(folderKey, sessionKey), undefined);
  });
});
