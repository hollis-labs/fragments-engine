export const READER_CARD_INTERACTIVE_SELECTOR = [
  'a',
  'button',
  'input',
  'select',
  'textarea',
  'summary',
  'audio',
  'video',
  '[controls]',
  '[contenteditable="true"]',
  '[role="button"]',
  '[role="checkbox"]',
  '[role="menuitem"]',
  '[data-reader-nav-exclude]',
  '[data-reader-gallery-control]',
].join(',')

export function hasReaderCardInteraction(target: EventTarget | null, card: HTMLElement): boolean {
  return target instanceof Element && target !== card && Boolean(target.closest(READER_CARD_INTERACTIVE_SELECTOR))
}

export function hasReaderCardSelection(card: HTMLElement): boolean {
  const selection = window.getSelection?.()
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return false
  const range = selection.getRangeAt(0)
  return card.contains(range.commonAncestorContainer)
}
