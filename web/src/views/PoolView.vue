<template>
  <el-config-provider :locale="zhCn">
    <section class="page-stack pool-page">
      <SectionHeader
        title="邮箱池"
        description="集中管理库存、项目接入和邮箱领取。"
      >
        <template #actions
          ><el-button :loading="loading" @click="refresh(true)"
            >刷新</el-button
          ></template
        >
      </SectionHeader>
      <RequestAlert
        v-if="error"
        :error="error"
        closable
        @close="error = null"
      />
      <el-tabs v-model="tab" @tab-change="refreshTab">
        <el-tab-pane label="邮箱库存" name="members">
          <div class="pool-toolbar">
            <el-select
              v-model="memberState"
              placeholder="全部状态"
              clearable
              @change="
                memberPage = 1;
                loadMembers();
              "
            >
              <el-option
                v-for="s in ['available', 'leased', 'used', 'paused']"
                :key="s"
                :label="stateName(s)"
                :value="s"
              />
            </el-select>
            <el-input
              v-model="search"
              placeholder="搜索邮箱"
              clearable
              @keyup.enter="
                memberPage = 1;
                loadMembers();
              "
            />
            <el-button
              @click="
                memberPage = 1;
                loadMembers();
              "
              >搜索</el-button
            >
            <el-button type="primary" @click="openEnroll"
              >加入已有邮箱</el-button
            >
          </div>
          <div class="pool-table-scroll" role="region" aria-label="邮箱库存列表" tabindex="0"><el-table
            :data="members.items"
            v-loading="loading"
            class="pool-table"
            empty-text="暂无库存。添加主号后，可加入已有邮箱或开启新邮箱自动入池。"
          >
            <el-table-column prop="address" label="邮箱" min-width="245" />
            <el-table-column prop="group_name" label="分组" min-width="110" />
            <el-table-column label="状态" width="135"
              ><template #default="{ row }"
                ><el-tag :type="stateType(row.state)">{{
                  stateName(row.state)
                }}</el-tag
                ><small v-if="!row.enabled"> 主号/邮箱停用</small></template
              ></el-table-column
            >
            <el-table-column label="更新时间" min-width="175"
              ><template #default="{ row }">{{
                date(row.updated_at)
              }}</template></el-table-column
            >
            <el-table-column label="操作" min-width="185"
              ><template #default="{ row }">
                <el-button
                  link
                  type="primary"
                  @click="showCredentials(row.alias_id)"
                  >凭据</el-button
                >
                <el-button
                  v-if="row.state === 'available' || row.state === 'paused'"
                  link
                  :disabled="busy"
                  @click="setMember(row)"
                  >{{
                    row.state === "paused" ? "恢复分配" : "暂停分配"
                  }}</el-button
                >
              </template></el-table-column
            >
          ></el-table></div>
          <el-pagination
            v-model:current-page="memberPage"
            :page-size="50"
            :total="members.total"
            layout="total, prev, pager, next"
            @current-change="loadMembers"
          />
        </el-tab-pane>

        <el-tab-pane label="领取记录" name="leases">
          <div class="pool-toolbar">
            <el-select
              v-model="leaseState"
              placeholder="全部状态"
              clearable
              @change="
                leasePage = 1;
                loadLeases();
              "
              ><el-option
                v-for="s in ['leased', 'used', 'released', 'expired']"
                :key="s"
                :label="stateName(s)"
                :value="s"
            /></el-select>
            <el-select
              v-model="clientFilter"
              placeholder="全部项目"
              clearable
              @change="
                leasePage = 1;
                loadLeases();
              "
              ><el-option
                v-for="c in clients"
                :key="c.id"
                :label="c.project"
                :value="c.id"
            /></el-select>
          </div>
          <div class="pool-table-scroll" role="region" aria-label="领取记录列表" tabindex="0"><el-table
            :data="leases.items"
            v-loading="loading"
            class="pool-table"
            empty-text="项目通过 API 领取邮箱后，记录会显示在这里。"
          >
            <el-table-column prop="address" label="邮箱" min-width="230" />
            <el-table-column prop="project" label="项目" min-width="110" />
            <el-table-column label="状态" width="100"
              ><template #default="{ row }"
                ><el-tag :type="stateType(row.state)">{{
                  stateName(row.state)
                }}</el-tag></template
              ></el-table-column
            >
            <el-table-column label="领取 / 到期时间" min-width="185"
              ><template #default="{ row }"
                ><div>{{ date(row.created_at) }}</div>
                <small>{{
                  row.state === "used"
                    ? "已确认使用，保留分配"
                    : date(row.expires_at)
                }}</small></template
              ></el-table-column
            >
            <el-table-column
              prop="request_id"
              label="请求编号"
              min-width="145"
              show-overflow-tooltip
            />
            <el-table-column label="操作" min-width="175"
              ><template #default="{ row }">
                <el-button
                  v-if="row.state === 'leased'"
                  link
                  type="primary"
                  :disabled="busy"
                  @click="actLease(row, 'commit')"
                  >确认使用</el-button
                >
                <el-button
                  v-if="row.state === 'leased'"
                  link
                  type="danger"
                  :disabled="busy"
                  @click="actLease(row, 'release')"
                  >释放</el-button
                >
                <el-button
                  v-if="row.state === 'used'"
                  link
                  @click="showCredentials(row.alias_id)"
                  >凭据</el-button
                >
              </template></el-table-column
            >
          ></el-table></div>
          <el-pagination
            v-model:current-page="leasePage"
            :page-size="50"
            :total="leases.total"
            layout="total, prev, pager, next"
            @current-change="loadLeases"
          />
        </el-tab-pane>

        <el-tab-pane label="项目 API Key" name="clients">
          <div class="pool-toolbar">
            <el-input
              v-model="projectName"
              placeholder="项目名称，如 my-app"
              maxlength="80"
            /><el-button type="primary" :loading="busy" @click="createClient"
              >创建项目 Key</el-button
            >
          </div>
          <p class="pool-hint">
            每个项目使用独立 Key 领取邮箱，只能读取自己的领取记录。项目 Key
            仅在创建或轮换时展示。
          </p>
          <div class="pool-table-scroll" role="region" aria-label="项目 API Key 列表" tabindex="0"><el-table
            :data="clients"
            v-loading="loading || clientsLoading"
            class="pool-table"
            empty-text="创建第一个项目，将 Key 配置到调用程序。"
          >
            <el-table-column prop="project" label="项目" min-width="150" />
            <el-table-column label="状态" width="100"
              ><template #default="{ row }"
                ><el-tag :type="row.enabled ? 'success' : 'info'">{{
                  row.enabled ? "启用" : "停用"
                }}</el-tag></template
              ></el-table-column
            >
            <el-table-column label="创建时间" min-width="185"
              ><template #default="{ row }">{{
                date(row.created_at)
              }}</template></el-table-column
            >
            <el-table-column label="操作" min-width="180"
              ><template #default="{ row }"
                ><el-button
                  link
                  type="primary"
                  :disabled="busy"
                  @click="updateClient(row, true)"
                  >轮换 Key</el-button
                ><el-button
                  link
                  :disabled="busy"
                  @click="updateClient(row, false)"
                  >{{ row.enabled ? "停用" : "启用" }}</el-button
                ></template
              ></el-table-column
            >
          ></el-table></div>
          <p class="pool-hint">
            停用项目会停止其邮箱池 API
            访问。已发放的单邮箱凭据可在「隐私邮箱」中单独轮换或停用。
          </p>
        </el-tab-pane>

        <el-tab-pane label="项目共用邮箱" name="shared">
          <div class="pool-toolbar">
            <el-select
              v-model="sharedClientId"
              placeholder="选择项目"
              filterable
              :disabled="loading || busy || sharedExporting"
              @change="changeSharedSelection"
            >
              <el-option
                v-for="client in clients"
                :key="client.id"
                :label="client.project + (client.enabled ? '' : '（已停用）')"
                :value="client.id"
              />
            </el-select>
            <el-select
              v-model="sharedAccountId"
              placeholder="选择启用的 iCloud 主号"
              filterable
              :disabled="loading || busy || sharedExporting"
              @change="changeSharedSelection"
            >
              <el-option
                v-for="account in sharedAccounts"
                :key="account.id"
                :label="account.email"
                :value="account.id"
              />
            </el-select>
            <el-button
              type="primary"
              :icon="Link"
              :loading="busy"
              :disabled="!sharedCanAssociate || loading || sharedLoading || sharedExporting"
              @click="bindSharedMailboxes"
            >关联全部 HME</el-button>
            <el-button
              :icon="Download"
              :loading="sharedExporting"
              :disabled="!sharedHasSelection || loading || sharedLoading || busy || sharedMailboxes.total === 0"
              @click="downloadSharedMailboxes"
            >下载 TXT</el-button>
          </div>
          <div class="pool-table-scroll" role="region" aria-label="项目共用邮箱列表" tabindex="0">
            <el-table
              :data="sharedMailboxes.items"
              v-loading="loading || sharedLoading"
              class="pool-table"
              empty-text="所选项目和主号暂无共用邮箱。"
            >
              <el-table-column prop="address" label="邮箱" min-width="260" />
              <el-table-column label="来源" min-width="135">
                <template #default="{ row }">{{ sharedSourceName(row.source) }}</template>
              </el-table-column>
              <el-table-column label="操作" width="130">
                <template #default="{ row }">
                  <el-button
                    v-if="row.source === 'shared'"
                    link
                    type="danger"
                    :icon="Delete"
                    :disabled="loading || sharedLoading || sharedExporting || busy"
                    @click="removeSharedMailbox(row)"
                  >移除关联</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
          <el-pagination
            v-model:current-page="sharedPage"
            :page-size="sharedPageSize"
            :total="sharedMailboxes.total"
            :disabled="loading || sharedLoading || sharedExporting || busy || !sharedHasSelection"
            layout="total, prev, pager, next"
            @current-change="loadSharedMailboxes()"
          />
        </el-tab-pane>

        <el-tab-pane label="库存与定时创建" name="accounts">
          <p class="pool-hint">
            开启自动入池后，新创建或新同步的邮箱会进入库存；已有邮箱在「邮箱库存」中手动选择加入。空闲目标为
            0
            时按计划创建；达到正数目标时跳过创建，领取后自动恢复。手动批量创建和单次探测均不使用本地创建额度，成功后不额外等待；仍受 Apple 实际响应影响。自动计划每主号滚动 1 小时最多 23 次：Apple Account 新通道 19 次、iCloud Web 旧通道 4 次，至少间隔 2 分钟并错峰执行。仅在明确限流且未产生地址时切换通道；结果不确定、含 HME 或目录确认中的尝试不会自动补造。明确限流默认暂停对应通道 1 小时，更长 Retry-After 以 Apple 为准。本地上限不代表 Apple 官方配额保证。
          </p>
          <el-table
            :data="accounts"
            empty-text="先到「主号管理」添加账号，并完成 Apple 登录。"
          >
            <el-table-column label="主号" min-width="240"
              ><template #default="{ row }"
                ><router-link
                  :to="{
                    name: 'account-detail',
                    params: { id: row.account_id },
                  }"
                  >{{ row.email }}</router-link
                ></template
              ></el-table-column
            >
            <el-table-column label="新邮箱入池" width="120"
              ><template #default="{ row }"
                ><el-switch v-model="row.auto_enroll" /></template
            ></el-table-column>
            <el-table-column label="空闲目标" width="165"
              ><template #default="{ row }"
                ><el-input-number
                  v-model="row.target"
                  :min="0"
                  :max="10000"
                  :step="10"
                  controls-position="right"
                  style="width: 140px" /></template
            ></el-table-column>
            <el-table-column prop="available" label="空闲" width="75" />
            <el-table-column label="定时创建" min-width="200"
              ><template #default="{ row }"
                ><el-switch
                  :model-value="row.auto_create"
                  :disabled="busy"
                  @change="toggleAutoCreate(row, $event)"
                />
                <div>
                  <small>{{
                    row.auto_create
                      ? "下次检查：" + date(row.next_run_at)
                      : "已关闭"
                  }}</small>
                </div>
                <small v-if="row.creation_status === 'cooldown'" class="pool-cooldown">创建暂停中；请在主号详情查看是本地预算等待还是 Apple 限流</small>
                <small v-else-if="row.last_error" class="pool-error">{{
                  row.last_error
                }}</small></template
              ></el-table-column
            >
            <el-table-column label="操作" width="90"
              ><template #default="{ row }"
                ><el-button
                  type="primary"
                  link
                  :disabled="busy"
                  @click="saveAccount(row)"
                  >保存</el-button
                ></template
              ></el-table-column
            >
          </el-table>
        </el-tab-pane>

        <el-tab-pane label="API 接入" name="api">
          <div class="pool-api">
            <h3>1. 领取邮箱及完整凭据</h3>
            <p>
              使用项目 Key。request_id
              请在请求前保存，网络重试使用相同编号和参数。每次领取 1–50
              个邮箱，库存不足时整批保持原状。
            </p>
            <pre>{{ claimExample }}</pre>
            <h3>2. 查询本次领取后的新验证码</h3>
            <pre>{{ codeExample }}</pre>
            <p>
              data.success 为 true 时读取 data.otp；no_code 时等待至少 3 秒再请求。
              同一邮箱共用取件限流，429 时按 Retry-After 重试。可以追加
              after=RFC3339 时间，只查询更晚的邮件。
            </p>
            <h3>3. 确认、释放或续期</h3>
            <pre>
POST /api/v1/pool/leases/LEASE_ID/commit
POST /api/v1/pool/leases/LEASE_ID/release
POST /api/v1/pool/leases/LEASE_ID/renew
续期 JSON: {"ttl_seconds":1800}</pre
            >
            <p>
              默认领取 30 分钟，支持 60–86400 秒。成功后 commit
              保留分配；release
              或到期回收会轮换邮箱整套凭据。已确认使用的邮箱保持占用。所有请求均带项目
              Bearer Key。
            </p>
            <h3>4. 单邮箱 API、IMAPS 和 OAuth</h3>
            <pre>
GET /api/v1/otp
Authorization: Bearer MAILBOX_API_KEY

POST /oauth2/v2.0/token
Content-Type: application/x-www-form-urlencoded
grant_type=refresh_token&amp;client_id=CLIENT_ID&amp;refresh_token=REFRESH_TOKEN</pre
            >
            <p>
              领取结果包含邮箱地址、API Key、IMAP 密码、client_id、refresh_token
              和取码路径。IMAPS
              使用邮箱地址作为用户名。已有邮件归档随邮箱保留；按本次任务取码请使用领取记录的
              code 接口。
            </p>
          </div>
        </el-tab-pane>
      </el-tabs>

      <el-dialog
        v-model="enrollOpen"
        title="加入已有邮箱"
        width="min(760px,94vw)"
        destroy-on-close
        :show-close="!enrollSubmitting"
        :close-on-click-modal="!enrollSubmitting"
        :close-on-press-escape="!enrollSubmitting"
      >
        <p>
          选择启用主号下已确认且凭据完整、当前可分配的邮箱加入池中。列表支持分页浏览，也可以搜索地址定位。
        </p>
        <RequestAlert v-if="error" :error="error" closable @close="error = null" />
        <p v-if="enrollProgress" class="pool-hint">{{ enrollProgress }}</p>
        <div class="pool-toolbar">
          <el-input
            v-model="enrollDraft"
            placeholder="搜索邮箱"
            :disabled="enrollLocked"
            @keyup.enter="applyEnrollSearch"
          /><el-button :disabled="enrollLocked" @click="applyEnrollSearch">搜索</el-button>
          <el-button :disabled="enrollLocked || !enrollItems.length" @click="selectCurrentPage">全选本页</el-button>
          <el-button :disabled="enrollLocked || !enrollItems.length" @click="invertEnrollPage">反选本页</el-button>
          <el-button :disabled="enrollLocked || !enrollTotal" :loading="enrollSelectingAll" @click="selectAllResults">全选搜索结果</el-button>
          <el-button :disabled="enrollLocked || !selected.length" @click="clearSelection">清空选择</el-button>
          <span class="pool-enrollment-count" aria-live="polite">已选 {{ selected.length }}</span>
        </div>
        <div ref="enrollSelectionContainer" @pointerdown.capture="enrollDrag.pointerDown">
          <el-table
            ref="enrollTable"
            row-key="id"
            :data="enrollItems"
            v-loading="enrollLoading || enrollSelectingAll"
            max-height="380"
            @selection-change="onEnrollSelectionChange"
        >
          <el-table-column
            type="selection"
            width="45"
            :selectable="(row) => !enrollLocked && isSelectable(row)"
          >
            <template #default="{ row }">
              <el-checkbox
                :model-value="selected.includes(row.id)"
                :disabled="enrollLocked || !isSelectable(row)"
                :data-selection-id="row.id"
                :data-selection-disabled="enrollLocked || !isSelectable(row)"
                :aria-label="`勾选 ${row.address}`"
                @change="setEnrollRow(row, $event)"
              />
            </template>
          </el-table-column>
          <el-table-column prop="address" label="邮箱" /><el-table-column
            prop="accountEmail"
            label="主号"
          />
          </el-table>
        </div>
        <div v-if="enrollTotal > 0" class="enroll-pagination">
          <span>共 {{ enrollTotal }} 条</span>
          <el-pagination
            background
            layout="prev, pager, next"
            size="small"
            :current-page="enrollPage"
            :page-size="enrollPageSize"
            :total="enrollTotal"
            :disabled="enrollLocked"
            :pager-count="5"
            @current-change="changeEnrollPage"
          />
        </div>
        <template #footer
          ><el-button :disabled="enrollSubmitting" @click="closeEnroll">取消</el-button
          ><el-button
            type="primary"
            :disabled="selected.length === 0 || enrollLocked"
            :loading="enrollSubmitting"
            @click="enroll"
            >加入 {{ selected.length }} 个邮箱</el-button
          ></template
        >
      </el-dialog>

      <el-dialog
        v-model="secretOpen"
        :title="secretTitle"
        width="min(730px,94vw)"
        @closed="secret = ''"
      >
        <el-input :model-value="secret" type="textarea" :rows="12" readonly />
        <template #footer
          ><el-button type="primary" @click="copySecret">复制</el-button
          ><el-button @click="secretOpen = false">关闭</el-button></template
        >
      </el-dialog>
    </section>
  </el-config-provider>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Download, Link } from "@element-plus/icons-vue";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import { apiRequest } from "../api/client.js";
import { getAliasPage, getAllAliases, getAllAccounts } from "../api/admin.js";
import { mergePoolPageSelection, poolAliasSelectable, submitPoolEnrollment } from "../utils/poolEnrollment.js";
import { useAuth } from "../stores/auth.js";
import SectionHeader from "../components/SectionHeader.vue";
import RequestAlert from "../components/RequestAlert.vue";
import { copyText } from "../utils/clipboard.js";
import { createCheckboxDragSelection } from "../utils/checkboxDragSelection.js";
import { buildProjectMailboxExportText, buildProjectMailboxFilename, collectProjectMailboxes } from "../utils/projectMailboxExport.js";

const auth = useAuth();
const tab = ref("members"),
  loading = ref(false),
  busy = ref(false),
  error = ref(null);
const members = ref({ items: [], total: 0 }),
  leases = ref({ items: [], total: 0 }),
  clients = ref([]),
  accounts = ref([]);
const clientsLoaded = ref(false);
const clientsLoading = ref(false);
let clientsInFlight;
const memberState = ref(""),
  leaseState = ref(""),
  search = ref(""),
  clientFilter = ref(""),
  projectName = ref("");
const memberPage = ref(1),
  leasePage = ref(1);
const sharedClientId = ref(""),
  sharedAccountId = ref(""),
  sharedAccounts = ref([]),
  sharedMailboxes = ref({ items: [], total: 0 }),
  sharedPage = ref(1),
  sharedPageSize = 50,
  sharedLoading = ref(false),
  sharedExporting = ref(false);
const sharedHasSelection = computed(() =>
  clients.value.some((client) => client.id === sharedClientId.value) &&
  sharedAccounts.value.some((account) => account.id === sharedAccountId.value),
);
const sharedCanAssociate = computed(() => sharedHasSelection.value &&
  clients.value.some((client) => client.id === sharedClientId.value && client.enabled),
);
const sharedSourceName = (source) => ({ shared: "共用关联", claimed: "原有独占领取" })[source] || "其他来源";
const enrollOpen = ref(false),
  enrollDraft = ref(""),
  enrollSearch = ref(""),
  enrollPage = ref(1),
  enrollPageSize = 50,
  enrollTotal = ref(0),
  enrollItems = ref([]),
  selected = ref([]),
  enrollTable = ref(null),
  enrollProgress = ref("");
const enabledAccountIds = ref(new Set());
const enrollLoading = ref(false), enrollSelectingAll = ref(false), enrollSubmitting = ref(false);
const enrollLocked = computed(() => busy.value || enrollLoading.value || enrollSelectingAll.value || enrollSubmitting.value);
let enrollTicket = 0;
let restoringEnrollSelection = false;
let enrollAbortController;
const enrollSelectionContainer = ref(null);
let enrollRestoreQueued = false;
const enrollDrag = createCheckboxDragSelection({
  getContainer: () => enrollSelectionContainer.value,
  getSelected: () => selected.value,
  isDisabled: () => enrollLocked.value || !enrollOpen.value,
  onChange: (ids) => {
    const disabled = new Set(enrollItems.value.filter((row) => !isSelectable(row)).map((row) => row.id));
    selected.value = ids.filter((id) => !disabled.has(id));
    queueEnrollRestore();
  },
});
function queueEnrollRestore() {
  if (enrollRestoreQueued) return;
  enrollRestoreQueued = true;
  void nextTick(async () => {
    try { await restoreEnrollSelection(); } finally { enrollRestoreQueued = false; }
  });
}
function setEnrollRow(row, checked) {
  if (enrollLocked.value || !isSelectable(row)) return;
  const ids = new Set(selected.value);
  if (checked) ids.add(row.id); else ids.delete(row.id);
  selected.value = [...ids];
  queueEnrollRestore();
}
async function invertEnrollPage() {
  if (enrollLocked.value) return;
  const ids = new Set(selected.value);
  for (const row of enrollItems.value.filter(isSelectable)) {
    if (ids.has(row.id)) ids.delete(row.id); else ids.add(row.id);
  }
  selected.value = [...ids];
  await restoreEnrollSelection();
}
const secretOpen = ref(false),
  secretTitle = ref(""),
  secret = ref("");
const stateName = (s) =>
  ({
    available: "空闲",
    leased: "已领取",
    used: "已使用",
    paused: "暂停分配",
    released: "已释放",
    expired: "已到期",
  })[s] || s;
const stateType = (s) =>
  ({
    available: "success",
    leased: "warning",
    used: "primary",
    paused: "info",
    released: "info",
    expired: "info",
  })[s] || "info";
const date = (value) =>
  value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "-";
const query = (params) =>
  new URLSearchParams(
    Object.entries(params).filter(([, v]) => v !== "" && v != null),
  ).toString();
const request = (path, method = "GET", body, signal) =>
  apiRequest(path, { method, body, signal, csrfToken: auth.state.csrfToken });
let poolRefreshTicket = 0;
let poolRefreshController;
let memberReadTicket = 0;
let leaseReadTicket = 0;
let sharedReadTicket = 0;
let sharedReadController;
async function attempt(fn) {
  try {
    return await fn();
  } catch (e) {
    if (e.name !== "AbortError") error.value = e;
    return undefined;
  }
}
async function loadMembers(signal) {
  const ticket = ++memberReadTicket;
  await attempt(async () => {
    const result = await request(
      "/pool/members?" +
        query({
          state: memberState.value,
          q: search.value,
          limit: 50,
          offset: (memberPage.value - 1) * 50,
        }), "GET", undefined, signal instanceof AbortSignal ? signal : undefined,
    );
    if (ticket === memberReadTicket) members.value = result;
  });
}
async function loadLeases(signal) {
  const ticket = ++leaseReadTicket;
  await attempt(async () => {
    const result = await request(
      "/pool/leases?" +
        query({
          state: leaseState.value,
          client_id: clientFilter.value,
          limit: 50,
          offset: (leasePage.value - 1) * 50,
        }), "GET", undefined, signal instanceof AbortSignal ? signal : undefined,
    );
    if (ticket === leaseReadTicket) leases.value = result;
  });
}
async function loadSharedContext(signal) {
  const accountsList = await getAllAccounts({ signal });
  if (signal?.aborted || tab.value !== "shared") return;
  sharedAccounts.value = accountsList.filter((account) => account.enabled && account.mailboxType === "icloud");
  if (!sharedAccounts.value.some((account) => account.id === sharedAccountId.value)) {
    sharedAccountId.value = "";
    sharedPage.value = 1;
  }
  if (!clients.value.some((client) => client.id === sharedClientId.value)) {
    sharedClientId.value = "";
    sharedPage.value = 1;
  }
  await loadSharedMailboxes(signal);
}
async function loadSharedMailboxes(signal) {
  sharedReadController?.abort();
  const controller = new AbortController();
  sharedReadController = controller;
  const ticket = ++sharedReadTicket;
  const abort = () => controller.abort();
  if (signal?.aborted) abort();
  else signal?.addEventListener("abort", abort, { once: true });
  sharedMailboxes.value = { items: [], total: 0 };
  sharedLoading.value = false;
  if (!sharedHasSelection.value) {
    signal?.removeEventListener("abort", abort);
    return;
  }
  const clientID = sharedClientId.value;
  const accountID = Number(sharedAccountId.value);
  sharedLoading.value = true;
  try {
    let result;
    do {
      result = await request(
        `/pool/projects/${encodeURIComponent(clientID)}/mailboxes?` + query({
          account_id: accountID,
          limit: sharedPageSize,
          offset: (sharedPage.value - 1) * sharedPageSize,
        }), "GET", undefined, controller.signal,
      );
      if (controller.signal.aborted || ticket !== sharedReadTicket) return;
      if (sharedPage.value > 1 && result.total <= (sharedPage.value - 1) * sharedPageSize) {
        sharedPage.value = Math.max(1, Math.ceil(result.total / sharedPageSize));
      } else break;
    } while (true);
    sharedMailboxes.value = result;
  } catch (e) {
    if (ticket === sharedReadTicket && e.name !== "AbortError") error.value = e;
  } finally {
    signal?.removeEventListener("abort", abort);
    if (ticket === sharedReadTicket) sharedLoading.value = false;
  }
}
function changeSharedSelection() {
  sharedPage.value = 1;
  void loadSharedMailboxes();
}
async function bindSharedMailboxes() {
  if (!sharedCanAssociate.value || sharedExporting.value || sharedLoading.value) return;
  await mutate(async () => {
    await ElMessageBox.confirm("关联所选主号下的全部隐藏邮箱到当前项目？已有共享关联保持原状。", "关联全部 HME");
    const result = await request(`/pool/projects/${encodeURIComponent(sharedClientId.value)}/mailboxes`, "POST", {
      account_id: Number(sharedAccountId.value),
    });
    ElMessage.success(`新增 ${result.added} 个关联，当前共 ${result.linked} 个`);
  });
}
async function removeSharedMailbox(row) {
  if (row?.source !== "shared" || !sharedHasSelection.value || sharedExporting.value || sharedLoading.value) return;
  await mutate(async () => {
    await ElMessageBox.confirm(`移除 ${row.address} 与当前项目的共享关联？`, "移除关联", { type: "warning" });
    await request(`/pool/projects/${encodeURIComponent(sharedClientId.value)}/mailboxes/${encodeURIComponent(row.alias_id)}`, "DELETE");
    ElMessage.success("关联已移除");
  });
}
async function downloadSharedMailboxes() {
  if (!sharedHasSelection.value || busy.value || sharedExporting.value || sharedLoading.value) return;
  const clientID = sharedClientId.value;
  const accountID = Number(sharedAccountId.value);
  const clientName = clients.value.find((client) => client.id === clientID)?.project;
  const accountEmail = sharedAccounts.value.find((account) => account.id === sharedAccountId.value)?.email;
  sharedExporting.value = true;
  error.value = null;
  try {
    const items = await collectProjectMailboxes(({ limit, offset }) => request(
      `/pool/projects/${encodeURIComponent(clientID)}/mailboxes?` + query({ account_id: accountID, limit, offset }),
    ), sharedPageSize);
    if (!items.length) {
      ElMessage.info("当前选择下没有关联邮箱");
      return;
    }
    const content = buildProjectMailboxExportText(items, location.origin);
    const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }));
    try {
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = buildProjectMailboxFilename(clientName, accountEmail);
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
    } finally {
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    }
  } catch (e) {
    error.value = e;
  } finally {
    sharedExporting.value = false;
  }
}
async function refresh(forceClients = false) {
  const ticket = ++poolRefreshTicket;
  poolRefreshController?.abort();
  const controller = new AbortController();
  poolRefreshController = controller;
  const activeTab = tab.value;
  if (activeTab !== "shared") {
    sharedReadController?.abort();
    ++sharedReadTicket;
    sharedLoading.value = false;
  }
  loading.value = true;
  error.value = null;
  try {
    const clientsPromise = forceClients || !clientsLoaded.value
      ? loadClients(forceClients).then((result) => {
        if (ticket === poolRefreshTicket) {
          clients.value = result;
          clientsLoaded.value = true;
        }
      })
      : Promise.resolve();
    await Promise.all([
      clientsPromise,
      activeTab === "members" ? loadMembers(controller.signal) :
        activeTab === "leases" ? loadLeases(controller.signal) :
          activeTab === "shared" ? clientsPromise.then(() => loadSharedContext(controller.signal)) :
          activeTab === "accounts" ? request("/pool/accounts", "GET", undefined, controller.signal).then((result) => {
            if (ticket === poolRefreshTicket) accounts.value = result;
          }) : Promise.resolve(),
    ]);
  } catch (e) {
    if (ticket === poolRefreshTicket && e.name !== "AbortError") error.value = e;
  } finally {
    if (ticket === poolRefreshTicket) loading.value = false;
  }
}
function loadClients(force = false) {
  if (clientsInFlight && !force) return clientsInFlight;
  clientsLoading.value = true;
  const promise = request("/pool/clients", "GET").finally(() => {
    if (clientsInFlight === promise) {
      clientsInFlight = undefined;
      clientsLoading.value = false;
    }
  });
  clientsInFlight = promise;
  return promise;
}
function refreshTab() {
  return refresh(false);
}
onBeforeUnmount(() => {
  ++poolRefreshTicket;
  ++memberReadTicket;
  ++leaseReadTicket;
  ++sharedReadTicket;
  poolRefreshController?.abort();
  sharedReadController?.abort();
});
async function mutate(fn) {
  if (busy.value) return;
  busy.value = true;
  error.value = null;
  try {
    await fn();
    await refresh(true);
  } catch (e) {
    if (e !== "cancel" && e !== "close") error.value = e;
  } finally {
    busy.value = false;
  }
}
function reveal(title, value) {
  secretTitle.value = title;
  secret.value =
    typeof value === "string" ? value : JSON.stringify(value, null, 2);
  secretOpen.value = true;
}
async function createClient() {
  await mutate(async () => {
    const result = await request("/pool/clients", "POST", {
      project: projectName.value,
    });
    reveal("项目 Key（本次显示）", result.api_key);
    projectName.value = "";
  });
}
async function updateClient(row, rotate) {
  await mutate(async () => {
    if (rotate)
      await ElMessageBox.confirm(
        "轮换后旧项目 Key 立即失效，领取记录保留。",
        "轮换项目 Key",
        { type: "warning" },
      );
    const result = await request("/pool/clients/" + row.id, "PATCH", {
      enabled: rotate ? row.enabled : !row.enabled,
      rotate,
    });
    if (rotate) reveal("新的项目 Key", result.api_key);
  });
}
async function setMember(row) {
  await mutate(() =>
    request("/pool/members/" + row.alias_id, "PATCH", {
      state: row.state === "paused" ? "available" : "paused",
    }),
  );
}
async function actLease(row, action) {
  await mutate(async () => {
    await ElMessageBox.confirm(
      action === "release"
        ? "释放后会轮换邮箱凭据，并重新进入空闲库存。"
        : "确认后该邮箱保留给当前项目。",
      action === "release" ? "释放邮箱" : "确认使用",
    );
    await request("/pool/leases/" + row.id + "/" + action, "POST");
  });
}
async function saveAccount(row) {
  await mutate(async () => {
    await request("/pool/accounts/" + row.account_id, "PUT", {
      auto_enroll: row.auto_enroll,
      target: row.target,
    });
    ElMessage.success("库存设置已保存");
  });
}
async function toggleAutoCreate(row, enabled) {
  await mutate(async () => {
    await request("/pool/accounts/" + row.account_id, "PUT", {
      auto_enroll: row.auto_enroll,
      target: row.target,
    });
    await request(
      "/accounts/" + row.account_id + "/aliases/auto-create",
      "PUT",
      { enabled },
    );
    row.auto_create = enabled;
  });
}
function beginEnrollRead() {
  enrollDrag.stop();
  enrollAbortController?.abort();
  const ticket = ++enrollTicket;
  enrollAbortController = new AbortController();
  return { signal: enrollAbortController.signal, current: () => enrollOpen.value && ticket === enrollTicket };
}

async function restoreEnrollSelection() {
  restoringEnrollSelection = true;
  try {
    await nextTick();
    const ids = new Set(selected.value);
    for (const row of enrollItems.value) enrollTable.value?.toggleRowSelection(row, ids.has(row.id));
    await nextTick();
  } finally {
    restoringEnrollSelection = false;
  }
}

async function loadEnroll({ includeAccounts = false } = {}) {
  const scope = beginEnrollRead();
  enrollLoading.value = true;
  error.value = null;
  try {
    const [result, accountList] = await Promise.all([
      getAliasPage("", { query: enrollSearch.value, enabled: true, limit: enrollPageSize, offset: (enrollPage.value - 1) * enrollPageSize, signal: scope.signal }),
      includeAccounts ? getAllAccounts({ signal: scope.signal }) : Promise.resolve(null),
    ]);
    if (!scope.current()) return;
    if (accountList) enabledAccountIds.value = new Set(accountList.filter((a) => a.enabled).map((a) => a.id));
    restoringEnrollSelection = true;
    enrollItems.value = result.items;
    enrollTotal.value = result.total;
    const stale = new Set(result.items.filter((row) => !isSelectable(row)).map((row) => row.id));
    selected.value = selected.value.filter((id) => !stale.has(id));
    await restoreEnrollSelection();
  } catch (e) {
    if (scope.current() && e.name !== "AbortError") error.value = e;
  } finally {
    if (scope.current()) enrollLoading.value = false;
  }
}

async function openEnroll() {
  if (busy.value || enrollOpen.value) return;
  enrollOpen.value = true;
  enrollPage.value = 1;
  enrollDraft.value = enrollSearch.value = enrollProgress.value = "";
  selected.value = [];
  enrollItems.value = [];
  enrollTotal.value = 0;
  enabledAccountIds.value = new Set();
  await loadEnroll({ includeAccounts: true });
}

function closeEnroll() {
  if (!enrollSubmitting.value) enrollOpen.value = false;
}

function resetEnroll() {
  enrollDrag.stop();
  ++enrollTicket;
  enrollAbortController?.abort();
  selected.value = [];
  enrollItems.value = [];
  enrollLoading.value = enrollSelectingAll.value = false;
}
watch(enrollOpen, (open) => { if (!open) resetEnroll(); }, { flush: "sync" });
onBeforeUnmount(resetEnroll);

function isSelectable(row) {
  return poolAliasSelectable(row, enabledAccountIds.value);
}

function onEnrollSelectionChange(rows) {
  if (restoringEnrollSelection || enrollLocked.value || !enrollOpen.value) return;
  selected.value = mergePoolPageSelection(selected.value, enrollItems.value, rows.filter(isSelectable));
}

async function selectCurrentPage() {
  if (enrollLocked.value) return;
  selected.value = [...new Set([...selected.value, ...enrollItems.value.filter(isSelectable).map((row) => row.id)])];
  await restoreEnrollSelection();
}

async function clearSelection() {
  if (enrollLocked.value) return;
  enrollDrag.stop();
  selected.value = [];
  await restoreEnrollSelection();
}

async function applyEnrollSearch() {
  if (enrollLocked.value) return;
  enrollSearch.value = enrollDraft.value.trim();
  enrollPage.value = 1;
  selected.value = [];
  await loadEnroll();
}

async function selectAllResults() {
  if (enrollLocked.value) return;
  const scope = beginEnrollRead();
  enrollSelectingAll.value = true;
  error.value = null;
  try {
    const items = await getAllAliases("", { query: enrollSearch.value, enabled: true, signal: scope.signal });
    if (!scope.current()) return;
    selected.value = [...new Set(items.filter(isSelectable).map((row) => row.id))];
    await restoreEnrollSelection();
  } catch (e) {
    if (scope.current() && e.name !== "AbortError") error.value = e;
  } finally {
    if (scope.current()) enrollSelectingAll.value = false;
  }
}

async function changeEnrollPage(page) {
  if (enrollLocked.value) return;
  enrollPage.value = Math.max(1, Number(page) || 1);
  await loadEnroll();
}

async function enroll() {
  if (enrollLocked.value || !selected.value.length) return;
  enrollDrag.stop();
  const ticket = enrollTicket;
  const current = () => enrollOpen.value && ticket === enrollTicket;
  enrollSubmitting.value = true;
  error.value = null;
  try {
    await submitPoolEnrollment(selected.value,
      (ids) => request("/pool/members", "POST", { alias_ids: ids }),
      async ({ done, total, remaining }) => {
        selected.value = remaining;
        enrollProgress.value = `已完成 ${done}/${total}`;
        await restoreEnrollSelection();
      }, current);
    if (current()) {
      enrollOpen.value = false;
      await refresh(true);
    }
  } catch (e) {
    if (current()) error.value = e;
  } finally {
    enrollSubmitting.value = false;
  }
}
async function showCredentials(id) {
  await attempt(async () => {
    const result = await request("/aliases/" + id);
    const a = result.alias || result;
    reveal("邮箱完整凭据", {
      email: a.address,
      api_key: a.api_key,
      imap_password: a.imap_password,
      client_id: a.client_id,
      refresh_token: a.refresh_token,
      otp_url: new URL(a.otp_url_path, location.origin).href,
    });
  });
}
async function copySecret() {
  await attempt(async () => {
    await copyText(secret.value);
    ElMessage.success("已复制");
  });
}
const claimExample = computed(
  () =>
    `curl '${location.origin}/api/v1/pool/claim' \\\n  -H 'Authorization: Bearer PROJECT_KEY' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"request_id":"job-20260912-0001","count":1,"ttl_seconds":1800}'`,
);
const codeExample = computed(
  () =>
    `curl '${location.origin}/api/v1/pool/leases/LEASE_ID/code' \\\n  -H 'Authorization: Bearer PROJECT_KEY'`,
);
onMounted(() => refresh(true));
</script>

<style scoped>
.pool-page {
  min-width: 0;
}
.pool-table-scroll {
  min-width: 0;
  max-width: 100%;
  overflow-x: auto;
  overscroll-behavior-x: contain;
}
.pool-table-scroll .pool-table {
  min-width: 760px;
}
.pool-toolbar {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  margin: 16px 0;
}
.pool-toolbar .el-select,
.pool-toolbar .el-input {
  width: 230px;
}
.pool-hint {
  line-height: 1.8;
  color: var(--el-text-color-secondary);
  margin: 14px 0 20px;
}

.enroll-pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
  color: var(--text-secondary);
  font-size: 13px;
}

@media (max-width: 560px) {
  .enroll-pagination {
    align-items: flex-start;
    flex-direction: column;
  }
}
.pool-error {
  color: var(--el-color-danger);
}
.pool-cooldown { color: var(--el-color-success); }
.el-pagination {
  margin-top: 20px;
  overflow-x: auto;
}
.pool-api {
  max-width: 880px;
  line-height: 1.8;
}
.pool-api h3 {
  margin: 24px 0 8px;
}
.pool-api pre {
  padding: 18px;
  background: var(--el-fill-color-light);
  border-radius: 8px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 13px;
}
small {
  color: var(--el-text-color-secondary);
}
.pool-page :deep(.el-table__empty-text) {
  line-height: 1.8;
  padding: 20px 0;
  width: 80%;
}
</style>
