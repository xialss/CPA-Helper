import { readonly, ref } from 'vue'

import type { AuthUser } from '@/shared/types/api'

const currentUser = ref<AuthUser | null>(null)

export function setCurrentUser(user: AuthUser | null): void {
  currentUser.value = user
}

export function useCurrentUser() {
  return {
    currentUser: readonly(currentUser),
    setCurrentUser,
  }
}
