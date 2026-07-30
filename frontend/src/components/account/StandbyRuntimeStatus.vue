<template>
  <div
    v-if="visible"
    class="flex min-w-0 flex-col items-start gap-1"
    data-testid="standby-runtime-status"
  >
    <span :class="['badge text-xs', badgeClass]" data-testid="standby-runtime-badge">
      {{ badgeText }}
    </span>

    <span v-if="stale" class="text-[11px] leading-4 text-amber-600 dark:text-amber-400">
      {{ t('admin.accounts.standby.runtime.pendingRefreshHint') }}
    </span>
    <template v-else>
      <span
        v-if="primaryID"
        class="max-w-[240px] truncate text-[11px] leading-4 text-gray-500 dark:text-gray-400"
        :title="primaryTitle"
        data-testid="standby-runtime-primary"
      >
        {{ t('admin.accounts.standby.runtime.primaryLabel') }}：{{ primaryName }} (#{{ primaryID }})
      </span>
      <span
        v-else-if="runtimeState === 'invalid'"
        class="text-[11px] leading-4 text-red-500 dark:text-red-400"
        data-testid="standby-runtime-primary"
      >
        {{ t('admin.accounts.standby.runtime.primaryUnavailable') }}
      </span>
      <span
        v-if="runtimeState === 'active' && matchedTriggerLabels.length > 0"
        class="max-w-[260px] text-[11px] leading-4 text-emerald-600 dark:text-emerald-400"
        data-testid="standby-runtime-matched"
      >
        {{ t('admin.accounts.standby.runtime.matchedLabel') }}：{{ matchedTriggerLabels.join(conditionSeparator) }}
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, StandbyRuntimeState, StandbyTriggerType } from '@/types'

const props = withDefaults(defineProps<{
  account: Account
  stale?: boolean
}>(), {
  stale: false
})

const { t } = useI18n()

const runtimeState = computed<StandbyRuntimeState | undefined>(() => props.account.standby_runtime_state)
const visible = computed(() => props.stale || runtimeState.value !== undefined)
const primaryID = computed(() => props.account.standby_for_account_id ?? null)
const primaryName = computed(() => {
  const name = props.account.standby_primary_name?.trim()
  if (name) return name
  return t('admin.accounts.standby.runtime.unknownPrimary')
})
const primaryTitle = computed(() => `${primaryName.value} (#${primaryID.value})`)
const conditionSeparator = computed(() => t('admin.accounts.standby.runtime.conditionSeparator'))

const matchedTriggerLabels = computed(() => {
  return (props.account.standby_matched_trigger_types ?? []).map((trigger: StandbyTriggerType) =>
    t(`admin.accounts.standby.triggers.${trigger}.label`)
  )
})

const badgeClass = computed(() => {
  if (props.stale) return 'badge-warning'
  switch (runtimeState.value) {
    case 'active':
      return 'badge-success'
    case 'waiting':
      return 'badge-primary'
    case 'invalid':
      return 'badge-danger'
    default:
      return 'badge-gray'
  }
})

const badgeText = computed(() => {
  if (props.stale) return t('admin.accounts.standby.runtime.states.pending')
  if (!runtimeState.value) return ''
  return t(`admin.accounts.standby.runtime.states.${runtimeState.value}`)
})
</script>
