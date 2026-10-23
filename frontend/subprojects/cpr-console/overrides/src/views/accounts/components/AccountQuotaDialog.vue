<script setup lang="ts">
import type { Account } from '@/api'
import { ZDialog } from '@codex-proxy/ui'
import AccountQuotaPanel from './account-quota-panel/index.vue'

defineProps<{ account: Account | null, refreshing: boolean }>()
const open = defineModel<boolean>({ required: true })
const emit = defineEmits<{
  refreshQuota: [accountId: string]
  quotaReset: [accountId: string]
  accountUpdated: [account: Account]
}>()
</script>

<template>
  <ZDialog v-model="open" title="额度详情" :description="account?.name" width="36rem">
    <AccountQuotaPanel v-if="account" :account="account" :refreshing="refreshing"
      @refresh-quota="emit('refreshQuota', $event)" @quota-reset="emit('quotaReset', $event)"
      @account-updated="emit('accountUpdated', $event)" />
  </ZDialog>
</template>
