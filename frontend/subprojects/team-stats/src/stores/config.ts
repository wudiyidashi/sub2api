import { defineStore } from 'pinia';
import { ref, watch, nextTick } from 'vue';
import type { ApiKey } from '@/api/types';
import { pMapLimit } from '@/utils/reduce';

export interface VirtualGroup { id: string; name: string; color: string; apiKeyIds: number[]; note?: string; }
export interface AppConfig {
  timezone: string;
  refreshInterval: number;
  currency: 'USD';
  costSource: 'actual' | 'total';
  theme: 'auto' | 'light' | 'dark';
  groups: VirtualGroup[];
  tzVersion?: number;
}
const STORAGE_KEY = 'sub2api.teamStats.config.v1';
const defaults = (): AppConfig => ({ timezone: 'Asia/Shanghai', refreshInterval: 60, currency: 'USD', costSource: 'actual', theme: 'auto', groups: [], tzVersion: 2 });
/** Pick supported fields; legacy exports never persist API credentials or server URLs. */
export function normalizeConfig(value: unknown): AppConfig {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('配置必须是 JSON 对象');
  const v = value as Partial<AppConfig>;
  const cfg = defaults();
  if (v.timezone && v.tzVersion === 2) cfg.timezone = v.timezone;
  new Intl.DateTimeFormat('en', { timeZone: cfg.timezone });
  if (v.refreshInterval !== undefined) cfg.refreshInterval = v.refreshInterval;
  if (!Number.isInteger(cfg.refreshInterval) || (cfg.refreshInterval !== 0 && (cfg.refreshInterval < 30 || cfg.refreshInterval > 86400))) throw new Error('刷新间隔必须为 0 或 30–86400 秒');
  if (v.costSource !== undefined) {
    if (!['actual', 'total'].includes(v.costSource)) throw new Error('成本口径无效');
    cfg.costSource = v.costSource;
  }
  if (v.theme !== undefined) {
    if (!['auto', 'light', 'dark'].includes(v.theme)) throw new Error('主题无效');
    cfg.theme = v.theme;
  }
  if (v.groups !== undefined && !Array.isArray(v.groups)) throw new Error('分组必须是数组');
  if ((v.groups?.length ?? 0) > 100) throw new Error('最多支持 100 个分组');
  const groupIDs = new Set<string>();
  const keyIDs = new Set<number>();
  cfg.groups = (v.groups ?? []).map(g => {
    if (!g || typeof g.id !== 'string' || !/^[A-Za-z0-9_-]{1,100}$/.test(g.id) || groupIDs.has(g.id)) throw new Error('分组 ID 无效或重复');
    groupIDs.add(g.id);
    if (typeof g.name !== 'string' || !g.name.trim() || g.name.length > 100) throw new Error('分组名称不能为空，最多 100 字');
    if (typeof g.color !== 'string' || !/^#[0-9a-f]{6}$/i.test(g.color)) throw new Error('分组颜色必须是六位十六进制色值');
    if (g.note !== undefined && (typeof g.note !== 'string' || g.note.length > 1000)) throw new Error('分组备注最多 1000 字');
    if (!Array.isArray(g.apiKeyIds) || g.apiKeyIds.length > 1000) throw new Error('每组最多 1000 个密钥');
    for (const id of g.apiKeyIds) {
      if (!Number.isSafeInteger(id) || id <= 0 || keyIDs.has(id)) throw new Error('密钥 ID 无效或被重复分配');
      keyIDs.add(id);
      if (keyIDs.size > 1000) throw new Error('所有分组合计最多 1000 个密钥');
    }
    return { id: g.id, name: g.name.trim(), color: g.color, apiKeyIds: [...g.apiKeyIds], note: g.note };
  });
  return cfg;
}
export const useConfigStore = defineStore('team-stats-config', () => {
  const initial = defaults();
  const timezone = ref(initial.timezone);
  const refreshInterval = ref(initial.refreshInterval);
  const currency = ref(initial.currency);
  const costSource = ref(initial.costSource);
  const theme = ref(initial.theme);
  const groups = ref<VirtualGroup[]>([]);
  const allApiKeys = ref<ApiKey[]>([]);
  const loadingApiKeys = ref(false);
  const hydrated = ref(false);
  const authed = ref(false);
  const syncState = ref<'idle' | 'saving' | 'saved' | 'error'>('idle');
  const syncMsg = ref('');
  let suppressWatch = false;
  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  let saveQueue: Promise<void> = Promise.resolve();
  function snapshot(): AppConfig {
    return normalizeConfig({ timezone: timezone.value, refreshInterval: refreshInterval.value, currency: currency.value, costSource: costSource.value, theme: theme.value, groups: groups.value, tzVersion: 2 });
  }
  async function applyConfig(cfg: AppConfig) {
    suppressWatch = true;
    timezone.value = cfg.timezone; refreshInterval.value = cfg.refreshInterval;
    currency.value = cfg.currency; costSource.value = cfg.costSource;
    theme.value = cfg.theme; groups.value = cfg.groups;
    await nextTick(); suppressWatch = false;
  }
  async function loadFromBackend() {
    hydrated.value = false;
    try {
      const { endpoints } = await import('@/api/endpoints');
      await applyConfig(normalizeConfig(await endpoints.loadConfig()));
      hydrated.value = true; syncState.value = 'saved';
    } catch (e) {
      syncState.value = 'error'; syncMsg.value = (e as Error).message; throw e;
    }
  }
  function saveToBackend(): Promise<void> {
    if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
    if (!authed.value || !hydrated.value) return Promise.resolve();
    let cfg: AppConfig;
    try { cfg = snapshot(); } catch (e) {
      syncState.value = 'error'; syncMsg.value = (e as Error).message; return Promise.resolve();
    }
    syncState.value = 'saving'; syncMsg.value = '';
    // Serialize saves so a slower earlier edit cannot overwrite a later one.
    saveQueue = saveQueue.then(async () => {
      try {
        const { endpoints } = await import('@/api/endpoints');
        await endpoints.saveConfig(cfg);
        try { localStorage.setItem(STORAGE_KEY, JSON.stringify(cfg)); } catch { /* Backup is optional. */ }
        syncState.value = 'saved';
      } catch (e) { syncState.value = 'error'; syncMsg.value = (e as Error).message; }
    });
    return saveQueue;
  }
  function scheduleSave() {
    if (!authed.value || !hydrated.value) return;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => { saveTimer = null; void saveToBackend(); }, 800);
  }
  watch([timezone, refreshInterval, currency, costSource, theme, groups], () => {
    if (!suppressWatch) scheduleSave();
  }, { deep: true });
  function importJson(text: string): { ok: true } | { ok: false; error: string } {
    try {
      const cfg = normalizeConfig(JSON.parse(text));
      void applyConfig(cfg).then(scheduleSave); return { ok: true };
    } catch (e) { return { ok: false, error: (e as Error).message }; }
  }
  function exportJson() { return JSON.stringify(snapshot(), null, 2); }
  function reset() {
    if (!window.confirm('恢复默认配置会清空所有管理员共享的虚拟分组，确定继续？')) return;
    void applyConfig(defaults()).then(scheduleSave);
  }
  async function loadAllApiKeys() {
    loadingApiKeys.value = true;
    try {
      const { endpoints } = await import('@/api/endpoints');
      const list = await endpoints.groupsAll();
      const keys = (await pMapLimit(list, g => endpoints.groupAllApiKeys(g.id), 4)).flat();
      allApiKeys.value = [...new Map(keys.map(k => [k.id, k])).values()];
    } catch (e) { syncMsg.value = '读取密钥失败：' + (e as Error).message; }
    finally { loadingApiKeys.value = false; }
  }
  function setAuthed(ok: boolean) { authed.value = ok; }
  function isConfigured() { return authed.value && hydrated.value; }
  return { timezone, refreshInterval, currency, costSource, theme, groups, allApiKeys, loadingApiKeys, hydrated, syncState, syncMsg, authed, loadFromBackend, saveToBackend, loadAllApiKeys, setAuthed, importJson, exportJson, reset, isConfigured };
});
