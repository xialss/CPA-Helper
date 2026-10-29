import { readonly, ref } from 'vue'

import { setFeatureSession } from '@/features/feature-center/state/useFeatureState'

import type { AuthUser } from '@/shared/types/api'

const currentUser = ref<AuthUser | null>(null)

export function setCurrentUser(user: AuthUser | null): void {
  setFeatureSession(user && !user.must_change_password ? user.id : null)
  currentUser.value = user
}

export function useCurrentUser() {
  return {
    currentUser: readonly(currentUser),
    setCurrentUser,
  }
}
