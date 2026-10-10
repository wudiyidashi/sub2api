<template>
  <div data-testid="account-driver-selector">
    <label class="input-label">{{ t('admin.accounts.accountDriver.label') }}</label>
    <Select
      :model-value="modelValue"
      :options="options"
      :disabled="readonly || loading"
      :aria-label="t('admin.accounts.accountDriver.label')"
      @update:model-value="selectDriver"
    />
    <p v-if="readonly || modelValue === NATIVE_DRIVER_ID" class="input-hint">
      {{ t(readonly ? selectedDriver?.capabilities?.switch ? 'admin.accounts.accountDriver.readonlySwitchHint' : 'admin.accounts.accountDriver.readonlyHint' : 'admin.accounts.accountDriver.nativeHint') }}
    </p>
    <p v-if="modelValue === CODEX_HTTP_DRIVER_ID" class="input-hint" data-testid="account-driver-transport">
      {{ t(selectedDriver?.transport_protocols?.includes('websocket') ? 'admin.accounts.accountDriver.websocketTransportHint' : 'admin.accounts.accountDriver.httpTransportHint') }}
    </p>
    <p v-if="modelValue === CODEX_HTTP_DRIVER_ID && selectedDriver?.lifecycle_driver_id === 'native'" class="input-hint">
      {{ t('admin.accounts.accountDriver.nativeLifecycleHint') }}
    </p>
    <p v-if="selectionReset" class="mt-1 text-sm text-amber-600 dark:text-amber-400" role="status">
      {{ t('admin.accounts.accountDriver.selectionReset') }}
    </p>
    <p v-if="unavailable" class="mt-1 text-sm text-amber-600 dark:text-amber-400" role="status">
      {{ t('admin.accounts.accountDriver.loadFailed') }}
    </p>
    <p v-if="selectedDriver && !selectedDriver.available" class="mt-1 text-sm text-amber-600 dark:text-amber-400" role="status">
      {{ t('admin.accounts.accountDriver.unavailable') }}
    </p>
    <p v-if="selectedDriver?.version" class="input-hint" data-testid="account-driver-version">
      {{ t('admin.accounts.accountDriver.version', { version: selectedDriver.version }) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { CODEX_HTTP_DRIVER_ID, NATIVE_DRIVER_ID, useAccountDrivers } from './useAccountDrivers'
import type { AccountDriver, AccountDriverBinding } from './types'
import { accountDriverLabel } from './display'

const props = withDefaults(defineProps<{
  modelValue: string
  active: boolean
  platform: string
  accountType: string
  readonly?: boolean
  binding?: AccountDriverBinding
}>(), { readonly: false })
const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:ready': [value: boolean]
  'update:driver': [value: AccountDriver | AccountDriverBinding | undefined]
}>()
const { t } = useI18n()
const { drivers, loading, unavailable, resolved, load } = useAccountDrivers()
const selectionReset = ref(false)
const selectedDriver = computed(() => {
  if (loading.value && props.binding) return props.binding
  const liveDriver = drivers.value.find(driver => driver.id === props.modelValue)
  if (liveDriver) return liveDriver
  if (!props.binding) return undefined
  // A previously saved binding is still visible when its runtime stops. A successful
  // catalogue refresh supersedes that snapshot for enabling new transport settings.
  if (resolved.value && props.binding.id !== NATIVE_DRIVER_ID) return { ...props.binding, available: false }
  return props.binding
})

const options = computed(() => {
  const result = drivers.value.map(driver => {
    const compatible = driver.platforms.includes(props.platform) && driver.account_types.includes(props.accountType)
    const integrated = driver.id === NATIVE_DRIVER_ID || driver.id === CODEX_HTTP_DRIVER_ID
    const disabled = !driver.available || !compatible || !integrated
    const name = accountDriverLabel(driver, t)
    return { value: driver.id, label: disabled ? `${name} (${t('admin.accounts.accountDriver.unavailable')})` : name, disabled }
  })
  if (!result.some(option => option.value === NATIVE_DRIVER_ID)) {
    result.unshift({ value: NATIVE_DRIVER_ID, label: t('admin.accounts.accountDriver.native'), disabled: false })
  }
  if (!result.some(option => option.value === props.modelValue)) {
    result.push({
      value: props.modelValue,
      label: accountDriverLabel({ id: props.modelValue, name: props.binding?.name }, t),
      disabled: true
    })
  }
  return result
})

function selectDriver(value: string | number | boolean | null) {
  const option = options.value.find(option => option.value === value)
  if (!props.readonly && option && !option.disabled) {
    selectionReset.value = false
    emit('update:modelValue', option.value)
  }
}

watch([() => props.active, () => JSON.stringify(props.binding)], ([active, bindingKey], [wasActive, previousBindingKey]) => {
  if (active && (!wasActive || bindingKey !== previousBindingKey)) {
    selectionReset.value = false
    void load()
  }
}, { immediate: true })
watch([() => props.platform, () => props.accountType], ([platform, accountType]) => {
  if (!props.readonly && props.modelValue === CODEX_HTTP_DRIVER_ID && (platform !== 'openai' || accountType !== 'oauth')) {
    selectionReset.value = true
    emit('update:modelValue', NATIVE_DRIVER_ID)
  }
})
watch(() => options.value.find(option => option.value === props.modelValue)?.disabled === false,
  ready => emit('update:ready', ready), { immediate: true })
watch(selectedDriver, driver => emit('update:driver', driver), { immediate: true })
</script>
