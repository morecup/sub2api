<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.codexResetCredits')"
    width="normal"
    @close="handleClose"
  >
    <div class="space-y-4">
      <!-- Account Info -->
      <div v-if="account" class="flex items-center gap-3 rounded-xl border border-gray-200 bg-gray-50 p-3 dark:border-dark-500 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-amber-500">
          <Icon name="creditCard" size="md" class="text-white" :stroke-width="2" />
        </div>
        <div>
          <div class="font-semibold text-gray-900 dark:text-gray-100">{{ account.name }}</div>
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ account.platform }} / {{ account.type }}</div>
        </div>
      </div>

      <!-- Credits List -->
      <div v-if="loading" class="py-8 text-center text-sm text-gray-400">
        {{ t('common.loading') }}...
      </div>

      <div v-else-if="credits.length === 0" class="py-8 text-center text-sm text-gray-400">
        {{ t('admin.accounts.codexNoCredits') }}
      </div>

      <div v-else class="space-y-2">
        <div
          v-for="credit in credits"
          :key="credit.credit_id"
          class="flex items-center justify-between rounded-lg border border-gray-200 p-3 dark:border-dark-500 dark:bg-dark-700"
        >
          <div class="flex items-center gap-3">
            <div class="flex h-8 w-8 items-center justify-center rounded-lg bg-amber-100 dark:bg-amber-500/20">
              <Icon name="creditCard" size="sm" class="text-amber-600" />
            </div>
            <div>
              <div class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ credit.credit_id }}</div>
              <div v-if="credit.redeem_request_id" class="text-xs text-gray-400">{{ credit.redeem_request_id }}</div>
            </div>
          </div>
          <button
            @click="handleConsume(credit)"
            :disabled="consumingId === credit.credit_id"
            class="px-3 py-1.5 text-xs font-medium text-white bg-amber-600 hover:bg-amber-700 rounded-lg disabled:opacity-50"
          >
            {{ consumingId === credit.credit_id ? t('common.loading') + '...' : t('admin.accounts.codexConsumeCredit') }}
          </button>
        </div>
      </div>
    </div>

    <template #footer>
      <button @click="handleClose" class="px-4 py-2 text-sm text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-600 rounded-lg">
        {{ t('common.close') }}
      </button>
      <button
        @click="loadCredits"
        :disabled="loading"
        class="px-4 py-2 text-sm font-medium text-amber-600 hover:bg-amber-50 dark:text-amber-400 dark:hover:bg-amber-500/10 rounded-lg disabled:opacity-50"
      >
        {{ t('common.refresh') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { accountsAPI } from '@/api/admin/accounts'
import { useAppStore } from '@/stores/app'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits(['close'])
const { t } = useI18n()
const appStore = useAppStore()

const credits = ref<any[]>([])
const loading = ref(false)
const consumingId = ref('')

watch(() => props.show, (visible) => {
  if (visible && props.account) {
    loadCredits()
  } else {
    credits.value = []
  }
})

async function loadCredits() {
  if (!props.account) return
  loading.value = true
  try {
    credits.value = await accountsAPI.getRateLimitResetCredits(props.account.id)
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.accounts.codexCreditsFailed'))
  } finally {
    loading.value = false
  }
}

async function handleConsume(credit: any) {
  if (!props.account) return
  consumingId.value = credit.credit_id
  try {
    await accountsAPI.consumeResetCredit(props.account.id, credit.credit_id, credit.redeem_request_id)
    appStore.showSuccess(t('admin.accounts.codexCreditConsumed'))
    await loadCredits()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.accounts.codexConsumeFailed'))
  } finally {
    consumingId.value = ''
  }
}

function handleClose() {
  emit('close')
}
</script>
