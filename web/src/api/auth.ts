// Optional gateway API token (GATEWAY_API_TOKEN). The UI asks for it the first
// time the gateway answers 401 and keeps it in localStorage for this browser.
const KEY = 'omegagrid_api_token'

export function getToken(): string {
  try {
    return localStorage.getItem(KEY) ?? ''
  } catch {
    return ''
  }
}

export function authHeaders(): Record<string, string> {
  const t = getToken()
  return t ? { Authorization: `Bearer ${t}` } : {}
}

/** Prompt for a token after a 401; returns true when one was entered. */
export function promptForToken(): boolean {
  const t = window.prompt('This gateway requires an API token (GATEWAY_API_TOKEN):')?.trim()
  if (!t) return false
  try {
    localStorage.setItem(KEY, t)
  } catch {
    return false
  }
  return true
}
