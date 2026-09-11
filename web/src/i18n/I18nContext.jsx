import React, { createContext, useCallback, useContext, useMemo, useState } from 'react'
import { DEFAULT_LANGUAGE, LANGUAGES, detectLanguage, isSupportedLanguage, translations } from './translations'

const I18nContext = createContext()

const STORAGE_KEY = 'ui.language'

// A stored choice wins over the browser's preference; anything unknown (or a
// blocked storage) falls back to detection.
function initialLanguage() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored && isSupportedLanguage(stored)) return stored
  } catch (_) {
    // Storage can be unavailable; detection still works.
  }
  return detectLanguage()
}

export function I18nProvider({ children }) {
  const [language, setLanguageState] = useState(initialLanguage)

  const setLanguage = useCallback((code) => {
    if (!isSupportedLanguage(code)) return
    setLanguageState(code)
    try {
      localStorage.setItem(STORAGE_KEY, code)
    } catch (_) {
      // The choice still applies to this session.
    }
  }, [])

  const t = useCallback((key) => (
    translations[language]?.[key] || translations[DEFAULT_LANGUAGE]?.[key] || key
  ), [language])

  const value = useMemo(() => ({
    language,
    languages: LANGUAGES,
    setLanguage,
    t
  }), [language, setLanguage, t])

  return (
    <I18nContext.Provider value={value}>
      {children}
    </I18nContext.Provider>
  )
}

export function useI18n() {
  const context = useContext(I18nContext)
  if (!context) {
    throw new Error('useI18n must be used within I18nProvider')
  }
  return context
}
