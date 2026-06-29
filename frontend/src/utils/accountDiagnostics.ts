import type { Account } from '@/types'

const ANTHROPIC_OAUTH_TYPES = new Set(['oauth', 'setup-token'])

export function getAccountUUID(account: Account | null | undefined): string {
  const extra = account?.extra as Record<string, unknown> | undefined
  const value = extra?.account_uuid
  if (typeof value === 'string') {
    return value.trim()
  }
  if (value == null) {
    return ''
  }
  return String(value).trim()
}

export function isAnthropicOAuthMissingAccountUUID(account: Account | null | undefined): boolean {
  return (
    account?.platform === 'anthropic' &&
    ANTHROPIC_OAUTH_TYPES.has(account.type) &&
    getAccountUUID(account) === ''
  )
}
