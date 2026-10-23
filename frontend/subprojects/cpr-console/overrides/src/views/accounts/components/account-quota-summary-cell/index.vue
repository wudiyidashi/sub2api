<script setup lang="ts">
import type { Account } from '@/api'
import { computed } from 'vue'
import { visibleSummaryQuotaWindows } from '../../constants'
import AccountCapacityIndicator from '../AccountCapacityIndicator.vue'
import AccountQuotaPanelEntry from '../account-quota-panel/Entry.vue'

const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ showConcurrency: [], showQuota: [] }>()
const windows = computed(() => visibleSummaryQuotaWindows(props.account.quota.windows))
const visibleWindows = computed(() => windows.value.slice(0, 2))
</script>

<template>
  <div class="grid min-w-0 gap-1.5 py-2">
    <template v-if="account.authenticationKind === 'api_key'">
      <strong class="font-mono text-cp-xs tabular-nums text-cp-text">{{ account.usage.totalTokensDisplay }} Tokens</strong>
      <span class="text-cp-xs text-cp-text-tertiary">上游额度未提供</span>
    </template>
    <template v-else>
      <button type="button" class="grid w-full min-w-0 cursor-pointer gap-2 rounded-sm border-0 bg-transparent p-0 text-left focus-visible:outline-2 focus-visible:outline-cp-control-outline"
        :aria-label="`查看${account.name}的额度详情`" @click.stop="emit('showQuota')">
        <span v-if="visibleWindows.length" class="text-cp-xs text-cp-text-secondary">上游额度已用</span>
        <AccountQuotaPanelEntry v-for="window in visibleWindows" :key="window.key" :label="null" :windows="[window]" />
        <span v-if="!visibleWindows.length" class="text-cp-xs text-cp-text-tertiary">{{ account.capabilities.quota ? '额度待观测' : '暂不支持查询上游额度' }}</span>
        <span v-if="windows.length > 2" class="text-cp-xs text-cp-link">另有 {{ windows.length - 2 }} 个窗口，查看详情</span>
      </button>
      <span class="text-[10px] text-cp-text-tertiary">刷新 {{ account.quota.refreshedAtDisplay }}</span>
    </template>
    <AccountCapacityIndicator class="justify-self-end" :capacity="account.capacity" @show-details="emit('showConcurrency')" />
  </div>
</template>
