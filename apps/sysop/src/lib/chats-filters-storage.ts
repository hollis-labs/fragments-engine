import { EMPTY_CHAT_FILTERS, parseChatFilters, type ChatFilters } from './chats'

// Filters are non-secret view state; chat content and any credential never go here.
const KEY = 'sysop:chats:filters:v1'

export function loadChatFilters(): ChatFilters {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? parseChatFilters(JSON.parse(raw)) : { ...EMPTY_CHAT_FILTERS }
  } catch {
    return { ...EMPTY_CHAT_FILTERS }
  }
}

export function saveChatFilters(filters: ChatFilters): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(filters))
  } catch {
    // Storage may be unavailable or full; the page still works unpersisted.
  }
}
