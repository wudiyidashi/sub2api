<script setup lang="ts">
import { ref, computed } from 'vue';
import { useConfigStore, type VirtualGroup } from '@/stores/config';
import { endpoints } from '@/api/endpoints';

const config = useConfigStore();

const pingState = ref<'idle' | 'ok' | 'fail'>('idle');
const pingMsg = ref('');
const importText = ref('');
const importMsg = ref('');

// Custom modal states for selecting keys
const editingGroup = ref<VirtualGroup | null>(null);
const keySearchQuery = ref('');
const showKeySelector = ref(false);

async function testConnection() {
  pingState.value = 'idle';
  pingMsg.value = '';
  try {
    const s = await endpoints.ping();
    pingState.value = 'ok';
    pingMsg.value = `已认证为 ${s.email}（role=${s.role}）。`;
    config.loadAllApiKeys();
  } catch (e) {
    pingState.value = 'fail';
    pingMsg.value = (e as Error).message;
  }
}

function addGroup() {
  const id = `g-${Date.now().toString(36)}`;
  const color = randColor();
  config.groups.push({ id, name: '新分组', color, apiKeyIds: [], note: '自定义人名分组' });
}

function removeGroup(id: string) {
  const idx = config.groups.findIndex((g) => g.id === id);
  if (idx >= 0) config.groups.splice(idx, 1);
}

// Open modal for key assignment
function openKeySelector(g: VirtualGroup) {
  editingGroup.value = g;
  keySearchQuery.value = '';
  showKeySelector.value = true;
}

// Filter keys based on search query
const filteredApiKeys = computed(() => {
  const q = keySearchQuery.value.trim().toLowerCase();
  if (!q) return config.allApiKeys;
  return config.allApiKeys.filter(k =>
    String(k.id).includes(q) ||
    (k.name && k.name.toLowerCase().includes(q))
  );
});

function isKeySelected(keyId: number): boolean {
  if (!editingGroup.value) return false;
  return editingGroup.value.apiKeyIds.includes(keyId);
}

function toggleKeySelection(keyId: number) {
  if (!editingGroup.value) return;
  const ids = editingGroup.value.apiKeyIds;
  const idx = ids.indexOf(keyId);
  if (idx >= 0) {
    ids.splice(idx, 1);
  } else {
    // Prevent duplicate assignment across different groups
    // (A key can only belong to one virtual group to prevent double counting)
    config.groups.forEach(g => {
      const gIdx = g.apiKeyIds.indexOf(keyId);
      if (gIdx >= 0) g.apiKeyIds.splice(gIdx, 1);
    });
    ids.push(keyId);
  }
}

// Quick Tool 1: Import from sub2api groups
async function importFromSub2apiGroups() {
  try {
    const groups = await endpoints.groupsAll();
    for (const g of groups) {
      if (config.groups.find((x) => x.name === g.name)) continue;
      const keys = await endpoints.groupAllApiKeys(g.id);
      config.groups.push({
        id: `from-sub-${g.id}`,
        name: g.name,
        color: randColor(),
        apiKeyIds: keys.map((k) => k.id).filter(id => !config.groups.some(group => group.apiKeyIds.includes(id))),
        note: `从 sub2api group#${g.id} 导入`
      });
    }
    config.loadAllApiKeys();
  } catch (e) {
    alert(`导入失败: ${(e as Error).message}`);
  }
}

// Quick Tool 2: Import from sub2api users
async function importFromSub2apiUsers() {
  try {
    const rankingRes = await endpoints.usersRanking({ limit: 100 });
    const users = rankingRes.ranking;

    const keysByUser: Record<number, number[]> = {};
    for (const k of config.allApiKeys) {
      if (k.user_id) {
        if (!keysByUser[k.user_id]) keysByUser[k.user_id] = [];
        keysByUser[k.user_id].push(k.id);
      }
    }

    for (const [userIdStr, keyIds] of Object.entries(keysByUser)) {
      const userId = Number(userIdStr);
      const userMeta = users.find(u => u.user_id === userId);
      const name = userMeta ? userMeta.email.split('@')[0] : `用户 #${userId}`;

      if (config.groups.find(x => x.name === name)) continue;

      config.groups.push({
        id: `from-user-${userId}`,
        name,
        color: randColor(),
        apiKeyIds: keyIds.filter(id => !config.groups.some(group => group.apiKeyIds.includes(id))),
        note: `从 sub2api user#${userId} (${userMeta?.email || '无邮箱'}) 自动导入`
      });
    }
  } catch (e) {
    alert(`从用户导入失败: ${(e as Error).message}`);
  }
}

// Quick Tool 3: Split unassigned keys individually
function createGroupForEachUnassignedKey() {
  const assignedIds = new Set(config.groups.flatMap(g => g.apiKeyIds));
  const unassignedKeys = config.allApiKeys.filter(k => !assignedIds.has(k.id));

  if (unassignedKeys.length === 0) {
    alert('暂无未分组密钥，无需分割。');
    return;
  }

  for (const k of unassignedKeys) {
    config.groups.push({
      id: `key-group-${k.id}`,
      name: k.name || `密钥 #${k.id}`,
      color: randColor(),
      apiKeyIds: [k.id],
      note: `基于未分组密钥 "${k.name}" 独立创建`
    });
  }
}

function exportJson() {
  const blob = new Blob([config.exportJson()], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `sub2api-stats-config-${new Date().toISOString().slice(0, 10)}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

function doImport() {
  if (!importText.value.trim()) return;
  const r = config.importJson(importText.value);
  importMsg.value = r.ok ? '导入成功' : `失败：${r.error}`;
  if (r.ok) {
    importText.value = '';
    config.loadAllApiKeys();
  }
}

function randColor(): string {
  const palette = ['#0066cc', '#34c759', '#ff9500', '#af52de', '#ff3b30', '#5856d6', '#ff2d55', '#30b0c7'];
  return palette[Math.floor(Math.random() * palette.length)];
}
</script>

<template>
  <div class="space-y-8 max-w-[960px] mx-auto pb-12">
    <div class="flex items-end justify-between">
      <div>
        <h1 class="text-xl font-bold tracking-tight text-text">系统设置</h1>
        <p class="text-xs text-muted mt-1">管理展示偏好与团队虚拟分组映射</p>
      </div>
      <div class="flex items-center gap-3">
        <span class="text-xs font-medium" :class="{
          'text-muted': config.syncState === 'idle',
          'text-amber-500': config.syncState === 'saving',
          'text-success': config.syncState === 'saved',
          'text-error': config.syncState === 'error',
        }">
          <span v-if="config.syncState === 'idle'">未保存</span>
          <span v-else-if="config.syncState === 'saving'">正在保存…</span>
          <span v-else-if="config.syncState === 'saved'">已同步到服务器</span>
          <span v-else>同步失败：{{ config.syncMsg }}</span>
        </span>
        <button class="btn-primary text-xs px-4 h-8 font-medium" @click="config.saveToBackend()">
          立即保存
        </button>
      </div>
    </div>

    <!-- Connection Settings Card -->
    <div class="apple-card p-6 space-y-4">
      <h2 class="text-sm font-semibold text-muted uppercase tracking-wider mb-2">服务连接</h2>

      <p class="text-xs text-muted leading-relaxed">
        复用主后台的管理员登录。查看全团队用量，分组与展示配置由管理员共享，无需额外配置密钥。
      </p>

      <div class="flex items-center gap-4 pt-2">
        <button class="btn-primary text-xs px-5 h-9 font-medium" @click="testConnection">
          测试连通性
        </button>
        <span v-if="pingState === 'ok'" class="text-success text-xs font-medium flex items-center gap-1.5">
          <span class="h-2 w-2 rounded-full bg-success"></span>
          {{ pingMsg }}
        </span>
        <span v-if="pingState === 'fail'" class="text-error text-xs font-medium flex items-center gap-1.5">
          <span class="h-2 w-2 rounded-full bg-error"></span>
          连接失败: {{ pingMsg }}
        </span>
      </div>
    </div>

    <!-- Display Settings Card -->
    <div class="apple-card p-6">
      <h2 class="text-sm font-semibold text-muted uppercase tracking-wider mb-4">展示与配置</h2>
      <div class="grid grid-cols-2 gap-4 md:grid-cols-5">
        <label class="block col-span-2 md:col-span-1">
          <span class="text-xs text-muted font-medium">统计时区</span>
          <input v-model="config.timezone" class="apple-input mt-1 w-full text-xs h-9 rounded-xl" />
        </label>

        <label class="block">
          <span class="text-xs text-muted font-medium">自动刷新 (秒, 0=关，最少 30)</span>
          <input v-model.number="config.refreshInterval" type="number" min="0" max="86400" class="apple-input mt-1 w-full text-xs h-9 rounded-xl" />
        </label>

        <label class="block">
          <span class="text-xs text-muted font-medium">展示货币</span>
          <select v-model="config.currency" class="apple-select mt-1 w-full text-xs h-9 rounded-xl">
            <option value="USD">USD ($)</option>

          </select>
        </label>

        <label class="block">
          <span class="text-xs text-muted font-medium">成本口径</span>
          <select v-model="config.costSource" class="apple-select mt-1 w-full text-xs h-9 rounded-xl">
            <option value="actual">实际成本 (actual)</option>
            <option value="total">名义成本 (total)</option>
          </select>
        </label>

        <label class="block">
          <span class="text-xs text-muted font-medium">界面主题</span>
          <select v-model="config.theme" class="apple-select mt-1 w-full text-xs h-9 rounded-xl">
            <option value="auto">跟随系统</option>
            <option value="light">明亮 (Light)</option>
            <option value="dark">深色 (Dark)</option>
          </select>
        </label>
      </div>
    </div>

    <!-- Group Management Card -->
    <div class="apple-card p-6 space-y-4">
      <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-4">
        <div>
          <h2 class="text-sm font-semibold text-muted uppercase tracking-wider">人名虚拟分组管理</h2>
          <p class="text-[10px] text-muted mt-0.5">将 API 密钥归集到对应的人名分组中以完成数据透视</p>
        </div>

        <div class="flex flex-wrap gap-2">
          <button class="btn-secondary text-xs px-4 h-8 font-medium" @click="addGroup">新增分组</button>
          <button class="btn-secondary text-xs px-4 h-8 font-medium" @click="importFromSub2apiGroups">
            从 Group 导入
          </button>
          <button class="btn-secondary text-xs px-4 h-8 font-medium" @click="importFromSub2apiUsers">
            从 User 导入
          </button>
          <button class="btn-secondary text-xs px-4 h-8 font-medium" @click="createGroupForEachUnassignedKey">
            分割未分组 key
          </button>
          <button class="btn-secondary text-xs px-4 h-8 font-medium" @click="exportJson">导出 JSON</button>
        </div>
      </div>

      <div class="overflow-x-auto border border-border rounded-2xl">
        <table class="apple-table text-left">
          <thead>
            <tr>
              <th class="w-1/4">分组人名</th>
              <th class="w-12">底色</th>
              <th>绑定 API 密钥 (Key IDs)</th>
              <th class="w-1/4">描述备注</th>
              <th class="w-16"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="g in config.groups" :key="g.id">
              <td class="px-3 py-3">
                <input v-model="g.name" class="apple-input w-full text-xs h-8 font-medium rounded-lg" placeholder="例如: 张三" />
              </td>
              <td class="px-3 py-3 text-center">
                <input v-model="g.color" type="color" class="h-8 w-8 cursor-pointer rounded-full border border-border bg-bg p-0.5 overflow-hidden" />
              </td>
              <td class="px-3 py-3">
                <div class="flex items-center gap-2">
                  <div class="flex-1 bg-bg border border-border rounded-xl px-2 py-1 text-xs font-mono min-h-[32px] max-h-16 overflow-y-auto leading-relaxed flex items-center">
                    {{ g.apiKeyIds.length > 0 ? g.apiKeyIds.join(', ') : '未绑定密钥' }}
                  </div>
                  <button class="btn-secondary text-xs px-3 h-8 font-medium" @click="openKeySelector(g)">
                    选择 Key
                  </button>
                </div>
              </td>
              <td class="px-3 py-3">
                <input v-model="g.note" class="apple-input w-full text-xs h-8 rounded-lg" placeholder="备注用途" />
              </td>
              <td class="px-3 py-3 text-center">
                <button class="btn-danger text-xs px-3 h-8 font-medium" @click="removeGroup(g.id)">
                  删除
                </button>
              </td>
            </tr>
            <tr v-if="!config.groups.length">
              <td colspan="5" class="text-center py-10 text-muted text-sm font-medium">
                尚未配置任何虚拟分组。请点击“新增分组”手动创建，或通过连通测试后导入。
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Config Import Code Block -->
    <div class="apple-card p-6 space-y-3">
      <h2 class="text-sm font-semibold text-muted uppercase tracking-wider">导入配置 JSON</h2>
      <textarea
        v-model="importText"
        rows="5"
        placeholder="在此粘贴先前导出的配置 JSON 字符串..."
        class="w-full rounded-2xl border border-border bg-bg p-3 font-mono text-xs text-text focus:border-accent focus:ring-2 focus:ring-accent-dim outline-none transition-all"
      />
      <div class="flex items-center gap-3">
        <button class="btn-secondary text-xs px-5 h-9 font-medium" @click="doImport">立即导入</button>
        <span v-if="importMsg" class="text-xs text-muted font-medium bg-bg border border-border px-3 py-1.5 rounded-xl">{{ importMsg }}</span>
      </div>
    </div>

    <!-- Danger Zone Card -->
    <div class="rounded-2xl border border-error/20 bg-error/5 p-6 space-y-4">
      <div class="flex items-center gap-3 text-error">
        <svg class="h-5 w-5" viewBox="0 0 20 20" fill="currentColor">
          <path fill-rule="evenodd" d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
        </svg>
        <h2 class="text-sm font-bold uppercase tracking-wider">危险区域 (Danger Zone)</h2>
      </div>
      <p class="text-xs text-muted leading-relaxed">
        恢复默认会清空所有管理员共享的虚拟分组，并重置展示偏好。建议操作前先“导出 JSON”备份。
      </p>
      <button class="btn-danger text-xs px-5 h-9 font-medium" @click="config.reset()">
        恢复默认配置
      </button>
    </div>

    <!-- Custom Popover Key Selector Modal -->
    <div v-if="showKeySelector && editingGroup" class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4">
      <div class="bg-surface rounded-3xl w-full max-w-[500px] flex flex-col justify-between max-h-[85vh] p-6 shadow-2xl border border-border">
        <div>
          <div class="flex items-center justify-between border-b border-border pb-3 mb-4">
            <h3 class="text-base font-bold text-text">选择 API 密钥 - {{ editingGroup.name }}</h3>
            <button
              class="h-8 w-8 rounded-full flex items-center justify-center text-muted hover:text-text hover:bg-surface-hover transition-all text-xl cursor-pointer"
              @click="showKeySelector = false"
            >
              ×
            </button>
          </div>

          <!-- Search key bar -->
          <div class="mb-4">
            <input
              v-model="keySearchQuery"
              placeholder="搜索密钥名称或 ID..."
              class="apple-input w-full text-xs h-9 rounded-full px-4"
            />
          </div>

          <!-- Keys list checkboxes -->
          <div class="overflow-y-auto max-h-[40vh] space-y-2 pr-1 text-xs">
            <div
              v-for="k in filteredApiKeys"
              :key="k.id"
              class="flex items-center justify-between p-3 rounded-xl border border-border bg-bg/40 hover:bg-surface-hover cursor-pointer transition-all"
              @click="toggleKeySelection(k.id)"
            >
              <div class="flex items-center gap-3">
                <input
                  type="checkbox"
                  :checked="isKeySelected(k.id)"
                  class="h-4 w-4 rounded border-border text-accent focus:ring-accent"
                  @click.stop="toggleKeySelection(k.id)"
                />
                <div>
                  <span class="font-semibold text-text">{{ k.name }}</span>
                  <span class="text-[10px] text-muted font-mono ml-2 bg-bg px-1.5 py-0.5 rounded border border-border">ID: {{ k.id }}</span>
                </div>
              </div>
              <span
                class="text-[10px] font-semibold px-2 py-0.5 rounded-full inline-block"
                :class="k.status === 'active' ? 'bg-success/15 text-success' : 'bg-error/15 text-error'"
              >
                {{ k.status === 'active' ? '活跃' : '禁用' }}
              </span>
            </div>

            <div v-if="!filteredApiKeys.length" class="text-center text-muted py-8">
              未搜寻到可用 API 密钥，请检查连接状态或过滤词。
            </div>
          </div>
        </div>

        <div class="flex items-center justify-between border-t border-border pt-4 mt-6">
          <span class="text-[10px] text-muted font-medium">已选：{{ editingGroup.apiKeyIds.length }} 个密钥</span>
          <button class="btn-primary text-xs px-5 h-9 font-medium" @click="showKeySelector = false">
            确认
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Settings specific styles */
</style>
