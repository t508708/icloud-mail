import test from "node:test";
import assert from "node:assert/strict";
import { readFile, access } from "node:fs/promises";

const root = new URL("../../", import.meta.url);
const read = (path) => readFile(new URL(path, root), "utf8");

test("release version matches package metadata and public documentation", async () => {
  const version = (await read("VERSION")).trim();
  const manifest = JSON.parse(await read("web/package.json"));
  const lock = JSON.parse(await read("web/package-lock.json"));
  assert.match(version, /^\d+\.\d+\.\d+$/);
  assert.equal(manifest.version, version);
  assert.equal(lock.version, version);
  assert.equal(lock.packages[""].version, version);
  assert.deepEqual(lock.packages[""].dependencies, manifest.dependencies);
  assert.deepEqual(lock.packages[""].devDependencies, manifest.devDependencies);
  assert.ok((await read("README.md")).includes(`**当前版本：v${version}**`));
  assert.ok((await read("DELIVERY.md")).includes(`**v${version}**`));
  assert.ok((await read("CHANGELOG.md")).includes(`\n## v${version} - `));
  const offline = await read("compose.offline.yaml");
  assert.ok(offline.includes(`image: icloud-api:v${version}\n`));
  assert.ok(offline.includes(`image: icloud-api-postgres:v${version}\n`));
});

test("release documentation retains upstream attribution and license notice", async () => {
  for (const path of ["README.md", "NOTICE.md"]) {
    const source = await read(path);
    assert.ok(source.includes("https://github.com/mangobubu/icloud-api"));
    assert.ok(source.includes("t508708/icloud-mail"));
  }
  assert.ok((await read("README.md")).includes("[NOTICE.md](NOTICE.md)"));
});

test("local links in release documentation resolve after reference extraction", async () => {
  for (const path of ["README.md", "NOTICE.md", "DELIVERY.md", "CHANGELOG.md", "docs/REFERENCE.md"]) {
    const source = await read(path);
    for (const match of source.matchAll(/\[[^\]]*\]\(([^)\s]+)\)/g)) {
      const target = match[1];
      if (/^(?:https?:|mailto:|#)/.test(target)) continue;
      const url = new URL(target, new URL(path, root));
      url.hash = "";
      await assert.doesNotReject(access(url), `${path}: ${target}`);
    }
  }
});
