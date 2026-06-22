<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.codexInvite')"
    width="normal"
    @close="handleClose"
  >
    <div class="space-y-4">
      <!-- Account Info -->
      <div v-if="account" class="flex items-center gap-3 rounded-xl border border-gray-200 bg-gray-50 p-3 dark:border-dark-500 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-blue-500">
          <Icon name="gift" size="md" class="text-white" :stroke-width="2" />
        </div>
        <div>
          <div class="font-semibold text-gray-900 dark:text-gray-100">{{ account.name }}</div>
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ account.platform }} / {{ account.type }}</div>
        </div>
      </div>

      <!-- Eligibility Check -->
      <div v-if="eligibilityChecked" class="rounded-lg p-3" :class="eligibility?.is_eligible ? 'bg-green-50 dark:bg-green-500/10' : 'bg-red-50 dark:bg-red-500/10'">
        <div class="flex items-center gap-2">
          <Icon :name="eligibility?.is_eligible ? 'check' : 'x'" size="sm" :class="eligibility?.is_eligible ? 'text-green-600' : 'text-red-600'" />
          <span class="text-sm font-medium" :class="eligibility?.is_eligible ? 'text-green-700 dark:text-green-400' : 'text-red-700 dark:text-red-400'">
            {{ eligibility?.is_eligible ? t('admin.accounts.codexInviteEligible') : t('admin.accounts.codexInviteNotEligible') }}
          </span>
        </div>
        <p v-if="eligibility?.reason" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ eligibility.reason }}</p>
      </div>

      <!-- Email Input -->
      <div class="space-y-1.5">
        <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.accounts.codexInviteEmails') }}
        </label>
        <textarea
          v-model="emailsText"
          rows="3"
          :disabled="sending"
          :placeholder="t('admin.accounts.codexInviteEmailsPlaceholder')"
          class="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm dark:border-dark-500 dark:bg-dark-700 dark:text-gray-100"
        />
        <p class="text-xs text-gray-400">{{ t('admin.accounts.codexInviteEmailsHint') }}</p>
      </div>
    </div>

    <template #footer>
      <button @click="handleClose" class="px-4 py-2 text-sm text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-600 rounded-lg">
        {{ t('common.cancel') }}
      </button>
      <button
        @click="handleCheckEligibility"
        :disabled="checking"
        class="px-4 py-2 text-sm font-medium text-blue-600 hover:bg-blue-50 dark:text-blue-400 dark:hover:bg-blue-500/10 rounded-lg disabled:opacity-50"
      >
        {{ checking ? t('common.loading') + '...' : t('admin.accounts.codexCheckEligibility') }}
      </button>
      <button
        @click="handleInvite"
        :disabled="sending || !emailsText.trim()"
        class="px-4 py-2 text-sm font-medium text-white bg-blue-600 hover:bg-blue-700 rounded-lg disabled:opacity-50"
      >
        {{ sending ? t('common.loading') + '...' : t('admin.accounts.codexSendInvite') }}
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

const emailsText = ref('')
const eligibility = ref<any>(null)
const eligibilityChecked = ref(false)
const checking = ref(false)
const sending = ref(false)

watch(() => props.show, (visible) => {
  if (visible) {
    emailsText.value = ''
    eligibility.value = null
    eligibilityChecked.value = false
    checking.value = false
    sending.value = false
  }
})

async function handleCheckEligibility() {
  if (!props.account) return
  checking.value = true
  try {
    eligibility.value = await accountsAPI.getInviteEligibility(props.account.id)
    eligibilityChecked.value = true
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.accounts.codexInviteFailed'))
  } finally {
    checking.value = false
  }
}

async function handleInvite() {
  if (!props.account || !emailsText.value.trim()) return
  const emails = emailsText.value
    .split(/[\n,]/)
    .map(e => e.trim())
    .filter(e => e.length > 0)
  if (emails.length === 0) return

  sending.value = true
  try {
    await accountsAPI.inviteFriends(props.account.id, emails)
    appStore.showSuccess(t('admin.accounts.codexInviteSent'))
    emit('close')
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.accounts.codexInviteFailed'))
  } finally {
    sending.value = false
  }
}

function handleClose() {
  emit('close')
}
</script>
