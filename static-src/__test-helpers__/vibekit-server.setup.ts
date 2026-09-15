// Starts the vibekit binary named by SSE_FIXTURE for the e2e-sse project and hands its
// origin to the tests through `inject("vibekitURL")`; a no-op when the variable is unset,
// which is how the project skips itself on a machine with no binary (the same belt the
// library's own fixture suite wears). The binary has to be built with `-tags vibekit_test`:
// only that build mounts the SSE control surface the suite drives and honours
// VIBEKIT_TEST_PORT.
import { type ChildProcess, spawn } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { TestProject } from "vitest/node";

declare module "vitest" {
  export interface ProvidedContext {
    vibekitURL: string;
  }
}

const READY_TIMEOUT_MS = 20_000;
const EXIT_TIMEOUT_MS = 10_000;

// A kiro-cli that accepts the ACP spawn and never answers `initialize`. A prompt's
// bridge then sits in its handshake budget instead of failing, so the prompt's one Mutate
// is the ONLY chat:X mutation the fixture makes: without it a missing kiro-cli appends a
// bridge-failure event row about 1.5 s later, a second mutation that would make the digest
// answer `changed` for chat:X whatever the two frames were stamped with.
const STALLING_KIRO_CLI = '#!/bin/sh\ncase "$1" in acp) exec sleep 600 ;; *) exit 1 ;; esac\n';

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      probe.close(() => {
        if (address === null || typeof address === "string") {
          reject(new Error("could not read the probe listener's port"));
          return;
        }
        resolve(address.port);
      });
    });
  });
}

/** Polls the test route until it answers JSON. Not /api/health, which reports 503 until
 *  kiro-cli is installed. A 200 that is not JSON is the SPA shell: the binary was built
 *  without the tag and mounts no /api/test/ route. */
async function waitForReady(url: string, child: ChildProcess): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  let lastError = "not yet";
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`vibekit exited with ${String(child.exitCode)} before listening`);
    }
    try {
      const res = await fetch(`${url}/api/test/sse`);
      if (res.status === 200 && res.headers.get("content-type")?.includes("json") === true) {
        return;
      }
      if (res.status === 200) {
        child.kill("SIGTERM");
        throw new Error(
          `${url}/api/test/sse answered the page shell, not JSON: SSE_FIXTURE names a binary built without -tags vibekit_test`,
        );
      }
      lastError = `status ${String(res.status)}`;
    } catch (e: unknown) {
      if (e instanceof Error && e.message.startsWith(url)) {
        throw e;
      }
      lastError = e instanceof Error ? e.message : String(e);
    }
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 100);
    });
  }
  child.kill("SIGTERM");
  throw new Error(
    `vibekit did not answer /api/test/sse within ${String(READY_TIMEOUT_MS)} ms: ${lastError}`,
  );
}

function waitForExit(child: ChildProcess): Promise<void> {
  return new Promise((resolve) => {
    if (child.exitCode !== null) {
      resolve();
      return;
    }
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
    }, EXIT_TIMEOUT_MS);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

export default async function setup(
  project: TestProject,
): Promise<(() => Promise<void>) | undefined> {
  const binary = process.env["SSE_FIXTURE"];
  if (binary === undefined || binary === "") {
    console.warn(
      "[e2e-sse] SSE_FIXTURE is unset, the suite skips itself: `go build -tags vibekit_test -o /tmp/vibekit-test .` then `SSE_FIXTURE=/tmp/vibekit-test npx vitest --run --project e2e-sse`",
    );
    return undefined;
  }
  const scratch = mkdtempSync(join(tmpdir(), "vibekit-e2e-"));
  // The binary refuses to boot without its config dir and creates nothing above it.
  for (const dir of ["home", "config", "work", "bin"]) {
    mkdirSync(join(scratch, dir));
  }
  writeFileSync(join(scratch, "bin", "kiro-cli"), STALLING_KIRO_CLI);
  chmodSync(join(scratch, "bin", "kiro-cli"), 0o755);
  const port = await freePort();
  const url = `http://127.0.0.1:${String(port)}`;
  const child = spawn(binary, [], {
    env: {
      PATH: `${join(scratch, "bin")}:${process.env["PATH"] ?? ""}`,
      HOME: join(scratch, "home"),
      KIRO_CONFIG_DIR: join(scratch, "config"),
      KIRO_WORK_DIR: join(scratch, "work"),
      VIBEKIT_TEST_PORT: String(port),
    },
    stdio: ["ignore", "inherit", "inherit"],
  });
  try {
    await waitForReady(url, child);
  } catch (e: unknown) {
    await waitForExit(child);
    rmSync(scratch, { recursive: true, force: true });
    throw e;
  }
  project.provide("vibekitURL", url);
  return async () => {
    child.kill("SIGTERM");
    await waitForExit(child);
    rmSync(scratch, { recursive: true, force: true });
  };
}
