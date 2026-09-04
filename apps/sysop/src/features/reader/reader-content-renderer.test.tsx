// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/react'

import { imageFixture, readerFixture, videoFixture } from './fixtures'
import { ReaderContentRenderer } from './reader-content-renderer'

afterEach(cleanup)

describe('ReaderContentRenderer', () => {
  it.each([
    ['article', readerFixture({ renderer: 'article' }), 'article'],
    ['image', imageFixture, 'image'],
    ['video', videoFixture, 'video'],
    ['audio', readerFixture({ renderer: 'audio' }), 'audio'],
    ['document', readerFixture({ renderer: 'document' }), 'document'],
    ['text', readerFixture({ renderer: 'text' }), 'article'],
    ['future-kind', readerFixture({ renderer: 'future-kind' }), 'unknown'],
  ])('dispatches %s safely without shell or route dependencies', (_kind, item, expected) => {
    const { container } = render(<ReaderContentRenderer item={item} presentation="card" />)
    expect(container.querySelector(`[data-reader-renderer="${expected}"]`)).toBeTruthy()
  })
})
