<template>
  <div class="text-sm" data-testid="account-driver-badge">
    <span class="text-gray-700 dark:text-gray-300">{{ name }}</span>
    <div v-if="binding?.version" class="text-xs text-gray-500 dark:text-gray-400">{{ binding.version }}</div>
    <div v-if="binding && !binding.available" class="text-xs text-amber-600 dark:text-amber-400">
      {{ t('admin.accounts.accountDriver.unavailable') }}
    </div>
    <div v-if="maintenanceActive" class="text-xs text-amber-600 dark:text-amber-400" role="status">
      {{ t(`admin.accounts.accountDriver.maintenance.states.${maintenance?.state}`) }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountDriverBinding, AccountDriverMaintenanceStatus } from './types'
import { isAccountDriverMaintenanceActive } from './maintenance'
import { accountDriverLabel } from './display'

const props = defineProps<{ binding?: AccountDriverBinding; maintenance?: AccountDriverMaintenanceStatus | null }>()
const { t } = useI18n()
const name = computed(() => accountDriverLabel(props.binding, t))
const maintenanceActive = computed(() => isAccountDriverMaintenanceActive(props.maintenance))
</script>
