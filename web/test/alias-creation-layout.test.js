import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const componentPath = new URL("../src/components/AliasCreationPanel.vue", import.meta.url);
const viewPath = new URL("../src/views/AccountDetailView.vue", import.meta.url);

test("batch creation stays compact without progress or created-address summary", async () => {
  const source = await readFile(componentPath, "utf8");
  assert.doesNotMatch(source, /el-progress|<details|已创建\s*\{\{/);
  assert.match(source, /creation-job-panel__title/);
  assert.match(source, /creation-job-panel__apple/);
});

test("batch creation is a sibling section of the Apple directory section", async () => {
  const source = await readFile(viewPath, "utf8");
  const directory = source.indexOf('title="隐私邮箱"');
  const batch = source.indexOf("alias-creation-row");
  const batchSectionEnd = source.indexOf("</section>", batch);
  const following = source.indexOf('<section class="section-block">', batchSectionEnd);
  assert.ok(directory >= 0 && batch > directory && following > batch);
  assert.match(source, /v-if="!isCustomMailbox" class="section-block alias-creation-row"/);
});
