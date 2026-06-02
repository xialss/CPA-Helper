import {
  currentLanguage,
  isEnglish,
  localize,
  setLanguage,
  toggleLanguage,
  useLanguagePreference,
} from './language'
import { copiedText, errorText, localizedKeeperStatusDetail, localizedServerMessage } from './messages'

export {
  currentLanguage,
  isEnglish,
  localize,
  setLanguage,
  toggleLanguage,
  useLanguagePreference,
  type AppLanguage,
} from './language'
export { copiedText, errorText, localizedApiErrorMessage, localizedKeeperStatusDetail, localizedServerMessage } from './messages'

export function useI18n() {
  return {
    copiedText,
    currentLanguage,
    errorText,
    isEnglish,
    keeperStatusText: localizedKeeperStatusDetail,
    language: currentLanguage,
    localize,
    serverText: localizedServerMessage,
    setLanguage,
    t: localize,
    toggleLanguage,
    useLanguagePreference,
  }
}
