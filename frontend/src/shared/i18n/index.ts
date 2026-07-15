import {
  currentLanguage,
  isEnglish,
  localize,
  setLanguage,
  toggleLanguage,
  useLanguagePreference,
} from './language'
import {
  copiedText,
  errorText,
  localizedKeeperStatusDetail,
  localizedServerMessage,
  localizedUsageChannelFallbackLabel,
} from './messages'

export {
  currentLanguage,
  isEnglish,
  localize,
  setLanguage,
  toggleLanguage,
  useLanguagePreference,
  type AppLanguage,
} from './language'
export {
  copiedText,
  errorText,
  localizedApiErrorMessage,
  localizedKeeperStatusDetail,
  localizedServerMessage,
  localizedUsageChannelFallbackLabel,
} from './messages'

export function useI18n() {
  return {
    copiedText,
    currentLanguage,
    errorText,
    isEnglish,
    keeperStatusText: localizedKeeperStatusDetail,
    usageChannelFallbackLabel: localizedUsageChannelFallbackLabel,
    language: currentLanguage,
    localize,
    serverText: localizedServerMessage,
    setLanguage,
    t: localize,
    toggleLanguage,
    useLanguagePreference,
  }
}
