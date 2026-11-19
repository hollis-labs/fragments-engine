import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient } from '@/lib/api'
import { readerItem } from '@/test/reader-fixture'
import { ReaderActionPill } from './ReaderInlineActions'

describe('Reader media acquisition', () => {
  it.each(['unsupported_media_type', 'provider_failed'])('offers Load media according to the failure %s', (code) => {
    const item = readerItem({
      actions: [{ command: 'request_asset_acquisition', input_schema: 'schema', expected_revision_required: true }],
      media: [{
        attachment: { attachment_id: 'attachment', fragment_revision_id: 'revision-1', media_asset_id: 'asset', role: 'inline', position: 0 },
        media_asset_id: 'asset', kind: 'image',
        variants: [{ asset_variant_id: 'variant', kind: 'original', custody: 'mirror', acquisition_state: 'failed', failure: { code, message: 'Cannot show image', retryable: false } }],
      }],
    })
    render(<ApiProvider client={apiClient}><ReaderActionPill item={item} onItemChange={() => {}} command="request_asset_acquisition" /></ApiProvider>)
    const button = screen.queryByRole('button', { name: 'Load media' })
    if (code === 'unsupported_media_type') expect(button).toBeNull()
    else expect(button).not.toBeNull()
  })
})
